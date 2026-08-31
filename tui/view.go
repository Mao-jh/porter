package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	styleHeader  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	styleCursor  = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleBarFill = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleBarRest = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleDone    = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleFail    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	stylePause   = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styleDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleErr     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

// View 渲染整个界面（纯字符串，可测试断言）。
func (m Model) View() string {
	var b strings.Builder
	b.WriteString(styleHeader.Render("downloader-tui  ") + styleDim.Render(m.status))
	b.WriteString("\n")
	if m.errMsg != "" {
		b.WriteString(styleErr.Render("错误: " + m.errMsg))
		b.WriteString("\n")
	}
	if m.adding {
		b.WriteString("新增 URL> " + m.input.View() + "\n")
	}

	barWidth := 24
	for i, t := range m.tasks {
		cursor := "  "
		if i == m.cursor {
			cursor = styleCursor.Render("> ")
		}
		var stateCol string
		switch t.State {
		case StateDone:
			stateCol = styleDone.Render("✔ " + t.State.String())
		case StateFailed:
			stateCol = styleFail.Render("✗ " + t.State.String())
		case StatePaused:
			stateCol = stylePause.Render("⏸ " + t.State.String())
		default:
			stateCol = "· " + t.State.String()
		}
		bar, pct := renderBar(t.Done, t.Size, barWidth)
		line := fmt.Sprintf("%s%-18s %s %s %s %s/%s",
			cursor,
			trunc(t.Output, 18),
			stateCol,
			bar,
			pct,
			humanBytes(t.Done), humanBytes(t.Size),
		)
		if t.State == StateRunning && t.Speed > 0 {
			line += " " + humanBytes(int64(t.Speed)) + "/s"
			if t.ETA > 0 {
				line += " ETA " + formatETA(t.ETA)
			}
		}
		if t.Err != nil {
			line += " " + styleErr.Render(trunc(t.Err.Error(), 40))
		}
		b.WriteString(line + "\n")
	}
	if len(m.tasks) == 0 && !m.adding {
		b.WriteString(styleDim.Render("（无任务，按 a 添加）") + "\n")
	}
	if m.QuitReason == "failed" {
		b.WriteString(styleErr.Render("selftest: 存在未完成任务\n"))
	}
	return b.String()
}

// renderBar 渲染进度条与百分比。size<=0（未知大小/流式）返回旋转提示条。
func renderBar(done, size int64, width int) (string, string) {
	if size <= 0 {
		return "[" + strings.Repeat("░", width) + "]", "--%"
	}
	frac := float64(done) / float64(size)
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	filled := int(frac * float64(width))
	bar := styleBarFill.Render(strings.Repeat("█", filled)) +
		styleBarRest.Render(strings.Repeat("░", width-filled))
	return bar, fmt.Sprintf("%3.0f%%", frac*100)
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// humanBytes 人类可读字节数（1024 进制，1 位小数，B 不带小数）。
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// formatETA 剩余秒数 → 人类可读（Xs / Xm Ys / Xh Ym）。tui 独立 module，
// 复制 cli.summary 的同名小工具（避免跨 module 导出）。
func formatETA(secs int64) string {
	if secs <= 0 {
		return "-"
	}
	h, m, s := secs/3600, (secs%3600)/60, secs%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}
