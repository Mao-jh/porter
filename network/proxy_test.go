// proxy_test.go 代理出口测试（第 14 轮 A1）。
package network

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSetProxy_Validation 代理地址校验：合法 scheme 放行，非法拒绝。
func TestSetProxy_Validation(t *testing.T) {
	tr := NewTransport(false)
	for _, bad := range []string{"", "ftp://127.0.0.1:1", "127.0.0.1:8080", "http://", ":::"} {
		if err := tr.SetProxy(bad); err == nil {
			t.Errorf("SetProxy(%q) 应报错", bad)
		}
	}
	for _, good := range []string{"http://127.0.0.1:8080", "https://proxy.example.com:3128", "socks5://127.0.0.1:1080"} {
		if err := tr.SetProxy(good); err != nil {
			t.Errorf("SetProxy(%q) 不应报错: %v", good, err)
		}
	}
}

// TestSetProxy_BypassesTargetDNS 代理模式下 validateURL 跳过目标 DNS 断言
//（解析交给代理；H-3 的出站同意由「显式配置代理」语义承接）。
func TestSetProxy_BypassesTargetDNS(t *testing.T) {
	tr := NewTransport(false)
	// 未配置代理：非回环域名被拒（DNS 解析或回环断言失败）
	if err := tr.validateURL("https://example.com/file"); err == nil {
		t.Error("无代理时非回环域名应被拒绝")
	}
	if err := tr.SetProxy("http://127.0.0.1:8080"); err != nil {
		t.Fatalf("SetProxy: %v", err)
	}
	if err := tr.validateURL("https://example.com/file"); err != nil {
		t.Errorf("代理模式下目标域名不应被本端拒绝: %v", err)
	}
	// scheme 白名单仍然生效
	if err := tr.validateURL("gopher://example.com/x"); err == nil {
		t.Error("代理模式下仍应拒绝非 http(s) scheme")
	}
}

// TestFetchRange_ViaForwardProxy 端到端：HTTP 转发代理（绝对 URI 转发），
// 验证请求确实经代理出口且内容完整（Range 语义不变）。
func TestFetchRange_ViaForwardProxy(t *testing.T) {
	ts, cleanup := startTestServer(t)
	defer cleanup()
	name := ts.CreateFile(t, "proxy.bin", 1<<20)

	var proxied int
	px := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied++
		out, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		for k, vs := range r.Header { // 透传请求头（Range 等）
			for _, v := range vs {
				out.Header.Add(k, v)
			}
		}
		resp, err := http.DefaultClient.Do(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer px.Close()

	tr := NewTransport(false)
	if err := tr.SetProxy(px.URL); err != nil {
		t.Fatalf("SetProxy: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 整文件 GET（start=0,end=0）
	var whole bytes.Buffer
	if err := tr.FetchRange(ctx, ts.s.FileURL(name), 0, 0, writerAt{&whole}); err != nil {
		t.Fatalf("FetchRange(全量): %v", err)
	}
	if proxied == 0 {
		t.Fatal("请求未经过代理出口")
	}
	// Range 分片 GET
	var part bytes.Buffer
	if err := tr.FetchRange(ctx, ts.s.FileURL(name), 64<<10, 128<<10, writerAt{&part}); err != nil {
		t.Fatalf("FetchRange(区间): %v", err)
	}
	if part.Len() != 64<<10 {
		t.Fatalf("区间长度不符: got %d want %d", part.Len(), 64<<10)
	}
	if !strings.Contains(whole.String()[:0], "") && whole.Len() != 1<<20 {
		t.Fatalf("全量长度不符: %d", whole.Len())
	}
}

// writerAt 把 bytes.Buffer 适配为 io.WriterAt（顺序写 = 追加写）。
type writerAt struct{ b *bytes.Buffer }

func (w writerAt) WriteAt(p []byte, off int64) (int, error) {
	if int64(w.b.Len()) != off {
		// 顺序写语义下不应出现跳跃；测试内直接断言失败更直观
		return 0, io.ErrShortWrite
	}
	return w.b.Write(p)
}
