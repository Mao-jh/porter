// round14_test.go 第 14 轮新增能力测试：-i/-j/-proxy/-load-cookies/-summary 解析、
// tasks 子命令渲染、Content-Disposition 自动命名端到端。
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

// TestParse_NewFlags 新标志解析（-proxy/-j/-load-cookies/-summary）。
func TestParse_NewFlags(t *testing.T) {
	ck := filepath.Join(t.TempDir(), "cookies.txt")
	_ = os.WriteFile(ck, []byte("x"), 0o600)
	opt, err := Parse([]string{
		"-proxy", "socks5://127.0.0.1:1080",
		"-j", "3",
		"-load-cookies", ck,
		"-summary",
		"http://127.0.0.1/a",
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if opt.Proxy != "socks5://127.0.0.1:1080" {
		t.Errorf("Proxy = %q", opt.Proxy)
	}
	if opt.Jobs != 3 {
		t.Errorf("Jobs = %d", opt.Jobs)
	}
	if opt.CookieFile != ck {
		t.Errorf("CookieFile = %q", opt.CookieFile)
	}
	if !opt.Summary {
		t.Error("Summary 应为 true")
	}
}

// TestParse_BoolFlagNoSwallow 布尔标志 -summary 不得吞掉其后的 URL。
func TestParse_BoolFlagNoSwallow(t *testing.T) {
	opt, err := Parse([]string{"-summary", "http://127.0.0.1/a", "http://127.0.0.1/b"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !opt.Summary {
		t.Error("Summary 应为 true")
	}
	if len(opt.URLs) != 2 {
		t.Errorf("URLs = %v", opt.URLs)
	}
}

// TestParse_URLFile -i 列表文件：注释/空行忽略、与位置参数合并、畸形行报错。
func TestParse_URLFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "urls.txt")
	if err := os.WriteFile(f, []byte(
		"# 注释\nhttp://127.0.0.1/1\n\nhttps://127.0.0.1/2\n# 尾注释\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opt, err := Parse([]string{"-i", f, "ftp://127.0.0.1/3"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// 预扫描把标志集中到位置参数之前 → 位置 URL 在前，-i 文件条目追加在后
	if len(opt.URLs) != 3 || opt.URLs[0] != "ftp://127.0.0.1/3" ||
		opt.URLs[1] != "http://127.0.0.1/1" || opt.URLs[2] != "https://127.0.0.1/2" {
		t.Errorf("URLs = %v", opt.URLs)
	}

	bad := filepath.Join(t.TempDir(), "bad.txt")
	_ = os.WriteFile(bad, []byte("gopher://127.0.0.1/x"), 0o600)
	if _, err := Parse([]string{"-i", bad}); err == nil {
		t.Error("畸形协议行应报错")
	}
}

// TestParse_NoURLs 无位置参数且无 -i 时报错。
func TestParse_NoURLs(t *testing.T) {
	if _, err := Parse([]string{"-summary"}); err == nil {
		t.Error("应报错")
	}
}

// TestListTasks tasks 子命令渲染（按更新时间倒序）。
func TestListTasks(t *testing.T) {
	var sb strings.Builder
	states := []*persist.State{
		{ID: "a.bin", URL: "http://127.0.0.1/a", FileSize: 2 << 20, Done: 1 << 20,
			Status: "running", UpdatedAt: time.Now().Add(-time.Minute).UnixNano()},
		{ID: "b.bin", URL: "http://127.0.0.1/b", FileSize: 1 << 20, Done: 1 << 20,
			Status: "done", UpdatedAt: time.Now().UnixNano()},
	}
	if err := listTasks(&sb, states); err != nil {
		t.Fatalf("listTasks: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "共 2 个任务") {
		t.Errorf("缺少任务计数:\n%s", out)
	}
	// 更新时间倒序：b.bin 在 a.bin 之前
	if strings.Index(out, "b.bin") > strings.Index(out, "a.bin") {
		t.Errorf("倒序错误:\n%s", out)
	}
	if !strings.Contains(out, "50.0%") || !strings.Contains(out, "100.0%") {
		t.Errorf("缺少百分比:\n%s", out)
	}
}

// TestListTasks_Empty 空状态输出提示。
func TestListTasks_Empty(t *testing.T) {
	var sb strings.Builder
	if err := listTasks(&sb, nil); err != nil {
		t.Fatalf("listTasks: %v", err)
	}
	if !strings.Contains(sb.String(), "无任务记录") {
		t.Errorf("空提示缺失: %q", sb.String())
	}
}

// TestPrintSummary 摘要输出（-summary 路径，经 summaryTracker）。
func TestPrintSummary(t *testing.T) {
	var sb strings.Builder
	tr := newSummaryTracker()
	tr.renderAt(&sb, []*persist.State{{
		ID: "c.bin", URL: "http://127.0.0.1/c", FileSize: 4 << 20, Done: 3 << 20, Status: "running",
	}}, time.Unix(1000, 0), true)
	out := sb.String()
	if !strings.Contains(out, "[进度]") || !strings.Contains(out, "75.0%") || !strings.Contains(out, "3.0/4.0MiB") {
		t.Errorf("摘要格式不符: %q", out)
	}
	var sb2 strings.Builder
	tr.renderAt(&sb2, nil, time.Unix(1001, 0), true) // 空集不输出（无 panic 即通过）
	if sb2.Len() != 0 {
		t.Errorf("空集应无输出: %q", sb2.String())
	}
}

// TestAutoFilename_CD 端到端：无 -o 时输出名取自 Content-Disposition。
func TestAutoFilename_CD(t *testing.T) {
	ts, err := testserver.New(testserver.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()

	old := newTransport
	newTransport = func() *network.Transport { return network.NewTransport(false) }
	defer func() { newTransport = old }()

	cwd, _ := os.Getwd()
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	opt := &Options{
		URL:      ts.BaseURL() + "/cd/setup%20v2.exe",
		URLs:     []string{ts.BaseURL() + "/cd/setup%20v2.exe"},
		Verify:   "",
		StateDir: filepath.Join(tmp, "state"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Run(ctx, opt); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := filepath.Join(tmp, "setup v2.exe")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("CD 命名产物缺失 %s: %v", want, err)
	}
}

// TestRun_JobsCap -j=1 时多任务仍全部完成（并发上限只影响调度，不丢任务）。
func TestRun_JobsCap(t *testing.T) {
	ts, err := testserver.New(testserver.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	var urls []string
	for _, n := range []string{"j1.bin", "j2.bin", "j3.bin"} {
		if _, err := ts.CreateFile(n, 256<<10); err != nil {
			t.Fatal(err)
		}
		urls = append(urls, ts.FileURL(n))
	}

	old := newTransport
	newTransport = func() *network.Transport { return network.NewTransport(false) }
	defer func() { newTransport = old }()

	outDir := t.TempDir()
	opt := &Options{
		URL: urls[0], URLs: urls,
		Output:   outDir, // 多 URL：-o 为目录
		Jobs:     1,
		Verify:   "",
		Summary:  true, // 顺带覆盖 summary 输出路径
		StateDir: filepath.Join(outDir, "state"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := Run(ctx, opt); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, n := range []string{"j1.bin", "j2.bin", "j3.bin"} {
		if _, err := os.Stat(filepath.Join(outDir, n)); err != nil {
			t.Errorf("产物缺失 %s: %v", n, err)
		}
	}
}

// TestCookieE2EThroughCLI cookie 文件经 CLI 加载并完成下载（注入正确性由
// network 层 TestSetCookies_DomainMatchAndMerge 覆盖；此处验证 CLI 接线全链路）。
func TestCookieE2EThroughCLI(t *testing.T) {
	ts, err := testserver.New(testserver.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	if _, err := ts.CreateFile("withck.bin", 64<<10); err != nil {
		t.Fatal(err)
	}

	ck := filepath.Join(t.TempDir(), "cookies.txt")
	content := "127.0.0.1\tFALSE\t/\tFALSE\t1893456000\tsid\tfrom-file\n" +
		"#HttpOnly_.example.com\tTRUE\t/\tTRUE\t1893456000\ttoken\tt0p\n"
	if err := os.WriteFile(ck, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	old := newTransport
	newTransport = func() *network.Transport { return network.NewTransport(false) }
	defer func() { newTransport = old }()

	tmp := t.TempDir()
	opt := &Options{
		URL:        ts.FileURL("withck.bin"),
		URLs:       []string{ts.FileURL("withck.bin")},
		Output:     filepath.Join(tmp, "withck.bin"),
		CookieFile: ck,
		Verify:     "sha256",
		StateDir:   filepath.Join(tmp, "state"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Run(ctx, opt); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// sha256 校验在 Run 内完成（通过即内容完整）——cookie 加载/解析/注入链路无误
}
