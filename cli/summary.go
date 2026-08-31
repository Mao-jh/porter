// summary.go 实现 `-summary` 的速率/ETA 增强（第 19 轮）：
// summaryTracker 用相邻 store 快照差分计算每任务速率与剩余 ETA，
// 输出格式：[进度] <状态> <已完成/总大小> (百分比) <速率> ETA <剩余>  <输出>  <URL>。
package cli

import (
	"fmt"
	gio "io"
	"sort"
	"time"

	"github.com/Mao-jh/porter/persist"
)

// speedEmaAlpha EMA 平滑系数（R20）：速率=α×上帧EMA + (1-α)×瞬时值。
// 首个有历史帧播种瞬时值（不做混合），避免首帧被 α 拉低。
const speedEmaAlpha = 0.5

// summaryTracker 跨帧跟踪任务进度（速率经 EMA 平滑，抗瞬时抖动）。
type summaryTracker struct {
	prev map[string]int64  // id -> 上帧 done 字节
	ema  map[string]float64 // id -> 平滑速率（字节/秒）
	at   time.Time         // 上帧时间
}

func newSummaryTracker() *summaryTracker {
	return &summaryTracker{prev: map[string]int64{}, ema: map[string]float64{}, at: time.Now()}
}

// render 输出本帧摘要（时间取当前；renderAt 为可注入时间的测试变体）。
func (s *summaryTracker) render(w gio.Writer, states []*persist.State) {
	s.renderAt(w, states, time.Now())
}

// renderAt 按指定时刻输出摘要并更新内部快照。
func (s *summaryTracker) renderAt(w gio.Writer, states []*persist.State, now time.Time) {
	if len(states) == 0 {
		return
	}
	dt := now.Sub(s.at).Seconds()
	next := make(map[string]int64, len(states))
	sorted := make([]*persist.State, len(states))
	copy(sorted, states)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for _, st := range sorted {
		done := st.Done
		next[st.ID] = done
		prev, had := s.prev[st.ID]
		speed := 0.0
		if had && dt > 0 {
			instant := float64(done-prev) / dt
			if instant < 0 {
				instant = 0 // done 回落（任务重启）：瞬时速率钳 0
			}
			if prevEma, ok := s.ema[st.ID]; ok {
				speed = speedEmaAlpha*prevEma + (1-speedEmaAlpha)*instant
			} else {
				speed = instant // 播种：首个有历史帧直接用瞬时值
			}
			s.ema[st.ID] = speed
		}
		eta := ""
		if st.FileSize > 0 && speed > 0 {
			eta = formatETA(float64(st.FileSize-done) / speed)
		}
		pct := 0.0
		if st.FileSize > 0 {
			pct = float64(done) / float64(st.FileSize) * 100
			if pct > 100 {
				pct = 100
			}
		}
		speedStr := "-"
		if had && dt > 0 {
			speedStr = humanSpeed(speed)
		}
		etaStr := "-"
		if eta != "" {
			etaStr = eta
		}
		line := fmt.Sprintf("[进度] %-6s %s (%.1f%%) %s ETA %s  %s  %s\n",
			st.Status, humanBytes(done, st.FileSize), pct, speedStr, etaStr, st.ID, st.URL)
		_, _ = gio.WriteString(w, line)
	}
	s.prev, s.at = next, now
}

// humanBytes 格式化 已完成/总大小（自动 KiB/MiB 单位）。
func humanBytes(done, total int64) string {
	switch {
	case total >= 1<<20 || done >= 1<<20:
		return fmt.Sprintf("%.1f/%.1fMiB", float64(done)/(1<<20), float64(total)/(1<<20))
	case total >= 1<<10 || done >= 1<<10:
		return fmt.Sprintf("%.1f/%.1fKiB", float64(done)/(1<<10), float64(total)/(1<<10))
	default:
		return fmt.Sprintf("%d/%dB", done, total)
	}
}

// humanSpeed 格式化速率（B/s → KiB/s → MiB/s）。
func humanSpeed(bps float64) string {
	switch {
	case bps >= 1<<20:
		return fmt.Sprintf("%.1fMiB/s", bps/(1<<20))
	case bps >= 1<<10:
		return fmt.Sprintf("%.1fKiB/s", bps/(1<<10))
	default:
		return fmt.Sprintf("%.0fB/s", bps)
	}
}

// formatETA 秒数 → "Xh Ym" / "Xm Ys" / "Xs"。
func formatETA(secs float64) string {
	if secs < 0 {
		return "-"
	}
	s := int64(secs)
	h, m, sec := s/3600, (s%3600)/60, s%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, sec)
	default:
		return fmt.Sprintf("%ds", sec)
	}
}
