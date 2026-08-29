// hls.go 实现 HLS（HTTP Live Streaming，RFC 8216）传输层（第 13 轮新增）。
// 设计：媒体播放列表解析为**段序列**，按明文长度映射为一段连续虚拟字节空间；
// Probe 返回虚拟总长（Range=true），cli 引擎的分片并行/工作窃取/字节级续传/校验
// 全部零改动复用——本层把虚拟区间翻译为 1..n 个真实段请求（边缘段发子 Range）。
//
// 合规边界（与 Transport/FTP 同层）：
//   - 仅 VOD：无 #EXT-X-ENDLIST（直播流）直接拒绝——下载任务必须有限；
//   - 资源上限：播放列表 ≤1MiB / 段数 ≤2048 / 单段 ≤64MiB / 密钥 ≤8（防滥用）；
//   - 跨主机段请求剥离 Cookie/Authorization/Proxy-Authorization（与重定向策略同源）；
//   - 加密仅支持 METHOD=AES-128；SAMPLE-AES 等显式拒绝（不做 DRM 绕过）；
//   - AES-128 播放列表退化为顺序全量：密文含 PKCS7 填充，明文长度不可预知，
//     虚拟映射不成立（Probe 返回 size=0 → 引擎单连接流式）。诚实降级，续传不可用。
package network

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// 资源上限（合规：所有外部输入有界）。
const (
	maxPlaylistBytes   = 1 << 20         // 播放列表上限 1 MiB
	maxHLSSegments     = 2048            // 段数上限
	maxHLSSegmentBytes = int64(64 << 20) // 单段（明文）上限 64 MiB
	maxHLSKeyBytes     = 4 << 10         // 密钥响应上限
	maxHLSKeys         = 8               // 单播放列表密钥数上限
)

// HLSTransport 实现 HLS 的 Fetcher。基于已有 *Transport 发起全部 HTTP 请求，
// 复用其回环校验（H-3）/UA/透传头/限速/重定向策略/故障注入。
type HLSTransport struct {
	tr    *Transport
	mu    sync.Mutex
	plans map[string]*hlsPlan // 原始播放列表 URL → 已解析虚拟映射
}

// NewHLSTransport 构造 HLS 传输层（tr 为共享 HTTP 传输层）。
func NewHLSTransport(tr *Transport) *HLSTransport {
	return &HLSTransport{tr: tr, plans: make(map[string]*hlsPlan)}
}

// IsHLSURL 判断 URL 是否按 HLS 处理：http(s) 且路径（去查询串）以 .m3u8 结尾。
func IsHLSURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return strings.HasSuffix(strings.ToLower(u.Path), ".m3u8")
}

// hlsKey 一条 EXT-X-KEY 的解析结果。
type hlsKey struct {
	uri   string
	iv    [16]byte
	ivSet bool
}

// hlsSegment 一个媒体段（虚拟空间中的连续区间）。
type hlsSegment struct {
	url       string // 绝对 URL
	byteStart int64  // EXT-X-BYTERANGE 起点（0=整资源）
	byteLen   int64  // BYTERANGE 长度（0=整资源，长度由探测得出）
	start     int64  // 虚拟空间起点（明文偏移）
	size      int64  // 明文长度（加密流式模式恒 0）
	seq       int64  // 媒体序列号（缺省 IV 依据）
	key       *hlsKey
}

// hlsPlan 已解析的播放列表（构建后只读，多协程并发读安全；密钥缓存由 kmu 保护）。
type hlsPlan struct {
	segments     []hlsSegment
	total        int64
	encrypted    bool
	playlistHost string
	baseHeaders  map[string]string // 播放列表宿主的透传头（跨主机段剥离凭据后使用）
	keys         map[string][]byte
	kmu          sync.Mutex
}

// offsetWriter 把「逻辑偏移 0」的写入平移 base（dst 仍按绝对虚拟偏移写）。
type offsetWriter struct {
	dst  io.WriterAt
	base int64
}

func (w *offsetWriter) WriteAt(p []byte, off int64) (int, error) {
	return w.dst.WriteAt(p, w.base+off)
}

// Probe 解析播放列表并（明文模式）探测各段长度，返回虚拟总长。
// 加密模式返回 (0,false,nil)——引擎自动退化为流式单连接。
func (h *HLSTransport) Probe(ctx context.Context, urlStr string) (int64, bool, error) {
	p, err := h.plan(ctx, urlStr)
	if err != nil {
		return 0, false, err
	}
	if p.encrypted {
		return 0, false, nil
	}
	return p.total, true, nil
}

// plan 取（或构建并缓存）播放列表的虚拟映射。
func (h *HLSTransport) plan(ctx context.Context, urlStr string) (*hlsPlan, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if p, ok := h.plans[urlStr]; ok {
		return p, nil
	}
	p, err := h.buildPlan(ctx, urlStr)
	if err != nil {
		return nil, err
	}
	h.plans[urlStr] = p
	return p, nil
}

// buildPlan 拉取播放列表（主列表→选流，深度 ≤1）→ 解析 → 明文模式并发探测段长。
func (h *HLSTransport) buildPlan(ctx context.Context, urlStr string) (*hlsPlan, error) {
	body, mediaURL, err := h.fetchPlaylist(ctx, urlStr)
	if err != nil {
		return nil, err
	}
	parsed, err := parsePlaylist(mediaURL, body)
	if err != nil {
		return nil, err
	}
	if parsed.master {
		if len(parsed.variants) == 0 {
			return nil, fmt.Errorf("hls: 主播放列表无变体")
		}
		best := parsed.variants[0]
		for _, v := range parsed.variants[1:] {
			if v.bandwidth > best.bandwidth {
				best = v // 取最高码率变体
			}
		}
		body, mediaURL, err = h.fetchPlaylist(ctx, best.url)
		if err != nil {
			return nil, err
		}
		if parsed, err = parsePlaylist(mediaURL, body); err != nil {
			return nil, err
		}
		if parsed.master {
			return nil, fmt.Errorf("hls: 主播放列表嵌套超过 1 层")
		}
	}
	if !parsed.endlist {
		return nil, fmt.Errorf("hls: 直播流（无 #EXT-X-ENDLIST）不支持——下载任务必须有限")
	}
	if len(parsed.segments) == 0 {
		return nil, fmt.Errorf("hls: 播放列表无媒体段")
	}
	if len(parsed.segments) > maxHLSSegments {
		return nil, fmt.Errorf("hls: 段数 %d 超过上限 %d", len(parsed.segments), maxHLSSegments)
	}

	p := &hlsPlan{playlistHost: mediaURL.Host, baseHeaders: h.tr.snapshotHeaders(),
		keys: make(map[string][]byte)}
	for _, s := range parsed.segments {
		if s.key != nil {
			p.encrypted = true
		}
		p.segments = append(p.segments, s)
	}
	if !p.encrypted {
		if err := h.probeSizes(ctx, p); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// fetchPlaylist 取播放列表正文（主/媒体通用，上限 maxPlaylistBytes）。
func (h *HLSTransport) fetchPlaylist(ctx context.Context, raw string) ([]byte, *url.URL, error) {
	body, err := h.tr.getBounded(ctx, raw, maxPlaylistBytes)
	if err != nil {
		return nil, nil, err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, nil, err
	}
	return body, u, nil
}

// probeSizes 并发（≤16）探测各段明文长度并构建虚拟偏移。任一段失败/未知即报错。
func (h *HLSTransport) probeSizes(ctx context.Context, p *hlsPlan) error {
	sizes := make([]int64, len(p.segments))
	errs := make([]error, len(p.segments))
	sem := make(chan struct{}, 16)
	var wg sync.WaitGroup
	for i := range p.segments {
		seg := &p.segments[i]
		if seg.byteLen > 0 {
			sizes[i] = seg.byteLen // BYTERANGE 长度清单已给出，免探测
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, u string) {
			defer wg.Done()
			defer func() { <-sem }()
			n, _, err := h.tr.probe(ctx, u, h.headersFor(p, u))
			if err != nil {
				errs[i] = err
				return
			}
			if n <= 0 {
				errs[i] = fmt.Errorf("hls: 段大小未知（需服务端提供 Content-Length）")
				return
			}
			if n > maxHLSSegmentBytes {
				errs[i] = fmt.Errorf("hls: 段大小 %d 超过上限 %d", n, maxHLSSegmentBytes)
				return
			}
			sizes[i] = n
		}(i, seg.url)
	}
	wg.Wait()
	var start int64
	for i := range p.segments {
		if errs[i] != nil {
			return fmt.Errorf("hls: 段 %d 探测失败: %w", i, errs[i])
		}
		p.segments[i].start = start
		p.segments[i].size = sizes[i]
		start += sizes[i]
	}
	p.total = start
	return nil
}

// headersFor 计算段请求的透传头：同宿主原样；跨宿主剥离凭据头（与重定向策略同源）。
func (h *HLSTransport) headersFor(p *hlsPlan, target string) map[string]string {
	if len(p.baseHeaders) == 0 {
		return nil
	}
	if hostOf(target) == p.playlistHost {
		return p.baseHeaders
	}
	out := make(map[string]string, len(p.baseHeaders))
	for k, v := range p.baseHeaders {
		switch http.CanonicalHeaderKey(k) {
		case "Cookie", "Authorization", "Proxy-Authorization":
			continue
		}
		out[k] = v
	}
	return out
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return strings.ToLower(u.Host)
	}
	return ""
}

// FetchRange 下载虚拟区间 [start,end)（end=0 表示到结尾）；加密流仅接受 (0,0) 全量。
func (h *HLSTransport) FetchRange(ctx context.Context, urlStr string, start, end int64, dst io.WriterAt) error {
	p, err := h.plan(ctx, urlStr)
	if err != nil {
		return err
	}
	if p.encrypted {
		if start != 0 || end != 0 {
			return fmt.Errorf("hls: 加密流仅支持顺序全量下载（PKCS7 明文长度不可预知，无续传）")
		}
		return h.fetchSequential(ctx, p, dst)
	}
	if end == 0 {
		end = p.total
	}
	if end > p.total {
		end = p.total
	}
	if start >= end {
		return nil
	}
	off := start
	for i := range p.segments {
		seg := &p.segments[i]
		segEnd := seg.start + seg.size
		if segEnd <= off {
			continue
		}
		if seg.start >= end {
			break
		}
		rel0 := off - seg.start
		if rel0 < 0 {
			rel0 = 0
		}
		rel1 := segEnd
		if segEnd > end {
			rel1 = end
		}
		rel1 -= seg.start
		ow := &offsetWriter{dst: dst, base: seg.start + rel0 - start}
		if err := h.tr.fetchRange(ctx, seg.url, rel0, rel1, ow, h.headersFor(p, seg.url)); err != nil {
			return err
		}
		off = seg.start + rel1
	}
	if off != end {
		return fmt.Errorf("hls: 段映射未覆盖 [%d,%d)（播放列表可能已变更）", start, end)
	}
	return nil
}

// fetchSequential 加密流顺序下载：逐段取密文流式解密写入（内存 64KiB 级，与段长无关）。
func (h *HLSTransport) fetchSequential(ctx context.Context, p *hlsPlan, dst io.WriterAt) error {
	var off int64
	buf := make([]byte, 64<<10)
	for i := range p.segments {
		seg := &p.segments[i]
		if seg.key == nil {
			return fmt.Errorf("hls: 加密流中出现未加密段（播放列表不一致）")
		}
		key, err := h.keyFor(ctx, p, seg.key)
		if err != nil {
			return err
		}
		iv := hlsIV(seg.key, seg.seq)
		body, cl, err := h.tr.openStream(ctx, seg.url, h.headersFor(p, seg.url))
		if err != nil {
			return err
		}
		if cl >= 0 {
			if cl > maxHLSSegmentBytes+16 {
				body.Close()
				return fmt.Errorf("hls: 段密文 %d 超过上限", cl)
			}
		}
		cr, err := newCBCReader(body, key, iv)
		if err != nil {
			body.Close()
			return err
		}
		var written int64
		for {
			n, rerr := cr.Read(buf)
			if n > 0 {
				if _, werr := dst.WriteAt(buf[:n], off+written); werr != nil {
					cr.Close()
					return werr
				}
				written += int64(n)
			}
			if rerr != nil {
				cr.Close()
				if rerr == io.EOF {
					break
				}
				return fmt.Errorf("hls: 段 %d 解密流失败: %w", i, rerr)
			}
		}
		off += written
	}
	return nil
}

// keyFor 懒取并缓存密钥（≤16 字节，数量上限 maxHLSKeys）。
func (h *HLSTransport) keyFor(ctx context.Context, p *hlsPlan, k *hlsKey) ([16]byte, error) {
	var out [16]byte
	p.kmu.Lock()
	defer p.kmu.Unlock()
	if b, ok := p.keys[k.uri]; ok {
		copy(out[:], b)
		return out, nil
	}
	if len(p.keys) >= maxHLSKeys {
		return out, fmt.Errorf("hls: 密钥数超过上限 %d", maxHLSKeys)
	}
	data, err := h.tr.getBounded(ctx, k.uri, maxHLSKeyBytes)
	if err != nil {
		return out, err
	}
	if len(data) != 16 {
		return out, fmt.Errorf("hls: 密钥长度 %d != 16", len(data))
	}
	p.keys[k.uri] = append([]byte(nil), data...)
	copy(out[:], data)
	return out, nil
}

// hlsIV 计算段 IV：显式 IV 优先；缺省 = 媒体序列号的 128 位大端（RFC 8216 §5.2）。
func hlsIV(k *hlsKey, seq int64) [16]byte {
	if k.ivSet {
		return k.iv
	}
	var iv [16]byte
	binary.BigEndian.PutUint64(iv[8:], uint64(seq))
	return iv
}

// ---------------------------------------------------------------------------
// 播放列表解析（纯函数，可独立单测）
// ---------------------------------------------------------------------------

type hlsVariant struct {
	url       string
	bandwidth int64
}

type parsedPlaylist struct {
	master    bool
	variants  []hlsVariant
	segments  []hlsSegment
	endlist   bool
	encrypted bool
}

// parsePlaylist 解析 m3u8 正文（media 或 master）。u 为播放列表 URL（相对段基于其解析）。
func parsePlaylist(u *url.URL, body []byte) (*parsedPlaylist, error) {
	out := &parsedPlaylist{}
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	first := true
	curKey := (*hlsKey)(nil)
	pendingVariant := int64(-1) // ≥0 表示上一行为 EXT-X-STREAM-INF（待变体 URI）
	pendingSeg := false         // 见到 EXTINF，等待段 URI
	pendingRangeLen := int64(-1)
	pendingRangeOff := int64(-1) // -1 表示未显式给出
	prevRangeURL := ""
	prevRangeEnd := int64(0)
	seq := int64(0)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		line = strings.TrimSpace(line)
		if first {
			if line != "#EXTM3U" {
				return nil, fmt.Errorf("hls: 缺少 #EXTM3U 头")
			}
			first = false
			continue
		}
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "#") { // URI 行（段或变体）
			ref, err := url.Parse(line)
			if err != nil {
				return nil, fmt.Errorf("hls: 非法 URI %q: %w", line, err)
			}
			resolved := u.ResolveReference(ref)
			if pendingVariant >= 0 {
				out.master = true
				out.variants = append(out.variants, hlsVariant{url: resolved.String(), bandwidth: pendingVariant})
				pendingVariant = -1
				continue
			}
			byteStart, byteLen := int64(0), int64(0)
			if pendingRangeLen >= 0 {
				byteLen = pendingRangeLen
				byteStart = pendingRangeOff
				if byteStart < 0 { // 缺省偏移 = 同资源上一段终点（RFC 8216 §4.3.2.2）
					if prevRangeURL != resolved.String() {
						return nil, fmt.Errorf("hls: BYTERANGE 缺省偏移要求与前段同资源")
					}
					byteStart = prevRangeEnd
				}
				prevRangeURL = resolved.String()
				prevRangeEnd = byteStart + byteLen
				pendingRangeLen, pendingRangeOff = -1, -1
			}
			out.segments = append(out.segments, hlsSegment{
				url: resolved.String(), byteStart: byteStart, byteLen: byteLen,
				seq: seq, key: curKey,
			})
			if curKey != nil {
				out.encrypted = true
			}
			seq++
			pendingSeg = false
			continue
		}
		// 标签行
		switch {
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			attrs, err := parseHLSAttrs(line[len("#EXT-X-STREAM-INF:"):])
			if err != nil {
				return nil, err
			}
			bw := int64(0)
			for _, kv := range attrs {
				if kv[0] == "BANDWIDTH" {
					if n, e := strconv.ParseInt(kv[1], 10, 64); e == nil && n > 0 {
						bw = n
					}
				}
			}
			pendingVariant = bw
		case strings.HasPrefix(line, "#EXT-X-KEY:"):
			attrs, err := parseHLSAttrs(line[len("#EXT-X-KEY:"):])
			if err != nil {
				return nil, err
			}
			curKey, err = parseHLSKey(attrs, u)
			if err != nil {
				return nil, err
			}
		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			attrs, err := parseHLSAttrs(line[len("#EXT-X-MAP:"):])
			if err != nil {
				return nil, err
			}
			uriAttr := ""
			for _, kv := range attrs {
				if kv[0] == "URI" {
					uriAttr = kv[1]
				}
			}
			if uriAttr == "" {
				return nil, fmt.Errorf("hls: EXT-X-MAP 缺少 URI")
			}
			ref, err := url.Parse(uriAttr)
			if err != nil {
				return nil, err
			}
			out.segments = append(out.segments, hlsSegment{
				url: u.ResolveReference(ref).String(), seq: seq, key: curKey,
			})
			if curKey != nil {
				out.encrypted = true
			}
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			if n, err := strconv.ParseInt(strings.TrimSpace(line[len("#EXT-X-MEDIA-SEQUENCE:"):]), 10, 64); err == nil && n >= 0 {
				seq = n
			}
		case strings.HasPrefix(line, "#EXT-X-BYTERANGE:"):
			spec := strings.TrimSpace(line[len("#EXT-X-BYTERANGE:"):])
			offPart := ""
			if i := strings.IndexByte(spec, '@'); i >= 0 {
				offPart = spec[i+1:]
				spec = spec[:i]
			}
			n, err := strconv.ParseInt(spec, 10, 64)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("hls: BYTERANGE 长度非法 %q", spec)
			}
			pendingRangeLen = n
			pendingRangeOff = -1
			if offPart != "" {
				o, err := strconv.ParseInt(offPart, 10, 64)
				if err != nil || o < 0 {
					return nil, fmt.Errorf("hls: BYTERANGE 偏移非法 %q", offPart)
				}
				pendingRangeOff = o
			}
		case line == "#EXT-X-ENDLIST":
			out.endlist = true
		case strings.HasPrefix(line, "#EXTINF"):
			pendingSeg = true
		default:
			// 未知标签：忽略（前向兼容）
		}
	}
	if first {
		return nil, fmt.Errorf("hls: 空播放列表")
	}
	_ = pendingSeg // 宽松处理：无 EXTINF 的裸 URI 仍按段接收
	return out, nil
}

// parseHLSKey 解析 EXT-X-KEY 属性为密钥描述（METHOD=NONE 返回 nil）。
func parseHLSKey(attrs [][2]string, u *url.URL) (*hlsKey, error) {
	method := ""
	uri := ""
	ivHex := ""
	for _, kv := range attrs {
		switch kv[0] {
		case "METHOD":
			method = kv[1]
		case "URI":
			uri = kv[1]
		case "IV":
			ivHex = kv[1]
		}
	}
	switch method {
	case "", "NONE":
		return nil, nil
	case "AES-128":
		if uri == "" {
			return nil, fmt.Errorf("hls: AES-128 密钥缺少 URI")
		}
		ref, err := url.Parse(uri)
		if err != nil {
			return nil, err
		}
		k := &hlsKey{uri: u.ResolveReference(ref).String()}
		if ivHex != "" {
			raw := strings.TrimPrefix(strings.ToLower(ivHex), "0x")
			b, err := hex.DecodeString(raw)
			if err != nil || len(b) != 16 {
				return nil, fmt.Errorf("hls: IV 非法（需 16 字节十六进制）")
			}
			copy(k.iv[:], b)
			k.ivSet = true
		}
		return k, nil
	default:
		return nil, fmt.Errorf("hls: 加密方法 %q 不支持（不做 DRM 绕过）", method)
	}
}

// parseHLSAttrs 解析 "K1=V1,K2=\"V2\"" 形式的 HLS 属性表（引号内逗号不分割）。
func parseHLSAttrs(s string) ([][2]string, error) {
	var out [][2]string
	i := 0
	for i < len(s) {
		eq := strings.IndexByte(s[i:], '=')
		if eq < 0 {
			if strings.TrimSpace(s[i:]) == "" {
				break
			}
			return nil, fmt.Errorf("hls: 属性缺少 '=': %q", s[i:])
		}
		k := strings.ToUpper(strings.TrimSpace(s[i : i+eq]))
		i += eq + 1
		var v string
		if i < len(s) && s[i] == '"' {
			j := strings.IndexByte(s[i+1:], '"')
			if j < 0 {
				return nil, fmt.Errorf("hls: 属性引号未闭合: %q", s[i:])
			}
			v = s[i+1 : i+1+j]
			i += j + 2
		} else {
			j := strings.IndexByte(s[i:], ',')
			if j < 0 {
				v = s[i:]
				i = len(s)
			} else {
				v = s[i : i+j]
				i += j + 1
			}
		}
		if i < len(s) && s[i] == ',' {
			i++
		}
		out = append(out, [2]string{k, v})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// AES-128-CBC 流式解密（64KiB 块读取 + 尾块滞留 + PKCS7 去填充）
// ---------------------------------------------------------------------------

// cbcReader 从密文流解密出明文流。为在 EOF 前识别 PKCS7 填充，最后一个
// 16 字节明文块始终滞留至下一块或 EOF；稳态内存 = 64KiB + 2 块，与段长无关。
type cbcReader struct {
	src     io.ReadCloser
	mode    cipher.BlockMode
	buf     [64 << 10]byte // 密文读取缓冲
	raw     []byte         // 未解密的块对齐残量（<16）
	pending []byte         // 已解密待读明文
	hold    [16]byte       // 滞留的尾部明文块
	hasHold bool
	srcEOF  bool
	done    bool
	err     error
}

func newCBCReader(src io.ReadCloser, key [16]byte, iv [16]byte) (*cbcReader, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return &cbcReader{src: src, mode: cipher.NewCBCDecrypter(block, iv[:]), raw: make([]byte, 0, 32)}, nil
}

// Close 关闭底层流（调用方在提前退出时调用）。
func (c *cbcReader) Close() error { return c.src.Close() }

func (c *cbcReader) Read(p []byte) (int, error) {
	for len(c.pending) == 0 {
		if c.err != nil {
			return 0, c.err
		}
		if c.done {
			return 0, io.EOF
		}
		if !c.srcEOF {
			n, rerr := c.src.Read(c.buf[:])
			if n > 0 {
				c.raw = append(c.raw, c.buf[:n]...)
			}
			if rerr == io.EOF {
				c.srcEOF = true
			} else if rerr != nil {
				c.err = rerr
				continue
			}
			c.decryptAligned()
		}
		if c.srcEOF {
			if err := c.finish(); err != nil {
				c.err = err
				continue
			}
		}
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

// decryptAligned 解密 raw 中全部完整块，滞留最后一个明文块。
func (c *cbcReader) decryptAligned() {
	aligned := len(c.raw) - len(c.raw)%16
	if aligned <= 0 {
		return
	}
	dec := make([]byte, aligned)
	c.mode.CryptBlocks(dec, c.raw[:aligned])
	keep := copy(c.raw, c.raw[aligned:])
	c.raw = c.raw[:keep]
	if c.hasHold {
		c.pending = append(c.pending, c.hold[:]...)
	}
	if aligned >= 16 {
		c.pending = append(c.pending, dec[:aligned-16]...)
		copy(c.hold[:], dec[aligned-16:])
		c.hasHold = true
	}
}

// finish EOF 收尾：密文必须块对齐，滞留块去 PKCS7 填充后吐出。
func (c *cbcReader) finish() error {
	if len(c.raw) != 0 {
		return fmt.Errorf("hls: 密文长度非块对齐")
	}
	if !c.hasHold {
		return fmt.Errorf("hls: 空的加密段")
	}
	pad := int(c.hold[15])
	if pad < 1 || pad > 16 {
		return fmt.Errorf("hls: PKCS7 填充非法")
	}
	for i := 16 - pad; i < 16; i++ {
		if int(c.hold[i]) != pad {
			return fmt.Errorf("hls: PKCS7 填充非法")
		}
	}
	c.pending = append(c.pending, c.hold[:16-pad]...)
	c.hasHold = false
	c.done = true
	return nil
}
