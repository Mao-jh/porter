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
	gio "io"
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

	"github.com/Mao-jh/porter/hash"
	"github.com/Mao-jh/porter/io"
	"github.com/Mao-jh/porter/network"
	"github.com/Mao-jh/porter/persist"
	"github.com/Mao-jh/porter/retry"
	"github.com/Mao-jh/porter/scheduler"
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

	Proxy      string // 代理出口（http/https/socks5，空=直连；第 14 轮）
	Jobs       int    // 并发任务数上限（0=按 mode 自动；第 14 轮，对标 aria2 -j）
	CookieFile string // Netscape cookie.txt 路径（空=不加载；第 14 轮）
	Summary    bool   // 周期性进度摘要输出到 stderr（第 14 轮）

	Outputs []string // 与 URLs 平行的逐任务输出名（-i 文件 "URL out=name" 行；空串=自动；第 16 轮）
}

// boolFlags 布尔标志集合：预扫描阶段不把下一个 token 消费为标志值。
var boolFlags = map[string]bool{"summary": true}

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
			// 除布尔标志（boolFlags）外全部带值：下一个 token 不以 - 开头则消费为标志值
			name := strings.TrimLeft(a, "-")
			if !boolFlags[name] && i+1 < len(args) && !(len(args[i+1]) > 1 && args[i+1][0] == '-') {
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
		proxy    = fs.String("proxy", "", "代理出口（http://host:port 或 socks5://host:port；设置即视为允许出站）")
		urlFile  = fs.String("i", "", "URL 列表文件（每行一个 URL，可带 \" out=name\" 指定输出名；空行与 # 注释忽略；对标 aria2 -i）")
		jobs     = fs.Int("j", 0, "并发任务数上限（0=按 -mode 自动决定；对标 aria2 -j）")
		ckFile   = fs.String("load-cookies", "", "Netscape cookie.txt 文件路径（按域匹配注入 Cookie 头）")
		summary  = fs.Bool("summary", false, "每秒输出一次任务进度摘要到 stderr")
		hdrs     headerList
	)
	fs.Var(&hdrs, "H", "透传请求头 \"Key: Value\"（可重复，如 -H \"Cookie: a=b\"）")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() == 0 && *urlFile == "" {
		return nil, errors.New("用法: downloader <url|-> [url2 ...] [-i urls.txt] [-o out] [-n shards] [-j jobs] [-limit bps] [-proxy URL] [-load-cookies file] [-summary] [-H \"K: V\"] [-mode default|max] [-verify sha256]")
	}
	urls := make([]string, 0, fs.NArg())
	outs := make([]string, 0, fs.NArg())
	for i := 0; i < fs.NArg(); i++ {
		u := fs.Arg(i)
		if !startsWithAny(u, "http://", "https://", "ftp://", "ftps://", "file://") {
			return nil, fmt.Errorf("不支持的 URL 协议: %s（仅 http/https/ftp/ftps/file）", u)
		}
		urls = append(urls, u)
		outs = append(outs, "")
	}
	// -i URL 列表文件：与位置参数合并；行内 " out=name" 指定输出名（第 16 轮，对标 aria2）
	if *urlFile != "" {
		fromFile, err := readURLFile(*urlFile)
		if err != nil {
			return nil, err
		}
		for _, e := range fromFile {
			urls = append(urls, e.url)
			outs = append(outs, e.out)
		}
	}
	if len(urls) == 0 {
		return nil, errors.New("未提供任何 URL（位置参数或 -i 文件）")
	}
	if *jobs < 0 {
		return nil, fmt.Errorf("非法 -j: %d（应为 ≥0）", *jobs)
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

		Proxy:      *proxy,
		Jobs:       *jobs,
		CookieFile: *ckFile,
		Summary:    *summary,
		Outputs:    outs,
	}, nil
}

// urlOutEntry 一行 -i 条目：URL 与可选输出名（"URL out=name"）。
type urlOutEntry struct {
	url string
	out string
}

// readURLFile 读取 -i URL 列表文件：每行一个 URL，空行与 # 开头注释忽略；
// 行内 " out=<name>"（空格分隔）为该任务的输出文件名（第 16 轮，对标 aria2 -i）。
// out 名经 sanitizeFilename 净化（防路径穿越/非法字符），净化后为空则回退自动命名。
func readURLFile(path string) ([]urlOutEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 URL 列表失败: %w", err)
	}
	var entries []urlOutEntry
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		u, out := line, ""
		if i := strings.Index(line, " out="); i >= 0 {
			u = strings.TrimSpace(line[:i])
			out = strings.TrimSpace(line[i+len(" out="):])
		}
		if !startsWithAny(u, "http://", "https://", "ftp://", "ftps://", "file://") {
			return nil, fmt.Errorf("URL 列表第 %d 行协议不支持: %s（仅 http/https/ftp/ftps/file）", len(entries)+1, u)
		}
		if out != "" {
			out = sanitizeFilename(out) // 防路径穿越/Windows 非法字符；空 → 自动命名
		}
		entries = append(entries, urlOutEntry{url: u, out: out})
	}
	return entries, nil
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
	// 单一共享 Transport：全局限速配额由所有任务/分片共同消耗
	//（若每任务独立 Transport，-limit 会变成"每任务限额"而非全局限额）。
	tr, nCookies, err := buildTransport(opt, false)
	if err != nil {
		return err
	}
	tr.SetGlobalLimit(opt.Limit)
	// 代理出口（第 14 轮）：设置即视为显式允许出站（network 层自动置 allowRemote）
	if opt.Proxy != "" {
		fmt.Fprintf(os.Stderr, "[proxy] 出口 %s（远程目标已随代理显式放行）\n", opt.Proxy)
	}
	if nCookies > 0 {
		fmt.Fprintf(os.Stderr, "[cookies] 已加载 %d 条（按域匹配）\n", nCookies)
	}
	// 协议分发：http(s) → tr，ftp(s) → FTP 传输层（共享同一限速配额，H-3 同边界）。
	fetch := network.NewMux(tr, false)
	outs := deriveOutputs(opt.URLs)
	// -i 文件 "out=name" 逐任务命名优先于自动推导（第 16 轮；净化已在 readURLFile 完成）
	for i := range outs {
		if i < len(opt.Outputs) && opt.Outputs[i] != "" {
			outs[i] = opt.Outputs[i]
		}
	}
	if len(opt.URLs) == 1 && opt.Output != "" {
		outs[0] = opt.Output // 单 URL：-o 为精确文件路径（兼容单任务语义）
	} else if opt.Output != "" {
		// 多 URL：-o 为输出目录，文件名取自自动推导/out= 命名并去重
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
	// -j 并发任务数上限（第 14 轮）：只下调不上调（上调越过 R-3 模式预算）
	if opt.Jobs > 0 && opt.Jobs < consumers {
		consumers = opt.Jobs
	}
	// -summary 周期进度摘要（第 14 轮）：读 store 快照，单行/任务，不刷屏
	stopSummary := make(chan struct{})
	summaryExited := make(chan struct{})
	if opt.Summary {
		go func() {
			defer close(summaryExited)
			tk := time.NewTicker(time.Second)
			defer tk.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-stopSummary:
					return
				case <-tk.C:
					printSummary(os.Stderr, store.All())
				}
			}
		}()
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
						rerr = runOne(ctx, fetch, tr, opt, t.URL, t.ID, store)
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
	if opt.Summary {
		close(stopSummary)
		<-summaryExited
		printSummary(os.Stderr, store.All()) // 终态快照
	}
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
			base = sanitizeFilename(path.Base(parsed.Path))
		}
		if base == "" {
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

// sanitizeFilename 将 URL 推导的文件名净化为跨平台安全形式（合规：防路径逃逸、
// Windows 保留设备名与非法字符；策略与 mcp.deriveOutputName 一致）。
// 返回空串表示净化后无有效名字（调用方回退默认名）。
func sanitizeFilename(name string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\': // 路径分隔符：杜绝目录穿越
			return '_'
		case r < 0x20 || r == 0x7f: // 控制字符
			return '_'
		case strings.ContainsRune(`<>:"|?*`, r): // Windows 非法字符
			return '_'
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." {
		return ""
	}
	// Windows 保留设备名（不分大小写、含带扩展名形式）：CON.zip 同样被系统劫持
	stem := name
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	switch strings.ToUpper(stem) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		name = "_" + name
	}
	name = strings.TrimRight(name, " .") // Windows 会截断结尾空格与点
	runes := []rune(name)
	if len(runes) > 200 { // 超长截断（含多字节安全）
		name = string(runes[:200])
	}
	return name
}

// taskSource 单任务实际下载源（Metalink 解析/选流后的产物）。
type taskSource struct {
	url        string
	size       int64          // Metalink <size> 交叉核对（0=未给出）
	verifyAlgo hash.Algorithm // Metalink 元数据哈希算法
	verifySum  string         // 期望十六进制值（非空 → 下载后强制比对）
}

// pickCandidate Metalink 候选 failover：按 priority 升序逐个探测，首个可达者胜出。
// 仅探测阶段 failover；传输中途失败不换源——字节级续传状态绑定单一源。
func pickCandidate(ctx context.Context, fetch network.Fetcher, candidates []network.MetalinkURL) (string, error) {
	var lastErr error
	for _, c := range candidates {
		if _, _, err := fetch.Probe(ctx, c.URL); err == nil {
			return c.URL, nil
		} else {
			lastErr = err
		}
	}
	return "", fmt.Errorf("metalink: 全部 %d 个候选不可达: %w", len(candidates), lastErr)
}

// runOne 执行单个 URL 的完整下载（协调 network/io/persist/hash）。
// fetch/tr 与 store 由调用方共享：限速配额全局统一、state.json 无并发写冲突。
// 第 13 轮：Metalink 元文件 → 候选 failover + 元数据哈希；.m3u8 → HLS 虚拟映射传输层。
func runOne(ctx context.Context, fetch network.Fetcher, tr *network.Transport, opt *Options, urlStr, output string, store *persist.Store) error {
	// 显式 -o（单 URL）时输出名完全由用户决定，任何自动命名（Metalink/CD/HLS）均不生效
	explicitOut := len(opt.URLs) == 1 && opt.Output != ""
	src := taskSource{url: urlStr}
	named := false // Metalink <file name> 已提供名字则跳过 Content-Disposition 推断
	if network.IsMetalinkURL(urlStr) {
		ml, err := network.FetchMetalink(ctx, tr, urlStr)
		if err != nil {
			return err
		}
		// 输出名改用 <file name> 属性——但显式 -o（单 URL）优先，尊重用户指定
		if !explicitOut {
			if n := sanitizeFilename(ml.Name); n != "" {
				output = filepath.Join(filepath.Dir(output), n)
				named = true
			}
		}
		picked, err := pickCandidate(ctx, fetch, ml.URLs)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "[metalink] 选用候选 %s\n", picked)
		src.url = picked
		src.size = ml.Size
		if ml.HashAlgo != "" {
			src.verifyAlgo, src.verifySum = hash.Algorithm(ml.HashAlgo), ml.HashSum
		}
		urlStr = src.url
	}
	// 自动文件名（第 14 轮，A4）：单 URL 且未显式 -o 且 Metalink 未命名时，
	// 询问服务端 Content-Disposition 建议（对标 IDM/Gopeed 默认行为；
	// 多 URL 模式保持 URL 推导 + 预去重，避免任务间同名冲突）。
	if len(opt.URLs) == 1 && opt.Output == "" && !named && startsWithAny(urlStr, "http://", "https://") {
		if n := sanitizeFilename(tr.ContentFilename(ctx, urlStr)); n != "" && n != filepath.Base(output) {
			output = filepath.Join(filepath.Dir(output), n)
		}
	}
	// HLS：.m3u8 后缀自动启用虚拟映射传输层（内部复用 tr 的回环校验/UA/限速/重定向策略）
	dlFetch := fetch
	if network.IsHLSURL(urlStr) {
		dlFetch = network.NewHLSTransport(tr)
		// R17：单 URL 未显式 -o 时，HLS 输出名去 .m3u8 后缀（CD 名优先，已在上方处理；
		// 此处兜底 URL 尾段命名，避免产物带播放列表后缀）
		if !explicitOut {
			if b := filepath.Base(output); strings.HasSuffix(strings.ToLower(b), ".m3u8") {
				output = filepath.Join(filepath.Dir(output), strings.TrimSuffix(b, ".m3u8"))
			}
		}
	}

	// 探测资源大小与 Range 支持（决定并行分片 vs 流式单连接）
	size, ranged, err := dlFetch.Probe(ctx, urlStr)
	if err != nil {
		return fmt.Errorf("探测资源失败: %w", err)
	}
	if src.size > 0 && size > 0 && src.size != size {
		return fmt.Errorf("metalink 大小与源不一致: 元数据 %d, 服务端 %d", src.size, size)
	}

	// 计划构造：断点续传恢复 > 显式 -n > 自动决策
	var plan *scheduler.Plan
	resume := false
	if st, ok := store.Get(output); ok && st.Status != "done" &&
		size > 0 && st.FileSize == size && len(st.Shards) > 0 {
		// R17 健壮性：仅当 .part 存在且尺寸与期望一致才续传；否则全新下载
		//（防止用户误删 .part 后按旧状态续传产生"已完成区为空洞"的损坏文件）
		if info, err := os.Stat(output + ".part"); err == nil && info.Size() == size {
			plan = planFromState(st, size)
			resume = true
		} else if err == nil {
			// .part 存在但尺寸不符（半截）：直接从头覆盖
			_ = os.Remove(output + ".part")
		}
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

	d := newDownloader(dlFetch, urlStr, sf, plan)

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

	// 流式校验（H-2：固定缓冲，不全文件读入内存）。
	// Metalink 元数据哈希（expected 非空）→ 期望值比对，不一致删除产物判失败；
	// 否则为"计算并打印"模式（算法取 -verify，缺省 sha256）。
	verifyAlgo := opt.Verify
	expected := src.verifySum
	if expected != "" {
		verifyAlgo = src.verifyAlgo // RFC 5854 元数据为权威校验源
	}
	if verifyAlgo != "" {
		f, err := os.Open(output)
		if err != nil {
			return err
		}
		sum, err := hash.Sum(f, verifyAlgo)
		f.Close()
		if err != nil {
			return fmt.Errorf("校验失败: %w", err)
		}
		if expected != "" && sum != expected {
			_ = os.Remove(output) // 防止损坏产物被误认为完整
			return fmt.Errorf("哈希不一致: 期望 %s(%s)=%s, 实际 %s（产物已删除）",
				expected, verifyAlgo, expected, sum)
		}
		fmt.Fprintf(os.Stderr, "[verify] %s(%s)=%s\n", output, verifyAlgo, sum)
	}
	return nil
}

// printSummary 输出任务进度摘要（-summary 周期输出 + 终态快照）。
// 单行/任务：状态 | 已完成/总大小 (百分比) | 输出名 | URL。按 ID 排序保证稳定。
func printSummary(w gio.Writer, states []*persist.State) {
	if len(states) == 0 {
		return
	}
	sort.Slice(states, func(i, j int) bool { return states[i].ID < states[j].ID })
	for _, st := range states {
		total := st.FileSize
		pct := 0.0
		if total > 0 {
			pct = float64(st.Done) / float64(total) * 100
			if pct > 100 {
				pct = 100
			}
		}
		sizeStr := fmt.Sprintf("%d/%dB", st.Done, total)
		if total >= 1<<20 || st.Done >= 1<<20 {
			sizeStr = fmt.Sprintf("%.1f/%.1fMiB", float64(st.Done)/(1<<20), float64(total)/(1<<20))
		} else if total >= 1<<10 || st.Done >= 1<<10 {
			sizeStr = fmt.Sprintf("%.1f/%.1fKiB", float64(st.Done)/(1<<10), float64(total)/(1<<10))
		}
		fmt.Fprintf(w, "[进度] %-6s %s (%.1f%%)  %s  %s\n", st.Status, sizeStr, pct, st.ID, st.URL)
	}
}

// infoSize 安全取文件大小（nil → -1）。
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
	tr     network.Fetcher // 协议无关（Mux 分发 http(s)/ftp(s)）
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

func newDownloader(tr network.Fetcher, url string, sf *io.SparseFile, plan *scheduler.Plan) *downloader {
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
		// 退避后整段重试（从 t.start 重传覆盖，幂等）。
		// 合规：服务端给出 Retry-After 时尊重其建议（取 max(本地退避, 建议)，上限 60s）。
		delay := d.retryC.Backoff(try)
		if ra, ok := network.RetryAfter(err); ok && ra > delay {
			if ra > time.Minute {
				ra = time.Minute
			}
			delay = ra
		}
		select {
		case <-parent.Done():
			return
		case <-time.After(delay):
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
