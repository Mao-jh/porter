package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	styleTitle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	styleCursor  = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleBarRest = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleDone    = lipgloss.NewStyle().Foreground(lipgloss.Color("12")) // 蓝：完成
	styleFail    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))  // 红：失败
	stylePause   = lipgloss.NewStyle().Foreground(lipgloss.Color("11")) // 黄：暂停
	styleRun     = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // 绿：下载中
	styleDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleErr     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	styleBtn     = lipgloss.NewStyle().Foreground(lipgloss.Color("14")) // 亮青：暂停/继续
	styleBtnDel  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))  // 红：删除
	styleMeta    = lipgloss.NewStyle().Foreground(lipgloss.Color("13")) // 紫：磁力解析状态
	styleFooter  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	stylePanel   = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 1)
)

// protoStyle 协议标签配色：HTTP 青 / BT 绿 / 磁力 紫（一眼区分任务类型）。
var protoStyle = map[string]lipgloss.Style{
	"http":   lipgloss.NewStyle().Foreground(lipgloss.Color("14")),
	"bt":     lipgloss.NewStyle().Foreground(lipgloss.Color("10")),
	"magnet": lipgloss.NewStyle().Foreground(lipgloss.Color("13")),
}

// protoTag 协议短标签（未知协议不渲染标签列，宽度顺延）。
var protoTag = map[string]string{"http": "HTTP", "bt": "BT", "magnet": "磁力"}

// dotStyle 状态色点：下载中=绿 暂停=黄 完成=蓝 失败=红 排队=灰。
var dotStyle = map[TaskState]lipgloss.Style{
	StateRunning: styleRun,
	StatePaused:  stylePause,
	StateDone:    styleDone,
	StateFailed:  styleFail,
	StateQueued:  lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
}

// stateDot 状态色点（●）+ 状态字（如 ●下载中），色点与文字同色。
func stateDot(s TaskState) string {
	st, ok := dotStyle[s]
	if !ok {
		st = dotStyle[StateQueued]
	}
	return st.Render("●" + stateWord(s))
}

// stateWord 状态字（色点旁 2~3 汉字）。
func stateWord(s TaskState) string {
	switch s {
	case StateRunning:
		return "下载中"
	case StatePaused:
		return "已暂停"
	case StateDone:
		return "完成"
	case StateFailed:
		return "失败"
	case StateQueued:
		return "排队"
	}
	return "?"
}

// helpLine 快捷键帮助（常驻底部操作栏，不受动态 status 影响）。
const helpLine = "a:添加  x:代理  s:设置  p:暂停/继续  d:删除  q:退出  ↑/↓/滚轮:选择  鼠标:点行或按钮"

// barWidth 任务行进度条宽度（单元格）。R34 从 24 收紧到 20，
// 为协议差异化信息列腾出宽度（HTTP 速度/ETA、BT 连接/做种、磁力解析状态）。
const barWidth = 20

// ---- 鼠标热区（R32 起） ----

// clickZone 可点击区：行尾操作按钮或任务行。坐标基于终端显示列/行（0-based，
// ANSI 转义不占列，宽字符按 2 列——统一用 lipgloss.Width 度量）。
type clickZone struct {
	action string // "select" | "pause" | "resume" | "delete"
	rowIdx int    // 目标任务索引（-1 表示不适用）
	y      int    // 终端行（View 首行为 0）
	start  int    // 显示列起点（含）
	end    int    // 显示列终点（不含）
}

type frame struct {
	buttons []clickZone // 行尾按钮（命中优先）
	rows    []clickZone // 任务行整行（选中）
}

// lastFrame 上一帧热区：View 渲染时重建，下一次 Update 的鼠标消息命中。
// Bubble Tea 事件循环单线程串行，View/Update 不并发，无锁安全。
var lastFrame frame

// hitZone 返回 (x,y) 命中的第一个热区；未命中返回 nil。
func hitZone(zones []clickZone, x, y int) *clickZone {
	for i := range zones {
		if zones[i].y == y && x >= zones[i].start && x < zones[i].end {
			return &zones[i]
		}
	}
	return nil
}

// View 渲染整个界面（纯字符串，可测试断言）。
// 布局：标题行 / 帮助行 / [错误提示] / [输入行] / 任务列表（含行尾按钮热区）。
func (m Model) View() string {
	if m.settings {
		return m.renderSettings()
	}
	lastFrame = frame{}
	var rows []string

	// 标题行（状态消息 dim 后置）
	title := styleTitle.Render("downloader-tui")
	if m.status != "" {
		title += "  " + styleDim.Render(m.status)
	}
	rows = append(rows, title)

	if m.errMsg != "" {
		rows = append(rows, styleErr.Render("提示: "+m.errMsg))
	}
	switch {
	case m.proxying:
		rows = append(rows, "代理出口> "+m.input.View())
	case m.adding:
		rows = append(rows, "新增 URL> "+m.input.View())
	case m.settingCustom:
		rows = append(rows, "自定义值> "+m.input.View())
	}

	// 全局汇总行（有任务时显示）：活动数 / 总速度 / 总进度
	if len(m.tasks) > 0 {
		rows = append(rows, styleDim.Render(m.summaryLine()))
	}

	// 任务行：y = 顶边框(1) + 头部行数 + 排序位；失败/暂停优先显示（taskOrder）
	headRows := len(rows)
	rowY := 1 + headRows
	for _, origIdx := range taskOrder(m.tasks) {
		t := m.tasks[origIdx]
		rows = append(rows, m.renderTaskRow(t, origIdx, rowY))
		rowY++
		// 错误详情展开行（点击失败行 toggle；点击本行收起）
		if m.expandedErr == origIdx && t.Err != nil {
			detail := "  " + styleErr.Render(cleanErr(t.Err))
			rows = append(rows, detail)
			lastFrame.rows = append(lastFrame.rows, clickZone{
				action: "select", rowIdx: origIdx, y: rowY,
				start: 2, end: 2 + lipgloss.Width(detail),
			})
			rowY++
		}
	}
	if len(m.tasks) == 0 && !m.adding {
		rows = append(rows, styleDim.Render("（无任务，按 a 添加）"))
	}
	if m.QuitReason == "failed" {
		rows = append(rows, styleErr.Render("selftest: 存在未完成任务"))
	}
	// 底部操作栏（R34）：分隔线 + 鼠标可点按钮（跟随选中任务）+ 常显快捷键。
	// y = 顶边框(1) + 已渲染行数（按钮热区与渲染行严格同步）。
	rows = append(rows, m.renderFooter(1+len(rows)))
	return stylePanel.Render(strings.Join(rows, "\n"))
}

// summaryLine 全局汇总：活动任务数 / 总速度 / 总进度（R33）。
// 总进度 = 已知大小任务的累计 done/size；无已知大小显示 --%。
func (m Model) summaryLine() string {
	active, doneTotal, sizeTotal := 0, int64(0), int64(0)
	var speed float64
	for _, t := range m.tasks {
		switch t.State {
		case StateRunning, StateQueued:
			active++
		}
		if t.State == StateRunning {
			speed += t.Speed
		}
		if t.Size > 0 {
			doneTotal += t.Done
			sizeTotal += t.Size
		}
	}
	pct := "--"
	if sizeTotal > 0 {
		pct = fmt.Sprintf("%d%%", int(float64(doneTotal)/float64(sizeTotal)*100))
	}
	return fmt.Sprintf("活动 %d · 总速 %s/s · 总进度 %s", active, humanBytes(int64(speed)), pct)
}

// taskOrder 按状态排序的显示顺序（返回原索引切片）：
// 失败 → 暂停 → 进行中/排队 → 完成；同状态保持原顺序（稳定排序）。
// 仅影响显示层，Model 内任务顺序与 cursor/热区 rowIdx 语义不变。
func taskOrder(tasks []*Task) []int {
	order := make([]int, len(tasks))
	for i := range tasks {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return stateRank(tasks[order[a]].State) < stateRank(tasks[order[b]].State)
	})
	return order
}

// stateRank 排序优先级（小=靠前）。
func stateRank(s TaskState) int {
	switch s {
	case StateFailed:
		return 0
	case StatePaused:
		return 1
	case StateRunning, StateQueued:
		return 2
	default:
		return 3
	}
}

// cleanErr 错误详情单行化（去换行/压缩空白），供展开行渲染。
func cleanErr(err error) string {
	return strings.Join(strings.Fields(err.Error()), " ")
}

// renderTaskRow 渲染单个任务行并记录热区（行尾按钮 + 整行选中）。
// R34 起行结构：cursor + [行首按钮区] + ●状态字 + 协议标签 + 文件名 + 进度条 + 字节 + 协议差异信息列。
// 断言关键词（勿破坏）：文件名/状态字/字节/█/ETA；按钮 x 恒 4（TestButtonPosFixed）。
func (m Model) renderTaskRow(t *Task, i, y int) string {
	cursor := "  "
	if i == m.cursor {
		cursor = styleCursor.Render("> ")
	}
	line := cursor + renderTaskButtons(t, i, y) + " " + stateDot(t.State)
	if tag, ok := protoTag[t.Proto]; ok {
		line += " " + protoStyle[t.Proto].Render(tag)
	}
	line += " " + trunc(t.Output, 14)
	bar, pct := renderBar(t.Done, t.Size, barWidth)
	line += " " + bar + " " + pct + " " + bytesPart(t)
	line += protoInfo(t)
	if t.Err != nil {
		// H-3 拒绝是最常见失败原因：短标签直接可读（详情见顶部 errMsg 指引 / 点击行展开）
		errText := trunc(cleanErr(t.Err), 24)
		if isLoopbackRefusal(t.Err) {
			errText = "安全边界拒绝(H-3)"
		}
		line += " " + styleErr.Render(errText)
	}

	// 整行选择热区（左边框 1 列 + padding 1 列起）
	lastFrame.rows = append(lastFrame.rows, clickZone{
		action: "select", rowIdx: i, y: y,
		start: 2, end: 2 + lipgloss.Width(line),
	})
	return line
}

// bytesPart 已下载/总大小；未知大小（size<=0，流式）显示 ??。
func bytesPart(t *Task) string {
	if t.Size <= 0 {
		return "??"
	}
	return fmt.Sprintf("%s/%s", humanBytes(t.Done), humanBytes(t.Size))
}

// protoInfo 协议差异化信息列（R34）：一眼看出任务类型——
// HTTP=速度+剩余 / BT=连接+做种 / 磁力=解析状态。
// 未知协议或非活动状态返回空（不挤占宽度）。
func protoInfo(t *Task) string {
	switch t.Proto {
	case "bt":
		if t.State == StateDone {
			if t.Seeds == 0 && t.Peers == 0 {
				return ""
			}
			return fmt.Sprintf(" %d 做种 %d 连接", t.Seeds, t.Peers)
		}
		if t.Seeds == 0 && t.Peers == 0 {
			return ""
		}
		return fmt.Sprintf(" %d 连接 %d 做种", t.Peers, t.Seeds)
	case "magnet":
		meta := t.Meta
		if meta == "" {
			meta = "解析完成"
		}
		return " " + styleMeta.Render(meta)
	default: // http：仅下载中且速率>0 时显示（与 R21 前格式一致：speed ETA）
		if t.State == StateRunning && t.Speed > 0 {
			s := " " + humanBytes(int64(t.Speed)) + "/s"
			if t.ETA > 0 {
				s += " ETA " + formatETA(t.ETA)
			}
			return s
		}
		return ""
	}
}

// renderFooter 底部操作栏（R34）：分隔线 + 鼠标可点按钮（跟随选中任务）+ 常显快捷键。
// 按钮放固定列位 x=2 起（边框内第一列），绝不随任务信息宽度跳动（踩坑：可变宽度旁按钮会跳）。
// y 由调用方传入（= 顶边框 1 + 已渲染行数），热区与渲染严格同步。
func (m Model) renderFooter(y int) string {
	sep := styleFooter.Render(strings.Repeat("─", 46))
	help := styleDim.Render(helpLine)
	btns := footerButtons(m.tasks, m.cursor, y)
	if btns == "" {
		return sep + "\n  " + help
	}
	return sep + "\n" + btns + "   " + help
}

// footerButtons 底栏按钮：跟随选中任务状态（running=暂停/移除，
// paused/failed/queued=继续/移除，done=移除）。动作与键盘/行首按钮同语义（activateZone 复用）。
// 按钮固定 6 列（[移除] 等），x 从 2 累加，与渲染一一对应。
func footerButtons(tasks []*Task, cursor, y int) string {
	if len(tasks) == 0 || cursor < 0 || cursor >= len(tasks) {
		return ""
	}
	t := tasks[cursor]
	type spec struct {
		label  string
		action string
		del    bool
	}
	var btns []spec
	switch t.State {
	case StateRunning:
		btns = []spec{{"暂停", "pause", false}, {"移除", "delete", true}}
	case StatePaused, StateFailed, StateQueued:
		btns = []spec{{"继续", "resume", false}, {"移除", "delete", true}}
	case StateDone:
		btns = []spec{{"移除", "delete", true}}
	}
	x := 2 // 左边框 1 + padding 1
	var b strings.Builder
	for j, s := range btns {
		if j > 0 {
			b.WriteString(" ")
			x++
		}
		style := styleBtn
		if s.del {
			style = styleBtnDel
		}
		lastFrame.buttons = append(lastFrame.buttons, clickZone{
			action: s.action, rowIdx: cursor, y: y,
			start: x, end: x + 6,
		})
		b.WriteString(style.Render("[" + s.label + "]"))
		x += 6
	}
	return b.String()
}

// btnAreaW 行首按钮区固定宽度（两按钮 "[暂停] [删除]" = 6+1+6 = 13 列）。
// 固定宽度保证按钮位置与右侧任务信息不随状态/参数变化而左右跳动。
const btnAreaW = 13

// renderTaskButtons 行首按钮：x = 左边框(1) + padding(1) + cursor(2) = 4。
// 按钮固定 6 列（半角括号 + 两汉字），热区与渲染严格同步；末尾补空格到固定宽度。
func renderTaskButtons(t *Task, i, y int) string {
	type spec struct {
		label  string
		action string
		del    bool
	}
	var btns []spec
	switch t.State {
	case StateRunning:
		btns = []spec{{"暂停", "pause", false}, {"删除", "delete", true}}
	case StatePaused, StateFailed, StateQueued:
		btns = []spec{{"继续", "resume", false}, {"删除", "delete", true}}
	case StateDone:
		btns = []spec{{"删除", "delete", true}}
	}
	x := 4 // 左边框 1 + padding 1 + cursor 2
	var b strings.Builder
	for j, s := range btns {
		if j > 0 {
			b.WriteString(" ")
			x++
		}
		style := styleBtn
		if s.del {
			style = styleBtnDel
		}
		lastFrame.buttons = append(lastFrame.buttons, clickZone{
			action: s.action, rowIdx: i, y: y,
			start: x, end: x + 6,
		})
		b.WriteString(style.Render("[" + s.label + "]"))
		x += 6
	}
	if pad := btnAreaW - lipgloss.Width(b.String()); pad > 0 {
		b.WriteString(strings.Repeat(" ", pad))
	}
	return b.String()
}

// renderBar 渲染进度条与百分比。size<=0（未知大小/流式）返回旋转提示条。
// 渐变配色：低进度红（9）→ 中黄（11）→ 高绿（10），100% 全绿。
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
	bar := barColor(frac).Render(strings.Repeat("█", filled)) +
		styleBarRest.Render(strings.Repeat("░", width-filled))
	return bar, fmt.Sprintf("%3.0f%%", frac*100)
}

// barColor 按完成比例选进度条颜色（R34 定稿阈值，用户规格）：
// <30% 红 / <70% 黄 / ≥70% 绿；100% 全绿。
func barColor(frac float64) lipgloss.Style {
	switch {
	case frac >= 0.70:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	case frac >= 0.30:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	}
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

// renderSettings 设置面板（边框包裹）：常用预设优先，自定义补全。
// 每行 [当前档位] 高亮；自定义值（不在预设）时高亮"自定义…"并回显实际值。
// 面板保持键盘交互（鼠标热区不覆盖设置面板，见 handleMouse）。
func (m Model) renderSettings() string {
	var rows []string
	rows = append(rows, styleTitle.Render("设置  ")+
		styleDim.Render("Enter/空格:切换档位  ↑/↓:选择  q/Esc:关闭"))
	if m.errMsg != "" {
		rows = append(rows, styleErr.Render("提示: "+m.errMsg))
	}
	if m.settingCustom {
		rows = append(rows, styleDim.Render("自定义值> ")+m.input.View())
	} else {
		rows = append(rows, "")
	}
	rows2 := []struct {
		name   string
		labels []string
		cur    int
	}{
		{settingRowNames[0], speedLabels, indexOf(speedPresets, m.baseOpt.Limit)},
		{settingRowNames[1], shardLabels, indexOf(shardPresets, m.baseOpt.Shards)},
		{settingRowNames[2], verifyLabels, indexOf(verifyPresets, string(m.baseOpt.Verify))},
		{settingRowNames[3], proxyLabels, indexOf(proxyPresets, m.baseOpt.Proxy)},
	}
	for i, r := range rows2 {
		prefix := "  "
		if i == m.settingRow {
			prefix = styleCursor.Render("> ")
		}
		var b strings.Builder
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
		rows = append(rows, b.String())
	}
	return stylePanel.Render(strings.Join(rows, "\n"))
}
