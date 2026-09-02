// Overlay 体系（方案 §7.2）：帮助 / 添加 / 代理 / 限速 / 确认删除 / 过滤。
//
// 统一 double 边框（╔═╗║╚╝），居中；Esc 逐层关闭。
// 简化实现：overlay 打开时全屏渲染（不拼接 ANSI 定位，避免转义干扰）。
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// box 用 double 边框包裹标题+内容行，居中对齐。
func box(title string, body []string, w int) []string {
	contentW := 0
	for _, l := range body {
		if lipgloss.Width(l) > contentW {
			contentW = lipgloss.Width(l)
		}
	}
	innerW := contentW + 2 // 左右 padding 各 1
	bw := innerW + 2       // 边框占 2
	if lipgloss.Width(title) > innerW {
		innerW = lipgloss.Width(title)
		bw = innerW + 2
	}
	// 顶边：╔═ [title] ═╗
	pad := innerW - lipgloss.Width(title)
	top := "╔═ " + stBold(colTxt()).Render(title) + strings.Repeat("═", pad+1) + "╗"
	mid := make([]string, 0, len(body)+2)
	mid = append(mid, top)
	for _, l := range body {
		padL := innerW - lipgloss.Width(l)
		mid = append(mid, "║ "+l+strings.Repeat(" ", padL)+" ║")
	}
	mid = append(mid, "╚"+strings.Repeat("═", bw-2)+"╝")
	return mid
}

// renderHelpOverlay 帮助（§7.2：60×20，键位分组罗列）。
func (m Model) renderHelpOverlay(base string, w, h int) string {
	body := []string{
		"  ── 导航 ────────────────",
		"  ↑/↓ k/j  选择    PgUp/PgDn  翻页    Home/End  首末",
		"  ── 任务 ────────────────",
		"  a  添加任务      p  暂停/继续     r  重试(失败)",
		"  d  删除(二次确认)  Space  多选标记   o/O  打开/目录",
		"  c  复制 URL      i  详情面板(布局A)",
		"  ── 全局 ────────────────",
		"  s  设置    x  代理    l  限速    /  过滤",
		"  Tab/1/2/3  布局 A/B/C    ?  帮助",
		"  q / Ctrl+C  退出",
	}
	lines := box(OvHelp, body, w)
	return centerOverlay(lines, w, h)
}

// renderConfirmOverlay 删除二次确认（§7.2：46×6，y/N）。
func (m Model) renderConfirmOverlay(base string, w, h int) string {
	name := "（未知）"
	idx := m.confirming.rowIdx
	if idx >= 0 && idx < len(m.tasks) {
		name = truncW(sanitizeGlyphs(m.tasks[idx].Output), 30)
	}
	body := []string{
		"  删除任务: " + stBold(colRed()).Render(name),
		"  " + st(colDim()).Render("此操作会清理断点续传状态，不可撤销"),
		"  " + stBold(colYellow()).Render("[ y ] 确认删除     [ n / Esc ] 取消"),
	}
	lines := box(OvDeleteConf, body, w)
	return centerOverlay(lines, w, h)
}

// renderLimitOverlay 限速输入（§7.2：40×6）。
func (m Model) renderLimitOverlay(base string, w, h int) string {
	body := []string{
		"  当前: " + st(colCyan()).Render(speedLabel(m.baseOpt.Limit)),
		"  " + st(colDim()).Render("如 5M / 1024k / 1048576（0=不限）"),
		"  > " + m.limitInput + "▏",
	}
	lines := box(OvSpeedLimit, body, w)
	return centerOverlay(lines, w, h)
}

// renderAddOverlay 添加任务（§7.2：70×12，URL 输入 + 保存路径 + 连接数）。
func (m Model) renderAddOverlay(base string, w, h int) string {
	body := []string{
		"  URL> " + m.input.View(),
		"  " + st(colDim()).Render("Enter 添加 · Esc 取消 · Ctrl+V 粘贴"),
	}
	lines := box(OvAddTask, body, w)
	return centerOverlay(lines, w, h)
}

// renderProxyOverlay 代理配置（§7.2：56×10）。
func (m Model) renderProxyOverlay(base string, w, h int) string {
	body := []string{
		"  当前: " + st(colCyan()).Render(proxyDisplay(m.baseOpt.Proxy)),
		"  > " + m.input.View(),
		"  " + st(colDim()).Render("http://host:port 或 socks5://host:port（空=直连）"),
		"  " + st(colDim()).Render("设置即视为允许公网出站"),
	}
	lines := box(OvProxy, body, w)
	return centerOverlay(lines, w, h)
}

// proxyDisplay 代理显示（空→直连）。
func proxyDisplay(p string) string {
	if p == "" {
		return "直连"
	}
	return p
}

// renderFilterBar 过滤输入（§7.2：底部单行）。
func (m Model) renderFilterBar(base string, w, h int) string {
	lines := strings.Split(base, "\n")
	bar := " / " + m.input.View()
	barLine := fillRow(bar, w, colPanel3())
	lines[h-3] = barLine
	return strings.Join(lines, "\n")
}

// centerOverlay 把 overlay 行居中叠加到全屏基底之上（§7.2 z 序）。
func centerOverlay(ov []string, w, h int) string {
	ovH := len(ov)
	top := (h - ovH) / 2
	if top < 0 {
		top = 0
	}
	// 空屏底色（保持终端干净）
	lines := make([]string, h)
	for i := range lines {
		lines[i] = stBg(colBg()).Render(strings.Repeat(" ", w))
	}
	for i, l := range ov {
		pad := (w - lipgloss.Width(l)) / 2
		if pad < 0 {
			pad = 0
		}
		lines[top+i] = strings.Repeat(" ", pad) + l
	}
	return strings.Join(lines, "\n")
}

// 让 fmt 在 overlay 内不误用（保留）。
var _ = fmt.Sprintf
