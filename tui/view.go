package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// 注意：本文件不再定义硬编码色值的包级 style（§11 "所有颜色走 token"）。
// 需要着色的地方一律经 tokens.go 的 col*()（渲染时求值，受终端颜色能力影响）
// 或 st/stBold/stFgBg 等助手现场构造。

// protoTag 协议短标签（未知协议不渲染标签列，宽度顺延）。
var protoTag = map[string]string{"http": "HTTP", "bt": "BT", "magnet": "磁力"}

// ---- 鼠标热区 ----

// clickZone 可点击区：任务行（选中）。坐标基于终端显示列/行（0-based，
// ANSI 转义不占列，宽字符按 2 列——统一用 lipgloss.Width 度量）。
type clickZone struct {
	action string // "select"
	rowIdx int    // 目标任务索引
	y      int    // 终端行（View 首行为 0）
	start  int    // 显示列起点（含）
	end    int    // 显示列终点（不含）
}

type frame struct {
	rows []clickZone // 任务行整行（选中）
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

// zoneRow 注册一行任务选择热区（布局渲染时调用）。
func (m Model) zoneRow(y, rowIdx int) {
	end := m.width
	if end <= 0 {
		end = 10000 // 未知宽度（测试）：充分宽，命中不设限
	}
	lastFrame.rows = append(lastFrame.rows, clickZone{
		action: "select", rowIdx: rowIdx, y: y, start: 0, end: end,
	})
}

// View 渲染整个界面（纯字符串，可测试断言）。
// 分派：settings → 布局（header+main+footer）→ overlay（§7.2 z 序）。
func (m Model) View() string {
	w, h := m.width, m.height
	if w <= 0 {
		w = 100
	}
	if h <= 0 {
		h = 25
	}
	lastFrame = frame{}

	// 设置面板（全屏）
	if m.settings {
		return m.renderSettings()
	}

	layout := m.layout
	if m.layoutAuto {
		if m.width <= 0 {
			// 未获得终端尺寸（测试/无头）：按方案 A 规格渲染
			layout = LayoutA
		} else {
			layout = autoLayout(w)
		}
	}
	// 布局最小尺寸约束（§8.4 缩放稳健）：窗口太小则降级到更紧凑的布局，
	// 保证输出绝不超出实际终端宽（不放大，避免换行错乱）。降级链 C→B→A。
	if layout == LayoutC && (w < LayoutCMinW || h < LayoutCMinH) {
		layout = LayoutB
	}
	if layout == LayoutB && w < LayoutBMinW {
		layout = LayoutA
	}

	header := m.headerRows(w, layout)
	var main []string
	switch layout {
	case LayoutA:
		main = m.renderLayoutA(w, h)
	case LayoutB:
		main = m.renderLayoutB(w, h)
	default:
		main = m.renderLayoutC(w, h)
	}
	footer := m.footerRows(w)

	lines := make([]string, 0, h)
	lines = append(lines, header...)
	lines = append(lines, main...)
	lines = append(lines, footer...)
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, emptyLine(w, colBg()))
	}
	base := strings.Join(lines, "\n")

	// Overlay（§7.2 z 序：overlay > footer > main > header）
	if m.helpOpen {
		return m.renderHelpOverlay(base, w, h)
	}
	if m.confirming != nil {
		return m.renderConfirmOverlay(base, w, h)
	}
	if m.limiting {
		return m.renderLimitOverlay(base, w, h)
	}
	if m.adding {
		return m.renderAddOverlay(base, w, h)
	}
	if m.proxying {
		return m.renderProxyOverlay(base, w, h)
	}
	if m.filtering {
		return m.renderFilterBar(base, w, h)
	}
	return base
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
// 下载中 > 错误 > 暂停 > 排队 > 完成（§6.2 排序优先级）；同状态保持原顺序（稳定排序）。
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

// stateRank 排序优先级（小=靠前；§6.2：downloading > error > paused > queued > completed）。
func stateRank(s TaskState) int {
	switch s {
	case StateRunning:
		return 0
	case StateFailed:
		return 1
	case StatePaused:
		return 2
	case StateQueued:
		return 3
	default:
		return 4
	}
}

// cleanErr 错误详情单行化（去换行/压缩空白），供展开行渲染。
func cleanErr(err error) string {
	return strings.Join(strings.Fields(err.Error()), " ")
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
	rows = append(rows, stBold(colTxt()).Render("设置  ")+
		st(colDim()).Render("Enter/空格:切换档位  ↑/↓:选择  q/Esc:关闭"))
	if m.errMsg != "" {
		rows = append(rows, st(colRed()).Render("提示: "+m.errMsg))
	}
	if m.settingCustom {
		rows = append(rows, st(colDim()).Render("自定义值> ")+m.input.View())
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
			prefix = stBold(colFocus()).Render("> ")
		}
		var b strings.Builder
		b.WriteString(prefix + fmt.Sprintf("%-4s", r.name) + " ")
		highlight := r.cur
		if highlight < 0 { // 自定义值：高亮"自定义…"档
			highlight = len(r.labels) - 1
		}
		for j, lbl := range r.labels {
			if j == highlight {
				b.WriteString("[" + stBold(colFocus()).Render(lbl) + "]")
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
			b.WriteString(" " + st(colDim()).Render(extra[i]))
		}
		rows = append(rows, b.String())
	}
	return panelStyle().Render(strings.Join(rows, "\n"))
}

// panelStyle 圆角边框面板（设置面板/详情等通用，§4.5 [SHOULD] 走 token）。
func panelStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colBorder()).
		Background(colPanel()).
		Padding(0, 1)
}
