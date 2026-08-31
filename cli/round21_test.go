// round21_test.go 第 21 轮测试：porter meta 响应头查看。
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

// TestRunMeta 响应头输出：状态行 + Content-Length/Accept-Ranges。
func TestRunMeta(t *testing.T) {
	ts, err := testserver.New(testserver.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	if _, err := ts.CreateFile("m.bin", 2<<20); err != nil {
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

	errMeta := RunMeta(ctx, &Options{URLs: []string{ts.FileURL("m.bin")}})
	_ = w.Close()
	os.Stdout = oldStdout
	buf := make([]byte, 8192)
	n, _ := r.Read(buf)
	out := string(buf[:n])
	if errMeta != nil {
		t.Fatalf("RunMeta: %v", errMeta)
	}
	if !strings.Contains(out, "HTTP/1.1 200 OK") {
		t.Errorf("缺状态行: %s", out)
	}
	if !strings.Contains(out, "Content-Length: 2097152") {
		t.Errorf("缺 Content-Length: %s", out)
	}
	if !strings.Contains(out, "Accept-Ranges: bytes") {
		t.Errorf("缺 Accept-Ranges: %s", out)
	}
}

// TestRunMeta_Errors 非回环（无代理）→ 聚合错误。
func TestRunMeta_Errors(t *testing.T) {
	old := newTransport
	newTransport = func() *network.Transport { return network.NewTransport(false) }
	defer func() { newTransport = old }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := RunMeta(ctx, &Options{URLs: []string{"http://10.0.0.1/x"}})
	if err == nil || !strings.Contains(err.Error(), "not loopback") {
		t.Errorf("非回环应报错: %v", err)
	}
}

// TestTransport_MetaDirect 直接调用：HEAD 头 + 回退路径不崩溃。
func TestTransport_MetaDirect(t *testing.T) {
	ts, err := testserver.New(testserver.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	if _, err := ts.CreateFile("d.bin", 1<<20); err != nil {
		t.Fatal(err)
	}
	tr := network.NewTransport(false)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	status, hdr, err := tr.Meta(ctx, ts.FileURL("d.bin"))
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	if !strings.Contains(status, "200") {
		t.Errorf("status = %q", status)
	}
	if hdr.Get("Content-Length") != "1048576" {
		t.Errorf("Content-Length = %q", hdr.Get("Content-Length"))
	}
}
