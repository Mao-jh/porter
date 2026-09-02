// 三套布局渲染（方案 §5）：A 紧凑单列 / B 主从双栏 / C 仪表盘。
//
// 约定：每个布局返回 main 区行（不含 header/footer，由 View 拼接铺满）。
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// visibleOrder 过滤 + 排序后的显示顺序（返回原索引）。
func (m Model) visibleOrder() []int {
	order := taskOrder(m.tasks)
	if m.filter == "" {
		return order
	}
	f := strings.ToLower(m.filter)
	out := make([]int, 0, len(order))
	for _, i := range order {
		t := m.tasks[i]
		if strings.Contains(strings.ToLower(t.Output), f) || strings.Contains(strings.ToLower(t.URL), f) {
			out = append(out, i)
		}
	}
	return out
}

// fillRow 铺背景到宽 w（行底色）。
func fillRow(s string, w int, bg lipgloss.Color) string {
	pad := w - lipgloss.Width(s)
	if pad < 0 {
		pad = 0
	}
	return stBg(bg).Render(s + strings.Repeat(" ", pad))
}

// ---- 布局 A · 紧凑单列（§5.1） ----

// itemY 方案 A 任务项起始 Y（3 内容行 + 1 间隙）。
var itemYs = []int{3, 7, 11, 15, 19, 23, 27, 31}

// renderLayoutA 紧凑单列：每项 3 行 + 1 间隙，无边框卡片式。
// 总高 h = header 2 + main (h-4) + footer 2。
func (m Model) renderLayoutA(w, h int) []string {
	var rows []string
	// 任务区起始 y=3（header 2 行 + 1 空行），底部留 footer 2 行
	rows = append(rows, emptyLine(w, colBg())) // 屏 y=2
	vis := m.visibleOrder()
	maxItems := (h - 5) / 4
	shown := 0
	for _, i := range vis {
		if shown >= maxItems {
			break
		}
		// 屏 y = 2(空行) + shown*4 + 1 = 3 + shown*4
		zoneY := 3 + shown*4
		m.zoneRow(zoneY, i)
		shown++
		rows = append(rows, m.renderItemA(m.tasks[i], i, w)...)
		// 错误详情展开行（点击失败行 toggle，R33）
		if m.expandedErr == i && m.tasks[i].Err != nil {
			detail := "  " + styleErr.Render(cleanErr(m.tasks[i].Err))
			rows = append(rows, fillRow(detail, w, colBg()))
			m.zoneRow(zoneY+3, i)
		}
		if shown < len(vis) {
			rows = append(rows, emptyLine(w, colBg()))
		}
	}
	for len(rows) < h-4 {
		rows = append(rows, emptyLine(w, colBg()))
	}
	return rows
}

// renderItemA 单项 3 行（选中态：行首 █ 指示条 + PANEL2 底色）。
func (m Model) renderItemA(t *Task, i, w int) []string {
	sel := i == m.cursor
	bg := colPanel2()
	if !sel {
		bg = colBg()
	}
	barW := w - LayoutABarOffset
	if barW < 10 {
		barW = 10
	}

	nameFg := colDim()
	if sel {
		nameFg = colTxt()
	}
	// 行1：指示条 + 状态图标 + 文件名 + 协议/连接（右对齐）
	r1 := ""
	if sel {
		r1 += st(colFocus()).Render("█")
	} else {
		r1 += " "
	}
	r1 += " " + st(stateColor(t.State)).Render(stateIcon(t.State))
	name := truncW(sanitizeGlyphs(t.Output), w-24)
	r1 += " " + stBold(nameFg).Render(name)
	// 右端协议 + 连接
	proto := ""
	if tag, ok := protoTag[t.Proto]; ok {
		proto = tag
	}
	conn := ""
	if t.Proto == "http" && t.chunks.Total > 0 {
		conn = fmt.Sprintf(LblConn, t.chunks.Total)
	}
	right1 := strings.TrimSpace(proto + " · " + conn)
	if right1 != "" {
		if pad := w - lipgloss.Width(r1) - lipgloss.Width(right1) - 1; pad > 0 {
			r1 = r1 + strings.Repeat(" ", pad) + " " + st(colMute()).Render(right1)
		}
	}
	r1 = fillRow(r1, w, bg)

	// 行2：进度条 + 百分比 + 速度（右对齐）
	bar, pct := m.renderBarA(t, barW)
	r2 := "  " + bar
	pctStr := " " + st(colTxt()).Render(pct)
	r2 += pctStr
	spd := ""
	if t.State == StateRunning && t.Speed > 0 {
		spd = humanBytes(int64(t.Speed)) + "/s"
	}
	if spd != "" {
		if pad := w - lipgloss.Width(r2) - lipgloss.Width(spd) - 1; pad > 0 {
			r2 = r2 + strings.Repeat(" ", pad) + " " + stBold(colCyan()).Render(spd)
		}
	}
	r2 = fillRow(r2, w, bg)

	// 行3：大小 · 剩余 · 备注 + sparkline（右对齐）
	r3 := "  " + st(colDim()).Render(m.metaLine(t))
	sparkFg := colMute()
	if sel {
		sparkFg = colAccent()
	}
	sv := m.taskSpark(t)
	if len(sv) > 0 {
		sp := string(sparkline(sv, LayoutASparkW))
		if pad := w - lipgloss.Width(r3) - LayoutASparkW - 1; pad > 0 {
			r3 = r3 + strings.Repeat(" ", pad) + " " + st(sparkFg).Render(sp)
		}
	}
	r3 = fillRow(r3, w, bg)
	return []string{r1, r2, r3}
}

// renderBarA 进度条（亚字符）+ 百分比；未知大小 → 流式虚线。
func (m Model) renderBarA(t *Task, width int) (string, string) {
	if t.Size <= 0 {
		bar := "[" + strings.Repeat("░", width) + "]"
		return bar, "--%"
	}
	frac := float64(t.Done) / float64(t.Size)
	bc := barColorOf(t)
	bar := st(bc).Render(string(progressBar(frac, width, '█', '░')))
	return bar, fmt.Sprintf("%3.0f%%", frac*100)
}

// barColorOf 状态 → 进度条色（§6.2 表）。
func barColorOf(t *Task) lipgloss.Color {
	switch t.State {
	case StatePaused:
		return colYellow()
	case StateDone:
		return colGreen()
	case StateFailed:
		return colRed()
	default:
		return colAccent()
	}
}

// metaLine 数值行：大小 · 剩余 · 备注。
func (m Model) metaLine(t *Task) string {
	var parts []string
	if t.Size > 0 {
		parts = append(parts, fmt.Sprintf("%s / %s", humanBytes(t.Done), humanBytes(t.Size)))
	}
	if t.State == StateRunning && t.ETA > 0 {
		parts = append(parts, fmt.Sprintf(StLeft, formatETA(t.ETA)))
	}
	switch t.State {
	case StatePaused:
		parts = append(parts, StPaused)
	case StateFailed:
		if t.Err != nil {
			if isLoopbackRefusal(t.Err) {
				parts = append(parts, "安全边界拒绝(H-3)")
			} else {
				parts = append(parts, truncW(cleanErr(t.Err), 30))
			}
		}
	case StateQueued:
		parts = append(parts, StQueued)
	case StateDone:
		parts = append(parts, StCompleted)
	}
	return strings.Join(parts, "  ·  ")
}

// taskSpark 任务速度历史（面积图/sparkline 数据源）。
func (m Model) taskSpark(t *Task) []float64 {
	if t.speedRing == nil {
		return nil
	}
	return t.speedRing.vals()
}

// ---- 布局 B · 主从双栏（§5.2） ----

// renderLayoutB 左队列 + 中缝 + 右详情面板。
// 宽度/高度自适应：右栏宽 = min(63, w-40)（窄窗压缩），右栏网格高 = h-4（随高度缩放）。
func (m Model) renderLayoutB(w, h int) []string {
	rw := LayoutBRW
	if w < LayoutBNarrow {
		rw = w - 40 // 窄窗压缩右栏
	}
	if rw > w-1 { // 右栏不能超出窗口（双保险；View 已保证 B 需 w≥LayoutBMinW）
		rw = w - 1
	}
	if rw < LayoutBRWMin {
		rw = LayoutBRWMin
	}
	rx := w - rw
	lx := rx - 1 // 中缝竖线列
	lw := lx     // 左栏宽 x=0..lx-1
	ix := rx + 2 // 右栏内容起点
	iw := rw - 4 // 右栏内容宽
	boxH := h - 4

	rows := make([]string, 0, boxH)
	for y := 0; y < boxH; y++ {
		left := m.renderQueueAt(w, lw, y)
		sep := "│"
		right := m.renderDetailAt(w, ix, iw, boxH, y)
		rows = append(rows, left+sep+right)
	}
	return rows
}

// renderQueueAt 左栏某行的内容（标题/分隔/队列项/空白）。
func (m Model) renderQueueAt(w, lw, y int) string {
	// 屏 y=2..27 → 本地 y=0..25（此处 y 为本地行号）
	screenY := y + 2
	// y=0（屏 y=2）: QUEUE 标题
	if y == 0 {
		return fillRow(stBold(colDim()).Render(fmt.Sprintf(LblQueue+" %d", len(m.tasks))), lw, colBg())
	}
	if y == 1 {
		return st(colBorder()).Render(strings.Repeat("─", lw))
	}
	vis := m.visibleOrder()
	// 活动区：仅非已完成任务（已完成任务只在"已完成"分组显示，§5.2 队列语义）
	active := vis[:0:0]
	for _, i := range vis {
		if m.tasks[i].State != StateDone {
			active = append(active, i)
		}
	}
	// 活动任务行（每项 2 行）从屏 y=4 起
	if screenY >= 4 && screenY <= 16 {
		idx := (screenY - 4) / 2
		if idx < len(active) {
			if (screenY-4)%2 == 0 {
				m.zoneRow(screenY, active[idx]) // 行 0 注册选中热区
			}
			return m.renderQueueItem(active[idx], (screenY-4)%2, lw)
		}
		return fillRow("", lw, colBg())
	}
	if screenY == 19 {
		return fillRow(stBold(colMute()).Render(LblCompleted), lw, colBg())
	}
	if screenY == 20 {
		return st(colBorder()).Render(strings.Repeat("─", lw))
	}
	if screenY >= 21 && screenY <= 26 {
		// 已完成任务
		var done []int
		for _, i := range vis {
			if m.tasks[i].State == StateDone {
				done = append(done, i)
			}
		}
		idx := (screenY - 21) / 2
		if idx < len(done) {
			if (screenY-21)%2 == 0 {
				m.zoneRow(screenY, done[idx])
			}
			return m.renderQueueItem(done[idx], (screenY-21)%2, lw)
		}
		return fillRow("", lw, colBg())
	}
	return fillRow("", lw, colBg())
}

// renderQueueItem 队列项（2 行：文件名+速度 / 迷你进度条+pct+ETA）。
func (m Model) renderQueueItem(i, rowInItem, lw int) string {
	t := m.tasks[i]
	sel := i == m.cursor
	bg := colBg()
	if sel {
		bg = colPanel2()
	}
	if rowInItem%4 == 2 { // 第 2 行（偶数）斑马纹
		if !sel {
			bg = colPanel()
		}
	}
	prefix := " "
	if sel {
		prefix = st(colFocus()).Render("█")
	}
	if rowInItem == 0 {
		// 行0：图标 + 序号 + 文件名 + 速度
		line := prefix + " " + st(stateColor(t.State)).Render(stateIcon(t.State))
		line += " " + st(colMute()).Render(fmt.Sprintf("%02d", i+1))
		name := truncW(sanitizeGlyphs(t.Output), lw-20)
		line += " " + st(colTxt()).Render(name)
		spd := ""
		if t.State == StateRunning && t.Speed > 0 {
			spd = humanBytes(int64(t.Speed)) + "/s"
		}
		if spd != "" {
			if pad := lw - lipgloss.Width(line) - lipgloss.Width(spd) - 1; pad > 0 {
				line = line + strings.Repeat(" ", pad) + " " + stBold(colCyan()).Render(spd)
			}
		}
		return fillRow(line, lw, bg)
	}
	// 行1：迷你进度条 + pct + ETA
	barW := LayoutBBarW
	bar := st(colMute()).Render(strings.Repeat("░", barW))
	pct := "--"
	if t.Size > 0 {
		frac := float64(t.Done) / float64(t.Size)
		bar = st(barColorOf(t)).Render(string(progressBar(frac, barW, '█', '░')))
		pct = fmt.Sprintf("%3.0f%%", frac*100)
	}
	line := prefix + " " + bar + " " + st(colTxt()).Render(pct)
	eta := ""
	switch t.State {
	case StateRunning:
		if t.ETA > 0 {
			eta = formatETA(t.ETA)
		}
	case StatePaused:
		eta = StPaused
	case StateFailed:
		eta = StFailed
	case StateQueued:
		eta = StQueued
	case StateDone:
		eta = StDone
	}
	if eta != "" {
		if pad := lw - lipgloss.Width(line) - lipgloss.Width(eta) - 1; pad > 0 {
			line = line + strings.Repeat(" ", pad) + " " + st(colMute()).Render(eta)
		}
	}
	return fillRow(line, lw, bg)
}

// renderDetailAt 右栏某行（boxH 为右栏网格高度，y 为本地行号）。
func (m Model) renderDetailAt(w, ix, iw, boxH, y int) string {
	g := m.buildDetailGrid(ix, iw, boxH)
	if y < g.h {
		return g.row(y)
	}
	return strings.Repeat(" ", iw+4)
}

// buildDetailGrid 构造右栏详情面板网格（宽 iw+4，高 boxH 自适应 §8.4）。
// 高度分层：boxH≥16 完整（进度条/面积图/分片图/元数据）；<16 隐藏面积图；
// <14 隐藏分片图+图例，仅保留进度条与元数据。
func (m Model) buildDetailGrid(ix, iw, boxH int) *cellGrid {
	if boxH < 8 {
		boxH = 8
	}
	sel := m.cursor >= 0 && m.cursor < len(m.tasks)
	t := &Task{Output: "（无选中）", Size: 0, Done: 0}
	if sel {
		t = m.tasks[m.cursor]
	}
	g := newCellGrid(iw+4, boxH)
	// 外框 round，聚焦色
	g.border(0, 0, iw+4, boxH, borderRound, colFocus())
	// 标题：文件名（框内顶部）
	title := truncW(sanitizeGlyphs(t.Output), iw-2)
	g.putStr(2, 0, title, colTxt(), bgTok0())

	// y=1: 状态徽章（§4.5）+ 协议信息
	bdg := " " + stateIcon(t.State) + " " + stateWord(t.State) + " "
	g.putStr(2, 1, bdg, stateColor(t.State), colPanel3())
	proto := ""
	if tag, ok := protoTag[t.Proto]; ok {
		proto = tag
	}
	if proto != "" {
		r := st(colDim()).Render(proto + " · " + fmt.Sprintf(LblConnStr, connN(t)))
		g.putStr(iw+2-lipgloss.Width(r), 1, r, colDim(), bgTok0())
	}

	// y=3: 主进度条（宽 iw）
	bar := string(progressBar(frac(t), iw, '█', '░'))
	g.putStr(2, 3, bar, barColorOf(t), bgTok0())

	// y=4: 百分比 + 大小 + 速度+ETA
	pct := "--"
	if t.Size > 0 {
		pct = fmt.Sprintf("%3.0f%%", frac(t)*100)
	}
	g.putStr(2, 4, pct, colTxt(), bgTok0())
	if t.Size > 0 {
		g.putStr(8, 4, fmt.Sprintf("%s / %s", humanBytes(t.Done), humanBytes(t.Size)), colDim(), bgTok0())
	}
	spd := ""
	if t.State == StateRunning && t.Speed > 0 {
		spd = fmt.Sprintf("%s  %s", humanBytes(int64(t.Speed))+"/s", fmt.Sprintf(StLeft, formatETA(t.ETA)))
	}
	if spd != "" {
		g.putStr(iw+2-lipgloss.Width(spd), 4, spd, colCyan(), bgTok0())
	}

	// y=6: 虚线分隔
	for x := 1; x < iw+3; x++ {
		g.put(x, 6, '┈', colBorder(), bgTok0())
	}

	// 底部固定区（时间轴/CHUNKS/分片图/图例/虚线/元数据）
	bottom := boxH - 1 // 元数据末行
	metaY := bottom - 2
	if metaY < 7 {
		metaY = 7 // 高度不足时紧贴进度区
	}
	dash2 := metaY - 1    // 元数据上方虚线
	legendY := dash2 - 1  // 图例
	chunkY := legendY - 2 // 分片图 2 行起点
	chunksTitle := chunkY - 1
	dash1 := chunksTitle - 1
	timeAxis := dash1 - 1

	// 底部各段按需渲染（空间不足跳过面积图/分片图）
	// 时间轴需在顶部虚线（y=6）下方 ≥1 行，否则隐藏分片图区避免重叠
	hasChunks := chunkY >= 9 && timeAxis > 7
	if hasChunks {
		g.putStr(2, chunksTitle, LblChunks, colDim(), bgTok0())
		cs := t.chunks
		info := fmt.Sprintf(LblChunkTotal, cs.Total)
		g.putStr(iw+2-lipgloss.Width(info), chunksTitle, info, colMute(), bgTok0())
		chunkMap(g, 2, chunkY, iw, 2, cs.Completed, cs.InFlight, cs.Total)
		lgd := fmt.Sprintf("%s  %s  %s", fmt.Sprintf(LblChunkDone, cs.Completed), fmt.Sprintf(LblChunkFly, cs.InFlight), fmt.Sprintf(LblChunkWait, cs.Total-cs.Completed))
		g.putStr(2, legendY, lgd, colMute(), bgTok0())
		for x := 1; x < iw+3; x++ {
			g.put(x, dash1, '┈', colBorder(), bgTok0())
		}
	}

	// 面积图：THROUGHPUT 标题到时间轴之间（至少 2 行）
	areaTop := 7
	areaBottom := timeAxis - 1
	if areaBottom > areaTop {
		g.putStr(2, 7, LblThroughput, colDim(), bgTok0())
		peak, avg := m.peakAvg(t)
		if peak > 0 {
			pl := fmt.Sprintf(LblPeakAvg, humanBytes(int64(peak))+"/s", humanBytes(int64(avg))+"/s")
			g.putStr(iw+2-lipgloss.Width(pl), 7, pl, colMute(), bgTok0())
		}
		areaChart(g, 2, areaTop+1, iw, areaBottom-areaTop, m.taskSpark(t), colAccent(), colPanel())
	}
	if timeAxis > 7 {
		g.putStr(1, timeAxis, LblTime60s, colMute(), bgTok0())
		g.putStr(iw+2-lipgloss.Width(LblNow), timeAxis, LblNow, colMute(), bgTok0())
	}

	// 虚线 + 元数据
	if dash2 >= 0 {
		for x := 1; x < iw+3; x++ {
			g.put(x, dash2, '┈', colBorder(), bgTok0())
		}
	}
	meta := [][2]string{
		{LblURL, truncW(t.URL, iw-12)},
		{LblSaveTo, truncW(t.Output, iw-12)},
		{LblConns, fmt.Sprintf("%d / %d", t.chunks.Total, connMax(t))},
	}
	for i, kv := range meta {
		yy := metaY + i
		if yy >= boxH-1 {
			break
		}
		g.putStr(2, yy, padLabel(kv[0], 8)+kv[1], colDim(), bgTok0())
	}
	return g
}

// frac 任务完成比例（钳制 [0,1]，未知大小返回 0）。
func frac(t *Task) float64 {
	if t.Size <= 0 {
		return 0
	}
	f := float64(t.Done) / float64(t.Size)
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// connN 当前连接数（徽章/元数据用）。
func connN(t *Task) int {
	return connMax(t)
}

func connMax(t *Task) int {
	if t.chunks.Total > 0 {
		return t.chunks.Total
	}
	return 8
}

// padLabel 用显示宽度补齐标签（CJK 按 2 列计）。
func padLabel(s string, w int) string {
	pad := w - lipgloss.Width(s)
	if pad <= 0 {
		return s
	}
	return s + strings.Repeat(" ", pad)
}

// peakAvg 速度峰值/均值。
func (m Model) peakAvg(t *Task) (float64, float64) {
	vals := m.taskSpark(t)
	if len(vals) == 0 {
		return 0, 0
	}
	peak, sum := vals[0], 0.0
	for _, v := range vals {
		if v > peak {
			peak = v
		}
		sum += v
	}
	return peak, sum / float64(len(vals))
}

// ---- 布局 C · 仪表盘（§5.3） ----

// cardSpec 统计卡定义。
type cardSpec struct {
	x, w  int
	title string
}

// renderLayoutC 仪表盘：4 卡 + 120s 吞吐图 + 表格 + 事件日志。
func (m Model) renderLayoutC(w, h int) []string {
	cards := []cardSpec{
		{0, 28, CardTotalSpeed},
		{30, 28, CardTasks},
		{60, 28, CardDownloaded},
		{90, 30, CardQueueEta},
	}
	var rows []string
	// y=2..7 卡片区（6 行）
	cardLines := make([][]string, 4)
	active, queued, _, _, totalSpeed, doneB, sizeB := m.summary()
	for ci, c := range cards {
		cardLines[ci] = m.renderCard(c, active, queued, totalSpeed, doneB, sizeB)
	}
	for y := 0; y < 6; y++ {
		var b strings.Builder
		for ci := range cards {
			seg := cardLines[ci][y]
			if ci > 0 {
				// 卡片间留 2 列
				b.WriteString("  ")
			}
			b.WriteString(seg)
		}
		rows = append(rows, fillRow(b.String(), w, colBg()))
	}
	// y=8.. 吞吐图（高度随剩余空间自适应，§8.4）
	avail := h - 4 - 6 /*cards*/ - 1 /*blank*/ - LayoutCTableMax
	tpRows := avail
	if tpRows > LayoutCTPRows {
		tpRows = LayoutCTPRows
	}
	if tpRows < LayoutCTPRowsMin {
		tpRows = LayoutCTPRowsMin
	}
	rows = append(rows, m.renderThroughput(w, tpRows)...)
	// 表格行数自适应：表头+最多 8 数据行，剩余空间留给事件日志
	rows = append(rows, emptyLine(w, colBg()))
	tableBudget := h - 4 - len(rows)
	if tableBudget > LayoutCTableMax {
		tableBudget = LayoutCTableMax
	}
	rows = append(rows, m.renderTableC(w, tableBudget)...)
	// 事件日志（剩余空间 ≥2 行时渲染最近事件，§5.3 y=30..32）
	remain := h - 4 - len(rows)
	if remain >= 2 && len(m.events) > 0 {
		n := remain
		if n > 3 {
			n = 3
		}
		rows = append(rows, m.renderEvents(w, n)...)
	}
	for len(rows) < h-4 {
		rows = append(rows, emptyLine(w, colBg()))
	}
	return rows
}

// renderEvents 事件日志（§5.3：时间戳 / 图标 / 文件名 / 消息）。n 为最多显示条数。
func (m Model) renderEvents(w, n int) []string {
	ev := m.events
	if len(ev) > n {
		ev = ev[len(ev)-n:]
	}
	var out []string
	for _, e := range ev {
		ts := e.at.Format("15:04:05")
		line := st(colMute()).Render(ts)
		line += " " + st(e.color).Render(e.icon)
		line += " " + truncW(sanitizeGlyphs(e.name), 34)
		msg := truncW(sanitizeGlyphs(e.msg), w-49)
		line += " " + st(colDim()).Render(msg)
		out = append(out, fillRow(line, w, colBg()))
	}
	return out
}

// renderCard 单张统计卡（6 行：边框+标题 / 大数字 / 副内容）。
func (m Model) renderCard(c cardSpec, active, queued int, totalSpeed float64, doneB, sizeB int64) []string {
	bg := colPanel()
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colBorder()).
		Background(bg).
		Width(c.w - 2).Height(4)
	title := stBold(colDim()).Render(c.title)
	big := stBold(colTxt()).Render("—")
	var sub string
	switch c.title {
	case CardTotalSpeed:
		big = stBold(colAccent()).Render(humanBytes(int64(totalSpeed)) + "/s")
		if m.globalSpeed != nil {
			sp := string(sparkline(m.globalSpeed.vals(), c.w-6))
			sub = st(colMute()).Render(sp)
		}
	case CardTasks:
		big = stBold(colTxt()).Render(fmt.Sprintf("%d / %d", active, len(m.tasks)))
		var icons []string
		for _, t := range m.tasks {
			icons = append(icons, st(stateColor(t.State)).Render(stateIcon(t.State)))
		}
		sub = strings.Join(icons, " ")
	case CardDownloaded:
		big = stBold(colGreen()).Render(humanBytes(doneB))
		if sizeB > 0 {
			sub = st(colMute()).Render(fmt.Sprintf(CardPctOf, int(float64(doneB)/float64(sizeB)*100), humanBytes(sizeB)))
		}
	case CardQueueEta:
		big = stBold(colCyan()).Render(fmt.Sprintf(HdrQueued, queued))
		sub = st(colMute()).Render(fmt.Sprintf(CardRemaining, queued))
	}
	inner := title + "\n" + big + "\n" + sub
	return strings.Split(box.Render(inner), "\n")
}

// renderThroughput 120s 吞吐图（含限速参考线，§5.3）。rows 为图高度（≥4，自适应 §8.4）。
func (m Model) renderThroughput(w, rows int) []string {
	if rows < LayoutCTPRowsMin {
		rows = LayoutCTPRowsMin
	}
	g := newCellGrid(w, rows)
	// 外框
	g.border(0, 0, w, rows, borderRound, colBorder())
	// 峰值标注
	vals := m.globalSpeed.vals()
	peak, _ := m.peakAgg(vals)
	if peak > 0 {
		lbl := fmt.Sprintf(LblPeak, humanBytes(int64(peak))+"/s")
		g.putStr(w-2-lipgloss.Width(lbl), 0, lbl, colMute(), bgTok0())
	}
	// Y 轴刻度（高度不足时减少刻度）
	labels := []struct {
		y    int
		text string
	}{{2, "60"}, {4, "40"}, {6, "20"}, {8, " 0"}}
	chartH := rows - 4 // 面积图行数（去掉边框+刻度+时间轴）
	if chartH < 1 {
		chartH = 1
	}
	for _, l := range labels {
		if l.y >= rows-2 {
			continue
		}
		g.putStr(1, l.y, l.text, colMute(), bgTok0())
	}
	// 面积图
	chartX, chartY, chartW := 5, 2, w-6
	areaChart(g, chartX, chartY, chartW, chartH, vals, colAccent(), colPanel())
	// 限速参考线（§5.3 [MUST 图内]）
	if m.baseOpt.Limit > 0 {
		g.putStr(chartX, chartY, strings.Repeat("┈", chartW), colRed(), bgTok0())
		capLbl := fmt.Sprintf(LblCap, humanBytes(m.baseOpt.Limit))
		g.putStr(w-2-lipgloss.Width(capLbl), chartY, capLbl, colRed(), bgTok0())
	}
	// 时间轴
	axisY := rows - 2
	if axisY > 2 {
		g.putStr(1, axisY, LblTime2m, colMute(), bgTok0())
		g.putStr(chartX, axisY, "┬─┴", colMute(), bgTok0())
		g.putStr(w-2-lipgloss.Width(LblNow), axisY, LblNow, colMute(), bgTok0())
	}
	return rowsFromGrid(g)
}

// peakAgg 全局速度峰值。
func (m Model) peakAgg(vals []float64) (float64, float64) {
	if len(vals) == 0 {
		return 0, 0
	}
	peak, sum := vals[0], 0.0
	for _, v := range vals {
		if v > peak {
			peak = v
		}
		sum += v
	}
	return peak, sum / float64(len(vals))
}

// rowsFromGrid 网格 → 行切片。
func rowsFromGrid(g *cellGrid) []string {
	out := make([]string, g.h)
	for y := 0; y < g.h; y++ {
		out[y] = g.row(y)
	}
	return out
}

// renderTableC 任务表格（§5.3 列坐标；0-based）。budget 为可用总行数（含表头，自适应 §8.4）。
func (m Model) renderTableC(w, budget int) []string {
	if budget < 3 {
		budget = 3
	}
	// 0-based 列起点（spec 1-based 减 1；size 让出空间给 12 列字节串）
	col := map[string]int{"ico": 0, "name": 2, "bar": 43, "pct": 75, "spd": 87, "eta": 96, "size": 104, "conn": 116}
	barW := LayoutCBarW // 表格进度条宽（§5.3 BW=26）
	var rows []string
	// 表头（中文）
	line := " · " + ColFile
	line = padTo(line, ColProgress, col["bar"]) + ColProgress
	line = padTo(line, ColDone, col["pct"]) + ColDone
	line = padTo(line, ColSpeed, col["spd"]) + ColSpeed
	line = padTo(line, ColEta, col["eta"]) + ColEta
	line = padTo(line, ColSize, col["size"]) + ColSize
	line = padTo(line, ColConn, col["conn"]) + ColConn
	rows = append(rows, fillRow(stBg(colPanel3()).Render(line), w, colPanel3()))

	maxData := budget - 1
	vis := m.visibleOrder()
	for ri, i := range vis {
		if ri >= maxData {
			break
		}
		t := m.tasks[i]
		sel := i == m.cursor
		bg := colBg()
		if ri%2 == 1 {
			bg = colPanel()
		}
		if sel {
			bg = colPanel2()
		}
		// 选中热区：屏 y = 21 + ri（§5.3）
		m.zoneRow(21+ri, i)
		// 行首：指示条（0 列）+ 空格 + 图标（2 列）
		line := " "
		if sel {
			line = st(colFocus()).Render("█")
		}
		line += " " + st(stateColor(t.State)).Render(stateIcon(t.State))
		// 文件名（name 列起，宽 40；与表头 FILE 对齐）
		name := truncW(sanitizeGlyphs(t.Output), col["bar"]-col["name"]-1)
		line = padTo(line, name, col["name"]+1) + name
		// 进度条
		bar := st(colMute()).Render(strings.Repeat("░", barW))
		pct := "--"
		if t.Size > 0 {
			frac := float64(t.Done) / float64(t.Size)
			bar = st(barColorOf(t)).Render(string(progressBar(frac, barW, '█', '░')))
			pct = fmt.Sprintf("%3.0f%%", frac*100)
		}
		line = padTo(line, bar, col["bar"]) + bar
		line = padTo(line, pct, col["pct"]) + pct
		// 速度
		spd := ""
		if t.State == StateRunning && t.Speed > 0 {
			spd = humanBytes(int64(t.Speed)) + "/s"
		} else if t.State == StateFailed {
			spd = "✕"
		} else if t.State == StatePaused {
			spd = "‖"
		}
		line = padTo(line, spd, col["spd"]) + stBold(colCyan()).Render(spd)
		// ETA
		eta := ""
		switch t.State {
		case StateRunning:
			if t.ETA > 0 {
				eta = formatETA(t.ETA)
			}
		case StateFailed:
			eta = StFailed
		case StatePaused:
			eta = StPaused
		}
		line = padTo(line, eta, col["eta"]) + st(colMute()).Render(eta)
		// SIZE
		size := ""
		if t.Size > 0 {
			size = fmt.Sprintf("%s/%s", humanBytes(t.Done), humanBytes(t.Size))
		}
		line = padTo(line, size, col["size"]) + st(colDim()).Render(size)
		// CONN
		conn := ""
		if t.chunks.Total > 0 {
			conn = fmt.Sprintf("%d/%d", t.chunks.InFlight, t.chunks.Total)
		}
		line = padTo(line, conn, col["conn"]) + st(colMute()).Render(conn)
		rows = append(rows, fillRow(line, w, bg))
	}
	for len(rows) < budget {
		rows = append(rows, emptyLine(w, colBg()))
	}
	return rows
}

// padTo 右对齐填充到目标列（基于显示宽度）。
func padTo(prefix, seg string, target int) string {
	pad := target - lipgloss.Width(prefix)
	if pad < 0 {
		pad = 0
	}
	return prefix + strings.Repeat(" ", pad)
}
