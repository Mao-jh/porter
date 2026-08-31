// round20_test.go 第 20 轮测试：probe 最终 URL（wget --spider 对标）。
package cli

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Mao-jh/porter/network"
	"github.com/Mao-jh/porter/testserver"
)

// TestRunProbe_FinalURL 重定向 URL：输出 final_url= 指向最终地址。
func TestRunProbe_FinalURL(t *testing.T) {
	ts, err := testserver.New(testserver.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	if _, err := ts.CreateFile("f.bin", 1<<20); err != nil {
		t.Fatal(err)
	}

	old := newTransport
	newTransport = func() *network.Transport { return network.NewTransport(false) }
	defer func() { newTransport = old }()

	// /redirect?to=<目标> 302 到目标（testserver 端点）
	src := ts.BaseURL() + "/redirect?to=" + ts.FileURL("f.bin")

	// 捕获 stdout
	r, w, _ := os.Pipe()
	oldStdout := os.Stdout
	os.Stdout = w
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	errProbe := RunProbe(ctx, &Options{URLs: []string{src}})
	_ = w.Close()
	os.Stdout = oldStdout
	buf := make([]byte, 8192)
	n, _ := r.Read(buf)
	out := string(buf[:n])
	if errProbe != nil {
		t.Fatalf("RunProbe: %v", errProbe)
	}
	if !strings.Contains(out, "url="+src) {
		t.Errorf("缺少原 url: %s", out)
	}
	if !strings.Contains(out, "final_url="+ts.FileURL("f.bin")) {
		t.Errorf("缺少 final_url（应指向重定向目标）: %s", out)
	}
}

// TestRunProbe_NoFinalURL 无重定向：不输出 final_url。
func TestRunProbe_NoFinalURL(t *testing.T) {
	ts, err := testserver.New(testserver.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	if _, err := ts.CreateFile("n.bin", 1<<20); err != nil {
		t.Fatal(err)
	}
	old := newTransport
	newTransport = func() *network.Transport { return network.NewTransport(false) }
	defer func() { newTransport = old }()

	r, w, _ := os.Pipe()
	oldStdout := os.Stdout
	os.Stdout = w
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	errProbe := RunProbe(ctx, &Options{URLs: []string{ts.FileURL("n.bin")}})
	_ = w.Close()
	os.Stdout = oldStdout
	buf := make([]byte, 8192)
	n, _ := r.Read(buf)
	out := string(buf[:n])
	if errProbe != nil {
		t.Fatalf("RunProbe: %v", errProbe)
	}
	if strings.Contains(out, "final_url=") {
		t.Errorf("无重定向不应输出 final_url: %s", out)
	}
}

// TestFinalURLFor 直接函数：代理/Cookie 透传路径返回最终地址。
func TestFinalURLFor(t *testing.T) {
	ts, err := testserver.New(testserver.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	src := ts.BaseURL() + "/redirect?to=" + ts.FileURL("f.bin")
	got := FinalURLFor(ctx, "", "", false, nil, src)
	if got == "" {
		t.Fatal("FinalURLFor 返回空串")
	}
	if got != ts.FileURL("f.bin") {
		t.Errorf("final = %q, 期望 %q", got, ts.FileURL("f.bin"))
	}
	// 非回环（无代理）→ 空串
	if got := FinalURLFor(ctx, "", "", false, nil, "https://example.com/x"); got != "" {
		t.Errorf("非回环应返回空串: %q", got)
	}
}
