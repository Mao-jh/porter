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
