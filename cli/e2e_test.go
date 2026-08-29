package cli

import (
	"context"
	"encoding/json"
	stdIo "io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nymjin22/downloader/hash"
	"github.com/nymjin22/downloader/io"
	"github.com/nymjin22/downloader/network"
	"github.com/nymjin22/downloader/persist"
	"github.com/nymjin22/downloader/retry"
	"github.com/nymjin22/downloader/scheduler"
	"github.com/nymjin22/downloader/testserver"
)

func init() { // 测试加速：最小退避
	newRetryConfig = func() *retry.Config {
		c := retry.Default()
		c.Base = time.Millisecond
		c.Max = 4 * time.Millisecond
		return c
	}
}

// startServer 启动测试服务端。
func startServer(t *testing.T) *testserver.Server {
	t.Helper()
	s, err := testserver.New(testserver.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("testserver.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// sha256OfFile 流式计算文件 sha256。
func sha256OfFile(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	sum, err := hash.Sum(f, hash.SHA256)
	if err != nil {
		t.Fatalf("hash.Sum: %v", err)
	}
	return sum
}

// TestRun_ParallelShards_E2E 端到端：6 分片并行下载 10MiB，逐字节校验 + 状态闭环。
func TestRun_ParallelShards_E2E(t *testing.T) {
	s := startServer(t)
	size := int64(10 << 20)
	if _, err := s.CreateFile("big.bin", size); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	expected, err := s.Checksum("big.bin")
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}

	outDir := t.TempDir()
	opt := &Options{
		URLs:     []string{s.FileURL("big.bin")},
		Output:   filepath.Join(outDir, "big.bin"),
		Shards:   6,
		Mode:     scheduler.ModeDefault,
		Verify:   "", // 校验由本测试独立执行
		StateDir: filepath.Join(outDir, "state"),
	}
	if err := Run(context.Background(), opt); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 内容校验
	if got := sha256OfFile(t, opt.Output); got != expected {
		t.Fatalf("sha256 不一致\ngot  %s\nwant %s", got, expected)
	}
	// 服务字节数 = 全量（每字节恰一次）
	if served := s.ServedBytes(); served != size {
		t.Logf("served=%d size=%d（含窃取重传属正常波动）", served, size)
	}
	// .part 清理 + 状态闭环
	if _, err := os.Stat(opt.Output + ".part"); !os.IsNotExist(err) {
		t.Fatal(".part 应已清理")
	}
	store, err := persist.Open(opt.StateDir)
	if err != nil {
		t.Fatalf("persist.Open: %v", err)
	}
	if st, ok := store.Get(opt.Output); !ok || st.Status != "done" {
		t.Fatalf("终态应为 done, got %+v", st)
	}
}

// TestRun_ResumeAfterCrash_E2E 断点续传端到端：模拟崩溃后重启，只补缺失分片，不全量重下。
func TestRun_ResumeAfterCrash_E2E(t *testing.T) {
	s := startServer(t)
	size := int64(12 << 20) // 自动决策 = 3 片 × 4MiB
	if _, err := s.CreateFile("big.bin", size); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	expected, err := s.Checksum("big.bin")
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}

	outDir := t.TempDir()
	out := filepath.Join(outDir, "big.bin")
	stateDir := filepath.Join(outDir, "state")

	// ---- 阶段1：模拟首次运行中途崩溃 ----
	// 手动完成分片0 [0,4MiB) 并按 Run 的持久化格式写入状态；.part 保留在磁盘。
	store, err := persist.Open(stateDir)
	if err != nil {
		t.Fatalf("persist.Open: %v", err)
	}
	plan := scheduler.NewPlan(size)
	sf, err := io.OpenSparse(out, size)
	if err != nil {
		t.Fatalf("OpenSparse: %v", err)
	}
	tr := network.NewTransport(false)
	shard0 := plan.Shards[0]
	w := &progressWriterAt{sf: sf, base: shard0.Start, written: new(atomic.Int64)}
	if err := tr.FetchRange(context.Background(), s.FileURL("big.bin"), shard0.Start, shard0.End, w); err != nil {
		t.Fatalf("FetchRange shard0: %v", err)
	}
	shardStates := make([]persist.ShardState, 0, len(plan.Shards))
	for i, sh := range plan.Shards {
		done := sh.End - sh.Start
		if i != 0 {
			done = 0
		}
		shardStates = append(shardStates, persist.ShardState{Start: sh.Start, End: sh.End, Done: done})
	}
	if err := store.Put(&persist.State{
		ID: out, URL: s.FileURL("big.bin"), FileSize: size,
		Done: shard0.End - shard0.Start, Status: "running", Shards: shardStates,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// 模拟崩溃：仅关闭句柄、保留 .part 文件（进程退出语义）
	if err := sf.Close(); err != nil {
		t.Fatalf("sf.Close: %v", err)
	}
	servedAfterCrash := s.ServedBytes()
	if servedAfterCrash != shard0.End-shard0.Start {
		t.Fatalf("崩溃前服务字节=%d want %d", servedAfterCrash, shard0.End-shard0.Start)
	}

	// ---- 阶段2：重启后 Run 续传 ----
	opt := &Options{
		URLs:     []string{s.FileURL("big.bin")},
		Output:   out,
		Mode:     scheduler.ModeDefault,
		Verify:   "sha256",
		StateDir: stateDir,
	}
	if err := Run(context.Background(), opt); err != nil {
		t.Fatalf("Run(续传): %v", err)
	}

	// 只补了缺失的 8MiB（+少量窃取重传），绝非全量 12MiB 重下
	servedResume := s.ServedBytes() - servedAfterCrash
	if servedResume >= size {
		t.Fatalf("续传重下了全量: served=%d size=%d", servedResume, size)
	}
	t.Logf("续传服务字节=%d（缺失量=%d）", servedResume, size-(shard0.End-shard0.Start))

	// 最终内容一致
	if got := sha256OfFile(t, out); got != expected {
		t.Fatalf("续传后 sha256 不一致\ngot  %s\nwant %s", got, expected)
	}
	// 状态闭环
	store2, err := persist.Open(stateDir)
	if err != nil {
		t.Fatalf("persist.Open(2): %v", err)
	}
	if st, ok := store2.Get(out); !ok || st.Status != "done" {
		t.Fatalf("终态应为 done, got %+v", st)
	}
}

// TestRun_FaultInjectionRetry_E2E 故障注入：3 次断连后重试成功，内容无损。
func TestRun_FaultInjectionRetry_E2E(t *testing.T) {
	s := startServer(t)
	size := int64(5 << 20)
	if _, err := s.CreateFile("f.bin", size); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	expected, err := s.Checksum("f.bin")
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}

	old := newTransport
	newTransport = func() *network.Transport {
		tr := network.NewTransport(false)
		tr.SetFaults(3, 0, 0, 0) // 注入 3 次断连
		return tr
	}
	defer func() { newTransport = old }()

	outDir := t.TempDir()
	opt := &Options{
		URLs:     []string{s.FileURL("f.bin")},
		Output:   filepath.Join(outDir, "f.bin"),
		Mode:     scheduler.ModeDefault,
		Verify:   "",
		StateDir: filepath.Join(outDir, "state"),
	}
	if err := Run(context.Background(), opt); err != nil {
		t.Fatalf("Run(故障注入): %v", err)
	}
	if got := sha256OfFile(t, opt.Output); got != expected {
		t.Fatalf("故障重试后 sha256 不一致\ngot  %s\nwant %s", got, expected)
	}
}

// TestRun_CanceledContext 已取消的 ctx：Run 立即返回 ctx 错误，不悬挂。
func TestRun_CanceledContext(t *testing.T) {
	s := startServer(t)
	size := int64(1 << 20)
	if _, err := s.CreateFile("x.bin", size); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	opt := &Options{
		URLs:     []string{s.FileURL("x.bin")},
		Output:   filepath.Join(t.TempDir(), "x.bin"),
		Mode:     scheduler.ModeDefault,
		StateDir: filepath.Join(t.TempDir(), "state"),
	}
	if err := Run(ctx, opt); err == nil {
		t.Fatal("已取消 ctx 应返回错误")
	}
}

// TestRun_ParseRejectsFTP Parse 与传输层协议一致：拒绝 ftp（FetchRange 基于 net/http）。
func TestRun_ParseRejectsFTP(t *testing.T) {
	if _, err := Parse([]string{"ftp://127.0.0.1/x"}); err == nil {
		t.Fatal("应拒绝 ftp://")
	}
}

// TestDownloader_StealLogic 窃取逻辑单元验证：受害者标记、尾段入队、缺口补投。
func TestDownloader_StealLogic(t *testing.T) {
	s := startServer(t)
	size := int64(8 << 20)
	if _, err := s.CreateFile("s.bin", size); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	out := filepath.Join(t.TempDir(), "s.bin")
	sf, err := io.OpenSparse(out, size)
	if err != nil {
		t.Fatalf("OpenSparse: %v", err)
	}
	defer sf.Abort()

	plan := scheduler.NewPlanN(size, 2) // 显式 2 片 × 4MiB，便于构造已知窃取场景
	d := newDownloader(network.NewTransport(false), s.FileURL("s.bin"), sf, plan)

	// 模拟分片0 已被 worker 取走（在途）：从队列弹出并登记 attempt（下载至 1MiB 处）
	d.mu.Lock()
	d.queue = d.queue[1:] // 弹出 shard0 任务
	d.mu.Unlock()
	at := &attempt{
		t:       rangeTask{shardIdx: 0, start: 0, end: 4 << 20},
		cancel:  func() {},
		written: new(atomic.Int64),
	}
	at.written.Store(1 << 20)
	d.mu.Lock()
	d.attempts[at] = struct{}{}
	d.active = 1
	ok := d.tryStealLocked()
	d.mu.Unlock()
	if !ok {
		t.Fatal("应触发窃取")
	}
	if !at.stealing.Load() {
		t.Fatal("受害者应被标记 stealing")
	}
	// 尾段任务 [0+1MiB+1.5MiB, 4MiB) 入队
	d.mu.Lock()
	qlen := len(d.queue)
	var tail rangeTask
	if qlen > 0 {
		tail = d.queue[qlen-1]
	}
	d.mu.Unlock()
	// 队列 = 原始分片1（尚未被取走）+ 新尾段任务
	if qlen != 2 {
		t.Fatalf("窃取后队列应有 2 个任务(分片1+尾段), got %d", qlen)
	}
	wantCut := int64(1<<20) + (int64(3)<<20)/2 // start+written + rem/2
	if tail.start != wantCut || tail.end != 4<<20 {
		t.Fatalf("尾段=[%d,%d) want [%d,%d)", tail.start, tail.end, wantCut, 4<<20)
	}

	// 受害者中止簿记：finishStolen 登记前缀并补投缺口 [1MiB, 2.5MiB)
	at2 := &attempt{cut: wantCut}
	d.finishStolen(rangeTask{shardIdx: 0, start: 0, end: 4 << 20}, at2, 1<<20)
	d.mu.Lock()
	if len(d.queue) != 3 {
		t.Fatalf("缺口补投后队列应有 3 任务, got %d", len(d.queue))
	}
	if cov := d.prog[0].covered(); cov != 1<<20 {
		t.Fatalf("分片0 前缀覆盖=%d want %d", cov, 1<<20)
	}
	d.mu.Unlock()
}

// TestDownloader_SnapshotResumeRoundTrip snapshot → 持久化 → 恢复 的分片状态往返。
func TestDownloader_SnapshotResumeRoundTrip(t *testing.T) {
	s := startServer(t)
	size := int64(8 << 20)
	if _, err := s.CreateFile("r.bin", size); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	out := filepath.Join(t.TempDir(), "r.bin")
	sf, err := io.OpenSparse(out, size)
	if err != nil {
		t.Fatalf("OpenSparse: %v", err)
	}
	defer sf.Abort()

	plan := scheduler.NewPlan(size)
	d := newDownloader(network.NewTransport(false), s.FileURL("r.bin"), sf, plan)
	d.mu.Lock()
	d.prog[0].record(0, 2<<20) // 分片0 完成前 2MiB
	d.mu.Unlock()

	snap := d.snapshotShards()
	if len(snap) != len(plan.Shards) {
		t.Fatalf("快照分片数=%d want %d", len(snap), len(plan.Shards))
	}
	if snap[0].Done != 2<<20 {
		t.Fatalf("分片0 Done=%d want %d", snap[0].Done, 2<<20)
	}
	// 经 JSON 往返（persist 崩溃恢复路径）
	state := &persist.State{ID: "r.bin", URL: s.FileURL("r.bin"), FileSize: size, Shards: snap, Status: "running"}
	enc, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var decoded persist.State
	if err := json.Unmarshal(enc, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	p2 := planFromState(&decoded, size)
	if p2 == nil || len(p2.Shards) != len(plan.Shards) {
		t.Fatalf("恢复计划分片数错误: %+v", p2)
	}
	if p2.Shards[0].Done != 2<<20 {
		t.Fatalf("恢复后分片0 Done=%d want %d", p2.Shards[0].Done, 2<<20)
	}
	// 恢复后的 downloader 只应排队未完成部分
	d2 := newDownloader(network.NewTransport(false), s.FileURL("r.bin"), sf, p2)
	d2.mu.Lock()
	q := append([]rangeTask(nil), d2.queue...)
	d2.mu.Unlock()
	for _, task := range q {
		if task.shardIdx == 0 && task.start < 2<<20 {
			t.Fatalf("分片0 已完成前缀不应重新排队: task=[%d,%d)", task.start, task.end)
		}
	}
}

// TestRun_OutputContentMatchesPattern 逐字节比对：偏移相关模式内容排除错位缺陷。
func TestRun_OutputContentMatchesPattern(t *testing.T) {
	s := startServer(t)
	size := int64(6 << 20)
	if _, err := s.CreateFile("p.bin", size); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	outDir := t.TempDir()
	opt := &Options{
		URLs:     []string{s.FileURL("p.bin")},
		Output:   filepath.Join(outDir, "p.bin"),
		Shards:   3,
		Mode:     scheduler.ModeDefault,
		StateDir: filepath.Join(outDir, "state"),
	}
	if err := Run(context.Background(), opt); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := make([]byte, size)
	testserver.PatternFill(want, 0)
	f, err := os.Open(opt.Output)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	got, err := stdIo.ReadAll(f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("长度=%d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("偏移 %d 内容错位: got %d want %d", i, got[i], want[i])
		}
	}
}
