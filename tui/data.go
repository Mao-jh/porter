// 数据层：速度环形缓冲 / 事件日志 / 分片统计（方案 §6 与 §8.2）。
//
// 环形缓冲用于面积图与 sparkline 的数据源：速度采样 1Hz，任务级 60 笔
// （方案 B 右栏 60s 图），全局级 120 笔（方案 C 120s 图）。
package tui

import (
	"time"

	"github.com/Mao-jh/porter/persist"
	"github.com/charmbracelet/lipgloss"
)

// speedRing 定长环形缓冲（§8.2：避免列表无限增长）。
type speedRing struct {
	buf  []float64
	size int
	head int // 下一个写入位
	// 1Hz 节流：距上次采样 <1s 不写（tick 500ms，但图表数据 1Hz 采样）
	last time.Time
	full bool
}

// newSpeedRing 新建容量为 n 的环形缓冲。
func newSpeedRing(n int) *speedRing {
	return &speedRing{buf: make([]float64, n), size: n}
}

// push 采样写入（1Hz 节流）。返回是否实际写入。
func (r *speedRing) push(v float64, now time.Time) bool {
	if !r.last.IsZero() && now.Sub(r.last) < time.Second {
		return false
	}
	r.buf[r.head] = v
	r.head = (r.head + 1) % r.size
	r.last = now
	if r.head == 0 {
		r.full = true
	}
	return true
}

// vals 按时间顺序返回现有数据（截断/完整）。
func (r *speedRing) vals() []float64 {
	n := r.head
	if r.full {
		n = r.size
	}
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		idx := r.head - n + i
		if idx < 0 {
			idx += r.size
		}
		out[i] = r.buf[idx]
	}
	return out
}

// append 强制写入（测试用，跳过节流）。长度超过容量时留最近 n 笔。
func (r *speedRing) append(v float64) {
	r.buf = append(r.buf, v)
	if len(r.buf) > r.size {
		r.buf = r.buf[len(r.buf)-r.size:]
	}
	r.head = len(r.buf)
	r.full = true
}

// logEvent 事件日志条目（方案 C 底部事件行 / 诊断）。
type logEvent struct {
	at    time.Time
	icon  string
	name  string // 文件名
	msg   string // 消息（已完成/失败原因/操作）
	color lipgloss.Color
}

// eventLogMax 事件日志保留条数。
const eventLogMax = 32

// addEvent 追加事件（超限丢弃最旧）。
func (m *Model) addEvent(icon, name, msg string, color lipgloss.Color) {
	m.events = append(m.events, logEvent{
		at: time.Now(), icon: icon, name: name, msg: msg, color: color,
	})
	if len(m.events) > eventLogMax {
		m.events = m.events[len(m.events)-eventLogMax:]
	}
}

// chunkStat 任务分片统计（方案 B/C 分片图数据源）。
type chunkStat struct {
	Total     int // 总分片数
	Completed int // 已完成
	InFlight  int // 在途
}

// chunksFromShards 由 persist 分片状态计算统计。
// Done 为"自 Start 起连续完成前缀"：前缀长 == 分片长 → 完成；>0 且未完 → 在途。
func chunksFromShards(shards []persist.ShardState) chunkStat {
	var cs chunkStat
	cs.Total = len(shards)
	for _, s := range shards {
		shardLen := s.End - s.Start
		switch {
		case shardLen <= 0:
		case s.Done >= shardLen:
			cs.Completed++
		case s.Done > 0:
			cs.InFlight++
		}
	}
	return cs
}
