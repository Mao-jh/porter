// Package scheduler 实现下载调度引擎：分片决策、并发、优先级队列、限速。
// 分片策略（面向 3 vCPU，详见 DESIGN.md §3）：
//   - 单任务默认分片数 = 3（=核心数）
//   - 每片最小粒度 1 MiB，文件 < 5 MiB 退化为单连接
//   - 大文件细粒度上限 8 MiB/片，总连接数封顶 6
//   - 动态策略：实时测量每片吞吐，慢片合并、快片再分
package scheduler

import (
	"container/heap"
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// Shard 表示一个 [Start, End) 半开区间的分片。End=0 表示到文件末尾（未知大小）。
type Shard struct {
	Index  int   // 分片编号
	Start  int64 // 起始偏移（含）
	End    int64 // 结束偏移（不含）；0 表示未知/到EOF
	Done   int64 // 已下载字节数
	Active bool  // 是否正在传输

	throughput int64 // 最近采样吞吐 (bytes/sec)，0 表示未测量
	sampleAt   time.Time
}

// Chunk 是 Shard 的不可变视图，用于网络层发起 Range 请求。
type Chunk struct {
	Start, End int64
}

// 常量（设计决策，来源：§2 设计决策「分片策略」）
const (
	DefaultShards   = 3       // 自动决策默认分片数 = vCPU 数
	MinShardSize    = 1 << 20 // 1 MiB 每片最小粒度
	SmallFileThresh = 5 << 20 // 5 MiB 以下退化为单连接
	MaxShardSize    = 8 << 20 // 8 MiB/片 大文件细粒度上限
	MaxConnections  = 6       // 自动决策连接数封顶（内存红线 H-1/H-2 保守值）
	// MaxExplicitConnections 显式 -n 指定的连接上限（第 6 轮 16 → 本轮扩展档位）。
	// 档位语义：自动决策仍封顶 MaxConnections=6；显式 -n 档位 16/32/64 覆盖常规收益区
	// （服务器每 IP 并发限制普遍 4~16，超过收效甚微）；64 以上属极端档，128 仅在
	// 「单连接被服务器限速、且服务器不限制并发」的特定弱网场景有收益——
	// 128 连接的代价：慢启动拉满前的空窗放大、对服务器不友好（易触发限流/拉黑）、
	// 进度持久化与覆盖校验的簿记量线性增长（每 500ms 序列化 128 片）。每连接 64KiB
	// 缓冲，128 连接增量内存仅约 8MiB（H-1/H-2 红线远未被触达，非主要矛盾）。
	MaxExplicitConnections = 128
)

// Plan 为一个文件的完整分片计划。
type Plan struct {
	FileSize int64   `json:"file_size"` // 0 表示未知大小
	Shards   []Shard `json:"shards"`
	mu       sync.RWMutex
}

// NewPlan 根据已知文件大小生成初始分片计划。size=0 表示大小未知（单分片流式）。
func NewPlan(size int64) *Plan {
	p := &Plan{FileSize: size}
	if size <= 0 {
		p.Shards = []Shard{{Index: 0, Start: 0, End: 0}}
		return p
	}
	if size < SmallFileThresh {
		// 小文件：退化为单连接（§2 决策）
		p.Shards = []Shard{{Index: 0, Start: 0, End: size}}
		return p
	}

	// n = min(max(⌈size/8MiB⌉, 3), 6)：默认 3 片（=vCPU），每片尽量 ≤ MaxShardSize，
	// 连接数封顶 6，且为 Rebalance 快片再分预留空间（3→4→5→6）。
	n := int((size + MaxShardSize - 1) / MaxShardSize)
	if n < DefaultShards {
		n = DefaultShards
	}
	if n > MaxConnections {
		n = MaxConnections
	}

	base := size / int64(n)
	rem := size % int64(n)
	shards := make([]Shard, n)
	cur := int64(0)
	for i := 0; i < n; i++ {
		s := base
		if int64(i) < rem {
			s++
		}
		shards[i] = Shard{Index: i, Start: cur, End: cur + s}
		cur += s
	}
	p.Shards = shards
	return p
}

// NewPlanN 以显式分片数生成计划（-n 参数）。n<=0 或 size<=0 时退化为 NewPlan 自动决策；
// n 收敛到 [1, MaxExplicitConnections]，且不超过文件大小（避免零长分片）。
func NewPlanN(size int64, n int) *Plan {
	if size <= 0 || n <= 0 {
		return NewPlan(size)
	}
	if n > MaxExplicitConnections {
		n = MaxExplicitConnections
	}
	if n > int(size) {
		n = int(size)
	}
	if n < 1 {
		n = 1
	}
	p := &Plan{FileSize: size}
	base := size / int64(n)
	rem := size % int64(n)
	shards := make([]Shard, n)
	var cur int64
	for i := 0; i < n; i++ {
		s := base
		if int64(i) < rem {
			s++
		}
		shards[i] = Shard{Index: i, Start: cur, End: cur + s}
		cur += s
	}
	p.Shards = shards
	return p
}

// Chunks 返回当前所有分片的 Chunk 视图（线程安全快照）。
func (p *Plan) Chunks() []Chunk {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]Chunk, 0, len(p.Shards))
	for _, s := range p.Shards {
		out = append(out, Chunk{Start: s.Start, End: s.End})
	}
	return out
}

// UpdateThroughput 上报某分片最近采样吞吐，用于动态决策。
func (p *Plan) UpdateThroughput(idx int, bytes int64, d time.Duration) {
	if d <= 0 {
		return
	}
	tp := int64(float64(bytes) / d.Seconds())
	p.mu.Lock()
	defer p.mu.Unlock()
	if idx < 0 || idx >= len(p.Shards) {
		return
	}
	p.Shards[idx].throughput = tp
	p.Shards[idx].sampleAt = time.Now()
}

// Rebalance 执行动态策略：慢片合并、快片再分。
// 规则：若最快片吞吐 > 慢片 2 倍，且分片数 < MaxConnections，将快片一分为二；
//
//	若某片停滞(吞吐=0 且非完成)且存在已完成片，合并之。
//
// 返回是否发生了重平衡。
func (p *Plan) Rebalance() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.Shards) == 0 {
		return false
	}

	sorted := make([]int, len(p.Shards))
	for i := range sorted {
		sorted[i] = i
	}
	sort.Slice(sorted, func(i, j int) bool {
		return p.Shards[sorted[i]].throughput > p.Shards[sorted[j]].throughput
	})

	fast := sorted[0]
	if len(p.Shards) < MaxConnections && p.Shards[fast].throughput > 0 {
		slowTP := p.Shards[sorted[len(sorted)-1]].throughput
		if slowTP > 0 && p.Shards[fast].throughput > slowTP*2 {
			if p.splitLocked(fast) {
				return true
			}
		}
	}
	// 合并停滞片（吞吐=0 且未激活）；并入失败（如单片孤悬）则尝试下一个候选
	for _, idx := range sorted {
		s := p.Shards[idx]
		if !s.Active && s.throughput == 0 && s.Done < (s.End-s.Start) {
			if p.mergeStagnantLocked(idx) {
				return true
			}
		}
	}
	return false
}

// splitLocked 将 idx 片对半拆分（调用方持锁）。仅当 End 已知且足够大时有效。
// 新片必须插入到 idx+1 位置以保持 Shards 的地址连续性（S-2 不变量：无间隙）。
func (p *Plan) splitLocked(idx int) bool {
	s := p.Shards[idx]
	if s.End <= s.Start || (s.End-s.Start) < MinShardSize*2 {
		return false
	}
	mid := s.Start + (s.End-s.Start)/2
	newShard := Shard{Index: idx + 1, Start: mid, End: s.End, throughput: s.throughput / 2}
	p.Shards[idx].End = mid
	p.Shards = append(p.Shards, Shard{})
	copy(p.Shards[idx+2:], p.Shards[idx+1:])
	p.Shards[idx+1] = newShard
	for i := idx + 1; i < len(p.Shards); i++ {
		p.Shards[i].Index = i
	}
	return true
}

// mergeStagnantLocked 将停滞片并入相邻片（调用方持锁）：优先后继（前驱区间延长），
// 无后继时并入前驱（前驱 End 延长覆盖 idx）。并入活跃片属合法救援：活跃流延长下载区间。
func (p *Plan) mergeStagnantLocked(idx int) bool {
	if idx+1 < len(p.Shards) {
		// 并入后继：idx.Start 不变、idx.End 延长为后继 End，删除后继
		p.Shards[idx].End = p.Shards[idx+1].End
		p.Shards = append(p.Shards[:idx+1], p.Shards[idx+2:]...)
		for i := idx; i < len(p.Shards); i++ {
			p.Shards[i].Index = i
		}
		return true
	}
	if idx > 0 {
		// 并入前驱：前驱 End 延长为 idx.End，删除 idx
		p.Shards[idx-1].End = p.Shards[idx].End
		p.Shards = append(p.Shards[:idx], p.Shards[idx+1:]...)
		for i := idx - 1; i < len(p.Shards); i++ {
			p.Shards[i].Index = i
		}
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// 优先级队列（container/heap）
// ---------------------------------------------------------------------------

// Task 是调度单元。
type Task struct {
	ID       string
	URL      string
	Priority int // 越大越优先
	Plan     *Plan
	Status   Status
	Created  time.Time
	index    int
}

// Status 任务状态。
type Status int

const (
	StatusPending Status = iota
	StatusRunning
	StatusPaused
	StatusDone
	StatusFailed
)

// PriorityQueue 按 Priority 最大堆。
type PriorityQueue []*Task

func (pq PriorityQueue) Len() int { return len(pq) }
func (pq PriorityQueue) Less(i, j int) bool {
	return pq[i].Priority > pq[j].Priority // 最大堆
}
func (pq PriorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index, pq[j].index = i, j
}
func (pq *PriorityQueue) Push(x any) {
	t := x.(*Task)
	t.index = len(*pq)
	*pq = append(*pq, t)
}
func (pq *PriorityQueue) Pop() any {
	old := *pq
	n := len(old)
	t := old[n-1]
	t.index = -1
	*pq = old[:n-1]
	return t
}

// Scheduler 调度引擎。
type Scheduler struct {
	mu      sync.RWMutex
	queue   PriorityQueue
	running map[string]*Task
	cpus    int // 可用 CPU 数（用于 R-3 限速）

	// R-3 CPU 模式开关
	Mode     Mode
	cpuLimit float64 // 默认 0.6 = 60%

	cond *sync.Cond
}

// Mode CPU 模式（R-3）。
type Mode int

const (
	ModeDefault Mode = iota // 单任务限速至可用 CPU 的 60%
	ModeMaxPerf Mode = iota // 解除限速、允许满载
)

// NewScheduler 构造调度器。cpus 为可用 CPU 数。
func NewScheduler(cpus int) *Scheduler {
	if cpus <= 0 {
		cpus = DefaultShards
	}
	s := &Scheduler{
		queue:    make(PriorityQueue, 0),
		running:  make(map[string]*Task),
		cpus:     cpus,
		Mode:     ModeDefault,
		cpuLimit: 0.6,
	}
	s.cond = sync.NewCond(&s.mu)
	heap.Init(&s.queue)
	return s
}

// SetMode 切换 CPU 模式（R-3 模式开关）。
func (s *Scheduler) SetMode(m Mode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Mode = m
	if m == ModeMaxPerf {
		s.cpuLimit = 1.0
	} else {
		s.cpuLimit = 0.6
	}
}

// Submit 提交任务，按优先级入队。任务仅需非空 ID；URL 由执行层解释。
func (s *Scheduler) Submit(t *Task) error {
	if t == nil || t.ID == "" {
		return errors.New("scheduler: invalid task")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.running[t.ID]; ok {
		return errors.New("scheduler: duplicate task id")
	}
	if t.Priority <= 0 {
		t.Priority = 1
	}
	t.Status = StatusPending
	t.Created = time.Now()
	heap.Push(&s.queue, t)
	s.cond.Broadcast()
	return nil
}

// Next 阻塞获取下一个待运行任务（受并发上限约束）。
func (s *Scheduler) Next(ctx context.Context) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if len(s.queue) == 0 {
			if len(s.running) == 0 {
				return nil, ErrNoTasks
			}
			s.cond.Wait()
			continue
		}
		if len(s.running) >= s.slotsLocked() {
			s.cond.Wait()
			continue
		}
		t := heap.Pop(&s.queue).(*Task)
		t.Status = StatusRunning
		s.running[t.ID] = t
		return t, nil
	}
}

// Slots 返回当前模式下的并发任务槽位数：default 为 max(1, ⌈cpus×0.6⌉)，max 为 cpus。
// 多任务模式的消费协程数即取该值（R-3 模式开关的真实运行时作用点）。
func (s *Scheduler) Slots() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.slotsLocked()
}

func (s *Scheduler) slotsLocked() int {
	limit := s.cpus
	if s.Mode == ModeDefault {
		limit = int(float64(s.cpus) * s.cpuLimit)
		if limit < 1 {
			limit = 1
		}
	}
	return limit
}

// Done 标记任务完成并释放 slot。
func (s *Scheduler) Done(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.running[id]; ok {
		t.Status = StatusDone
		delete(s.running, id)
	}
	s.cond.Broadcast()
}

// ErrNoTasks 队列空且无运行中任务。
var ErrNoTasks = errors.New("scheduler: no tasks")
