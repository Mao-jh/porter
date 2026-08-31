// h2_test.go 第 19 轮：HTTP/2 显式启用与协商验证。
// 说明：产品代码不注入 TLS 信任；本测试仅验证 Transport 的 h2 能力（多路复用路径），
// 故在测试内临时设置 TLSClientConfig（InsecureSkipVerify + NextProtos=h2）。
package network

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestTransport_ForceHTTP2 生产 Transport 显式声明强制尝试 HTTP/2。
func TestTransport_ForceHTTP2(t *testing.T) {
	tr := NewTransport(false)
	ht, ok := tr.client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("Transport 内部应为 *http.Transport")
	}
	if !ht.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2 应为 true（自定义 DialContext 下自动协商被关闭）")
	}
}

// TestHTTP2_NegotiationAndMultiplexing 对 h2 服务端协商到 HTTP/2，
// 并发 6 个请求（模拟 6 分片）全部以 h2 完成——多路复用路径可用。
func TestHTTP2_NegotiationAndMultiplexing(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprintf(w, "proto=%s", r.Proto)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	tr := NewTransport(false)
	ht := tr.client.Transport.(*http.Transport)
	// 测试注入 TLS 信任（httptest 自签名证书）+ 显式声明 h2
	ht.TLSClientConfig = &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2", "http/1.1"}}
	ht.ForceAttemptHTTP2 = true

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var mu sync.Mutex
	var protos []string
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ { // 模拟 6 分片并发
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/x", nil)
			resp, err := tr.client.Do(req)
			if err != nil {
				t.Errorf("并发请求失败: %v", err)
				return
			}
			_ = resp.Body.Close()
			mu.Lock()
			protos = append(protos, fmt.Sprintf("%d.%d", resp.ProtoMajor, resp.ProtoMinor))
			mu.Unlock()
		}()
	}
	wg.Wait()
	if hits.Load() != 6 {
		t.Errorf("服务端应收到 6 个请求, got %d", hits.Load())
	}
	for _, p := range protos {
		if p != "2.0" {
			t.Errorf("应协商到 HTTP/2.0, got %s", p)
		}
	}
}
