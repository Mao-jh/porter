# DESIGN.md — 架构与接口契约

> 第 1 轮（G-1）；**第 4 轮修订**（G-4 通过后）：§2.4 新增 ShardState、§3.1 分片公式修订、§四 数据流更新为范围队列+工作窃取引擎。接口函数签名保持冻结。

## 一、架构分层

```
cmd/downloader   ← 可执行入口（阶段1→Linux bin，阶段2→.exe）
cli/             ← 命令行参数解析 + 下载协调器
  ↓
scheduler/       ← 调度引擎：分片决策 / 并发 / 优先级队列 / CPU 限速
network/         ← 协议层：HTTP(S) Range + 故障注入（H-3 仅 127.0.0.0/8）
io/              ← IO 写入层：稀疏文件 / 固定 64KiB 环形缓冲 / 原子落盘
persist/         ← 持久化层：任务状态 JSON（断点续传）
hash/            ← 校验层：流式 MD5/SHA1/SHA256
testserver/      ← 本地环回测试服务端（127.0.0.1，G-3 基础设施）
```

**依赖方向**：`cli → scheduler / network / io / persist / hash`（单向，无循环）。
`testserver` 可被 `network`/`cli` 的测试引用，生产路径不依赖。

## 二、核心接口契约（Interface Contracts）

> 契约冻结原则：阶段 2 不得修改以下签名；Windows 侧行为不一致须回阶段 1 修复。

### 2.1 scheduler — 分片引擎
```go
// 分片计划
// 第 6 轮：显式分片数上限 MaxExplicitConnections = 16（-n 参数，对齐 aria2 -x）；
// 自动决策仍封顶 MaxConnections = 6。
func NewPlan(size int64) *Plan
func (p *Plan) Chunks() []Chunk          // 返回 [start,end) 半开区间
func (p *Plan) UpdateThroughput(idx int, bytes int64, d time.Duration)
func (p *Plan) Rebalance() bool          // 慢片合并/快片再分

// 调度器
func NewScheduler(cpus int) *Scheduler
func (s *Scheduler) SetMode(m Mode)      // R-3：ModeDefault(≤60%) / ModeMaxPerf
func (s *Scheduler) Slots() int          // 第 6 轮：当前模式的并发任务槽位数（多任务消费协程数）
func (s *Scheduler) Submit(t *Task) error
func (s *Scheduler) Next(ctx) (*Task, error)
func (s *Scheduler) Done(id string)
```
**不变量**：`Plan.Chunks()` 各片满足 `start_i == end_{i-1}`、`end_n == fileSize`、`end > start`（S-2）。

### 2.2 io — 写入层
```go
func NewRingBuffer(size int) *RingBuffer        // 固定 64KiB，不扩容
func (r *RingBuffer) Write(p []byte) (int, error) // 满则阻塞（背压）
func (r *RingBuffer) Read(p []byte) (int, error)

func OpenSparse(path string, size int64) (*SparseFile, error) // 预分配
func (sf *SparseFile) WriteAt(p []byte, off int64) (int, error)
func (sf *SparseFile) Commit() error             // .part → 原子 rename
func (sf *SparseFile) Abort()

func CopyBuffer(dst io.Writer, src io.Reader, buf []byte) (int64, error) // 显式固定 buf
```
**不变量**：环形缓冲容量固定；`Commit` 为原子操作；无整文件 `[]byte` 驻留（H-1/H-2）。

### 2.3 network — 传输层
```go
func NewTransport(allowRemote bool) *Transport
func (t *Transport) SetFaults(dc, to, too, se int32) // 断连/超时/429/5xx
func (t *Transport) FetchRange(ctx, urlStr string, start, end int64, dst io.WriterAt) error
```
**不变量**：Dialer `LocalAddr = 127.0.0.1`；非回环主机拒绝（H-3）；`end=0` 表示到 EOF。

**第 6 轮新增契约**：
```go
func (t *Transport) SetGlobalLimit(bytesPerSec int64) // 全局限速（<=0 取消）；多连接共享配额
func (t *Transport) SetHeaders(h map[string]string)   // 每请求透传头（Cookie/Authorization 等）
```
- 限速算法：平滑字节排班（leaky bucket 节奏，`network.rateLimiter`），FIFO 公平、无突发；
  作用于响应体读取路径，多任务/多分片聚合速率不超过配额。
- 超时策略变更：`http.Client.Timeout` 移除（会切断低速/限速大文件），改为
  拨号 5s / TLS 握手 10s / 响应头 15s；响应体停滞由调用方上下文取消兜底。

**第 12 轮新增契约（Mux 分发，DESIGN §2.3b）**：
```go
type Fetcher interface {   // cli 引擎依赖的最小协议面
    Probe(ctx, urlStr) (size int64, ranged bool, err error)
    FetchRange(ctx, urlStr string, start, end int64, dst io.WriterAt) error
}
func NewMux(httpT *Transport, allowRemote bool) *Mux  // http(s)/ftp(s) 按 scheme 分发
func NewFTPTransport(allowRemote bool) *FTPTransport  // 纯标准库 FTP/FTPS 客户端
```

**第 13 轮新增契约（协议扩展：file / HLS / Metalink4）**：
```go
func NewFileTransport() *FileTransport        // file:// 本地复制（仅绝对路径，host 空/localhost）
func IsHLSURL(raw string) bool                // http(s) 且路径以 .m3u8 结尾
func NewHLSTransport(tr *Transport) *HLSTransport // 实现 Fetcher；复用 tr 的校验/UA/限速
func IsMetalinkURL(raw string) bool           // http(s) 且路径以 .meta4/.metalink 结尾
func FetchMetalink(ctx, tr, raw) (*Metalink, error)
func ParseMetalink(body []byte) (*Metalink, error)
```
- **HLS 虚拟映射**：媒体播放列表 → 段序列 → 按明文长度映射为连续虚拟空间；
  `Probe` 返回虚拟总长（ranged=true），引擎的分片并行/工作窃取/字节级续传零改动复用；
  `FetchRange` 把虚拟区间翻译为 1..n 个段请求（边缘段发子 Range）。主播放列表取最高
  BANDWIDTH 变体（深度 ≤1）。加密（AES-128）段密文含 PKCS7 填充、明文长度不可预知 →
  `Probe` 返回 (0,false)，引擎自动退化为流式单连接顺序解密（64KiB 块 + 尾块滞留），
  续传不可用（诚实降级）。合规边界：仅 VOD（无 ENDLIST 拒绝）、播放列表 ≤1MiB、
  段数 ≤2048、单段 ≤64MiB、密钥 ≤8；SAMPLE-AES 等 DRM 方法显式拒绝；
  跨主机段剥离 Cookie/Authorization/Proxy-Authorization（与重定向策略同源）。
- **Metalink4**：候选按 priority 升序（缺省最低），探测阶段 failover（≤32 个）；
  `<size>` 与服务端交叉核对；元数据 `<hash>` 交 cli 做**期望值校验**（不符 → 删产物判失败）。
- **私有变体**：`Transport.probe/fetchRange(..., headers map[string]string)` 支持按请求
  头注入（HLS 跨主机剥离用）；`getBounded`（≤max 的 GET）与 `openStream`（流式 GET）
  供播放列表/密钥/元文件复用完整校验路径。公开签名不变（冻结原则）。

**第 14 轮新增契约（代理 / Cookie / 自动命名）**：
```go
func (t *Transport) SetProxy(raw string) error   // http(s)/socks5 代理出口（net/http 原生支持，零自实现）
func (t *Transport) SetCookies(cs []Cookie)      // 整体替换 cookie 集合；nil 清空
type Cookie struct{ Domain, Name, Value string }
func ParseNetscapeCookies(data []byte) ([]Cookie, error)  // curl/wget/aria2 通用 7 列格式
func (t *Transport) ContentFilename(ctx, urlStr) string   // RFC 6266/5987 文件名建议（无则空串）
```
- **代理语义（H-3 产品决策）**：显式配置代理即视为显式允许出站——`allowRemote` 自动置位；
  代理成为唯一出口（http.Transport 只拨号代理），`validateURL` 跳过目标 DNS 预解析
  （解析与可达性交给代理，避免本端 DNS 泄露）；scheme 白名单（http/https/socks5）不变。
- **Cookie 注入**：在透传头应用之后、发请求之前执行；仅域匹配（后缀匹配，`.example.com`
  与 `example.com` 等价），不区分 path/secure（诚实简化）；与 `-H "Cookie: ..."` 共存时
  透传值在前（RFC 6265 取首现者 → 透传优先）。`probe/fetchRange/getBounded/openStream`
  四条请求路径全部覆盖；跨主机重定向剥离策略不变（applyCookies 在 CheckRedirect 之前）。
- **自动命名优先级**：显式 `-o` > Metalink `<file name>` > Content-Disposition > URL 尾段
  （仅单 URL 且未显式 `-o` 时启用 CD 查询；多 URL 保持 URL 推导 + 预去重防同名冲突）。
- **CLI 新增**：`-i urls.txt`（每行一 URL，# 注释/空行忽略）、`-j N`（并发任务上限，
  只下调不上调）、`-proxy`、`-load-cookies`、`-summary`（每秒 store 快照摘要）；
  `porter tasks [-state-dir]` 子命令列出持久化任务（`cli.listTasks`，注入 Writer 可测）。

**第 17 轮新增契约（探测复用 / retry / 续传守卫 / HLS 命名）**：
```go
func buildTransport(opt *Options, allowRemote bool) (*network.Transport, int, error) // proxy/cookie/headers 统一构建（RunMulti/RunProbe/retry 共用）
func ProbeURL(ctx, proxy, cookieFile string, allowRemote bool, headers map[string]string, urlStr string) (size int64, ranged bool, name string, err error) // porter probe 与 MCP download_probe 共用
func ParseRetry(args []string) (*Options, error)  // retry 子命令旗标（无 URL 位置参数）
func RunRetry(ctx, opt *Options) error            // 串行续传重跑 store 中 status!=done 的任务（错误聚合）
```
- **续传守卫（健壮性修复）**：恢复分片计划前校验 `.part` 存在且尺寸与期望一致；
  缺失/不符则删除半截 `.part` 并全新下载——杜绝"误删 .part 后按旧状态续传产生
  已完成区为空洞的损坏文件"。
- **HLS 自动命名**：未显式 `-o` 时输出名去 `.m3u8` 后缀（CD 名优先；单/多 URL 同规则）。
- **MCP download_probe**：第 5 个工具，`cli.ProbeURL` 语义同源（-proxy/-load-cookies/
  AllowRemote 透传），输出 size_bytes/ranged/name。

**第 18 轮新增契约（磁盘预检 / stdout 流式）**：
```go
func diskFreeBytes(path string) (int64, error)                 // build tags 平台实现（Windows: kernel32!GetDiskFreeSpaceExW via syscall.NewLazyDLL；其余: syscall.Statfs，零依赖）
func preflightDisk(output string, size int64) error            // 下载前空间预检（size<=0 跳过；.part 已有量折算；查询失败降级警告）
func runStream(ctx, dlFetch network.Fetcher, urlStr string) error // -o -：单连接顺序写 stdout
func validateStreamOutput(opt *Options) error                  // -o - 约束：单 URL、无 -n 分片
```
- **磁盘预检时机**：`runOne` 中 `probe → 计划构造 → preflightDisk → OpenSparse`；已知大小
  且非流式时执行；不足直接失败（早期失败语义，对标 IDM/wget）。
- **流式模式语义**：`-o -` 在内容形态包装（Metalink/HLS）之后、探测之前短路——
  单连接顺序流，跳过 .part/续传/校验/持久化；Metalink failover、HLS 选流/解密不受影响。
- 跨平台：`diskfree_windows.go` / `diskfree_unix.go`（`//go:build` 标记），
  Windows 经 `syscall.NewLazyDLL` 直调 kernel32（零第三方依赖，B-1 不变）。

### 2.4 persist — 持久化
```go
func Open(dir string) (*Store, error)
func (s *Store) Put(st *State) error   // 变更即原子落盘
func (s *Store) Get(id string) (*State, bool)
func (s *Store) All() []*State

// 第 4 轮新增：每分片进度（字节级断点续传）
type ShardState struct {
    Start, End int64 // 分片区间 [Start,End)
    Done       int64 // 自 Start 起连续完成的前缀长度
}
// State.Shards []ShardState（json: shards, omitempty）
```
**不变量**：`flushLocked` 先写 `.tmp` 再 `rename`；进程崩溃后可恢复（S-3）；
在途尝试的已写前缀计入快照（`snapshotShards`），崩溃最多损失一个持久化周期（500ms）。

### 2.5 hash — 校验
```go
func New(algo Algorithm) (hash.Hash, error)
func Sum(r io.Reader, algo Algorithm) (string, error) // 流式，固定 64KiB buf
```

### 2.6 testserver — 测试服务端
```go
func New(cfg Config) (*Server, error)   // 绑定 127.0.0.1:0
func (s *Server) BaseURL() string
func (s *Server) CreateFile(name string, size int64) (string, error) // 稀疏
func (s *Server) Checksum(name string) (string, error)
func (s *Server) SetFaults(n int32)
```
**不变量**：仅 127.0.0.0/8（H-3）；Range 请求返回 206 + 正确 `Content-Range`。

## 三、关键算法契约

### 3.1 分片决策（scheduler.NewPlan）【第 4 轮修订】
| 输入 size | 输出分片数 | 依据 |
|---|---|---|
| `size <= 0` | 1（流式，`End=0`） | 未知大小退化 |
| `size < 5 MiB` | 1 | 小文件退化单连接 |
| `size >= 5 MiB` | `min(max(⌈size/8MiB⌉, 3), 6)` | 默认 3（vCPU），连接封顶 6 |
| 每片粒度 | ≤8 MiB（size≤48MiB 时严格成立；更大文件受连接封顶约束） | 细粒度上限 |

> 修订理由：原公式 `min(max(⌊size/1MiB⌋,3),6)` 恒产出 ≥5 片且无法再拆分，与
> 「每片 ≤8MiB」「Rebalance 预留拆分空间」两条约束在数学上互斥。新公式满足：
> 默认 3、封顶 6、≤48MiB 时每片 ≤8MiB、5/6 片场景可为 Rebalance 拆分提供空间。
> 显式分片数由 `NewPlanN(size, n)` 承接（-n 参数），n 收敛到 [1, MaxConnections]。

### 3.2 动态重平衡
- **算法层**（`Plan.Rebalance`）：若 **最快片吞吐 > 最慢片 × 2** 且 **当前片数 < 6** → 快片对半拆分（新片插入 idx+1 保持地址连续）；若 **某片停滞**（吞吐=0 且未激活）→ 并入相邻片（优先后继，尾片并入前驱）。重平衡后**边界仍须满足 §2.1 不变量**（单测断言）。
- **运行时层**（`cli.downloader` 工作窃取）：worker 完成本分片后若队列为空且有在途任务，窃取最大剩余尾段（≥2MiB）：入队尾段任务并取消受害者；受害者中止时已写前缀入账、缺口自动补投。重叠区写入同一服务器内容，幂等无害。

### 3.3 重试（retry）
`Backoff(attempt) = clamp(Base×2^attempt, ≤30s) × (1 ± Jitter)`，Base=1s，Jitter=0.2。
可重试：429 / 5xx / 断连 / 超时；4xx(除429) 不重试。

### 3.4 内存红线
- **H-1** 峰值 ≤ 3072 MiB：通过稀疏预分配 + 固定缓冲保证；
- **H-2** 稳态 ≤ 512 MiB：环形缓冲固定 64KiB + 流式哈希/落盘，无大块堆分配。

## 四、数据流向（第 6 轮：多任务队列）【第 4 轮：范围队列 + 工作窃取】
```
cli.Run
  → RunMulti: 单一共享 Transport（SetGlobalLimit/SetHeaders）+ 共享 persist.Store
  → 逐任务 scheduler.Submit → 消费者 Slots() 个 → Next 领取 → runOne:
      → network.Probe (HEAD/Range GET：大小 + Accept-Ranges)
  → 计划构造：续传恢复 planFromState > NewPlanN(size, -n) > NewPlan(0) 流式
  → io.OpenSparse (.part 不截断；全新任务先清理残留)
  → scheduler.Submit/Next (DESIGN §二 流程登记)
  → downloader.run:
      范围任务队列 → workers 并行：
        network.FetchRange(ctx, url, start, end, progressWriterAt)
          → io.SparseFile.WriteAt (分片并行落盘，64KiB 缓冲)
        失败 → network.Retryable? → retry.Config 退避重试 / 终止引擎
        worker 空闲 → 窃取慢连接尾段（tryStealLocked）
      每 500ms → snapshotShards → persist.Put (原子)
  → 覆盖守卫（每分片 Done == End-Start）
  → SparseFile.Commit (.part → 原子 rename)
  → persist.Put (done) → [可选] hash.Sum (流式校验)
```

## 五、构建约束（两阶段）
- **阶段 1（Linux）**：`GOFLAGS=-mod=readonly GOPROXY=off CGO_ENABLED=0 go build -ldflags="-s -w" ./cmd/downloader`
- **阶段 2（Windows）**：同命令 + `GOOS=windows GOARCH=amd64`，产出 `.exe`；`dumpbin /dependents` 验证无动态依赖。
- **零第三方依赖**：`go.mod` 仅 `module downloader` + Go 标准库。

**第 19 轮新增契约（HTTP/2 显式启用 / summary 速率 ETA）**：
- `Transport`：`http.Transport.ForceAttemptHTTP2 = true`——自定义 DialContext 时
  net/http 默认不自动协商 h2；显式强制后对 https 目标（h2 服务端）多路复用，
  6 分片可共享一条连接。测试注入 TLS 信任验证协商（产品代码不引入 TLS 配置面）。
- `cli.summaryTracker`：相邻 store 快照差分 → 每任务速率（B/s 人读格式化）与
  ETA（`formatETA`：Xs / Xm Ys / Xh Ym）；`renderAt(w, states, now)` 时间可注入
  便于测试；`-summary` 周期输出与终态快照均经 tracker。
- 新测试：`network/h2_test.go`（配置断言 + httptest TLS h2 协商/并发复用）、
  `cli/round19_test.go`（两帧差分速率/ETA、回落钳 0、格式化用例）。

**第 20 轮新增契约（probe 最终 URL / summary EMA 平滑）**：
- `Transport.FinalURL(ctx, urlStr)`：HEAD（回退 Range GET 0-0）返回 `resp.Request.URL`
  （http.Client 自动跟随跳转后的最终地址；wget --spider 对标）；失败空串。
- CLI `porter probe`：http(s) 且最终地址≠输入时输出 `final_url=`；
  `cli.FinalURLFor` 供 MCP `download_probe` 复用（`final_url` 字段，缺省 omitempty）。
- `summaryTracker` 速率 EMA 平滑（α=0.5）：首个有历史帧播种瞬时值（不做混合），
  之后 `speed = α·prev + (1-α)·instant`；瞬时速率钳 0（done 回落），ETA 按平滑速率计算。

**第 21 轮新增契约（porter meta / TUI ETA）**：
- `Transport.Meta(ctx, urlStr)`：HEAD（回退 Range GET 0-0）→ (状态行, Header 副本)；
  `cli.RunMeta` 输出 `<url> <HTTP/1.1 200 OK>` + 排序后的 `key: value` 头
  （对标 curl -I；支持 -proxy/-load-cookies/-H）。
- TUI：`Task.ETA`（剩余秒）= (Size-Done)/Speed；view 追加 `ETA Xs/Xm Ys/Xh Ym`
  （本地复制 `formatETA`，tui 独立 module 不跨导出）；防溢出钳制 2^62。

**第 22 轮新增契约（实测驱动修复）**：
- `summaryTracker` 渲染分两档：周期性帧（`render`，`showDone=false`）仅输出
  `status!=done` 的活跃任务，避免已完成历史每帧刷屏；终态快照（`renderAll`，
  `showDone=true`）输出全部任务。速率/ETA 跟踪对全部任务保持更新（不因跳过输出
  而缺帧）。`renderAt(w, states, now, showDone)` 测试签名同步扩展。
- 输出文件打开失败时检测父目录缺失：`cli.runOne` 报 `输出目录不存在 %q（请先创建…）`；
  `preflightDisk` 查询失败警告同步提示目录缺失——与 curl 语义一致（不自动建目录），
  但报错人读友好。
- 可验证性：`cmd/testserver` 增 `-addr`（固定监听地址，默认仍 `127.0.0.1:0` 随机）；
  `scripts/demo.sh` 一键试用（12 项核心能力演示 + 自动清理，退出码可断言）纳入
  `run_tests.sh` T5；`scripts/mcp_smoke.py` 为 MCP stdio 冒烟（逐条 JSON-RPC、
  `-state-root` 隔离、含 probe 与产物校验）。
