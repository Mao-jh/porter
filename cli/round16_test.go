// round16_test.go 第 16 轮：-i out= 行内命名、porter rm/clean、porter probe。
package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mao-jh/porter/network"
	"github.com/Mao-jh/porter/persist"
	"github.com/Mao-jh/porter/testserver"
)

// TestParse_URLFileOut -i 文件 "URL out=name" 行内命名解析 + 净化。
func TestParse_URLFileOut(t *testing.T) {
	f := filepath.Join(t.TempDir(), "urls.txt")
	content := "# 注释\n" +
		"http://127.0.0.1/a.bin out=alpha.bin\n" +
		"http://127.0.0.1/b.bin\n" +
		"https://127.0.0.1/../../evil out=../escape.exe\n" // 路径穿越被净化
	if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	opt, err := Parse([]string{"-i", f})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(opt.URLs) != 3 || len(opt.Outputs) != 3 {
		t.Fatalf("URLs/Outputs 长度 = %d/%d", len(opt.URLs), len(opt.Outputs))
	}
	if opt.Outputs[0] != "alpha.bin" {
		t.Errorf("out= 未生效: %q", opt.Outputs[0])
	}
	if opt.Outputs[1] != "" {
		t.Errorf("无 out= 行应为空: %q", opt.Outputs[1])
	}
	if opt.Outputs[2] == "" || strings.ContainsAny(opt.Outputs[2], "/\\") {
		t.Errorf("路径穿越应被净化: %q", opt.Outputs[2])
	}
}

// TestRun_URLFileOut 端到端：-i out= 命名生效（与 -o 目录共存）。
func TestRun_URLFileOut(t *testing.T) {
	ts, err := testserver.New(testserver.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	for _, n := range []string{"a.bin", "b.bin"} {
		if _, err := ts.CreateFile(n, 256<<10); err != nil {
			t.Fatal(err)
		}
	}

	old := newTransport
	newTransport = func() *network.Transport { return network.NewTransport(false) }
	defer func() { newTransport = old }()

	f := filepath.Join(t.TempDir(), "urls.txt")
	content := ts.FileURL("a.bin") + " out=renamed-a.bin\n" + ts.FileURL("b.bin") + "\n"
	if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	opt := &Options{
		URLs:     []string{ts.FileURL("a.bin"), ts.FileURL("b.bin")},
		Outputs:  []string{"renamed-a.bin", ""},
		Output:   outDir,
		Verify:   "",
		StateDir: filepath.Join(outDir, "state"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := Run(ctx, opt); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "renamed-a.bin")); err != nil {
		t.Errorf("out= 命名产物缺失: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "b.bin")); err != nil {
		t.Errorf("自动命名产物缺失: %v", err)
	}
}

// TestRemoveTasks 删除任务：done 可删（连带 .part）；running 且有 .part 拒绝；clean 仅清 done。
func TestRemoveTasks(t *testing.T) {
	dir := t.TempDir()
	store, err := persist.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	put := func(id, status string, done, size int64) {
		t.Helper()
		if err := store.Put(&persist.State{ID: id, URL: "http://127.0.0.1/" + id,
			FileSize: size, Done: done, Status: status, UpdatedAt: time.Now().UnixNano()}); err != nil {
			t.Fatal(err)
		}
	}
	put("done.bin", "done", 1024, 1024)
	put("running.bin", "running", 512, 1024)

	// 精确删除：done.bin 成功，running.bin（有 .part）拒绝
	partFile := "done.bin.part"
	_ = os.WriteFile(partFile, []byte("partial"), 0o600) // 相对 cwd；测试结束自动清理
	defer os.Remove(partFile)
	_ = os.WriteFile("running.bin.part", []byte("partial"), 0o600)
	defer os.Remove("running.bin.part")

	removed, refused, err := RemoveTasks(dir, []string{"done.bin", "running.bin"}, false)
	if err != nil {
		t.Fatalf("RemoveTasks: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, 期望 1", removed)
	}
	if len(refused) == 0 || !strings.Contains(refused[0], "running.bin") {
		t.Errorf("running 任务应被拒绝: %v", refused)
	}
	if _, err := os.Stat(partFile); err == nil {
		t.Error("done.bin.part 应随状态删除")
	}
	// clean：仅清 done（此时只剩 running.bin）
	removed2, _, err := RemoveTasks(dir, nil, true)
	if err != nil {
		t.Fatalf("RemoveTasks(clean): %v", err)
	}
	if removed2 != 0 {
		t.Errorf("clean 不应删除 running 任务, removed=%d", removed2)
	}
}

// TestRunProbe 探测输出：size/ranged/name 齐全；非回环报错。
func TestRunProbe(t *testing.T) {
	ts, err := testserver.New(testserver.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	if _, err := ts.CreateFile("probe.bin", 3<<20); err != nil {
		t.Fatal(err)
	}
	old := newTransport
	newTransport = func() *network.Transport { return network.NewTransport(false) }
	defer func() { newTransport = old }()

	// 捕获 stdout
	r, w, _ := os.Pipe()
	oldStdout := os.Stdout
	os.Stdout = w
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	errProbe := RunProbe(ctx, &Options{URLs: []string{ts.FileURL("probe.bin")}})
	_ = w.Close()
	os.Stdout = oldStdout
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	out := string(buf[:n])
	if errProbe != nil {
		t.Fatalf("RunProbe: %v", errProbe)
	}
	if !strings.Contains(out, "size=3145728") || !strings.Contains(out, "ranged=true") {
		t.Errorf("探测输出缺失: %s", out)
	}

	// CD 端点 → name= 输出
	r2, w2, _ := os.Pipe()
	os.Stdout = w2
	errProbe2 := RunProbe(ctx, &Options{URLs: []string{ts.BaseURL() + "/cd/setup%20v2.exe"}})
	_ = w2.Close()
	os.Stdout = oldStdout
	buf2 := make([]byte, 4096)
	n2, _ := r2.Read(buf2)
	out2 := string(buf2[:n2])
	if errProbe2 != nil {
		t.Fatalf("RunProbe(cd): %v", errProbe2)
	}
	if !strings.Contains(out2, "name=setup v2.exe") {
		t.Errorf("CD 名缺失: %s", out2)
	}

	// 非回环 → 聚合错误
	if err := RunProbe(ctx, &Options{URLs: []string{"https://example.com/x"}}); err == nil {
		t.Error("非回环应报错")
	}
}
