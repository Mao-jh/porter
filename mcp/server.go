// Package mcpserver Porter —— 下载器的 MCP（Model Context Protocol）插件，
// 让 AI 客户端（ZCode / Claude / Cursor 等）以工具调用方式驱动下载：
// AI 的专职搬运工。
//
// 设计要点（AI 第一用户）：
//   - 工具集：download_start（异步启动）/ download_status（进度与状态）/
//     download_cancel（取消，进度已落盘可续传）/ list_tasks（含历史恢复扫描）；
//   - 长下载天然异步：start 立即返回 task_id，AI 轮询 status；
//   - 每任务独立 state 子目录（URL 哈希），重启后 list_tasks 可恢复历史，
//     重复 download_start 同 URL 即续传（引擎字节级续传）；
//   - 安全边界：默认仅回环（H-3）；Config.AllowRemote 为显式产品开关。
package mcpserver

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Mao-jh/porter/cli"
	"github.com/Mao-jh/porter/hash"
	"github.com/Mao-jh/porter/persist"
)

// Config MCP 下载服务配置。
type Config struct {
	StateRoot   string // 状态根目录（每任务一个子目录）
	AllowRemote bool   // 允许非回环目标（默认 false，H-3 审计边界）
	Verify      string // 校验算法（""/none 跳过；sha256/sha1/md5）
	Limit       int64  // 全局限速 字节/秒（0=不限）
	OutputDir   string // 默认输出目录（空=进程工作目录）

	Proxy      string // 代理出口（http(s)/socks5；第 14/15 轮，设置即视为允许出站）
	CookieFile string // Netscape cookie.txt 路径（第 14/15 轮，按域匹配注入）
}

// task 运行中任务句柄。
type task struct {
	ID           string
	URL          string
	Output       string
	StateDir     string
	cancel       context.CancelFunc
	doneCh       chan error
	started      time.Time
	lastErrState string // 终态跨轮询保留（doneCh 抽取后置 nil）
}

// Downloader MCP 工具背后的任务注册表。
type Downloader struct {
	cfg   Config
	mu    sync.Mutex
	tasks map[string]*task
	seq   int
}

// NewDownloader 构造下载服务。
func NewDownloader(cfg Config) *Downloader {
	if cfg.StateRoot == "" {
		cfg.StateRoot = ".downloader-mcp"
	}
	_ = os.MkdirAll(cfg.StateRoot, 0o755)
	return &Downloader{cfg: cfg, tasks: map[string]*task{}}
}

// ---- 工具参数/结果类型（字段标签即 MCP schema）----

type DownloadStartIn struct {
	URL       string `json:"url" jsonschema:"必填，http/https/ftp/ftps 下载地址（默认仅允许 127.0.0.0/8 回环）"`
	OutputDir string `json:"output_dir,omitempty" jsonschema:"可选，输出目录（缺省用服务端配置或当前目录）"`
	LimitBps  int64  `json:"limit_bps,omitempty" jsonschema:"可选，本次任务限速 字节/秒（0=用服务端配置）"`
}

type DownloadStartOut struct {
	TaskID string `json:"task_id" jsonschema:"任务 ID，用于 status/cancel"`
	Output string `json:"output" jsonschema:"输出文件路径"`
	State  string `json:"state" jsonschema:"当前状态"`
}

type DownloadStatusIn struct {
	TaskID string `json:"task_id,omitempty" jsonschema:"可选，单个任务 ID；缺省返回全部任务"`
}

type TaskInfo struct {
	ID       string  `json:"id"`
	URL      string  `json:"url"`
	Output   string  `json:"output"`
	State    string  `json:"state"`
	Done     int64   `json:"done_bytes"`
	Size     int64   `json:"size_bytes"`
	Speed    float64 `json:"speed_bps,omitempty"`
	Error    string  `json:"error,omitempty"`
	ElapsedS float64 `json:"elapsed_sec,omitempty"`
}

type DownloadStatusOut struct {
	Tasks []TaskInfo `json:"tasks"`
}

type DownloadCancelIn struct {
	TaskID string `json:"task_id" jsonschema:"必填，要取消的任务 ID"`
}

type DownloadCancelOut struct {
	Cancelled bool   `json:"cancelled"`
	State     string `json:"state"`
}

type ListTasksIn struct{}

// ---- 探测（第 17 轮，对标 CLI porter probe）----

type DownloadProbeIn struct {
	URL string `json:"url" jsonschema:"必填，要探测的 URL（http/https/ftp/ftps/file）"`
}

type DownloadProbeOut struct {
	URL      string `json:"url"`
	Size     int64  `json:"size_bytes"` // 0=未知（流式）
	Ranged   bool   `json:"ranged"`     // 服务端是否支持 Range（可并行分片）
	Name     string `json:"name,omitempty"`
	FinalURL string `json:"final_url,omitempty"` // 重定向后最终地址（R20）
	Error    string `json:"error,omitempty"`
}

// Probe 探测单个 URL：大小 / Range 支持 / 服务端建议文件名（不下载）。
// 复用 cli.ProbeURL（与 porter probe 同一传输构建 + 协议分发 + CD 查询路径）。
func (d *Downloader) Probe(ctx context.Context, urlStr string) (DownloadProbeOut, error) {
	if !startsWithAnyScheme(urlStr, "http://", "https://", "ftp://", "ftps://", "file://") {
		return DownloadProbeOut{}, fmt.Errorf("仅支持 http/https/ftp/ftps/file URL: %s", urlStr)
	}
	size, ranged, name, err := cli.ProbeURL(ctx, d.cfg.Proxy, d.cfg.CookieFile,
		d.cfg.AllowRemote, nil, urlStr)
	if err != nil {
		return DownloadProbeOut{}, err
	}
	out := DownloadProbeOut{URL: urlStr, Size: size, Ranged: ranged, Name: name}
	if startsWithAnyScheme(urlStr, "http://", "https://") {
		if final := cli.FinalURLFor(ctx, d.cfg.Proxy, d.cfg.CookieFile,
			d.cfg.AllowRemote, nil, urlStr); final != "" && final != urlStr {
			out.FinalURL = final
			// 重定向资源：原始 URL 的 HEAD 无 Content-Disposition（302 无此头），
			// 建议名只能从最终资源取（CDN/对象存储常见）。
			if out.Name == "" {
				if _, _, n2, err := cli.ProbeURL(ctx, d.cfg.Proxy, d.cfg.CookieFile,
					d.cfg.AllowRemote, nil, final); err == nil && n2 != "" {
					out.Name = n2
				}
			}
		}
	}
	return out, nil
}

// ---- 核心逻辑 ----

func (d *Downloader) stateDirFor(urlStr string) string {
	sum := sha1.Sum([]byte(urlStr))
	return filepath.Join(d.cfg.StateRoot, "t"+hex.EncodeToString(sum[:])[:12])
}

func deriveOutputName(raw string) string {
	base := ""
	if u, err := url.Parse(raw); err == nil {
		base = path.Base(u.Path)
	}
	if base == "" || base == "." || base == "/" {
		base = fmt.Sprintf("download-%d.bin", time.Now().UnixNano()%100000)
	}
	return strings.NewReplacer(`<`, `_`, `>`, `_`, `:`, `_`, `"`, `_`, `/`, `_`,
		`\`, `_`, `|`, `_`, `?`, `_`, `*`, `_`).Replace(base)
}

// Start 启动一个下载任务（异步；引擎完成事件写入 doneCh）。
// 同 URL 重复调用 = 断点续传（引擎按持久化分片进度继续）。
func (d *Downloader) Start(urlStr, outputDir string, limitBps int64) (DownloadStartOut, error) {
	if !startsWithAnyScheme(urlStr, "http://", "https://", "ftp://", "ftps://", "file://") {
		return DownloadStartOut{}, fmt.Errorf("仅支持 http/https/ftp/ftps/file URL: %s", urlStr)
	}
	// AI-first：本地可判定的 URL 语法错误同步拒绝（此前非法 URL 语法如
	// "http://[::1" 会启动为 running，AI 需轮询才发现失败，浪费一次往返）。
	// checkLoopbackSync 只解析 host，语法错误（url.Parse 失败）在此兜底。
	if _, err := url.Parse(urlStr); err != nil {
		return DownloadStartOut{}, fmt.Errorf("URL 语法非法: %q（无法解析）: %v", urlStr, err)
	}
	// AI-first：H-3 同步拒绝。非法公网目标在 download_start 立即报错，
	// 不让 AI 等轮询才发现失败（错误文案与引擎一致，AI 可识别）。
	if err := checkLoopbackSync(urlStr, d.cfg.AllowRemote); err != nil {
		return DownloadStartOut{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seq++
	id := fmt.Sprintf("t%d", d.seq)
	dir := outputDir
	if dir == "" {
		dir = d.cfg.OutputDir
	}
	// AI-first：输出目录不存在则自动创建（CLI 遵循 curl 语义不建目录；
	// MCP 只有工具没有 shell，AI 无法自行 mkdir，故服务端代建）。
	if dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	output := filepath.Join(dir, deriveOutputName(urlStr))
	stateDir := d.stateDirFor(urlStr)
	_ = os.MkdirAll(stateDir, 0o755)

	limit := limitBps
	if limit == 0 {
		limit = d.cfg.Limit
	}
	opt := cli.Options{
		URLs:        []string{urlStr},
		Output:      output,
		StateDir:    stateDir,
		Limit:       limit,
		Verify:      hash.Algorithm(verifyAlgo(d.cfg.Verify)),
		Proxy:       d.cfg.Proxy,
		CookieFile:  d.cfg.CookieFile,
		AllowRemote: d.cfg.AllowRemote, // 第 27 轮：下载引擎与 probe 同边界（此前仅 probe 放行，下载仍被 H-3 拒）
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := &task{
		ID: id, URL: urlStr, Output: output, StateDir: stateDir,
		cancel: cancel, doneCh: make(chan error, 1), started: time.Now(),
	}
	go func() { t.doneCh <- cli.RunMulti(ctx, &opt) }()
	d.tasks[id] = t
	return DownloadStartOut{TaskID: id, Output: output, State: "running"}, nil
}

func verifyAlgo(v string) string {
	if v == "" || strings.EqualFold(v, "none") {
		return ""
	}
	return v
}

// startsWithAnyScheme URL 前缀白名单判断（与 cli.Parse 的协议白名单保持一致）。
func startsWithAnyScheme(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// checkLoopbackSync 同步回环校验（H-3）：allowRemote=false 时，解析目标 host，
// 只要解析出任一非回环 IP 即拒绝。错误文案与引擎一致（AI 可识别 H-3）。
// file:// 跳过（本地文件无网络目标）；解析失败交给引擎后续报错。
func checkLoopbackSync(raw string, allowRemote bool) error {
	if allowRemote {
		return nil
	}
	if startsWithAnyScheme(raw, "file://") {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	host := u.Hostname()
	if host == "" {
		return nil
	}
	reject := func(candidate net.IP) error {
		return fmt.Errorf("host %s resolves to non-loopback %s (H-3)", host, candidate)
	}
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsLoopback() {
			return reject(ip)
		}
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return nil // DNS 失败交给引擎（可能瞬时/配置问题，不当即误杀）
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return reject(ip)
		}
	}
	return nil
}

// Cancel 取消运行中的任务（引擎取消路径，进度已落盘；重新 Start 同 URL 即续传）。
func (d *Downloader) Cancel(taskID string) (DownloadCancelOut, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	t, ok := d.tasks[taskID]
	if !ok {
		return DownloadCancelOut{}, fmt.Errorf("未知任务 ID: %s", taskID)
	}
	if t.cancel != nil {
		t.cancel()
	}
	return DownloadCancelOut{Cancelled: true, State: "cancelling"}, nil
}

// Status 查询任务状态（TaskID 为空返回全部，含历史恢复扫描）。
func (d *Downloader) Status(taskID string) DownloadStatusOut {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := DownloadStatusOut{Tasks: []TaskInfo{}}
	now := time.Now()

	seen := map[string]bool{}
	// 先给运行注册表中的任务做实时快照
	for _, t := range d.tasks {
		if taskID != "" && t.ID != taskID {
			continue
		}
		seen[t.Output] = true
		info := TaskInfo{ID: t.ID, URL: t.URL, Output: t.Output, ElapsedS: now.Sub(t.started).Seconds()}
		if t.doneCh == nil {
			info.State = "queued"
		}
		select {
		case err := <-t.doneCh:
			t.doneCh = nil
			t.cancel = nil
			switch {
			case err == nil:
				info.State = "done"
			case err == context.Canceled || strings.Contains(err.Error(), "context canceled"):
				info.State = "paused"
			default:
				info.State = "failed"
				info.Error = err.Error()
			}
			t.lastErrState = info.State
		default:
			if t.doneCh != nil {
				info.State = "running"
			} else if t.lastErrState != "" {
				info.State = t.lastErrState
			}
		}
		if st, ok := readState(t.StateDir, t.Output); ok {
			info.Done, info.Size = st.Done, st.FileSize
			if info.State == "done" {
				info.Done = st.FileSize
			}
		}
		out.Tasks = append(out.Tasks, info)
	}

	// 历史恢复：不在注册表中的持久化任务（服务重启前启动的）
	if taskID == "" {
		entries, err := os.ReadDir(d.cfg.StateRoot)
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				b, err := os.ReadFile(filepath.Join(d.cfg.StateRoot, e.Name(), "state.json"))
				if err != nil {
					continue
				}
				var states map[string]persist.State
				if json.Unmarshal(b, &states) != nil {
					continue
				}
				for _, st := range states {
					if seen[st.ID] || st.URL == "" {
						continue
					}
					seen[st.ID] = true
					state := "paused"
					if st.Status == "done" {
						state = "done"
					}
					out.Tasks = append(out.Tasks, TaskInfo{
						ID: "(历史) " + st.ID, URL: st.URL, Output: st.ID,
						State: state, Done: st.Done, Size: st.FileSize,
					})
				}
			}
		}
	}
	return out
}

// NewToolServer 构造注册好全部工具的 MCP Server（测试与 main 共用）。
func NewToolServer(cfg Config) *mcp.Server {
	d := NewDownloader(cfg)
	s := mcp.NewServer(&mcp.Implementation{Name: "porter", Version: "1.0.0"}, nil)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "download_start",
		Description: "启动一个异步下载任务，立即返回 task_id；用 download_status 轮询进度。支持 http/https/ftp/ftps/file 协议；.m3u8 按 HLS（仅 VOD，AES-128 自动解密）、.meta4/.metalink 按 Metalink4（候选 failover + 自动哈希校验）处理。默认仅允许 127.0.0.0/8 回环地址（服务端 -allow-remote 可放开）。同 URL 重复调用会从断点续传。",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in DownloadStartIn) (*mcp.CallToolResult, DownloadStartOut, error) {
		out, err := d.Start(in.URL, in.OutputDir, in.LimitBps)
		if err != nil {
			return errResult[DownloadStartOut](err), DownloadStartOut{}, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "download_status",
		Description: "查询下载任务状态与进度（task_id 缺省返回全部任务，含历史任务）。",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in DownloadStatusIn) (*mcp.CallToolResult, DownloadStatusOut, error) {
		return nil, d.Status(in.TaskID), nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "download_cancel",
		Description: "取消运行中的下载任务（进度已落盘；重新 download_start 同 URL 即从断点续传）。",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in DownloadCancelIn) (*mcp.CallToolResult, DownloadCancelOut, error) {
		out, err := d.Cancel(in.TaskID)
		if err != nil {
			return errResult[DownloadCancelOut](err), DownloadCancelOut{}, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_tasks",
		Description: "列出全部下载任务（等价于 download_status 不带参数）。",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ListTasksIn) (*mcp.CallToolResult, DownloadStatusOut, error) {
		return nil, d.Status(""), nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "download_probe",
		Description: "探测目标资源：大小 / 是否支持 Range（可否并行分片）/ 服务端建议文件名（Content-Disposition），不下载。返回 size_bytes=0 表示大小未知（流式）。",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in DownloadProbeIn) (*mcp.CallToolResult, DownloadProbeOut, error) {
		out, err := d.Probe(ctx, in.URL)
		if err != nil {
			return errResult[DownloadProbeOut](err), DownloadProbeOut{}, nil
		}
		return nil, out, nil
	})

	return s
}

// errResult 构造 isError=true 的文本结果（领域错误走工具结果而非传输错误，AI 可读）。
func errResult[T any](err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
	}
}

// readState 读取单个任务 state.json 中的指定条目。
func readState(stateDir, id string) (persist.State, bool) {
	b, err := os.ReadFile(filepath.Join(stateDir, "state.json"))
	if err != nil {
		return persist.State{}, false
	}
	var states map[string]persist.State
	if err := json.Unmarshal(b, &states); err != nil {
		return persist.State{}, false
	}
	st, ok := states[id]
	return st, ok
}
