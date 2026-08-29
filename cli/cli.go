// Package cli 实现命令行接口（USAGE.md 的对应实现）。
// 用法：downloader <url> [-o output] [-n shards] [-mode default|max] [-verify sha256]
//
// 架构职责：
//   - 探测资源大小与 Range 支持（network.Transport.Probe）→ 决定并行分片 vs 流式单连接
//   - 打开 persist.Store，恢复每分片已完成前缀（断点续传）
//   - 分片作为范围任务进入共享队列；工作协程取任务下载，空闲时窃取慢分片的尾段
//     （IDM 式动态分段：快连接完成后接管慢连接的剩余区间）
//   - 结果经 io.SparseFile 稀疏落盘；进度周期性原子持久化，异常退出后可续传
//   - 完成后流式校验（hash.Sum），全程固定 64KiB 缓冲（H-1/H-2）
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nymjin22/downloader/hash"
	"github.com/nymjin22/downloader/io"
	"github.com/nymjin22/downloader/network"
	"github.com/nymjin22/downloader/persist"
	"github.com/nymjin22/downloader/retry"
	"github.com/nymjin22/downloader/scheduler"
)

// Options 解析后的命令行选项。
type Options struct {
	URL      string   // 首个 URL（单任务兼容字段）
	URLs     []string // 全部位置参数 URL（>1 个时走多任务队列）
	Output   string   // 单任务输出路径；多任务时必须为空（自动命名）
	Shards   int
	Mode     scheduler.Mode
	Verify   hash.Algorithm
	StateDir string
	Limit    int64             // 全局下载限速（字节/秒，0=不限），所有连接共享
	Headers  map[string]string // 每请求透传头（-H，Cookie/Authorization 等）
}

// headerList 收集可重复的 -H "Key: Value" 标志。
type headerList []string

func (h *headerList) String() string { return strings.Join(*h, "; ") }

func (h *headerList) Set(v string) error {
	if strings.IndexByte(v, ':') <= 0 {
		return fmt.Errorf("非法 -H（应为 \"Key: Value\"）: %q", v)
	}
	*h = append(*h, v)
	return nil
}

// headerMap 把 "Key: Value" 列表转为映射（同名后者覆盖前者）。
func headerMap(raw []string) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	m := make(map[string]string, len(raw))
	for _, h := range raw {
		i := strings.IndexByte(h, ':')
		if i <= 0 {
			return nil, fmt.Errorf("非法请求头: %q", h)
		}
		m[strings.TrimSpace(h[:i])] = strings.TrimSpace(h[i+1:])
	}
	return m, nil
}

// newTransport 生产传输层构造（H-3：禁止非回环）。测试可替换以注入故障。
var newTransport = func() *network.Transport { return network.NewTransport(false) }

// newRetryConfig 生产重试参数（1s 起步、±20% 抖动、上限 30s、最多 8 次）。
var newRetryConfig = func() *retry.Config { return retry.Default() }

// Parse 解析 args。Go flag 包在首个位置参数后停止解析（`downloader <url> -o x`
// 中的 -o 会被当成位置参数），故先把标志参数集中到位置参数之前再交给 fs.Parse。
func Parse(args []string) (*Options, error) {
	var flagArgs, posArgs []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) > 1 && a[0] == '-' {
			flagArgs = append(flagArgs, a)
			// 本工具全部为带值标志：若下一个 token 不以 - 开头则消费为标志值
			if i+1 < len(args) && !(len(args[i+1]) > 1 && args[i+1][0] == '-') {
				flagArgs = append(flagArgs, args[i+1])
				i++
			}
			continue
		}
		posArgs = append(posArgs, a)
	}
	args = append(flagArgs, posArgs...)

	fs := flag.NewFlagSet("downloader", flag.ContinueOnError)
	var (
		output   = fs.String("o", "", "输出路径（单 URL=文件路径；多 URL=输出目录，缺省为当前目录）")
		shards   = fs.Int("n", 0, "每任务分片数（0=自动决策，1..16）")
		mode     = fs.String("mode", "default", "CPU 模式: default(≤60%) | max(满载)；多任务时同时决定并发任务数")
		verify   = fs.String("verify", "sha256", "校验算法: sha256|sha1|md5|none")
		stateDir = fs.String("state-dir", ".downloader", "任务状态持久化目录")
		limit    = fs.Int64("limit", 0, "全局下载限速 字节/秒（0=不限，所有任务/分片共享）")
		hdrs     headerList
	)
	fs.Var(&hdrs, "H", "透传请求头 \"Key: Value\"（可重复，如 -H \"Cookie: a=b\"）")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() == 0 {
		return nil, errors.New("用法: downloader <url> [url2 ...] [-o out] [-n shards] [-limit bps] [-H \"K: V\"] [-mode default|max] [-verify sha256]")
	}
	urls := make([]string, 0, fs.NArg())
	for i := 0; i < fs.NArg(); i++ {
		u := fs.Arg(i)
		if !startsWithAny(u, "http://", "https://") {
			return nil, fmt.Errorf("不支持的 URL 协议: %s（仅 http/https）", u)
		}
		urls = append(urls, u)
	}
	hm, err := headerMap(hdrs)
	if err != nil {
		return nil, err
	}
	m := scheduler.ModeDefault
	switch *mode {
	case "default", "":
		m = scheduler.ModeDefault
	case "max", "maxperf":
		m = scheduler.ModeMaxPerf
	default:
		return nil, fmt.Errorf("非法 -mode: %s", *mode)
	}
	algo := hash.Algorithm(*verify)
	if algo == "none" {
		algo = ""
	}
	return &Options{
		URL:      urls[0],
		URLs:     urls,
		Output:   *output,
		Shards:   *shards,
		Mode:     m,
		Verify:   algo,
		StateDir: *stateDir,
		Limit:    *limit,
		Headers:  hm,
	}, nil
}

// Run 执行下载：单个 URL 直接下载；多个 URL 经调度器并发排队
// （并发任务数由 R-3 模式决定：default ⌈cpus×0.6⌉，max = cpus）。
// 单任务内断点续传流程：
//  1. Probe 探测大小；Store 中存在同 URL、同大小且未完成的状态时按分片恢复；
//  2. 范围任务队列 + 工作窃取并行下载，每分片完成区间实时记账；
//  3. 进度每 500ms 原子持久化（含在途前缀）；异常退出后重启续传；
//  4. 全部完成后覆盖守卫、原子提交 + 流式校验。
func Run(ctx context.Context, opt *Options) error {
	if opt == nil || len(opt.URLs) == 0 {
		return errors.New("cli: 无效选项")
	}
	return RunMulti(ctx, opt)
}

// RunMulti 执行一个或多个下载任务：全部任务经 scheduler.Submit 排队，
// 消费者按 R-3 模式决定的并发上限（Slots()）逐个领取执行；
// 每个任务内部仍是完整的分片并行引擎（工作窃取/字节级续传/覆盖守卫/校验）。
// 返回聚合错误（errors.Join），单个任务失败不影响其余任务。
func RunMulti(ctx context.Context, opt *Options) error {
	store, err := persist.Open(opt.StateDir)
	if err != nil {
		return fmt.Errorf("持久化打开失败: %w", err)
	}
	// 单一共享 Transport：全局限速配额由所有任务/分片共同消耗
	//（若每任务独立 Transport，-limit 会变成"每任务限额"而非全局限额）。
	tr := newTransport()
	tr.SetGlobalLimit(opt.Limit)
	if len(opt.Headers) > 0 {
		tr.SetHeaders(opt.Headers)
	}
	outs := deriveOutputs(opt.URLs)
	if len(opt.URLs) == 1 && opt.Output != "" {
		outs[0] = opt.Output // 单 URL：-o 为精确文件路径（兼容单任务语义）
	} else if opt.Output != "" {
		// 多 URL：-o 为输出目录，文件名自动取自 URL 并去重
		if err := os.MkdirAll(opt.Output, 0o755); err != nil {
			return fmt.Errorf("创建输出目录失败: %w", err)
		}
		for i := range outs {
			outs[i] = filepath.Join(opt.Output, outs[i])
		}
	}
	sched := scheduler.NewScheduler(runtime.NumCPU())
	sched.SetMode(opt.Mode)

	for i, u := range opt.URLs {
		if err := sched.Submit(&scheduler.Task{ID: outs[i], URL: u, Priority: 1}); err != nil {
			return fmt.Errorf("任务提交失败 %s: %w", u, err)
		}
	}

	type result struct {
		id  string
		err error
	}
	results := make(chan result, len(opt.URLs))
	consumers := sched.Slots()
	if consumers > len(opt.URLs) {
		consumers = len(opt.URLs)
	}
	var wg sync.WaitGroup
	for i := 0; i < consumers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				t, err := sched.Next(ctx)
				if err != nil {
					return // ErrNoTasks（全部完成）
				}
				var rerr error
				if ctxErr := ctx.Err(); ctxErr != nil {
					rerr = ctxErr // 上下文已取消：剩余任务统一记为取消，不再发起
				} else {
					rerr = runOne(ctx, tr, opt, t.URL, t.ID, store)
					if rerr == nil {
						fmt.Fprintf(os.Stderr, "[%s] 完成 <- %s\n", t.ID, t.URL)
					}
				}
				results <- result{t.ID, rerr}
				sched.Done(t.ID)
			}
		}()
	}
	wg.Wait()
	close(results)

	var errs []error
	for r := range results {
		if r.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.id, r.err))
		}
	}
	if ctx.Err() != nil {
		errs = append(errs, ctx.Err())
	}
	return errors.Join(errs...)
}

// deriveOutputs 从 URL 路径推导输出文件名并去重（多任务模式，-o 不可用）。
func deriveOutputs(urls []string) []string {
	seen := make(map[string]int, len(urls))
	outs := make([]string, len(urls))
	for i, u := range urls {
		base := ""
		if parsed, err := url.Parse(u); err == nil {
			base = path.Base(parsed.Path)
		}
		if base == "" || base == "." || base == "/" {
			base = fmt.Sprintf("download-%d.bin", i+1)
		}
		orig := base
		if n := seen[orig]; n > 0 { // 同名冲突：name-2.ext, name-3.ext ...
			ext := path.Ext(orig)
			base = fmt.Sprintf("%s-%d%s", strings.TrimSuffix(orig, ext), n+1, ext)
		}
		seen[orig]++ // 计数累加到原始名（而非改名后的名字）
		outs[i] = base
	}
	return outs
}

// runOne 执行单个 URL 的完整下载（协调 network/io/persist/hash）。
// tr 与 store 由调用方共享：限速配额全局统一、state.json 无并发写冲突。
func runOne(ctx context.Context, tr *network.Transport, opt *Options, urlStr, output string, store *persist.Store) error {
	// 探测资源大小与 Range 支持（决定并行分片 vs 流式单连接）
	size, ranged, err := tr.Probe(ctx, urlStr)
	if err != nil {
		return fmt.Errorf("探测资源失败: %w", err)
	}

	// 计划构造：断点续传恢复 > 显式 -n > 自动决策
	var plan *scheduler.Plan
	resume := false
	if st, ok := store.Get(output); ok && st.Status != "done" &&
		size > 0 && st.FileSize == size && len(st.Shards) > 0 {
		plan = planFromState(st, size)
		resume = true
	} else if size > 0 && ranged {
		plan = scheduler.NewPlanN(size, opt.Shards)
	}
	if plan == nil {
		// 大小未知或服务端不支持 Range：单连接流式（不发 Range 头，服务端 200 全量）
		plan = scheduler.NewPlan(0)
		resume = false
	}
	if !resume {
		_ = os.Remove(output + ".part") // 全新任务：清理上次残留的临时文件
	}

	// 稀疏文件：续传时保留 .part 已有内容（OpenSparse 不截断）
	sf, err := io.OpenSparse(output, plan.FileSize)
	if err != nil {
		return fmt.Errorf("打开输出文件失败: %w", err)
	}
	defer sf.Abort() // 未 Commit 前保证清理

	d := newDownloader(tr, urlStr, sf, plan)

	// 周期性进度持久化（stop 关闭后协程退出，保证无泄漏）
	stopFlush := make(chan struct{})
	flushExited := make(chan struct{})
	go func() {
		defer close(flushExited)
		tk := time.NewTicker(500 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopFlush:
				return
			case <-tk.C:
				flushState(store, output, urlStr, size, d.snapshotShards(), "running")
			}
		}
	}()

	// 并行下载
	if err := d.run(ctx); err != nil {
		flushState(store, output, urlStr, size, d.snapshotShards(), "running") // 保留最新进度供续传
		close(stopFlush)
		<-flushExited
		return err
	}
	close(stopFlush)
	<-flushExited
	// 覆盖守卫：已知大小时每分片必须完整覆盖（防任务静默丢失产生空洞文件）
	if size > 0 {
		for _, ss := range d.snapshotShards() {
			if ss.End > 0 && ss.Done != ss.End-ss.Start {
				return fmt.Errorf("分片覆盖不完整: [%d,%d) Done=%d", ss.Start, ss.End, ss.Done)
			}
		}
	}
	flushState(store, output, urlStr, size, d.snapshotShards(), "running")

	// 大小守卫：分片覆盖不变量在文件系统层面的最终校验
	if size > 0 {
		if info, err := os.Stat(output + ".part"); err != nil || info.Size() != size {
			return fmt.Errorf("分片覆盖校验失败: 期望 %d 字节, 实际 %d（err=%v）", size, infoSize(info), err)
		}
	}

	if err := sf.Commit(); err != nil {
		return fmt.Errorf("提交文件失败: %w", err)
	}
	flushState(store, output, urlStr, size, nil, "done")

	// 流式校验（H-2：固定缓冲，不全文件读入内存）
	if opt.Verify != "" {
		f, err := os.Open(output)
		if err != nil {
			return err
		}
		defer f.Close()
		sum, err := hash.Sum(f, opt.Verify)
		if err != nil {
			return fmt.Errorf("校验失败: %w", err)
		}
		fmt.Fprintf(os.Stderr, "[verify] %s(%s)=%s\n", output, opt.Verify, sum)
	}
	return nil
}

func infoSize(info os.FileInfo) int64 {
	if info == nil {
		return -1
	}
	return info.Size()
}

// planFromState 从持久化状态恢复分片计划（含各分片已完成前缀）。
func planFromState(st *persist.State, size int64) *scheduler.Plan {
	p := &scheduler.Plan{FileSize: size}
	shards := make([]scheduler.Shard, 0, len(st.Shards))
	for _, ss := range st.Shards {
		if ss.End <= ss.Start || ss.Start < 0 || ss.End > size {
			continue // 非法条目丢弃（防御性）
		}
		done := ss.Done
		if done > ss.End-ss.Start {
			done = ss.End - ss.Start
		}
		shards = append(shards, scheduler.Shard{Index: len(shards), Start: ss.Start, End: ss.End, Done: done})
	}
	if len(shards) == 0 {
		return nil
	}
	p.Shards = shards
	return p
}

// flushState 将当前任务状态原子持久化（失败仅记日志，不中断下载）。
func flushState(store *persist.Store, id, urlStr string, size int64, shards []persist.ShardState, status string) {
	var done int64
	for _, s := range shards {
		done += s.Done
	}
	if status == "done" {
		done = size
	}
	st := &persist.State{
		ID:        id,
		URL:       urlStr,
		FileSize:  size,
		Done:      done,
		Status:    status,
		UpdatedAt: time.Now().UnixNano(),
		Shards:    shards,
	}
	if err := store.Put(st); err != nil {
		fmt.Fprintf(os.Stderr, "[warn] 状态持久化失败: %v\n", err)
	}
}

// ---------------------------------------------------------------------------
// 下载引擎：范围任务队列 + 工作窃取
// ---------------------------------------------------------------------------

// rangeTask 是一段待下载区间 [start,end)。end=0 表示到 EOF（open-ended）。
type rangeTask struct {
	shardIdx int
	start    int64
	end      int64
}

// shardProgress 记录一个原始分片已完成区间的并集（支持乱序到达）。
type shardProgress struct {
	start     int64
	end       int64      // 原始分片终点（end=0 表示流式未知）
	intervals [][2]int64 // 已落盘且互不重叠的完成区间，按起点有序
}

// record 登记一段完成区间并合并相邻/重叠部分。
func (sp *shardProgress) record(s, e int64) {
	if e <= s {
		return
	}
	sp.intervals = append(sp.intervals, [2]int64{s, e})
	sort.Slice(sp.intervals, func(i, j int) bool { return sp.intervals[i][0] < sp.intervals[j][0] })
	merged := sp.intervals[:1]
	for _, iv := range sp.intervals[1:] {
		last := &merged[len(merged)-1]
		if iv[0] <= last[1] {
			if iv[1] > last[1] {
				last[1] = iv[1]
			}
		} else {
			merged = append(merged, iv)
		}
	}
	sp.intervals = merged
}

// covered 返回自 start 起连续覆盖的前缀长度。
func (sp *shardProgress) covered() int64 {
	cur := sp.start
	for _, iv := range sp.intervals {
		if iv[0] > cur {
			break
		}
		if iv[1] > cur {
			cur = iv[1]
		}
	}
	return cur - sp.start
}

// attempt 记录一次在途下载尝试（窃取决策与取消依据）。
type attempt struct {
	t        rangeTask
	written  *atomic.Int64 // 本 attempt 已写字节（原子）
	cancel   context.CancelFunc
	stealing atomic.Bool
	cut      int64 // 窃取切割点（stealing=true 时有效）
}

// downloader 范围队列下载引擎。
type downloader struct {
	tr     *network.Transport
	url    string
	sf     *io.SparseFile
	retryC *retry.Config

	mu       sync.Mutex
	cond     *sync.Cond
	queue    []rangeTask
	attempts map[*attempt]struct{}
	prog     map[int]*shardProgress
	active   int
	closed   bool
	failed   error

	wg sync.WaitGroup
}

// minStealSplit 窃取切割的最小剩余区间（1 MiB，过小不值得分裂连接）。
const minStealSplit = 1 << 20

func newDownloader(tr *network.Transport, url string, sf *io.SparseFile, plan *scheduler.Plan) *downloader {
	d := &downloader{
		tr:       tr,
		url:      url,
		sf:       sf,
		retryC:   newRetryConfig(),
		attempts: make(map[*attempt]struct{}),
		prog:     make(map[int]*shardProgress),
	}
	d.cond = sync.NewCond(&d.mu)
	for i, sh := range plan.Shards {
		sp := &shardProgress{start: sh.Start, end: sh.End}
		if sh.Done > 0 { // 恢复续传前缀
			sp.record(sh.Start, sh.Start+sh.Done)
		}
		d.prog[i] = sp
		if sh.End > 0 && sh.Start+sh.Done >= sh.End {
			continue // 本分片已完成
		}
		d.queue = append(d.queue, rangeTask{shardIdx: i, start: sh.Start + sh.Done, end: sh.End})
	}
	return d
}

// run 启动工作协程并阻塞至全部任务完成或失败。
func (d *downloader) run(ctx context.Context) error {
	tasks := len(d.queue)
	if tasks == 0 {
		return nil
	}
	workers := tasks // 任务数即连接数上限（≤6，NewPlan 保证）
	if workers > scheduler.MaxConnections {
		workers = scheduler.MaxConnections
	}
	// ctx 取消时关闭引擎，解除 pop 阻塞；正常结束后退出 watcher
	runDone := make(chan struct{})
	defer close(runDone)
	go func() {
		select {
		case <-ctx.Done():
			d.mu.Lock()
			d.closed = true
			d.cancelAllLocked()
			d.cond.Broadcast()
			d.mu.Unlock()
		case <-runDone:
		}
	}()
	for i := 0; i < workers; i++ {
		d.wg.Add(1)
		go d.worker(ctx)
	}
	d.wg.Wait()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failed != nil {
		return d.failed
	}
	return ctx.Err()
}

// fail 记录致命失败并关闭引擎（取消全部在途尝试）。
func (d *downloader) fail(err error) {
	d.mu.Lock()
	if d.failed == nil {
		d.failed = err
	}
	d.closed = true
	d.cancelAllLocked()
	d.cond.Broadcast()
	d.mu.Unlock()
}

func (d *downloader) cancelAllLocked() {
	for at := range d.attempts {
		at.cancel()
	}
}

// worker 从队列取任务直到引擎关闭且无在途任务。
func (d *downloader) worker(ctx context.Context) {
	defer d.wg.Done()
	for {
		t, ok := d.pop()
		if !ok {
			return
		}
		d.runTask(ctx, t)
	}
}

// pop 取下一个任务；队列空但存在在途任务时先尝试窃取，否则等待。
// 返回 false 表示引擎关闭或全部完成。
func (d *downloader) pop() (rangeTask, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for {
		if d.closed {
			return rangeTask{}, false
		}
		if len(d.queue) > 0 {
			t := d.queue[0]
			d.queue = d.queue[1:]
			d.active++
			return t, true
		}
		if d.active == 0 {
			return rangeTask{}, false // 队列空且无在途 = 全部完成
		}
		if d.tryStealLocked() {
			continue // 窃取产生的尾段任务已入队
		}
		d.cond.Wait()
	}
}

// tryStealLocked 窃取在途任务的最大剩余尾段：入队尾段任务并取消受害者，
// 受害者中止时把未覆盖的缺口（若有）放回队列。返回是否成功窃取。
func (d *downloader) tryStealLocked() bool {
	var victim *attempt
	var victimRem int64
	for at := range d.attempts {
		if at.stealing.Load() || at.t.end <= 0 {
			continue // 已被窃取 / 流式未知长度不可窃取
		}
		rem := at.t.end - (at.t.start + at.written.Load())
		if rem > victimRem {
			victim, victimRem = at, rem
		}
	}
	if victim == nil || victimRem < 2*minStealSplit {
		return false
	}
	cut := victim.t.start + victim.written.Load() + victimRem/2
	victim.cut = cut
	victim.stealing.Store(true)
	d.queue = append(d.queue, rangeTask{shardIdx: victim.t.shardIdx, start: cut, end: victim.t.end})
	victim.cancel()
	return true
}

// runTask 执行一个范围任务（含指数退避重试与窃取中止处理）。
func (d *downloader) runTask(parent context.Context, t rangeTask) {
	defer func() {
		d.mu.Lock()
		d.active--
		d.cond.Broadcast()
		d.mu.Unlock()
	}()

	for try := 0; ; try++ {
		atCtx, cancel := context.WithCancel(parent)
		at := &attempt{t: t, cancel: cancel, written: new(atomic.Int64)}
		w := &progressWriterAt{sf: d.sf, base: t.start, written: at.written}
		d.mu.Lock()
		if d.closed { // 引擎已关闭（失败/取消）：不再发起新尝试
			d.mu.Unlock()
			cancel()
			return
		}
		d.attempts[at] = struct{}{}
		d.mu.Unlock()

		err := d.tr.FetchRange(atCtx, d.url, t.start, t.end, w)
		wFinal := at.written.Load()

		d.mu.Lock()
		delete(d.attempts, at)
		stolen := at.stealing.Load()
		d.mu.Unlock()
		if stolen { // 被窃取中止：登记进度并补投缺口
			d.finishStolen(t, at, wFinal)
			cancel()
			return
		}

		if err == nil {
			d.mu.Lock()
			d.prog[t.shardIdx].record(t.start, t.end)
			d.mu.Unlock()
			cancel()
			return
		}
		// 必须在 cancel() 之前读取 ctx 状态：cancel 后 Err() 恒非 nil，
		// 会把真实传输错误误判为「上下文取消」而静默丢弃任务（数据缺失）。
		ctxErr := atCtx.Err()
		cancel()

		if ctxErr != nil { // 上下文取消（父 ctx 或引擎失败）：进度已有效，直接退出
			return
		}
		if !network.Retryable(err) || try+1 >= d.retryC.MaxTry {
			d.fail(fmt.Errorf("分片 [%d,%d) 下载失败: %w", t.start, t.end, err))
			return
		}
		// 退避后整段重试（从 t.start 重传覆盖，幂等）
		select {
		case <-parent.Done():
			return
		case <-time.After(d.retryC.Backoff(try)):
		}
	}
}

// finishStolen 窃取中止后的簿记：登记受害者已写前缀，并把缺口 [start+written, cut) 放回队列。
func (d *downloader) finishStolen(t rangeTask, at *attempt, wFinal int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prog[t.shardIdx].record(t.start, t.start+wFinal)
	if gap := t.start + wFinal; gap < at.cut {
		d.queue = append(d.queue, rangeTask{shardIdx: t.shardIdx, start: gap, end: at.cut})
	}
	d.cond.Broadcast()
}

// snapshotShards 导出各原始分片的连续覆盖进度（持久化用）。
// 在途尝试的已写前缀同样是有效落盘数据（FetchRange 顺序写入 .part），
// 一并计入快照 → 崩溃最多损失一个持久化周期（500ms）的进度，实现字节级续传。
func (d *downloader) snapshotShards() []persist.ShardState {
	d.mu.Lock()
	defer d.mu.Unlock()
	tmp := make(map[int]*shardProgress, len(d.prog))
	idxs := make([]int, 0, len(d.prog))
	for i, sp := range d.prog {
		c := &shardProgress{start: sp.start, end: sp.end}
		c.intervals = append(c.intervals, sp.intervals...)
		tmp[i] = c
		idxs = append(idxs, i)
	}
	for at := range d.attempts {
		if w := at.written.Load(); w > 0 {
			tmp[at.t.shardIdx].record(at.t.start, at.t.start+w)
		}
	}
	sort.Ints(idxs)
	out := make([]persist.ShardState, 0, len(idxs))
	for _, i := range idxs {
		sp := tmp[i]
		out = append(out, persist.ShardState{Start: sp.start, End: sp.end, Done: sp.covered()})
	}
	return out
}

// progressWriterAt 将响应体按 base 偏移写入 SparseFile，并原子累计已写字节。
type progressWriterAt struct {
	sf      *io.SparseFile
	base    int64
	written *atomic.Int64
}

func (w *progressWriterAt) WriteAt(p []byte, off int64) (int, error) {
	n, err := w.sf.WriteAt(p, w.base+off)
	w.written.Add(int64(n))
	return n, err
}

// startsWithAny 前缀判断。
func startsWithAny(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if len(s) >= len(p) && s[:len(p)] == p {
			return true
		}
	}
	return false
}
