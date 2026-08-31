// round17_test.go 第 17 轮测试：HLS 自动命名 / porter retry / 续传守卫 / ProbeURL。
package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mao-jh/porter/hash"
	"github.com/Mao-jh/porter/network"
	"github.com/Mao-jh/porter/persist"
	"github.com/Mao-jh/porter/scheduler"
	"github.com/Mao-jh/porter/testserver"
)

// TestRun_HLSAutoName 单 URL 无 -o：HLS 输出名去 .m3u8 后缀且内容完整。
func TestRun_HLSAutoName(t *testing.T) {
	ts, err := testserver.New(testserver.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	if _, err := ts.CreateFile("big.bin", 3<<20); err != nil {
		t.Fatal(err)
	}
	want, err := ts.Checksum("big.bin")
	if err != nil {
		t.Fatal(err)
	}

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
		URL:      ts.BaseURL() + "/hls/big.bin.m3u8",
		URLs:     []string{ts.BaseURL() + "/hls/big.bin.m3u8"},
		Verify:   hash.SHA256,
		StateDir: filepath.Join(tmp, "state"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := Run(ctx, opt); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := filepath.Join(tmp, "big.bin") // .m3u8 已去除
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("HLS 自动命名产物缺失 %s: %v", got, err)
	}
	if _, err := os.Stat(got + ".m3u8"); err == nil {
		t.Error("不应产出带 .m3u8 后缀的文件")
	}
	sum, err := fileSHA256(got)
	if err != nil {
		t.Fatal(err)
	}
	if sum != want {
		t.Errorf("HLS 自动命名内容 sha256 不符: got %s want %s", sum, want)
	}
}

// TestRunRetry_SkipsDoneAndResumesPending retry：done 跳过、running 续传重跑完成。
func TestRunRetry_SkipsDoneAndResumesPending(t *testing.T) {
	ts, err := testserver.New(testserver.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	const size = int64(4 << 20)
	for _, n := range []string{"done.bin", "pending.bin"} {
		if _, err := ts.CreateFile(n, size); err != nil {
			t.Fatal(err)
		}
	}
	wantPending, _ := ts.Checksum("pending.bin")

	old := newTransport
	newTransport = func() *network.Transport { return network.NewTransport(false) }
	defer func() { newTransport = old }()

	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	doneOut := filepath.Join(tmp, "done.bin")
	pendingOut := filepath.Join(tmp, "pending.bin")

	// 1) done 任务：正常完成
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := Run(ctx, &Options{
		URL: ts.FileURL("done.bin"), URLs: []string{ts.FileURL("done.bin")},
		Output: doneOut, Verify: hash.SHA256, StateDir: stateDir,
	}); err != nil {
		t.Fatalf("预置 done 任务失败: %v", err)
	}

	// 2) 手工构造 running 任务：状态含分片进度，但 .part 是 1 字节占位
	//    （模拟"崩溃后用户误删 .part / 残留半截文件"——续传守卫必须退回全新下载）
	store, err := persist.Open(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(&persist.State{
		ID: pendingOut, URL: ts.FileURL("pending.bin"),
		FileSize: size, Done: size / 2, Status: "running",
		UpdatedAt: time.Now().UnixNano(),
		Shards:    []persist.ShardState{{Start: 0, End: size, Done: size / 2}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pendingOut+".part", []byte{0}, 0o600); err != nil {
		t.Fatal(err)
	}

	// 3) retry：done 跳过，pending 重跑至完成
	if err := RunRetry(ctx, &Options{StateDir: stateDir, Verify: hash.SHA256}); err != nil {
		t.Fatalf("RunRetry: %v", err)
	}
	// 产物完整且内容正确（无空洞）
	sum, err := fileSHA256(pendingOut)
	if err != nil {
		t.Fatal(err)
	}
	if sum != wantPending {
		t.Errorf("retry 产物 sha256 不符: got %s want %s", sum, wantPending)
	}
	// done 任务未被重跑（状态仍 done）——重新打开 store 读取磁盘最新状态
	store2, err := persist.Open(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if st, ok := store2.Get(doneOut); !ok || st.Status != "done" {
		t.Errorf("done 任务状态异常: ok=%v st=%+v", ok, st)
	}
	if st, ok := store2.Get(pendingOut); !ok || st.Status != "done" {
		t.Errorf("pending 任务应已 done: ok=%v st=%+v", ok, st)
	}
}

// TestRunRetry_NoPending 无未完成任务：提示且成功。
func TestRunRetry_NoPending(t *testing.T) {
	tmp := t.TempDir()
	store, err := persist.Open(filepath.Join(tmp, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(&persist.State{ID: "a.bin", URL: "http://127.0.0.1/x",
		Status: "done", UpdatedAt: time.Now().UnixNano()}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := RunRetry(ctx, &Options{StateDir: filepath.Join(tmp, "state")}); err != nil {
		t.Fatalf("RunRetry(无 pending) 应成功: %v", err)
	}
}

// TestParseRetry 旗标解析。
func TestParseRetry(t *testing.T) {
	opt, err := ParseRetry([]string{"-state-dir", "s1", "-limit", "1024", "-proxy", "socks5://127.0.0.1:1080", "-summary"})
	if err != nil {
		t.Fatalf("ParseRetry: %v", err)
	}
	if opt.StateDir != "s1" || opt.Limit != 1024 || opt.Proxy != "socks5://127.0.0.1:1080" || !opt.Summary {
		t.Errorf("ParseRetry 解析不符: %+v", opt)
	}
	if _, err := ParseRetry([]string{"-bogus", "x"}); err == nil {
		t.Error("未知旗标应报错")
	}
}

// TestProbeURL_CD 探测：大小 / ranged / CD 建议名。
func TestProbeURL_CD(t *testing.T) {
	ts, err := testserver.New(testserver.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	if _, err := ts.CreateFile("p.bin", 1<<20); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	size, ranged, name, err := ProbeURL(ctx, "", "", false, nil, ts.BaseURL()+"/cd/setup%20v2.exe")
	if err != nil {
		t.Fatalf("ProbeURL: %v", err)
	}
	if size != 16 || ranged || name != "setup v2.exe" {
		t.Errorf("CD 探测: size=%d ranged=%v name=%q", size, ranged, name)
	}
	size, ranged, name, err = ProbeURL(ctx, "", "", false, nil, ts.FileURL("p.bin"))
	if err != nil {
		t.Fatalf("ProbeURL: %v", err)
	}
	if size != 1<<20 || !ranged || name != "" {
		t.Errorf("普通文件探测: size=%d ranged=%v name=%q", size, ranged, name)
	}
}

// TestResumeGuard_PartWrongSize 直接验证续传守卫：.part 尺寸不符 → 全新下载内容正确。
func TestResumeGuard_PartWrongSize(t *testing.T) {
	ts, err := testserver.New(testserver.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	const size = int64(2 << 20)
	if _, err := ts.CreateFile("g.bin", size); err != nil {
		t.Fatal(err)
	}
	want, _ := ts.Checksum("g.bin")

	old := newTransport
	newTransport = func() *network.Transport { return network.NewTransport(false) }
	defer func() { newTransport = old }()

	tmp := t.TempDir()
	out := filepath.Join(tmp, "g.bin")
	store, err := persist.Open(filepath.Join(tmp, "state"))
	if err != nil {
		t.Fatal(err)
	}
	// 状态声称已下载一半，但 .part 只有 1 字节 → 守卫必须弃用状态全新下载
	if err := store.Put(&persist.State{
		ID: out, URL: ts.FileURL("g.bin"), FileSize: size, Done: size / 2,
		Status: "running", UpdatedAt: time.Now().UnixNano(),
		Shards: []persist.ShardState{{Start: 0, End: size, Done: size / 2}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out+".part", []byte{0}, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := Run(ctx, &Options{
		URL: ts.FileURL("g.bin"), URLs: []string{ts.FileURL("g.bin")},
		Output: out, Verify: hash.SHA256, StateDir: filepath.Join(tmp, "state"),
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	sum, err := fileSHA256(out)
	if err != nil {
		t.Fatal(err)
	}
	if sum != want {
		t.Errorf("续传守卫产物 sha256 不符: got %s want %s", sum, want)
	}
}

// fileSHA256 计算文件 sha256。
func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return hash.Sum(f, hash.SHA256)
}

// 防止未使用导入（scheduler 仅用于文档性说明）
var _ = scheduler.MaxConnections
var _ = strings.TrimSpace
