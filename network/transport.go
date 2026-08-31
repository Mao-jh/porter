// Package network 实现协议层：HTTP/HTTPS Range 分片下载、故障注入。
// 网络约束（H-3/H-4）：所有 socket 绑定 127.0.0.0/8，禁止公网地址；无联网依赖。
package network

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultUserAgent 默认 UA（合规：主动标识自身，便于服务端识别与日志归因）。
// 用户通过 -H "User-Agent: ..." 显式覆盖时不追加。
const DefaultUserAgent = "Porter/0.3.0 (+https://github.com/Mao-jh/porter)"

// maxRedirects 单次请求最大重定向跳数（对齐主流浏览器/aria2 默认）。
const maxRedirects = 10

// FaultConfig 故障注入配置（§6 测试方案：断连/超时/429/5xx 各≥3次）。
type FaultConfig struct {
	DisconnectN atomic.Int32 // 剩余断连次数
	TimeoutN    atomic.Int32
	TooManyN    atomic.Int32 // 429
	ServerErrN  atomic.Int32 // 5xx
}

// Transport 下载传输层。
type Transport struct {
	client *http.Client
	faults *FaultConfig

	mu          sync.RWMutex
	limiter     *rateLimiter       // 全局限速（nil=不限速），多连接共享配额
	headers     map[string]string  // 每请求透传头（Cookie/Authorization 等）
	cookies     []Cookie           // cookie 文件加载的按域 cookie（第 14 轮）
	allowRemote bool               // 允许非回环目标（产品开关，默认 false；H-3 审计边界见 README）
	proxySet    bool               // 已配置代理（第 14 轮：显式出站同意，见 proxy.go）
}

// NewTransport 构造传输层。dialer 强制绑定 127.0.0.0/8（H-3）。
// 若 allowRemote=true（仅测试用），允许非回环地址——生产路径必须为 false。
//
// 超时策略：不在 http.Client 上设置总超时（会切断低速大文件/限速下载），
// 仅约束拨号与响应头阶段；响应体停滞由调用方上下文取消兜底。
func NewTransport(allowRemote bool) *Transport {
	dialer := &net.Dialer{
		LocalAddr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}, // H-3：本地出口
		Timeout:   5 * time.Second,
	}
	t := &Transport{allowRemote: allowRemote}
	// 回环校验引用 t.allowRemote（而非构造期快照）：SetProxy 置位后，
	// 代理地址（可能非回环）必须可拨号——代理语义见 proxy.go。
	t.client = &http.Client{
		Transport: &http.Transport{
			// 自定义 DialContext 默认会关闭自动 HTTP/2 协商（R19：显式强制尝试；
			// 对 https 目标与 h2 服务端协商多路复用——6 分片可共享一条连接）
			ForceAttemptHTTP2: true,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, _, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				t.mu.RLock()
				allow := t.allowRemote
				t.mu.RUnlock()
				ip := net.ParseIP(host)
				if ip == nil {
					// 域名解析：仅允许解析到回环（H-3）；代理模式下 addr 为代理主机
					if !allow {
						ips, err := net.LookupIP(host)
						if err != nil {
							return nil, err
						}
						for _, candidate := range ips {
							if !candidate.IsLoopback() {
								return nil, fmt.Errorf("network: 禁止非回环地址 %s(%s) (H-3)", host, candidate)
							}
						}
					}
				} else if !ip.IsLoopback() && !allow {
					return nil, fmt.Errorf("network: 禁止非回环地址 %s (H-3)", ip)
				}
				return dialer.DialContext(ctx, network, addr)
			},
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
		// 不设 Client.Timeout：限速/低速大文件的总时长不可预估，
		// 阶段性超时（拨号/响应头）+ 上下文取消已覆盖停滞场景。
		// 重定向合规策略：跳数上限、协议白名单、跨主机剥离敏感凭据头。
		// （重定向目标仍受拨号层回环强制约束——socket 层兜底，无法被 3xx 绕过。）
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("network: 重定向超过 %d 跳", maxRedirects)
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("network: 重定向到不支持的协议 %s", req.URL.Scheme)
			}
			if len(via) > 0 && req.URL.Host != via[len(via)-1].URL.Host {
				// 跨主机：剥离可携带凭据的头（Cookie/Authorization/Proxy-Authorization）
				for _, k := range []string{"Cookie", "Authorization", "Proxy-Authorization"} {
					req.Header.Del(k)
				}
			}
			return nil
		},
	}
	t.faults = &FaultConfig{}
	return t
}

// SetGlobalLimit 设置全局下载限速（字节/秒，<=0 取消限速）。
// 配额由该 Transport 的所有连接共享（多分片/多任务聚合速率不超过该值）。
func (t *Transport) SetGlobalLimit(bytesPerSec int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if bytesPerSec <= 0 {
		t.limiter = nil
		return
	}
	t.limiter = newRateLimiter(bytesPerSec)
}

// SetHeaders 设置每个请求透传的头（如 Cookie、Authorization）。
// 传入 nil 清空。值会被复制，设置后的修改不影响已拷贝内容。
func (t *Transport) SetHeaders(h map[string]string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(h) == 0 {
		t.headers = nil
		return
	}
	t.headers = make(map[string]string, len(h))
	for k, v := range h {
		t.headers[k] = v
	}
}

// snapshot 读取限速器与透传头（调用方持返回值使用，避免持锁做 IO）。
func (t *Transport) snapshot() (*rateLimiter, map[string]string) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.limiter, t.headers
}

// sharedLimiter 返回当前全局限速器（Mux 供 FTP 共享配额；nil=不限速）。
func (t *Transport) sharedLimiter() *rateLimiter {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.limiter
}

// applyUserAgent 为请求补默认 UA（用户显式透传 UA 时不覆盖；合规：主动标识自身）。
func applyUserAgent(req *http.Request, headers map[string]string) {
	if _, ok := headers["User-Agent"]; !ok {
		req.Header.Set("User-Agent", DefaultUserAgent)
	}
}

// snapshotHeaders 仅返回透传头副本。
func (t *Transport) snapshotHeaders() map[string]string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.headers
}

// SetFaults 配置故障注入计数。
func (t *Transport) SetFaults(dc, to, too, se int32) {
	t.faults.DisconnectN.Store(dc)
	t.faults.TimeoutN.Store(to)
	t.faults.TooManyN.Store(too)
	t.faults.ServerErrN.Store(se)
}

// Probe 探测目标资源：返回 (大小, 是否支持 Range, 错误)。
// 优先 HEAD 读取 Content-Length / Accept-Ranges；失败时回退到 Range GET bytes=0-0
// 解析 Content-Range 总长。size=0 表示大小未知（流式下载）。
func (t *Transport) Probe(ctx context.Context, urlStr string) (int64, bool, error) {
	return t.probe(ctx, urlStr, nil)
}

// probe 为 Probe 的私有变体：headers 非 nil 时替代全局透传头快照
//（HLS 跨主机段剥离凭据后传入，语义与重定向策略同源）。
func (t *Transport) probe(ctx context.Context, urlStr string, headers map[string]string) (int64, bool, error) {
	if err := t.validateURL(urlStr); err != nil {
		return 0, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, urlStr, nil)
	if err == nil {
		hdrs := headers
		if hdrs == nil {
			_, hdrs = t.snapshot()
		}
		for k, v := range hdrs {
			req.Header.Set(k, v)
		}
		applyUserAgent(req, hdrs)
		t.applyCookies(req, hdrs)
		if resp, err := t.client.Do(req); err == nil {
			cl := resp.Header.Get("Content-Length")
			ar := resp.Header.Get("Accept-Ranges")
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && cl != "" {
				if n, perr := strconv.ParseInt(cl, 10, 64); perr == nil && n > 0 {
					return n, ar == "bytes", nil
				}
			}
		}
	}
	// 回退：Range GET bytes=0-0
	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return 0, false, err
	}
	req2.Header.Set("Range", "bytes=0-0")
	hdrs2 := headers
	if hdrs2 == nil {
		hdrs2 = t.snapshotHeaders()
	}
	for k, v := range hdrs2 {
		req2.Header.Set(k, v)
	}
	applyUserAgent(req2, hdrs2)
	t.applyCookies(req2, hdrs2)
	resp, err := t.client.Do(req2)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusPartialContent {
		// Content-Range: bytes 0-0/<total>
		cr := resp.Header.Get("Content-Range")
		if i := strings.LastIndexByte(cr, '/'); i >= 0 {
			if n, perr := strconv.ParseInt(cr[i+1:], 10, 64); perr == nil && n > 0 {
				return n, true, nil
			}
		}
	}
	if resp.StatusCode == http.StatusOK {
		if cl := resp.Header.Get("Content-Length"); cl != "" {
			if n, perr := strconv.ParseInt(cl, 10, 64); perr == nil && n > 0 {
				return n, false, nil
			}
		}
	}
	return 0, false, fmt.Errorf("network: 无法探测资源大小 (%s)", resp.Status)
}

// Retryable 判断错误是否可重试：429/5xx/网络错误 → 是；4xx(除429) 与上下文取消 → 否。
func Retryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var nre nonRetryableError
	if errors.As(err, &nre) {
		return false
	}
	var he *httpError
	if errors.As(err, &he) {
		return he.status == http.StatusTooManyRequests || he.status >= 500
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	return true // 断连/重置等未知传输错误默认可重试
}

// RetryAfter 提取服务端 Retry-After 建议等待时长（合规：尊重服务端限流意愿，
// 退避时长取 max(本地退避, Retry-After)，由调用方设上限）。
func RetryAfter(err error) (time.Duration, bool) {
	var he *httpError
	if errors.As(err, &he) && he.retryAfter > 0 {
		return he.retryAfter, true
	}
	return 0, false
}

// FetchRange 发起 Range 请求，将 [start,end) 写入 dst（io.WriterAt）。
// end=0 表示到 EOF（open-ended，发送 "bytes=start-"）。
// start=0 且 end=0 表示完整下载（不发送 Range 头）。
// 响应体被严格限制在 end-start 字节内并校验完整性：服务器多给/少给都视为错误，
// 杜绝「200 全量响应被当作分片写入」导致的数据错位。
func (t *Transport) FetchRange(ctx context.Context, urlStr string, start, end int64, dst io.WriterAt) error {
	return t.fetchRange(ctx, urlStr, start, end, dst, nil)
}

// fetchRange 为 FetchRange 的私有变体：headers 非 nil 时替代全局透传头快照
//（HLS 跨主机段剥离凭据后传入）。限速器仍取全局快照（配额跨协议共享不变）。
func (t *Transport) fetchRange(ctx context.Context, urlStr string, start, end int64, dst io.WriterAt, headers map[string]string) error {
	if err := t.validateURL(urlStr); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return err
	}
	hdrs := headers
	if hdrs == nil {
		_, hdrs = t.snapshot()
	}
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	applyUserAgent(req, hdrs)
	t.applyCookies(req, hdrs)
	sentRange := false
	switch {
	case end > start:
		// 有界区间（含 start=0 的首分片）：必须发送 Range，否则 200 全量会错位
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end-1))
		sentRange = true
	case start > 0 && end == 0:
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", start))
		sentRange = true
	}
	// 故障注入（测试用）
	if t.faults != nil {
		if t.faults.DisconnectN.Load() > 0 {
			t.faults.DisconnectN.Add(-1)
			return errors.New("fault: connection reset")
		}
		if t.faults.TimeoutN.Load() > 0 {
			t.faults.TimeoutN.Add(-1)
			return errors.New("fault: timeout")
		}
		if t.faults.TooManyN.Load() > 0 {
			t.faults.TooManyN.Add(-1)
			return &httpError{status: 429}
		}
		if t.faults.ServerErrN.Load() > 0 {
			t.faults.ServerErrN.Add(-1)
			return &httpError{status: 503}
		}
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
		// OK
	case http.StatusOK:
		// 服务器忽略了 Range：分片请求返回全量内容必然错位，拒绝
		if sentRange {
			return nonRetryable(fmt.Errorf("network: 服务器不支持 Range（请求 [%d,%d) 返回 200 全量）", start, end))
		}
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return &httpError{status: resp.StatusCode, retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	case http.StatusInternalServerError, http.StatusBadGateway:
		return &httpError{status: resp.StatusCode, retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	default:
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return nonRetryable(fmt.Errorf("network: http %d", resp.StatusCode))
		}
		return fmt.Errorf("network: 意外状态码 %d", resp.StatusCode)
	}
	// dst 为 io.WriterAt：响应体从「逻辑偏移 0」顺序写入，物理落盘偏移由
	// WriterAt 适配器（如 cli.progressWriterAt）自行加上 Range 起点承担。
	// 响应体限制在期望长度内并校验完整性；固定 64KiB 缓冲（H-2）；
	// 配置了全局限速时以平滑节奏读取（多连接共享配额）。
	expected := int64(-1) // -1 = open-ended，读到 EOF
	if end > start {
		expected = end - start
	}
	srcBody := io.Reader(resp.Body)
	if l, _ := t.snapshot(); l != nil {
		srcBody = &throttledReader{ctx: ctx, r: resp.Body, l: l}
	}
	return writeWriterAt(dst, srcBody, expected)
}

// writeWriterAt 将 Reader 数据顺序写入 WriterAt，从偏移 0 起累计。
// 物理落盘偏移由 WriterAt 实现（如 progressWriterAt）叠加 base 承担，
// 本函数只保证「第 N 字节对应 WriteAt(_, N)」的连续语义。
// expected >= 0 时校验收到的字节数恰好等于期望（响应不完整即报错）。
// 固定 64KiB 缓冲，全程内存占用与文件大小无关（H-1/H-2）。
func writeWriterAt(dst io.WriterAt, src io.Reader, expected int64) error {
	buf := make([]byte, 64<<10) // 64 KiB
	var written int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.WriteAt(buf[:n], written); werr != nil {
				return werr
			}
			written += int64(n)
		}
		if err != nil {
			if err == io.EOF {
				if expected >= 0 && written != expected {
					return fmt.Errorf("network: 响应不完整: 收到 %d 字节, 期望 %d", written, expected)
				}
				return nil
			}
			return err
		}
	}
}

// validateURL 校验 URL：仅允许 http(s)；默认要求主机为 127.0.0.0/8 回环（H-3），
// Transport 构造时显式 allowRemote=true 才放行公网目标（产品开关，默认关闭）。
// 对域名形式（localhost 等）立即解析并断言全部解析结果满足约束，
// 杜绝"ParseIP=nil 提前放行"的漏洞（H-3 闭环）。
func (t *Transport) validateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return fmt.Errorf("unsupported scheme: %s (only http/https; H-4 offline)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("empty host")
	}
	// 代理模式：目标 DNS 解析与可达性交给代理（本端不预解析目标域，
	// 否则代理场景下的目标域名会在本端产生一次多余的 DNS 泄露）。
	if t.proxyConfigured() {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsLoopback() && !t.allowRemote {
			return fmt.Errorf("host %s not loopback (H-3)", ip)
		}
		return nil
	}
	// 域名形式：必须能解析（防止 DNS 劫持需核对解析结果满足约束）
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("cannot resolve %s: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("no IP for %s", host)
	}
	if !t.allowRemote {
		for _, candidate := range ips {
			if !candidate.IsLoopback() {
				return fmt.Errorf("host %s resolves to non-loopback %s (H-3)", host, candidate)
			}
		}
	}
	return nil
}

// Meta 返回资源响应头（状态行 + Header；对标 curl -I / wget --spider）。
// HEAD 优先；被服务器拒绝时回退 Range GET bytes=0-0（与 probe 同构）。
// 仅 http(s)；失败返回错误。
func (t *Transport) Meta(ctx context.Context, urlStr string) (string, http.Header, error) {
	if err := t.validateURL(urlStr); err != nil {
		return "", nil, err
	}
	for _, mk := range []struct {
		method string
		ranged bool
	}{{http.MethodHead, false}, {http.MethodGet, true}} {
		req, err := http.NewRequestWithContext(ctx, mk.method, urlStr, nil)
		if err != nil {
			return "", nil, err
		}
		if mk.ranged {
			req.Header.Set("Range", "bytes=0-0")
		}
		hdrs := t.snapshotHeaders()
		for k, v := range hdrs {
			req.Header.Set(k, v)
		}
		applyUserAgent(req, hdrs)
		t.applyCookies(req, hdrs)
		resp, err := t.client.Do(req)
		if err != nil {
			return "", nil, err
		}
		status := resp.Proto + " " + resp.Status
		hdr := resp.Header.Clone()
		_ = resp.Body.Close()
		return status, hdr, nil
	}
	return "", nil, errors.New("meta: 无可用请求方法")
}

// FinalURL 返回重定向解析后的最终 URL（对标 wget --spider 的最终地址展示；
// http.Client 自动跟随跳转，resp.Request.URL 即最终请求地址）。失败返回空串。
// 仅 http(s)。供 porter probe / MCP download_probe 输出。
func (t *Transport) FinalURL(ctx context.Context, urlStr string) string {
	if err := t.validateURL(urlStr); err != nil {
		return ""
	}
	for _, mk := range []struct {
		method string
		ranged bool
	}{{http.MethodHead, false}, {http.MethodGet, true}} {
		req, err := http.NewRequestWithContext(ctx, mk.method, urlStr, nil)
		if err != nil {
			return ""
		}
		if mk.ranged {
			req.Header.Set("Range", "bytes=0-0")
		}
		hdrs := t.snapshotHeaders()
		for k, v := range hdrs {
			req.Header.Set(k, v)
		}
		applyUserAgent(req, hdrs)
		t.applyCookies(req, hdrs)
		resp, err := t.client.Do(req)
		if err != nil {
			return ""
		}
		final := resp.Request.URL.String()
		_ = resp.Body.Close()
		return final
	}
	return ""
}

// ContentFilename 返回服务端建议文件名（RFC 6266 Content-Disposition 的
// filename 参数；RFC 5987 filename* 优先）。无该头或解析失败返回空串。
// 供 cli 自动命名使用（对标 IDM/Gopeed 的默认行为）。仅 http(s)。
func (t *Transport) ContentFilename(ctx context.Context, urlStr string) string {
	if err := t.validateURL(urlStr); err != nil {
		return ""
	}
	// HEAD 优先；被服务器拒绝时回退 Range GET bytes=0-0（与 probe 同构）
	for _, mk := range []struct {
		method string
		ranged bool
	}{{http.MethodHead, false}, {http.MethodGet, true}} {
		req, err := http.NewRequestWithContext(ctx, mk.method, urlStr, nil)
		if err != nil {
			return ""
		}
		if mk.ranged {
			req.Header.Set("Range", "bytes=0-0")
		}
		hdrs := t.snapshotHeaders()
		for k, v := range hdrs {
			req.Header.Set(k, v)
		}
		applyUserAgent(req, hdrs)
		t.applyCookies(req, hdrs)
		resp, err := t.client.Do(req)
		if err != nil {
			return ""
		}
		cd := resp.Header.Get("Content-Disposition")
		_ = resp.Body.Close()
		if cd != "" {
			if name := parseContentDisposition(cd); name != "" {
				return name
			}
		}
		// 4xx：不再重试另一方法；5xx/其他：尝试下一方法
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return ""
		}
	}
	return ""
}

// parseContentDisposition 解析 Content-Disposition 的文件名。
// filename*=UTF-8''<percent-encoded>（RFC 5987）优先于普通 filename；
// 支持 quoted-string（处理 \" 与 \\ 转义）与裸 token 两种形态。
func parseContentDisposition(v string) string {
	// RFC 5987 扩展参数优先（编码信息更权威）
	if i := strings.Index(v, "filename*="); i >= 0 {
		raw := v[i+len("filename*="):]
		if j := strings.IndexAny(raw, ";"); j >= 0 {
			raw = raw[:j]
		}
		raw = strings.Trim(strings.TrimSpace(raw), `"`)
		// 形如 charset'lang'percent-encoded
		if parts := strings.SplitN(raw, "'", 3); len(parts) == 3 {
			if dec, err := url.QueryUnescape(parts[2]); err == nil && dec != "" {
				return dec
			}
		}
	}
	if i := strings.Index(v, "filename="); i >= 0 {
		raw := strings.TrimSpace(v[i+len("filename="):])
		if j := strings.IndexByte(raw, ';'); j >= 0 {
			raw = strings.TrimSpace(raw[:j])
		}
		if len(raw) >= 2 && raw[0] == '"' {
			// quoted-string：处理转义
			var sb strings.Builder
			for k := 1; k < len(raw); k++ {
				if raw[k] == '\\' && k+1 < len(raw) {
					k++
					sb.WriteByte(raw[k])
					continue
				}
				if raw[k] == '"' {
					break
				}
				sb.WriteByte(raw[k])
			}
			return sb.String()
		}
		if raw != "" {
			return raw
		}
	}
	return ""
}

// httpError 携带状态码与服务端 Retry-After 建议的错误。
type httpError struct {
	status     int
	retryAfter time.Duration // >0 表示服务端显式建议等待（Retry-After 头）
}

func (e *httpError) Error() string {
	if e.retryAfter > 0 {
		return "http " + strconv.Itoa(e.status) + " (Retry-After " + e.retryAfter.String() + ")"
	}
	return "http " + strconv.Itoa(e.status)
}
func (e *httpError) Status() int { return e.status }

// parseRetryAfter 解析 Retry-After（RFC 7231）：仅接受秒数形式；HTTP-date 或
// 非法值返回 0（回退本地退避策略）。
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n <= 0 {
			return 0
		}
		return time.Duration(n) * time.Second
	}
	return 0
}

// Get 公开的有界 GET（≤max 字节，超出即错），走完整校验/UA/透传头/重定向策略。
// 供链接发现（页面抓取）与 HLS 播放列表、AES 密钥、Metalink 元文件等有界元数据拉取使用。
func (t *Transport) Get(ctx context.Context, urlStr string, max int64) ([]byte, error) {
	return t.getBounded(ctx, urlStr, max)
}

// getBounded GET 全文（≤max 字节，超出即错），走完整校验/UA/透传头/重定向策略。
// 供 HLS 播放列表、AES 密钥、Metalink 元文件等「有界元数据」拉取使用。
func (t *Transport) getBounded(ctx context.Context, urlStr string, max int64) ([]byte, error) {
	if err := t.validateURL(urlStr); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	hdrs := t.snapshotHeaders()
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	applyUserAgent(req, hdrs)
	t.applyCookies(req, hdrs)
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nonRetryable(fmt.Errorf("network: GET %d", resp.StatusCode))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("network: 响应超过上限 %d 字节", max)
	}
	return data, nil
}

// openStream 打开 GET 响应体流（调用方负责 Close）；返回 (流, Content-Length 或 -1, 错误)。
// 供加密 HLS 段流式解密使用（不驻留整段）。
func (t *Transport) openStream(ctx context.Context, urlStr string, headers map[string]string) (io.ReadCloser, int64, error) {
	if err := t.validateURL(urlStr); err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, 0, err
	}
	hdrs := headers
	if hdrs == nil {
		hdrs = t.snapshotHeaders()
	}
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	applyUserAgent(req, hdrs)
	t.applyCookies(req, hdrs)
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		body := resp.Body
		_ = body.Close()
		return nil, 0, nonRetryable(fmt.Errorf("network: GET %d", resp.StatusCode))
	}
	return resp.Body, resp.ContentLength, nil
}

// nonRetryable 包装为不可重试错误（Retryable 据此返回 false）。
type nonRetryableError struct{ err error }

func nonRetryable(err error) error        { return nonRetryableError{err} }
func (e nonRetryableError) Error() string { return e.err.Error() }
func (e nonRetryableError) Unwrap() error { return e.err }
