// 渲染辅助组件（方案 §4.5）：键帽 / 徽章 / 点线引导 / 汇总行 / header / footer。
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// keycap 键帽：反色 [ a ] + 灰色说明（§4.5）。选中键高亮。
func keycap(key, label string, focus bool) string {
	k := stFgBg(colTxt(), colPanel3()).Render(" " + key + " ")
	if focus {
		k = stFgBg(bgTok0(), colAccent()).Render(" " + key + " ")
	}
	return k + " " + st(colDim()).Render(label)
}

// badge 徽章：反色 状态块（§4.5），如 " ▼ DOWNLOADING "。
func badge(icon, word string, color lipgloss.Color) string {
	content := " " + icon + " " + word + " "
	return stFgBg(color, colPanel3()).Render(content)
}

// dotLine 点线引导：左右内容之间用 · 填充到指定宽（§4.5）。
func dotLine(left, right string, width int, leftStyle, rightStyle lipgloss.Style) string {
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	pad := width - lw - rw
	if pad < 1 {
		pad = 1
	}
	return leftStyle.Render(left) + st(colMute()).Render(strings.Repeat("·", pad)) + rightStyle.Render(right)
}

// summary 全局汇总（方案 B/C 顶栏/详情）：活动数/总速/总进度/排队/做种/校验。
func (m Model) summary() (active, queued, seeding, hashing int, totalSpeed float64, doneB, sizeB int64) {
	for _, t := range m.tasks {
		switch t.State {
		case StateRunning:
			active++
			totalSpeed += t.Speed
		case StateQueued:
			queued++
		}
		if t.Size > 0 {
			doneB += t.Done
			sizeB += t.Size
		}
	}
	return
}

// headerRows 新式 Header（§5.0 三布局共用，y=0..1）。
func (m Model) headerRows(w int, layout LayoutID) []string {
	logo := stFgBg(colTxt(), colAccent()).Render(" ⇣ ")
	name := stBold(colTxt()).Render(AppTitle + " " + AppVersion)
	left := logo + " " + name
	// 状态/错误提示（header 行内）
	if m.status != "" || m.errMsg != "" {
		msg := m.status
		if m.errMsg != "" {
			msg = m.errMsg
		}
		left += "  " + st(colDim()).Render(truncW(msg, 30))
	}

	active, queued, _, _, totalSpeed, _, _ := m.summary()
	var segs []string
	segs = append(segs, fmt.Sprintf(HdrActive, active), fmt.Sprintf(HdrSpeed, humanBytes(int64(totalSpeed))))
	if queued > 0 {
		segs = append(segs, fmt.Sprintf(HdrQueued, queued))
	}
	if len(m.tasks) > 0 {
		doneB, sizeB := int64(0), int64(0)
		for _, t := range m.tasks {
			if t.Size > 0 {
				doneB += t.Done
				sizeB += t.Size
			}
		}
		pct := 0
		if sizeB > 0 {
			pct = int(float64(doneB) / float64(sizeB) * 100)
		}
		segs = append(segs, fmt.Sprintf(HdrPct, pct))
	}
	segs = append(segs, fmt.Sprintf(HdrLayout, layout.String()))

	// 右侧段从右往左排
	right := ""
	for i := len(segs) - 1; i >= 0; i-- {
		seg := st(colCyan()).Render(segs[i])
		if right == "" {
			right = seg
		} else {
			right = seg + "   " + right
		}
	}

	row0 := left + " " + right
	// 超宽裁剪（窄窗）：右段超宽时截断，保证不越界
	if lipgloss.Width(row0) > w {
		rightW := w - lipgloss.Width(left) - 1
		if rightW > 8 {
			row0 = left + " " + truncW(right, rightW)
		} else {
			row0 = truncW(left, w)
		}
	}
	// 铺满宽度：右对齐段，左侧 logo 起
	row0 = padRight(row0, w, colBg())
	row1 := st(colBorder()).Render(strings.Repeat("─", w))
	return []string{row0, row1}
}

// padRight 用空格补齐到宽 w，并铺 PANEL 背景（header 整行）。
func padRight(s string, w int, bg lipgloss.Color) string {
	pad := w - lipgloss.Width(s)
	if pad < 0 {
		pad = 0
	}
	return stBg(bg).Render(s + strings.Repeat(" ", pad))
}

// footerKeycaps 上下文相关键帽（§7.1 [MUST] 单行 7–9 个）。
// 根据选中任务状态与 overlay 态动态给出主操作键（中文说明）。
func (m Model) footerKeycaps() []keycapDef {
	sel := m.cursor >= 0 && m.cursor < len(m.tasks)
	var caps []keycapDef
	caps = append(caps, keycapDef{"a", CapAdd})
	if sel {
		switch m.tasks[m.cursor].State {
		case StateRunning:
			caps = append(caps, keycapDef{"p", CapPause})
		case StatePaused, StateFailed, StateQueued:
			caps = append(caps, keycapDef{"p", CapResume})
		case StateDone:
			caps = append(caps, keycapDef{"o", CapOpen})
		}
		caps = append(caps, keycapDef{"d", CapDelete})
	}
	if sel {
		caps = append(caps, keycapDef{"s", CapSettings})
	}
	if m.filter != "" {
		caps = append(caps, keycapDef{"/", CapFilterOn})
	} else {
		caps = append(caps, keycapDef{"/", CapFilter})
	}
	caps = append(caps, keycapDef{"?", CapHelp})
	caps = append(caps, keycapDef{"q", CapQuit})
	return caps
}

type keycapDef struct {
	key   string
	label string
}

// footerRows 新式 Footer（§5.0）：末两行 = 分隔线 + 单行键帽。
func (m Model) footerRows(w int) []string {
	sep := st(colBorder()).Render(strings.Repeat("─", w))
	var b strings.Builder
	for i, c := range m.footerKeycaps() {
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(keycap(c.key, c.label, false))
	}
	// 右侧补布局提示
	layoutHint := st(colMute()).Render(fmt.Sprintf(CapTabHint, m.layout.String()))
	row := dotLine(b.String(), layoutHint, w, st(colTxt()), st(colMute()))
	// 超宽裁剪（窄窗）：键帽行截断，保证不越界
	if lipgloss.Width(row) > w {
		row = truncW(b.String(), w-8) + "  " + truncW("[Tab]", 5)
	}
	return []string{sep, row}
}

// emptyLine 空行（铺背景防残留）。
func emptyLine(w int, bg lipgloss.Color) string {
	return stBg(bg).Render(strings.Repeat(" ", w))
}
