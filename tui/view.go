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

// helpLine 快捷键帮助（常驻，不受动态 status 影响）。
const helpLine = "a:添加任务  x:代理  s:设置  p:暂停/继续  d:删除  q:退出  ↑/↓:选择"

// View 渲染整个界面（纯字符串，可测试断言）。
func (m Model) View() string {
	if m.settings {
		return m.renderSettings()
	}
	var b strings.Builder
	if m.status != "" {
		b.WriteString(styleHeader.Render("downloader-tui  ") + styleDim.Render(m.status))
	} else {
		b.WriteString(styleHeader.Render("downloader-tui"))
	}
	b.WriteString("\n" + styleDim.Render(helpLine) + "\n")
	if m.errMsg != "" {
		b.WriteString(styleErr.Render("提示: " + m.errMsg))
		b.WriteString("\n")
	}
	switch {
	case m.proxying:
		b.WriteString("代理出口> " + m.input.View() + "\n")
	case m.adding:
		b.WriteString("新增 URL> " + m.input.View() + "\n")
	case m.settingCustom:
		b.WriteString("自定义值> " + m.input.View() + "\n")
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
			// H-3 拒绝是最常见失败原因：短标签直接可读（详情见顶部 errMsg 指引）
			errText := trunc(t.Err.Error(), 40)
			if isLoopbackRefusal(t.Err) {
				errText = "安全边界拒绝(H-3)"
			}
			line += " " + styleErr.Render(errText)
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

// renderSettings 设置面板：常用预设优先，自定义补全。
// 每行 [当前档位] 高亮；自定义值（不在预设）时高亮"自定义…"并回显实际值。
func (m Model) renderSettings() string {
	var b strings.Builder
	b.WriteString(styleHeader.Render("设置  ") +
		styleDim.Render("Enter/空格:切换档位  ↑/↓:选择  q/Esc:关闭") + "\n")
	if m.errMsg != "" {
		b.WriteString(styleErr.Render("提示: " + m.errMsg) + "\n")
	}
	if m.settingCustom {
		b.WriteString(styleDim.Render("自定义值> ") + m.input.View() + "\n\n")
	} else {
		b.WriteString("\n")
	}
	rows := []struct {
		name   string
		labels []string
		cur    int
	}{
		{settingRowNames[0], speedLabels, indexOf(speedPresets, m.baseOpt.Limit)},
		{settingRowNames[1], shardLabels, indexOf(shardPresets, m.baseOpt.Shards)},
		{settingRowNames[2], verifyLabels, indexOf(verifyPresets, string(m.baseOpt.Verify))},
		{settingRowNames[3], proxyLabels, indexOf(proxyPresets, m.baseOpt.Proxy)},
	}
	for i, r := range rows {
		prefix := "  "
		if i == m.settingRow {
			prefix = styleCursor.Render("> ")
		}
		b.WriteString(prefix + fmt.Sprintf("%-4s", r.name) + " ")
		highlight := r.cur
		if highlight < 0 { // 自定义值：高亮"自定义…"档
			highlight = len(r.labels) - 1
		}
		for j, lbl := range r.labels {
			if j == highlight {
				b.WriteString("[" + styleCursor.Render(lbl) + "]")
			} else {
				b.WriteString(lbl)
			}
			if j < len(r.labels)-1 {
				b.WriteString(" ")
			}
		}
		if r.cur < 0 {
			extra := map[int]string{
				0: "(" + humanBytes(m.baseOpt.Limit) + "/s)",
				1: fmt.Sprintf("(%d)", m.baseOpt.Shards),
				2: "(" + string(m.baseOpt.Verify) + ")",
				3: "(" + m.baseOpt.Proxy + ")",
			}
			b.WriteString(" " + styleDim.Render(extra[i]))
		}
		b.WriteString("\n")
	}
	return b.String()
}
