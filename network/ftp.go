// ftp.go 实现协议层 FTP/FTPS（显式 TLS）客户端：纯标准库（协议应答自行解析）。
// 安全合规（与 Transport 同一边界，H-3）：
//   - 控制连接与数据连接均强制回环解析（allowRemote=false 时拒绝公网目标）；
//   - PASV 反弹攻击防御（RFC 2577）：被动模式返回的地址必须与控制连接对端一致，
//     否则拒绝连接——杜绝「客户端被诱导向第三方发起 TCP 连接」；
//   - FTPS 证书强制校验（不提供 InsecureSkipVerify 等价物）。
// Range 语义：REST 偏移 + RETR 流式，分片区间由读取端严格限长（与 HTTP 路径同约定）。
package network

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// FTPTransport 实现 FTP/FTPS 传输层。每次 Probe/FetchRange 独立建连
// （无连接池：协议有状态，复用的正确性成本高于 localhost 建连成本）。
type FTPTransport struct {
	allowRemote bool
	tlsCfg      *tls.Config // 非空时替换默认 TLS 配置（仅测试注入 RootCAs）
}

// NewFTPTransport 构造 FTP 传输层（allowRemote 语义同 NewTransport，H-3）。
func NewFTPTransport(allowRemote bool) *FTPTransport {
	return &FTPTransport{allowRemote: allowRemote}
}

// ftpRequest 解析后的 FTP(S) 下载请求。
type ftpRequest struct {
	host string
	port int
	user string
	pass string
	path string
	tls  bool // ftps:// 显式 TLS（AUTH TLS + PROT P）
}

// parseFTPURL 解析并校验 ftp(s):// URL。凭据取自 userinfo（缺省匿名）。
func parseFTPURL(raw string, allowRemote bool) (*ftpRequest, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	var tlsOn bool
	switch u.Scheme {
	case "ftp":
	case "ftps":
		tlsOn = true
	default:
		return nil, fmt.Errorf("unsupported scheme: %s (ftp/ftps)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("empty host")
	}
	port := 21
	if tlsOn {
		port = 990
	}
	if p := u.Port(); p != "" {
		port, err = strconv.Atoi(p)
		if err != nil || port <= 0 || port > 65535 {
			return nil, fmt.Errorf("invalid port: %s", p)
		}
	}
	if u.Path == "" {
		return nil, fmt.Errorf("ftp URL 缺少文件路径")
	}
	if u.Path == "/" {
		// 根路径合法（目录列取）；下载路径 / 会在 SIZE/RETR 阶段自然失败
	}
	// 回环强制（H-3）：与 HTTP 路径同规则——IP 字面量直接断言，域名解析结果全量断言。
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsLoopback() && !allowRemote {
			return nil, fmt.Errorf("host %s not loopback (H-3)", ip)
		}
	} else {
		ips, err := net.LookupIP(host)
		if err != nil {
			return nil, fmt.Errorf("cannot resolve %s: %w", host, err)
		}
		if !allowRemote {
			for _, candidate := range ips {
				if !candidate.IsLoopback() {
					return nil, fmt.Errorf("host %s resolves to non-loopback %s (H-3)", host, candidate)
				}
			}
		}
	}
	req := &ftpRequest{host: host, port: port, path: u.Path, tls: tlsOn}
	if u.User != nil {
		req.user = u.User.Username()
		req.pass, _ = u.User.Password()
	}
	if req.user == "" {
		req.user, req.pass = "anonymous", "porter"
	}
	return req, nil
}

// ftpConn 一条 FTP 控制连接（协议应答自行解析，支持多行 1xx-5xx）。
type ftpConn struct {
	conn    net.Conn
	br      *bufio.Reader
	bw      *bufio.Writer
	ctrlIP  net.IP // 控制连接对端 IP（PASV bounce 防御基准）
	dataTLS bool   // PROT P 生效：数据连接须 TLS 包装
	tlsCfg  *tls.Config
}

// dialControl 建立控制连接并完成 greeting/TLS/login。
func (f *FTPTransport) dialControl(ctx context.Context, req *ftpRequest) (*ftpConn, error) {
	conn, ctrlIP, err := dialGuarded(ctx, req.host, req.port, f.allowRemote)
	if err != nil {
		return nil, err
	}
	fc := &ftpConn{conn: conn, br: bufio.NewReader(conn), bw: bufio.NewWriter(conn),
		ctrlIP: ctrlIP, tlsCfg: f.tlsCfg}
	fc.setDeadline(15 * time.Second)
	code, _, err := fc.readReply()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ftp: 读取 greeting 失败: %w", err)
	}
	if code/100 != 2 {
		conn.Close()
		return nil, fmt.Errorf("ftp: 意外 greeting %d", code)
	}
	if req.tls {
		if err := fc.upgradeTLS(ctx, req.host); err != nil {
			conn.Close()
			return nil, err
		}
	}
	if err := fc.login(req); err != nil {
		conn.Close()
		return nil, err
	}
	return fc, nil
}

// upgradeTLS 执行 AUTH TLS → PBSZ 0 → PROT P（RFC 4217 显式 TLS）。
func (fc *ftpConn) upgradeTLS(ctx context.Context, host string) error {
	code, _, err := fc.cmd(2, "AUTH TLS")
	if err != nil {
		return err
	}
	if code != 234 {
		return fmt.Errorf("ftp: AUTH TLS 被拒绝 (%d)", code)
	}
	cfg := fc.effectiveTLSConfig(host)
	tconn := tls.Client(fc.conn, cfg)
	if err := tconn.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("ftp: TLS 握手失败: %w", err)
	}
	fc.conn = tconn
	fc.br.Reset(tconn)
	fc.bw.Reset(tconn)
	if code, _, err := fc.cmd(2, "PBSZ 0"); err != nil || code != 200 {
		if err == nil {
			err = fmt.Errorf("ftp: PBSZ 被拒绝 (%d)", code)
		}
		return err
	}
	if code, _, err := fc.cmd(2, "PROT P"); err != nil || code != 200 {
		if err == nil {
			err = fmt.Errorf("ftp: PROT P 被拒绝 (%d)", code)
		}
		return err
	}
	fc.dataTLS = true
	return nil
}

func (fc *ftpConn) login(req *ftpRequest) error {
	code, _, err := fc.cmd(0, "USER %s", req.user)
	if err != nil {
		return err
	}
	switch code {
	case 230: // 免密登录
		return nil
	case 331: // 需要密码
		code, _, err := fc.cmd(2, "PASS %s", req.pass)
		if err != nil {
			return err
		}
		if code != 230 {
			return fmt.Errorf("ftp: 登录被拒绝 (%d)", code)
		}
		return nil
	default:
		return fmt.Errorf("ftp: USER 被拒绝 (%d)", code)
	}
}

// setDeadline 设置控制连接阶段性超时（每次命令前重置；数据传输阶段由数据连接自理）。
func (fc *ftpConn) setDeadline(d time.Duration) {
	_ = fc.conn.SetDeadline(time.Now().Add(d))
}

// cmd 发送命令并读取应答，expectClass 为 0 时不校验类别（返回码交调用方判断）。
func (fc *ftpConn) cmd(expectClass int, format string, args ...any) (int, string, error) {
	fc.setDeadline(15 * time.Second)
	if _, err := fmt.Fprintf(fc.bw, format+"\r\n", args...); err != nil {
		return 0, "", err
	}
	if err := fc.bw.Flush(); err != nil {
		return 0, "", err
	}
	code, msg, err := fc.readReply()
	if err != nil {
		return 0, "", err
	}
	if expectClass != 0 && code/100 != expectClass {
		return code, msg, fmt.Errorf("ftp: 应答 %d（期望 %dxx）: %s", code, expectClass, msg)
	}
	return code, msg, nil
}

// readReply 读取一条完整 FTP 应答（含多行），返回 (最终码, 文本, 错误)。
func (fc *ftpConn) readReply() (int, string, error) {
	line, err := fc.br.ReadString('\n')
	if err != nil {
		return 0, "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if len(line) < 3 {
		return 0, "", fmt.Errorf("ftp: 应答过短 %q", line)
	}
	code, err := strconv.Atoi(line[:3])
	if err != nil {
		return 0, "", fmt.Errorf("ftp: 应答格式非法 %q", line)
	}
	var b strings.Builder
	b.WriteString(strings.TrimPrefix(line[3:], " "))
	if len(line) > 3 && line[3] == '-' { // 多行：持续读到 "code<space>"
		for {
			l2, err := fc.br.ReadString('\n')
			if err != nil {
				return 0, "", err
			}
			l2 = strings.TrimRight(l2, "\r\n")
			if strings.HasPrefix(l2, strconv.Itoa(code)+" ") {
				b.WriteString("\n" + strings.TrimPrefix(l2, strconv.Itoa(code)+" "))
				break
			}
			b.WriteString("\n" + l2)
		}
	}
	return code, b.String(), nil
}

// effectiveTLSConfig 生成数据/控制连接共用的 TLS 配置（证书强制校验，无跳过开关）。
func (fc *ftpConn) effectiveTLSConfig(host string) *tls.Config {
	if fc.tlsCfg != nil {
		cp := fc.tlsCfg.Clone()
		if cp.ServerName == "" {
			cp.ServerName = host
		}
		return cp
	}
	return &tls.Config{ServerName: host}
}

// openData 建立被动模式数据连接：EPSV 优先（IPv6 兼容、无地址歧义），回退 PASV。
// PASV 返回的地址必须与控制连接对端一致（RFC 2577 bounce 防御），否则拒绝。
func (fc *ftpConn) openData(ctx context.Context, allowRemote bool) (net.Conn, error) {
	if code, msg, err := fc.cmd(0, "EPSV"); err == nil && code == 229 {
		if port, perr := parseEPSVPort(msg); perr == nil {
			return fc.dialData(ctx, fc.ctrlIP.String(), port)
		}
		// EPSV 应答格式异常 → 回退 PASV
	}
	code, msg, err := fc.cmd(0, "PASV")
	if err != nil {
		return nil, err
	}
	if code != 227 {
		return nil, fmt.Errorf("ftp: 被动模式不可用 (%d)", code)
	}
	ip, port, perr := parsePASV(msg)
	if perr != nil {
		return nil, fmt.Errorf("ftp: PASV 应答解析失败: %w", perr)
	}
	if !ip.Equal(fc.ctrlIP) {
		return nil, fmt.Errorf("ftp: 被动模式返回地址 %s 与控制连接对端 %s 不符（bounce 防御）", ip, fc.ctrlIP)
	}
	return fc.dialData(ctx, ip.String(), port)
}

func (fc *ftpConn) dialData(ctx context.Context, host string, port int) (net.Conn, error) {
	conn, _, err := dialGuarded(ctx, host, port, true) // openData 已做地址一致性防御
	if err != nil {
		return nil, err
	}
	if fc.dataTLS {
		tconn := tls.Client(conn, fc.effectiveTLSConfig(host))
		if err := tconn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, fmt.Errorf("ftp: 数据连接 TLS 握手失败: %w", err)
		}
		conn = tconn
	}
	return conn, nil
}

// close 发送 QUIT 并关闭（best-effort，传输后调用）。
func (fc *ftpConn) close() {
	if fc.conn == nil {
		return
	}
	fc.setDeadline(2 * time.Second)
	_, _ = fmt.Fprintf(fc.bw, "QUIT\r\n")
	_ = fc.bw.Flush()
	_, _, _ = fc.readReply()
	_ = fc.conn.Close()
}

// dialGuarded 解析 host:port 并强制回环（allowRemote=false），拨号绑定本地 127.0.0.1（H-3）。
// 返回连接与对端解析 IP（供 bounce 防御比对）。
func dialGuarded(ctx context.Context, host string, port int, allowRemote bool) (net.Conn, net.IP, error) {
	var ctrlIP net.IP
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsLoopback() && !allowRemote {
			return nil, nil, fmt.Errorf("network: 禁止非回环地址 %s (H-3)", ip)
		}
		ctrlIP = ip
	} else {
		ips, err := net.LookupIP(host)
		if err != nil {
			return nil, nil, err
		}
		for _, candidate := range ips {
			if allowRemote || candidate.IsLoopback() {
				ctrlIP = candidate
				break
			}
		}
		if ctrlIP == nil {
			return nil, nil, fmt.Errorf("network: %s 未解析到可用回环地址 (H-3)", host)
		}
	}
	d := &net.Dialer{
		LocalAddr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0},
		Timeout:   5 * time.Second,
	}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ctrlIP.String(), strconv.Itoa(port)))
	if err != nil {
		return nil, nil, err
	}
	return conn, ctrlIP, nil
}

// parseEPSVPort 从 "229 ... (|||port|)" 提取端口。
func parseEPSVPort(msg string) (int, error) {
	i := strings.IndexByte(msg, '(')
	j := strings.LastIndexByte(msg, '|')
	if i < 0 || j <= i {
		return 0, fmt.Errorf("EPSV 应答无端口")
	}
	fields := strings.Split(strings.Trim(msg[i:], "|() \r\n"), "|")
	for _, f := range fields {
		if f == "" {
			continue
		}
		port, err := strconv.Atoi(strings.TrimSpace(f))
		if err == nil && port > 0 && port <= 65535 {
			return port, nil
		}
	}
	return 0, fmt.Errorf("EPSV 应答格式非法")
}

// parsePASV 从 "227 ... (h1,h2,h3,h4,p1,p2)" 提取地址（bounce 防御由调用方比对）。
func parsePASV(msg string) (net.IP, int, error) {
	i := strings.IndexByte(msg, '(')
	if i < 0 {
		return nil, 0, fmt.Errorf("PASV 应答无地址")
	}
	j := strings.IndexByte(msg[i+1:], ')')
	if j < 0 {
		return nil, 0, fmt.Errorf("PASV 应答无地址")
	}
	parts := strings.Split(msg[i+1:i+1+j], ",")
	if len(parts) != 6 {
		return nil, 0, fmt.Errorf("PASV 字段数 %d != 6", len(parts))
	}
	oct := make([]byte, 4)
	for k := 0; k < 4; k++ {
		n, err := strconv.Atoi(strings.TrimSpace(parts[k]))
		if err != nil || n < 0 || n > 255 {
			return nil, 0, fmt.Errorf("PASV 八位组非法")
		}
		oct[k] = byte(n)
	}
	p1, err1 := strconv.Atoi(strings.TrimSpace(parts[4]))
	p2, err2 := strconv.Atoi(strings.TrimSpace(parts[5]))
	if err1 != nil || err2 != nil {
		return nil, 0, fmt.Errorf("PASV 端口非法")
	}
	port := p1*256 + p2
	if port <= 0 || port > 65535 {
		return nil, 0, fmt.Errorf("PASV 端口越界")
	}
	return net.IP(oct), port, nil
}

// FTPEntry 目录列取结果条目。
type FTPEntry struct {
	Name    string
	IsDir   bool
	Size    int64
	ModTime time.Time // 零值=未知
}

// ListDir 列取 FTP 目录（MLSD 优先，回退 LIST 解析；均走被动模式数据连接）。
// urlStr 形如 ftp://host[:port]/dir/（目录路径）；返回条目不含 "." / ".."。
// 供链接发现（porter ls）使用；同样受 H-3 回环边界约束。
func (f *FTPTransport) ListDir(ctx context.Context, urlStr string) ([]FTPEntry, error) {
	req, err := parseFTPURL(urlStr, f.allowRemote)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(req.path, "/") {
		req.path += "/"
	}
	fc, err := f.dialControl(ctx, req)
	if err != nil {
		return nil, err
	}
	defer fc.close()
	if _, _, err := fc.cmd(2, "TYPE A"); err != nil {
		return nil, err
	}
	// MLSD 优先（RFC 3659）：type/size/modify 语义明确；先建被动数据连接
	//（协议顺序：EPSV/PASV 就绪 → MLSD 150 → 读数据 → 226）。
	dc, err := fc.openData(ctx, f.allowRemote)
	if err != nil {
		return nil, err
	}
	if code, _, err := fc.cmd(0, "MLSD %s", req.path); err == nil && code == 150 {
		data, rerr := io.ReadAll(io.LimitReader(dc, 8<<20))
		_ = dc.Close()
		fc.setDeadline(15 * time.Second)
		fcode, _, ferr := fc.readReply()
		if rerr != nil {
			return nil, rerr
		}
		_ = fcode
		_ = ferr
		entries, perr := parseMLSD(string(data))
		if perr == nil {
			return entries, nil
		}
		return nil, perr
	}
	_ = dc.Close()
	// LIST 回退（无 MLSD 或失败）：重新建立数据连接执行 LIST。
	dc2, err := fc.openData(ctx, f.allowRemote)
	if err != nil {
		return nil, err
	}
	if code, _, err := fc.cmd(0, "LIST %s", req.path); err != nil || code != 150 {
		_ = dc2.Close()
		return nil, fmt.Errorf("ftp: MLSD/LIST 均不可用: %v", err)
	}
	data, err := io.ReadAll(io.LimitReader(dc2, 8<<20))
	_ = dc2.Close()
	fc.setDeadline(15 * time.Second)
	_, _, _ = fc.readReply() // 消耗最终应答（成败不作为解析依据）
	if err != nil {
		return nil, err
	}
	return parseLIST(string(data), req.path)
}

// parseMLSD 解析 RFC 3659 MLSD 应答行：
// "type=file;size=123;modify=20200101120000; <name>"
func parseMLSD(data string) ([]FTPEntry, error) {
	var out []FTPEntry
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		sp := strings.IndexByte(line, ' ')
		if sp < 0 {
			continue
		}
		facts, name := line[:sp], strings.TrimSpace(line[sp+1:])
		if name == "." || name == ".." || name == "" {
			continue
		}
		e := FTPEntry{Name: name}
		for _, kv := range strings.Split(facts, ";") {
			kv = strings.TrimSpace(kv)
			if kv == "" {
				continue
			}
			eq := strings.IndexByte(kv, '=')
			if eq < 0 {
				continue
			}
			k, v := strings.ToLower(kv[:eq]), kv[eq+1:]
			switch k {
			case "type":
				e.IsDir = v == "dir"
			case "size":
				if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
					e.Size = n
				}
			case "modify":
				if t, err := time.Parse("20060102150405", v); err == nil {
					e.ModTime = t
				}
			}
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ftp: MLSD 无有效条目")
	}
	return out, nil
}

// parseLIST 解析 Unix/Windows 两种常见 LIST 应答形态。basePath 用于判定条目是否目录。
func parseLIST(data, basePath string) ([]FTPEntry, error) {
	var out []FTPEntry
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		e, ok := parseLISTLine(line)
		if !ok {
			continue
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ftp: LIST 无有效条目")
	}
	return out, nil
}

// parseLISTLine 解析单行 LIST 应答：Unix（-rw-r--r-- ...）与 Windows（MM-DD-YY hh:mmAM <DIR> ...）。
func parseLISTLine(line string) (FTPEntry, bool) {
	// Unix 形态：权限位 10 字符开头（drwxr-xr-x / -rw-r--r-- / lrwxrwxrwx）
	if len(line) >= 10 && (line[0] == '-' || line[0] == 'd' || line[0] == 'l') {
		fields := strings.Fields(line)
		// 至少: 权限 链接数 user group 大小 月 日 时间 名称
		if len(fields) >= 9 {
			e := FTPEntry{}
			e.IsDir = line[0] == 'd'
			if n, err := strconv.ParseInt(fields[4], 10, 64); err == nil && n >= 0 {
				e.Size = n
			}
			e.ModTime, _ = time.Parse("Jan _2 15:04", fields[5]+" "+fields[6]+" "+fields[7])
			if _, err := time.Parse("Jan _2 2006", fields[5]+" "+fields[6]+" "+fields[7]); err == nil {
				e.ModTime, _ = time.Parse("Jan _2 2006", fields[5]+" "+fields[6]+" "+fields[7])
			}
			e.Name = strings.Join(fields[8:], " ")
			if e.Name == "." || e.Name == ".." {
				return FTPEntry{}, false
			}
			return e, true
		}
	}
	// Windows 形态："MM-DD-YY hh:mmAM  <DIR> name" 或 "MM-DD-YY hh:mmAM  123 name"
	fields := strings.Fields(line)
	if len(fields) >= 4 && len(fields[0]) == 8 && fields[0][2] == '-' {
		e := FTPEntry{}
		if strings.EqualFold(fields[2], "<DIR>") {
			e.IsDir = true
			e.Name = strings.Join(fields[3:], " ")
		} else if n, err := strconv.ParseInt(fields[2], 10, 64); err == nil && n >= 0 {
			e.Size = n
			e.Name = strings.Join(fields[3:], " ")
		}
		if e.Name == "" || e.Name == "." || e.Name == ".." {
			return FTPEntry{}, false
		}
		e.ModTime, _ = time.Parse("01-02-06 15:04", fields[0]+" "+fields[1])
		return e, true
	}
	return FTPEntry{}, false
}

// Probe 探测 FTP 资源：SIZE 取大小；REST 0 判定 Range 支持。
// size=0 表示服务端不支持 SIZE（调用方退化为流式）。
func (f *FTPTransport) Probe(ctx context.Context, urlStr string) (int64, bool, error) {
	req, err := parseFTPURL(urlStr, f.allowRemote)
	if err != nil {
		return 0, false, err
	}
	fc, err := f.dialControl(ctx, req)
	if err != nil {
		return 0, false, err
	}
	defer fc.close()
	if _, _, err := fc.cmd(2, "TYPE I"); err != nil {
		return 0, false, err
	}
	var size int64
	if code, msg, err := fc.cmd(0, "SIZE %s", req.path); err == nil && code == 213 {
		if n, perr := strconv.ParseInt(strings.TrimSpace(msg), 10, 64); perr == nil && n >= 0 {
			size = n
		}
	}
	ranged := false
	if code, _, err := fc.cmd(0, "REST 0"); err == nil && code == 350 {
		ranged = true
	}
	return size, ranged, nil
}

// fetchRange 下载 [start,end) 区间写入 dst（语义与 Transport.FetchRange 一致：
// end=0 表示到 EOF；start=0 且 end=0 表示完整下载）。有界区间严格限长，
// 多给/少给均视为错误；lim 为与 HTTP 路径共享的全局限速器（nil=不限速）。
// 经 Mux 分发调用（Fetcher 接口由 Mux 实现）；非导出以隐藏限速器类型。
func (f *FTPTransport) fetchRange(ctx context.Context, urlStr string, start, end int64, lim *rateLimiter, dst io.WriterAt) error {
	req, err := parseFTPURL(urlStr, f.allowRemote)
	if err != nil {
		return err
	}
	fc, err := f.dialControl(ctx, req)
	if err != nil {
		return err
	}
	defer fc.close()
	if _, _, err := fc.cmd(2, "TYPE I"); err != nil {
		return err
	}
	if start > 0 {
		if _, _, err := fc.cmd(3, "REST %d", start); err != nil {
			return fmt.Errorf("ftp: REST %d 被拒绝（不支持续传）: %w", start, err)
		}
	}
	dc, err := fc.openData(ctx, f.allowRemote)
	if err != nil {
		return err
	}
	// ctx 取消（分片被窃取/引擎失败/任务取消）→ 关闭数据连接解除读取阻塞。
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = dc.Close()
		case <-done:
		}
	}()
	expected := int64(-1)
	if end > start {
		expected = end - start
	}
	srcBody := io.Reader(dc)
	if expected >= 0 {
		srcBody = io.LimitReader(dc, expected) // 有界区间严格限长（与 HTTP 完整性校验同约定）
	}
	if lim != nil {
		srcBody = &throttledReader{ctx: ctx, r: srcBody, l: lim}
	}
	code, _, err := fc.cmd(0, "RETR %s", req.path) // 125/150（1xx）先于数据传输
	if err != nil || code/100 != 1 {
		if err == nil {
			err = fmt.Errorf("ftp: RETR 应答 %d", code)
		}
		close(done)
		dc.Close()
		return fmt.Errorf("ftp: RETR 被拒绝: %w", err)
	}
	werr := writeWriterAt(dst, srcBody, expected)
	close(done)
	dc.Close()
	if werr != nil {
		return werr
	}
	// 完整性语义与 HTTP 路径一致：
	//   - 有界区间（expected>=0）：字节数已严格校验（writeWriterAt 少给即错），
	//     客户端读满即关闭数据连接，服务端随后报 426 属预期（其仍向 EOF 发送），
	//     最终应答不作为成败依据，仅读取消耗；
	//   - open-ended/全量：EOF 由服务端判定，必须收到 226 才算完整。
	fc.setDeadline(15 * time.Second)
	fcode, _, ferr := fc.readReply()
	if expected < 0 {
		if ferr != nil {
			return fmt.Errorf("ftp: 传输最终应答读取失败: %w", ferr)
		}
		if fcode != 226 {
			return fmt.Errorf("ftp: 传输未完成 (%d)", fcode)
		}
	}
	return nil
}
