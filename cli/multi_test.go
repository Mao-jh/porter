package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nymjin22/downloader/network"
	"github.com/nymjin22/downloader/persist"
	"github.com/nymjin22/downloader/scheduler"
)

// memSink 内存 WriterAt（限速测试用）。
type memSink struct{ buf []byte }

func (m *memSink) WriteAt(p []byte, off int64) (int, error) {
	if off != int64(len(m.buf)) {
		return 0, os.ErrInvalid
	}
	m.buf = append(m.buf, p...)
	return len(p), nil
}

// TestRun_MultiURL_E2E 多 URL 队列：两个文件并发下载，各自 sha256 闭环、
// 状态各自 done、同名 URL 自动去重命名。
func TestRun_MultiURL_E2E(t *testing.T) {
	s := startServer(t)
	sizes := map[string]int64{"a.bin": 5 << 20, "b.bin": 6 << 20}
	for name, size := range sizes {
		if _, err := s.CreateFile(name, size); err != nil {
			t.Fatalf("CreateFile %s: %v", name, err)
		}
	}
	expected := map[string]string{}
	for name := range sizes {
		sha, err := s.Checksum(name)
		if err != nil {
			t.Fatalf("Checksum %s: %v", name, err)
		}
		expected[name] = sha
	}

	outDir := t.TempDir()
	opt := &Options{
		URLs:     []string{s.FileURL("a.bin"), s.FileURL("b.bin"), s.FileURL("a.bin")},
		Output:   outDir, // 多 URL：-o 为输出目录
		Mode:     scheduler.ModeMaxPerf,
		Verify:   "", // 校验由本测试独立执行
		StateDir: filepath.Join(outDir, "state"),
	}
	if err := Run(context.Background(), opt); err != nil {
		t.Fatalf("RunMulti: %v", err)
	}

	// a.bin / a-2.bin / b.bin 均存在且内容正确
	pairs := []struct{ out, want string }{
		{"a.bin", "a.bin"},
		{"a-2.bin", "a.bin"},
		{"b.bin", "b.bin"},
	}
	for _, pr := range pairs {
		got := sha256OfFile(t, filepath.Join(outDir, pr.out))
		if got != expected[pr.want] {
			t.Errorf("%s sha256 不一致: got %s", pr.out, got)
		}
	}
	// 状态闭环
	store, err := persist.Open(opt.StateDir)
	if err != nil {
		t.Fatalf("persist.Open: %v", err)
	}
	for _, name := range []string{"a.bin", "a-2.bin", "b.bin"} {
		st, ok := store.Get(filepath.Join(outDir, name))
		if !ok || st.Status != "done" {
			t.Errorf("%s 状态应为 done, got %+v", name, st)
		}
	}
}

// TestRun_MultiURL_ErrorAggregation 含失败任务的多任务：返回聚合错误且不影响成功任务。
func TestRun_MultiURL_ErrorAggregation(t *testing.T) {
	s := startServer(t)
	if _, err := s.CreateFile("ok.bin", 1<<20); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	outDir := t.TempDir()
	opt := &Options{
		URLs:     []string{s.FileURL("ok.bin"), s.FileURL("missing.bin")},
		Output:   outDir,
		Mode:     scheduler.ModeDefault,
		Verify:   "",
		StateDir: filepath.Join(outDir, "state"),
	}
	err := Run(context.Background(), opt)
	if err == nil {
		t.Fatal("含失败任务时应返回聚合错误")
	}
	if !strings.Contains(err.Error(), "missing.bin") {
		t.Fatalf("聚合错误应包含失败任务标识, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outDir, "ok.bin")); statErr != nil {
		t.Fatalf("成功任务输出缺失: %v", statErr)
	}
}

// TestParse_HeaderAndLimit Parse 层：-H 重复收集、-limit 透传、非法输入报错、多 URL。
func TestParse_HeaderAndLimit(t *testing.T) {
	opt, err := Parse([]string{"http://127.0.0.1/a", "-limit", "1048576",
		"-H", "Cookie: a=b", "-H", "X-Test: yes"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if opt.Limit != 1048576 {
		t.Errorf("Limit=%d want 1048576", opt.Limit)
	}
	if opt.Headers["Cookie"] != "a=b" || opt.Headers["X-Test"] != "yes" {
		t.Errorf("Headers=%v", opt.Headers)
	}
	if len(opt.URLs) != 1 || opt.URL != "http://127.0.0.1/a" {
		t.Errorf("URLs=%v", opt.URLs)
	}
	if _, err := Parse([]string{"http://127.0.0.1/a", "-H", "bogus"}); err == nil {
		t.Error("无冒号的 -H 应报错")
	}
	opt2, err := Parse([]string{"http://127.0.0.1/a", "http://127.0.0.1/b"})
	if err != nil {
		t.Fatalf("Parse multi: %v", err)
	}
	if len(opt2.URLs) != 2 {
		t.Errorf("URLs=%v", opt2.URLs)
	}
}

// TestDeriveOutputs 输出名推导与去重规则。
func TestDeriveOutputs(t *testing.T) {
	outs := deriveOutputs([]string{
		"http://127.0.0.1/dir/big.bin",
		"http://127.0.0.1/dir/big.bin",
		"http://127.0.0.1/dir/big.bin?x=1",
		"http://127.0.0.1/only/",
	})
	// 目录型 URL（尾斜杠）取最后一段作为文件名
	want := []string{"big.bin", "big-2.bin", "big-3.bin", "only"}
	for i := range want {
		if outs[i] != want[i] {
			t.Errorf("outs[%d]=%s want %s", i, outs[i], want[i])
		}
	}
}

// TestRun_LimitThrottles 全局限速：下载 512KiB、限速 1MiB/s，耗时 ∈ [400ms, 5s]。
func TestRun_LimitThrottles(t *testing.T) {
	s := startServer(t)
	size := int64(512 << 10)
	if _, err := s.CreateFile("th.bin", size); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	tr := network.NewTransport(false)
	tr.SetGlobalLimit(1 << 20) // 1 MiB/s

	start := time.Now()
	sink := &memSink{}
	if err := tr.FetchRange(context.Background(), s.FileURL("th.bin"), 0, 0, sink); err != nil {
		t.Fatalf("FetchRange: %v", err)
	}
	elapsed := time.Since(start)
	if len(sink.buf) != int(size) {
		t.Fatalf("收到 %d 字节 want %d", len(sink.buf), size)
	}
	if elapsed < 400*time.Millisecond {
		t.Fatalf("限速未生效: 512KiB@1MiBps 耗时 %v（应 ≥400ms）", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("限速过度: 耗时 %v", elapsed)
	}
}

// TestRun_MultiURL_SharedLimit 多任务+多分片共享同一限速配额：
// 总量 2MiB、全局限速 512KiB/s → 无论并发多少连接，总耗时 ≥ 3.5s。
func TestRun_MultiURL_SharedLimit(t *testing.T) {
	s := startServer(t)
	size := int64(1 << 20) // 每文件 1MiB
	for _, name := range []string{"m1.bin", "m2.bin"} {
		if _, err := s.CreateFile(name, size); err != nil {
			t.Fatalf("CreateFile: %v", err)
		}
	}
	outDir := t.TempDir()
	opt := &Options{
		URLs:     []string{s.FileURL("m1.bin"), s.FileURL("m2.bin")},
		Output:   outDir,
		Mode:     scheduler.ModeMaxPerf,
		Verify:   "",
		Limit:    512 << 10, // 512KiB/s 全局
		StateDir: filepath.Join(outDir, "state"),
	}
	start := time.Now()
	if err := Run(context.Background(), opt); err != nil {
		t.Fatalf("Run: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 3500*time.Millisecond {
		t.Fatalf("全局限速未在多连接间共享: 2MiB@512KiBps 耗时 %v（应 ≥3.5s）", elapsed)
	}
}
