package network

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mao-jh/porter/testserver"
)

// TestValidateURL_RejectsNonLoopback H-3：拒绝公网地址。
func TestValidateURL_RejectsNonLoopback(t *testing.T) {
	bad := []string{
		"http://example.com/x",
		"https://8.8.8.8/x",
		"http://192.168.1.1/x",
		"ftp://10.0.0.1/x",
	}
	for _, u := range bad {
		tr := NewTransport(false)
		if err := tr.validateURL(u); err == nil {
			t.Errorf("应拒绝 %s", u)
		}
	}
}

// TestValidateURL_AcceptsLoopback H-3：允许回环地址。
func TestValidateURL_AcceptsLoopback(t *testing.T) {
	good := []string{
		"http://127.0.0.1/x",
		"http://127.0.0.2:8080/x",
		"http://localhost/x", // 解析到 127.0.0.1，DialContext 二次校验
	}
	for _, u := range good {
		tr := NewTransport(false)
		if err := tr.validateURL(u); err != nil {
			t.Errorf("应接受 %s, got %v", u, err)
		}
	}
}

// TestValidateURL_RejectsBadScheme 拒绝不支持的协议。
func TestValidateURL_RejectsBadScheme(t *testing.T) {
	tr := NewTransport(false)
	if err := tr.validateURL("gopher://127.0.0.1/x"); err == nil {
		t.Error("应拒绝 gopher 协议")
	}
	if err := tr.validateURL(""); err == nil {
		t.Error("空 URL 应报错")
	}
}

// TestRetryable_Classification 重试分类：429/5xx/网络错误可重试；4xx/上下文取消/显式标记不可重试。
func TestRetryable_Classification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"429", &httpError{status: 429}, true},
		{"500", &httpError{status: 500}, true},
		{"503", &httpError{status: 503}, true},
		{"404", &httpError{status: 404}, false},
		{"403", &httpError{status: 403}, false},
		{"reset", errors.New("fault: connection reset"), true},
		{"canceled", context.Canceled, false},
		{"deadline", context.DeadlineExceeded, false},
		{"nonRetryable-wrapped", nonRetryable(&httpError{status: 404}), false},
	}
	for _, c := range cases {
		if got := Retryable(c.err); got != c.want {
			t.Errorf("%s: Retryable=%v want %v", c.name, got, c.want)
		}
	}
}

// TestFetchRange_ContentAndOffsets 对本地 testserver 下载区间，逐字节校验偏移正确性。
func TestFetchRange_ContentAndOffsets(t *testing.T) {
	srv, cleanup := startTestServer(t)
	defer cleanup()
	size := int64(256 << 10)
	name := srv.CreateFile(t, "f.bin", size)
	want := make([]byte, size)
	testserver.PatternFill(want, 0)

	tr := NewTransport(false)
	ctx := context.Background()

	// 区间 [1KiB, 65KiB)：内容必须精确等于 want[1024:65536]
	bufAt := newSliceWriterAt()
	if err := tr.FetchRange(ctx, srv.URL(name), 1024, 65536, bufAt); err != nil {
		t.Fatalf("FetchRange: %v", err)
	}
	if !bytes.Equal(bufAt.buf, want[1024:65536]) {
		t.Fatalf("区间内容错位: got %d bytes", bufAt.Len())
	}

	// open-ended（end=0，start>0）：内容等于 want[4096:]
	bufOpen := newSliceWriterAt()
	if err := tr.FetchRange(ctx, srv.URL(name), 4096, 0, bufOpen); err != nil {
		t.Fatalf("FetchRange open-ended: %v", err)
	}
	if !bytes.Equal(bufOpen.buf, want[4096:]) {
		t.Fatalf("open-ended 内容错位: got %d want %d", bufOpen.Len(), size-4096)
	}

	// 全量（start=0,end=0）：无 Range 头，200
	bufFull := newSliceWriterAt()
	if err := tr.FetchRange(ctx, srv.URL(name), 0, 0, bufFull); err != nil {
		t.Fatalf("FetchRange full: %v", err)
	}
	if !bytes.Equal(bufFull.buf, want) {
		t.Fatalf("全量内容不一致: got %d want %d", bufFull.Len(), len(want))
	}
}

// TestFetchRange_RejectsRangeIgnored 服务器忽略 Range 返回 200 时必须报错（防数据错位）。
func TestFetchRange_RejectsRangeIgnored(t *testing.T) {
	// 环回 httptest：始终返回 200 全量
	mux := http.NewServeMux()
	mux.HandleFunc("/x", func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat([]byte{7}, 4096))
	})
	s := httptest.NewServer(mux)
	defer s.Close()

	tr := NewTransport(false)
	err := tr.FetchRange(context.Background(), s.URL+"/x", 1024, 2048, newSliceWriterAt())
	if err == nil {
		t.Fatal("服务器忽略 Range 时应报错")
	}
	if Retryable(err) {
		t.Fatal("Range 被忽略属不可重试错误")
	}
}

// TestProbe_SizeAndRangeSupport HEAD 探测大小 + Accept-Ranges。
func TestProbe_SizeAndRangeSupport(t *testing.T) {
	srv, cleanup := startTestServer(t)
	defer cleanup()
	size := int64(1 << 20)
	name := srv.CreateFile(t, "probe.bin", size)

	tr := NewTransport(false)
	got, ranged, err := tr.Probe(context.Background(), srv.URL(name))
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got != size || !ranged {
		t.Fatalf("Probe=(%d,%v) want (%d,true)", got, ranged, size)
	}
}

// TestProbe_RejectsNonLoopback H-3：探测非回环地址拒绝。
func TestProbe_RejectsNonLoopback(t *testing.T) {
	tr := NewTransport(false)
	if _, _, err := tr.Probe(context.Background(), "http://127.0.0.1:1/x"); err == nil {
		// 端口未监听属网络错误而非 H-3 拒绝——仅验证非法主机必然报错
		t.Log("回环未监听端口返回错误（可接受）")
	}
	if _, _, err := tr.Probe(context.Background(), "http://example.com/x"); err == nil {
		t.Fatal("非回环主机应拒绝")
	}
}

// sliceWriterAt 内存 WriterAt（测试用）。
type sliceWriterAt struct{ buf []byte }

func newSliceWriterAt() *sliceWriterAt { return &sliceWriterAt{} }

func (w *sliceWriterAt) WriteAt(p []byte, off int64) (int, error) {
	if off != int64(len(w.buf)) {
		return 0, fmt.Errorf("non-sequential write: off=%d len=%d", off, len(w.buf))
	}
	w.buf = append(w.buf, p...)
	return len(p), nil
}

func (w *sliceWriterAt) Len() int64 { return int64(len(w.buf)) }

// TestSetHeaders_Echo -H 透传：请求头经 /echo 回显逐项断言。
func TestSetHeaders_Echo(t *testing.T) {
	srv, cleanup := startTestServer(t)
	defer cleanup()

	tr := NewTransport(false)
	tr.SetHeaders(map[string]string{
		"Cookie":        "session=abc",
		"X-Test":        "yes",
		"Authorization": "Bearer token123",
	})
	buf := newSliceWriterAt()
	if err := tr.FetchRange(context.Background(), srv.s.BaseURL()+"/echo", 0, 0, buf); err != nil {
		t.Fatalf("FetchRange /echo: %v", err)
	}
	body := buf.buf
	for _, want := range []string{
		"Cookie=session=abc",
		"X-Test=yes",
		"Authorization=Bearer token123",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("回显缺少 %q\nbody=%s", want, body)
		}
	}
	// 清空头后不再携带
	tr.SetHeaders(nil)
	buf2 := newSliceWriterAt()
	if err := tr.FetchRange(context.Background(), srv.s.BaseURL()+"/echo", 0, 0, buf2); err != nil {
		t.Fatalf("FetchRange /echo 2: %v", err)
	}
	if strings.Contains(string(buf2.buf), "session=abc") {
		t.Error("SetHeaders(nil) 后不应再携带旧头")
	}
}

// TestDialContext_AllowRemote R29 回归：源地址绑定必须与 allowRemote 联动。
// 仅回环模式拨公网 → H-3 拒绝（目标校验）；放行模式拨公网 → 不得返回 H-3
// （此时应走默认源地址拨号，真实结果取决于网络，但绝不可是 H-3 拒绝）。
func TestDialContext_AllowRemote(t *testing.T) {
	cases := []struct {
		name        string
		allowRemote bool
		wantH3      bool
	}{
		{"仅回环模式拒绝公网", false, true},
		{"放行模式不拒公网", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := NewTransport(tc.allowRemote)
			_, err := tr.client.Transport.(*http.Transport).DialContext(context.Background(), "tcp", "203.0.113.1:443")
			if tc.wantH3 {
				if err == nil || !strings.Contains(err.Error(), "H-3") {
					t.Fatalf("仅回环模式应返回 H-3 拒绝, got %v", err)
				}
				return
			}
			if err != nil && strings.Contains(err.Error(), "H-3") {
				t.Fatalf("放行模式不应返回 H-3 拒绝, got %v", err)
			}
		})
	}
}

// TestDialContext_ProxySetSwitchesDialer R29 回归：SetProxy 运行中置位 allowRemote
// 后，拨号器必须切换到默认源地址（否则公网/远程代理必然 unreachable network）。
func TestDialContext_ProxySetSwitchesDialer(t *testing.T) {
	tr := NewTransport(false)
	if err := tr.SetProxy("http://127.0.0.1:7890"); err != nil {
		t.Fatalf("SetProxy: %v", err)
	}
	_, err := tr.client.Transport.(*http.Transport).DialContext(context.Background(), "tcp", "203.0.113.1:443")
	if err != nil && strings.Contains(err.Error(), "H-3") {
		t.Fatalf("代理置位后不应返回 H-3 拒绝, got %v", err)
	}
}
