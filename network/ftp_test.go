package network

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Mao-jh/porter/testserver"
)

// startFTPServer 启动回环 FTP 测试服务端，返回 (服务端, 基地址, 文件目录)。
func startFTPServer(t *testing.T, rate int64) (*testserver.FTPServer, string, string) {
	t.Helper()
	dir := t.TempDir()
	ftp, err := testserver.NewFTPServer(dir, rate)
	if err != nil {
		t.Fatalf("NewFTPServer: %v", err)
	}
	t.Cleanup(func() { _ = ftp.Close() })
	return ftp, ftp.URL(), dir
}

// TestParseFTPURL 解析规则：默认匿名、userinfo、默认端口、路径必填、回环强制。
func TestParseFTPURL(t *testing.T) {
	req, err := parseFTPURL("ftp://127.0.0.1:2121/dir/file.bin", false)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if req.host != "127.0.0.1" || req.port != 2121 || req.path != "/dir/file.bin" || req.tls {
		t.Fatalf("解析结果错误: %+v", req)
	}
	if req.user != "anonymous" {
		t.Errorf("默认用户应为 anonymous, got %q", req.user)
	}
	req, err = parseFTPURL("ftp://alice:secret@127.0.0.1/f.bin", false)
	if err != nil {
		t.Fatalf("parse userinfo: %v", err)
	}
	if req.user != "alice" || req.pass != "secret" {
		t.Errorf("userinfo 未解析: %q/%q", req.user, req.pass)
	}
	req, err = parseFTPURL("ftps://127.0.0.1/f.bin", false)
	if err != nil || !req.tls || req.port != 990 {
		t.Fatalf("ftps 默认端口/TLS 标志错误: %+v err=%v", req, err)
	}
	for _, bad := range []string{
		"http://127.0.0.1/x", // scheme 不符
		"ftp:///file.bin",    // 无主机
		"ftp://127.0.0.1",    // 无路径
		"ftp://10.0.0.1/x",   // 非回环（H-3）
		"ftp://192.168.1.5:21/x",
	} {
		if _, err := parseFTPURL(bad, false); err == nil {
			t.Errorf("应拒绝 %s", bad)
		}
	}
}

// TestFTP_DownloadAndRanges FTP 区间下载与 HTTP 路径同约定：
// SIZE/REST 探测 + 有界区间/open-ended/全量三种形态内容逐字节正确 + Mux 分发。
func TestFTP_DownloadAndRanges(t *testing.T) {
	_, base, dir := startFTPServer(t, 0)
	name := "f.bin"
	size := int64(256 << 10)
	if err := makeFTPTestFile(dir, name, size); err != nil {
		t.Fatalf("makeFTPTestFile: %v", err)
	}
	want := make([]byte, size)
	testserver.PatternFill(want, 0)
	url := base + "/" + name

	ftpT := NewFTPTransport(false)
	ctx := context.Background()

	// Probe：SIZE + REST 支持
	got, ranged, err := ftpT.Probe(ctx, url)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got != size || !ranged {
		t.Fatalf("Probe=(%d,%v) want (%d,true)", got, ranged, size)
	}

	// 有界区间：[1KiB, 65KiB)
	bufAt := newSliceWriterAt()
	if err := ftpT.fetchRange(ctx, url, 1024, 65536, nil, bufAt); err != nil {
		t.Fatalf("fetchRange bounded: %v", err)
	}
	if !bytes.Equal(bufAt.buf, want[1024:65536]) {
		t.Fatalf("区间内容错位: got %d bytes", bufAt.Len())
	}

	// open-ended（end=0，start>0）
	bufOpen := newSliceWriterAt()
	if err := ftpT.fetchRange(ctx, url, 4096, 0, nil, bufOpen); err != nil {
		t.Fatalf("fetchRange open-ended: %v", err)
	}
	if !bytes.Equal(bufOpen.buf, want[4096:]) {
		t.Fatalf("open-ended 内容错位: got %d want %d", bufOpen.Len(), size-4096)
	}

	// 全量（start=0,end=0）
	bufFull := newSliceWriterAt()
	if err := ftpT.fetchRange(ctx, url, 0, 0, nil, bufFull); err != nil {
		t.Fatalf("fetchRange full: %v", err)
	}
	if !bytes.Equal(bufFull.buf, want) {
		t.Fatalf("全量内容不一致: got %d want %d", bufFull.Len(), len(want))
	}

	// Mux 分发：ftp 走 FTP 传输层；未知 scheme 拒绝
	mux := NewMux(NewTransport(false), false)
	bufMux := newSliceWriterAt()
	if err := mux.FetchRange(ctx, url, 0, 0, bufMux); err != nil {
		t.Fatalf("Mux.FetchRange ftp: %v", err)
	}
	if !bytes.Equal(bufMux.buf, want) {
		t.Fatal("Mux ftp 全量内容不一致")
	}
	if _, _, err := mux.Probe(ctx, "gopher://127.0.0.1/x"); err == nil {
		t.Error("Mux 应拒绝未知 scheme")
	}
}

// TestFTP_BounceDefense PASV 返回非控制对端地址时必须拒绝（RFC 2577 反弹攻击防御）。
func TestFTP_BounceDefense(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go fakeFTPControl(conn)
		}
	}()

	ftpT := NewFTPTransport(false)
	url := "ftp://127.0.0.1:" + portOf(ln.Addr().String()) + "/x"
	err = ftpT.fetchRange(context.Background(), url, 0, 0, nil, newSliceWriterAt())
	if err == nil {
		t.Fatal("PASV 返回异地址时应拒绝")
	}
	if !strings.Contains(err.Error(), "bounce") {
		t.Fatalf("应报 bounce 防御错误, got: %v", err)
	}
}

// fakeFTPControl 极简脚本化控制连接：EPSV 拒绝 → PASV 回送公网地址 10.0.0.1。
func fakeFTPControl(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	bw := bufio.NewWriter(conn)
	reply := func(s string) { bw.WriteString(s + "\r\n"); bw.Flush() }
	reply("220 fake ready")
	for {
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.Fields(strings.TrimSpace(line))
		if len(cmd) == 0 {
			continue
		}
		switch strings.ToUpper(cmd[0]) {
		case "USER":
			reply("331 ok")
		case "PASS":
			reply("230 ok")
		case "TYPE":
			reply("200 ok")
		case "EPSV":
			reply("500 epsv unavailable") // 迫使客户端回退 PASV
		case "PASV":
			reply("227 Entering Passive Mode (10,0,0,1,4,0)") // 10.0.0.1:1024
		default:
			reply("502 no")
		}
	}
}

// TestParsePASV_EPSV 被动模式应答解析。
func TestParsePASV_EPSV(t *testing.T) {
	ip, port, err := parsePASV("227 Entering Passive Mode (127,0,0,1,200,10)")
	if err != nil || !ip.Equal(net.IPv4(127, 0, 0, 1).To4()) || port != 200*256+10 {
		t.Fatalf("parsePASV=(%v,%d,%v)", ip, port, err)
	}
	if _, _, err := parsePASV("227 garbage"); err == nil {
		t.Error("应拒绝无地址应答")
	}
	if p, err := parseEPSVPort("229 Entering Extended Passive Mode (|||51234|)"); err != nil || p != 51234 {
		t.Fatalf("parseEPSVPort=(%d,%v)", p, err)
	}
	if _, err := parseEPSVPort("229 no parens"); err == nil {
		t.Error("应拒绝无端口应答")
	}
}

// TestParseRetryAfter Retry-After 解析（秒数形式；HTTP-date/非法/缺省回退 0）。
func TestParseRetryAfter(t *testing.T) {
	if d := parseRetryAfter("30"); d != 30*time.Second {
		t.Errorf("30 → %v", d)
	}
	if d := parseRetryAfter("Wed, 21 Oct 2015 07:28:00 GMT"); d != 0 {
		t.Errorf("HTTP-date 回退 0, got %v", d)
	}
	if d := parseRetryAfter(""); d != 0 {
		t.Errorf("空 → 0, got %v", d)
	}
	he := &httpError{status: 429, retryAfter: 2 * time.Second}
	if d, ok := RetryAfter(he); !ok || d != 2*time.Second {
		t.Errorf("RetryAfter=(%v,%v)", d, ok)
	}
	if _, ok := RetryAfter(fmt.Errorf("plain")); ok {
		t.Error("普通错误不应携带 Retry-After")
	}
}

// TestHTTP_RetryAfterLive 对 /fault?type=429ra 的真实 429+Retry-After 响应断言错误携带建议时长。
func TestHTTP_RetryAfterLive(t *testing.T) {
	srv, cleanup := startTestServer(t)
	defer cleanup()
	srv.s.SetFaults(1) // 开启一次 429ra 故障注入

	tr := NewTransport(false)
	err := tr.FetchRange(context.Background(), srv.s.BaseURL()+"/fault?type=429ra", 0, 0, newSliceWriterAt())
	if err == nil {
		t.Fatal("429 应返回错误")
	}
	if !Retryable(err) {
		t.Error("429 应可重试")
	}
	if d, ok := RetryAfter(err); !ok || d != time.Second {
		t.Errorf("Retry-After 应为 1s, got (%v,%v)", d, ok)
	}
}

// TestHTTP_DefaultUserAgent 未显式透传 UA 时默认携带 Porter 标识（合规：主动自报家门）。
func TestHTTP_DefaultUserAgent(t *testing.T) {
	srv, cleanup := startTestServer(t)
	defer cleanup()

	tr := NewTransport(false)
	buf := newSliceWriterAt()
	if err := tr.FetchRange(context.Background(), srv.s.BaseURL()+"/echo", 0, 0, buf); err != nil {
		t.Fatalf("FetchRange: %v", err)
	}
	if !strings.Contains(string(buf.buf), "User-Agent="+DefaultUserAgent) {
		t.Errorf("默认 UA 缺失\nbody=%s", buf.buf)
	}
	// 显式透传 UA 时不覆盖
	tr.SetHeaders(map[string]string{"User-Agent": "custom/1.0"})
	buf2 := newSliceWriterAt()
	if err := tr.FetchRange(context.Background(), srv.s.BaseURL()+"/echo", 0, 0, buf2); err != nil {
		t.Fatalf("FetchRange 2: %v", err)
	}
	if strings.Contains(string(buf2.buf), DefaultUserAgent) || !strings.Contains(string(buf2.buf), "User-Agent=custom/1.0") {
		t.Errorf("显式 UA 应生效且不被覆盖\nbody=%s", buf2.buf)
	}
}

// TestHTTP_Redirect_StripsSensitiveHeadersCrossHost 跨主机重定向剥离 Cookie/Authorization；
// 同主机重定向保留。重定向链仍受拨号层回环强制（另一实例同为 127.0.0.1）。
func TestHTTP_Redirect_StripsSensitiveHeadersCrossHost(t *testing.T) {
	srvA, cleanupA := startTestServer(t) // 跳板
	srvB, cleanupB := startTestServer(t) // 目标（不同端口 → Host 不同）
	defer cleanupA()
	defer cleanupB()

	tr := NewTransport(false)
	tr.SetHeaders(map[string]string{"Cookie": "session=abc", "Authorization": "Bearer t1", "X-Keep": "yes"})

	// 跨主机（A → B）：敏感头必须被剥离
	buf := newSliceWriterAt()
	target := srvB.s.BaseURL() + "/echo"
	redirectURL := srvA.s.BaseURL() + "/redirect?to=" + target
	if err := tr.FetchRange(context.Background(), redirectURL, 0, 0, buf); err != nil {
		t.Fatalf("跨主机重定向 FetchRange: %v", err)
	}
	body := string(buf.buf)
	if strings.Contains(body, "session=abc") || strings.Contains(body, "Bearer t1") {
		t.Errorf("跨主机重定向不应携带敏感头\nbody=%s", body)
	}
	if !strings.Contains(body, "X-Keep=yes") {
		t.Errorf("普通头应保留\nbody=%s", body)
	}

	// 同主机（A → A）：保留
	buf2 := newSliceWriterAt()
	redirectURL2 := srvA.s.BaseURL() + "/redirect?to=" + srvA.s.BaseURL() + "/echo"
	if err := tr.FetchRange(context.Background(), redirectURL2, 0, 0, buf2); err != nil {
		t.Fatalf("同主机重定向 FetchRange: %v", err)
	}
	if !strings.Contains(string(buf2.buf), "session=abc") {
		t.Errorf("同主机重定向应保留 Cookie\nbody=%s", buf2.buf)
	}
}

// TestHTTP_Redirect_RejectsBadScheme 重定向到不支持协议时拒绝（协议白名单）。
func TestHTTP_Redirect_RejectsBadScheme(t *testing.T) {
	srv, cleanup := startTestServer(t)
	defer cleanup()

	tr := NewTransport(false)
	redirectURL := srv.s.BaseURL() + "/redirect?to=gopher://127.0.0.1/x"
	err := tr.FetchRange(context.Background(), redirectURL, 0, 0, newSliceWriterAt())
	if err == nil {
		t.Fatal("重定向到 gopher 应拒绝")
	}
}

// portOf 从 "ftp://127.0.0.1:PORT" / "127.0.0.1:PORT" 提取端口。
func portOf(u string) string {
	i := strings.LastIndexByte(u, ':')
	return u[i+1:]
}

// makeFTPTestFile 在 FTP 服务端目录生成确定性测试文件（与 HTTP CreateFile 同模式，
// 偏移相关内容可暴露 Range 错位）。
func makeFTPTestFile(dir, name string, size int64) error {
	f, err := os.OpenFile(dir+"/"+name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, 64<<10)
	var off int64
	for off < size {
		n := int64(len(buf))
		if size-off < n {
			n = size - off
		}
		testserver.PatternFill(buf[:n], off)
		if _, err := f.WriteAt(buf[:n], off); err != nil {
			return err
		}
		off += n
	}
	return nil
}

// TestDialGuarded_AllowRemote R29 回归：FTP 拨号源地址绑定与 allowRemote 联动。
// 仅回环模式拨公网 → H-3 拒绝；放行模式 → 不返回 H-3。
func TestDialGuarded_AllowRemote(t *testing.T) {
	_, _, err := dialGuarded(context.Background(), "203.0.113.1", 21, false)
	if err == nil || !strings.Contains(err.Error(), "H-3") {
		t.Fatalf("仅回环模式应返回 H-3 拒绝, got %v", err)
	}
	_, _, err = dialGuarded(context.Background(), "203.0.113.1", 21, true)
	if err != nil && strings.Contains(err.Error(), "H-3") {
		t.Fatalf("放行模式不应返回 H-3 拒绝, got %v", err)
	}
}
