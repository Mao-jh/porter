package proto

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ---- 共享小工具（原型层复制，避免牵动正式 tui 包） ----

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

// clipTo 按显示宽度裁剪到 w 列（CJK 按 2 列，忽略 ANSI）。
func clipTo(s string, w int) string {
	var b strings.Builder
	cur := 0
	for _, r := range s {
		ww := lipgloss.Width(string(r))
		if cur+ww > w {
			break
		}
		b.WriteRune(r)
		cur += ww
	}
	return b.String()
}

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

// 状态色点：下载中=绿 暂停=黄 完成=蓝 失败=红 排队=灰（一眼区分，与文字状态并列）。
var dotStyle = map[string]lipgloss.Style{
	"running": lipgloss.NewStyle().Foreground(lipgloss.Color("10")), // 绿
	"paused":  lipgloss.NewStyle().Foreground(lipgloss.Color("11")), // 黄
	"done":    lipgloss.NewStyle().Foreground(lipgloss.Color("12")), // 蓝
	"failed":  lipgloss.NewStyle().Foreground(lipgloss.Color("9")),  // 红
	"queued":  lipgloss.NewStyle().Foreground(lipgloss.Color("245")), // 灰
}

func stateDot(state string) string {
	st, ok := dotStyle[state]
	if !ok {
		st = dotStyle["queued"]
	}
	return st.Render("●")
}

// 协议标签：HTTP/BT/磁力 短标签，色区分。
var protoTag = map[string]string{
	"http":   "HTTP",
	"bt":     "BT",
	"magnet": "磁力",
}

// bar 三色进度条（30%/70% 阈值，用户定稿规格）：
//
//	<30% 红 / <70% 黄 / ≥70% 绿；未知大小（size<=0）→ 旋转提示条。
func bar(done, size int64, width int) (string, string) {
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
	var c string
	switch {
	case frac >= 0.70:
		c = "10" // 绿
	case frac >= 0.30:
		c = "11" // 黄
	default:
		c = "9" // 红
	}
	bar := lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Render(strings.Repeat("█", filled)) +
		lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(strings.Repeat("░", width-filled))
	return bar, fmt.Sprintf("%3.0f%%", frac*100)
}

// ---- 变体 A：现状基线（行首按钮 + 状态文字 + 33/66 阈值 + 无协议列 + 文字帮助行） ----

// VariantA 现状布局，作为对比参照物。
func VariantA(ts []PTask) string {
	var rows []string
	rows = append(rows, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")).Render("downloader-tui"))
	rows = append(rows, lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(
		"a:添加任务  x:代理  s:设置  p:暂停/继续  d:删除  q:退出  ↑/↓/滚轮:选择  鼠标:点行或按钮"))
	for _, t := range ts {
		rows = append(rows, rowA(t))
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(0, 1).
		Render(strings.Join(rows, "\n"))
}

func rowA(t PTask) string {
	var stateCol string
	switch t.State {
	case "done":
		stateCol = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render("✔ 完成")
	case "failed":
		stateCol = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("✗ 失败")
	case "paused":
		stateCol = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render("⏸ 已暂停")
	default:
		stateCol = "· " + stateLabel(t.State)
	}
	b, pct := bar(t.Done, t.Size, 24)
	line := fmt.Sprintf("%-18s %s %s %s %s/%s",
		trunc(t.Name, 18), stateCol, b, pct,
		humanBytes(t.Done), humanBytes(t.Size))
	if t.State == "running" && t.Speed > 0 {
		line += " " + humanBytes(int64(t.Speed)) + "/s"
		if t.ETA > 0 {
			line += " ETA " + formatETA(t.ETA)
		}
	}
	if t.Err != "" {
		line += " " + lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render(trunc(oneLine(t.Err), 40))
	}
	return line
}

func stateLabel(s string) string {
	switch s {
	case "running":
		return "下载中"
	case "paused":
		return "已暂停"
	case "queued":
		return "排队"
	default:
		return s
	}
}

// oneLine 错误单行化：\n 会断行、破坏热区 y 坐标，先压成一行再截断。
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// ---- 变体 B：协议差异化 + 状态色点 + 30/70 阈值 + 底部操作栏按钮（定稿候选） ----

// VariantB 目标布局：顶栏标题 / 中部任务列表 / 底部操作栏。
// 任务行 = 状态色点 + 协议标签 + 文件名 + 进度条 + 协议差异信息列 + 行首操作按钮。
func VariantB(ts []PTask) string {
	var rows []string
	rows = append(rows, headerB())
	for _, t := range ts {
		rows = append(rows, rowB(t))
	}
	rows = append(rows, footerB("running")) // 底栏按钮跟随选中任务状态
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(0, 1).
		Render(strings.Join(rows, "\n"))
}

func headerB() string {
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")).Render("downloader-tui") +
		"  " + lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("活动 3 · 总速 5.6MiB/s · 总进度 34%")
}

// rowB 一行布局（列位固定，按钮恒在行首 x=4 区域，不随信息宽度跳动）：
//
//	[按钮区] ● 协议 文件名…… [██████] 58% 2.0GB  3.4MiB/s ETA 4m
func rowB(t PTask) string {
	btns := btnAreaB(t)
	dot := stateDot(t.State)
	tag := protoTag[t.Proto]
	if tag == "" {
		tag = "?"
	}
	b, pct := bar(t.Done, t.Size, 24)
	line := btns + " " + dot + " " + tag + " " + trunc(t.Name, 16)
	line += " " + b + " " + pct + " " + bytesB(t) + infoB(t)
	if t.Err != "" {
		line += " " + lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render(trunc(oneLine(t.Err), 34))
	}
	return line
}

// infoB 协议差异化信息列：HTTP=速度+ETA / BT=连接数+做种 / 磁力=解析状态。
func infoB(t PTask) string {
	switch t.Proto {
	case "bt":
		if t.State == "done" {
			return fmt.Sprintf(" %d 做种 %d 连接", t.Seeds, t.Peers)
		}
		return fmt.Sprintf(" %d 连接 %d 做种", t.Peers, t.Seeds)
	case "magnet":
		meta := t.Meta
		if meta == "" {
			meta = "解析完成"
		}
		return " " + lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Render(meta)
	default: // http
		if t.State == "running" && t.Speed > 0 {
			s := " " + humanBytes(int64(t.Speed)) + "/s"
			if t.ETA > 0 {
				s += " ETA " + formatETA(t.ETA)
			}
			return s
		}
		return ""
	}
}

// bytesB 已下载/总大小；未知大小（size<=0）显示 ??。
func bytesB(t PTask) string {
	if t.Size <= 0 {
		return "??"
	}
	return fmt.Sprintf("%s/%s", humanBytes(t.Done), humanBytes(t.Size))
}

// btnAreaB 行首操作按钮区（固定宽度 13，位置恒定；"鼠标:点按钮"热区在正式移植时注册）。
func btnAreaB(t PTask) string {
	var btns []string
	switch t.State {
	case "running":
		btns = []string{"[暂停]", "[删除]"}
	case "paused", "failed", "queued":
		btns = []string{"[继续]", "[删除]"}
	case "done":
		btns = []string{"[删除]"}
	}
	b := strings.Join(btns, " ")
	if pad := 13 - lipgloss.Width(b); pad > 0 {
		b += strings.Repeat(" ", pad)
	}
	return b
}

// footerB 底部操作栏：分隔线 + 鼠标可点按钮（跟随选中任务）+ 常显快捷键。
func footerB(selState string) string {
	btn := func(label string) string {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Render("[" + label + "]")
	}
	del := lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("[移除]")
	var bar string
	switch selState {
	case "running":
		bar = btn("暂停") + " " + del
	case "paused", "failed", "queued":
		bar = btn("继续") + " " + btn("重试") + " " + del
	case "done":
		bar = del
	}
	sep := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(strings.Repeat("─", 70))
	return sep + "\n" + bar + "    " +
		lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("a:添加 x:代理 s:设置 q:退出 ↑/↓/滚轮:选择")
}

// ---- 变体 C：双行任务卡（窄屏友好：信息分两行，不再挤一行） ----

// VariantC 每任务两行：第一行 色点+文件名+进度条；第二行缩进 协议信息+按钮。
func VariantC(ts []PTask) string {
	var rows []string
	rows = append(rows, headerB())
	for _, t := range ts {
		rows = append(rows, rowC(t)...)
	}
	rows = append(rows, footerB("running"))
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(0, 1).
		Render(strings.Join(rows, "\n"))
}

func rowC(t PTask) []string {
	b, pct := bar(t.Done, t.Size, 28)
	l1 := stateDot(t.State) + " " + trunc(t.Name, 40) + " " + b + " " + pct
	l2 := "   " + protoTag[t.Proto] + "  " + bytesC(t) + infoB(t) + "  " + btnAreaB(t)
	if t.Err != "" {
		l2 += " " + lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render(trunc(oneLine(t.Err), 30))
	}
	return []string{l1, l2}
}

func bytesC(t PTask) string {
	if t.Size <= 0 {
		return "未知大小"
	}
	return fmt.Sprintf("%s/%s", humanBytes(t.Done), humanBytes(t.Size))
}

// ---- 并排输出（原型三步法第 2 步：变体并排） ----

// SideBySide 把多个变体按行横向并排拼成一段文本（每栏固定栏宽，按显示宽度裁剪）。
// 返回的 []string 每元素一行，可直接 join 输出；ANSI 样式保留。
func SideBySide(cols []string, colWidth int) string {
	splits := make([][]string, len(cols))
	maxRows := 0
	for i, c := range cols {
		lines := strings.Split(c, "\n")
		splits[i] = lines
		if len(lines) > maxRows {
			maxRows = len(lines)
		}
	}
	var b strings.Builder
	for r := 0; r < maxRows; r++ {
		for ci := range cols {
			cell := ""
			if r < len(splits[ci]) {
				cell = splits[ci][r]
			}
			b.WriteString(clipTo(cell, colWidth))
			if pad := colWidth - lipgloss.Width(cell); pad > 0 {
				b.WriteString(strings.Repeat(" ", pad))
			}
			b.WriteString(" │ ")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
