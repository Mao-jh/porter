// 字符网格渲染引擎（方案 §4 核心原语 + §8 宽字符/差分渲染）。
//
// cell 是带前景/背景色的单字符；宽字符（CJK）占 2 列，第二列存 width=2 标记。
// 布局层把所有内容画进 cellGrid，最后铺成字符串；差分渲染由调用方
// （Bubble Tea 自动整屏重绘 + 本网格只承载坐标精确的图形区）承担。
package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// cell 网格单元：r=字符，w=显示宽度（1/2），fg/bg 前景/背景色。
type cell struct {
	r    rune
	w    int
	fg   lipgloss.Color
	bg   lipgloss.Color
	hasF bool // 是否显式设置前景色
	hasB bool // 是否显式设置背景色
}

// cellGrid 宽×高的字符网格（坐标 x=0..w-1, y=0..h-1）。
type cellGrid struct {
	w, h int
	c    []cell
}

// newCellGrid 新建网格；所有单元初始化为空格（无颜色，背景未定义）。
func newCellGrid(w, h int) *cellGrid {
	g := &cellGrid{w: w, h: h, c: make([]cell, w*h)}
	for i := range g.c {
		g.c[i] = cell{r: ' ', w: 1}
	}
	return g
}

// in 判断坐标是否越界。
func (g *cellGrid) in(x, y int) bool { return x >= 0 && x < g.w && y >= 0 && y < g.h }

// put 写入单字符（宽字符在第二列自动填占位）。
// 若末字符为宽字符，其后一列写占位（r=0, w=2），保证行拼接宽度正确。
func (g *cellGrid) put(x, y int, ch rune, fg, bg lipgloss.Color) {
	if !g.in(x, y) {
		return
	}
	w := runewidth.RuneWidth(ch)
	if w < 1 {
		w = 1
	}
	idx := y*g.w + x
	g.c[idx] = cell{r: ch, w: w, fg: fg, bg: bg, hasF: true, hasB: true}
	if w == 2 && g.in(x+1, y) {
		g.c[y*g.w+x+1] = cell{r: 0, w: 2, fg: fg, bg: bg, hasF: true, hasB: true}
	}
}

// putStr 写入字符串（字符逐个 put，含宽字符处理）。
func (g *cellGrid) putStr(x, y int, s string, fg, bg lipgloss.Color) {
	px := x
	for _, r := range s {
		g.put(px, y, r, fg, bg)
		px += runewidth.RuneWidth(r)
	}
}

// fill 以单字符+背景填充矩形区（显式背景色，§8.1 [MUST]）。
func (g *cellGrid) fill(x, y, w, h int, ch rune, fg, bg lipgloss.Color) {
	for yy := y; yy < y+h; yy++ {
		for xx := x; xx < x+w; xx++ {
			g.put(xx, yy, ch, fg, bg)
		}
	}
}

// fillBg 仅铺背景色（不写字符，空格+背景）。
func (g *cellGrid) fillBg(x, y, w, h int, bg lipgloss.Color) {
	for yy := y; yy < y+h; yy++ {
		for xx := x; xx < x+w; xx++ {
			if !g.in(xx, yy) {
				continue
			}
			idx := yy*g.w + xx
			g.c[idx].bg = bg
			g.c[idx].hasB = true
		}
	}
}

// border 画矩形边框（§3.3 边框样式字符集）。
func (g *cellGrid) border(x, y, w, h int, style borderStyle, fg lipgloss.Color) {
	if w < 2 || h < 2 {
		return
	}
	b := borderChars(style)
	g.put(x, y, b.tl, fg, bgTok0())
	for i := 1; i < w-1; i++ {
		g.put(x+i, y, b.h, fg, bgTok0())
		g.put(x+i, y+h-1, b.h, fg, bgTok0())
	}
	g.put(x+w-1, y, b.tr, fg, bgTok0())
	g.put(x, y+h-1, b.bl, fg, bgTok0())
	g.put(x+w-1, y+h-1, b.br, fg, bgTok0())
	for i := 1; i < h-1; i++ {
		g.put(x, y+i, b.v, fg, bgTok0())
		g.put(x+w-1, y+i, b.v, fg, bgTok0())
	}
}

// borderStyle 边框样式枚举（§3.3）。
type borderStyle int

const (
	borderRound  borderStyle = iota // ╭─╮│╰╯ 面板/卡片（默认）
	borderLight                     // ┌─┐│└┘ 内部分隔线
	borderHeavy                     // ┏━┓┃┗┛ 聚焦面板
	borderDouble                    // ╔═╗║╚╝ 模态对话框
)

type borderCharsT struct{ tl, tr, bl, br, h, v rune }

func borderChars(s borderStyle) borderCharsT {
	switch s {
	case borderLight:
		return borderCharsT{'┌', '┐', '└', '┘', '─', '│'}
	case borderHeavy:
		return borderCharsT{'┏', '┓', '┗', '┛', '━', '┃'}
	case borderDouble:
		return borderCharsT{'╔', '╗', '╚', '╝', '═', '║'}
	default:
		return borderCharsT{'╭', '╮', '╰', '╯', '─', '│'}
	}
}

// bgTok0 边框/占位背景——网格坐标系下统一用终端默认（透明）。
func bgTok0() lipgloss.Color { return lipgloss.Color("") }

// row 输出指定行的 ANSI 字符串（布局拼接用）。
func (g *cellGrid) row(y int) string {
	if y < 0 || y >= g.h {
		return ""
	}
	var b strings.Builder
	start := y * g.w
	for x := 0; x < g.w; {
		cl := g.c[start+x]
		if cl.r == 0 { // 宽字符第二列占位
			x++
			continue
		}
		end := x + 1
		for end < g.w {
			nc := g.c[start+end]
			if nc.r == 0 || nc.r != cl.r { // 字符不同不可合并（边框/内容混排）
				break
			}
			if nc.fg != cl.fg || nc.bg != cl.bg || nc.hasF != cl.hasF || nc.hasB != cl.hasB {
				break
			}
			end++
		}
		if cl.hasF || cl.hasB {
			b.WriteString(colorize(strings.Repeat(string(cl.r), end-x), cl.fg, cl.bg, cl.hasF, cl.hasB))
		} else {
			b.WriteString(strings.Repeat(string(cl.r), end-x))
		}
		x = end
	}
	return b.String()
}

// String 网格 → 字符串（每行逐 cell 用 ANSI 着色拼接）。
// 相邻同色 cell 合并为一次转义（减少输出量，配合差分渲染）。
func (g *cellGrid) String() string {
	var b strings.Builder
	for y := 0; y < g.h; y++ {
		start := y * g.w
		for x := 0; x < g.w; {
			cl := g.c[start+x]
			if cl.r == 0 { // 宽字符第二列占位：无内容
				x++
				continue
			}
			// 收集一段相同字符且颜色相同的连续 cell
			end := x + 1
			for end < g.w {
				nc := g.c[start+end]
				if nc.r == 0 || nc.r != cl.r {
					break
				}
				if nc.fg != cl.fg || nc.bg != cl.bg || nc.hasF != cl.hasF || nc.hasB != cl.hasB {
					break
				}
				end++
			}
			if cl.hasF || cl.hasB {
				b.WriteString(colorize(strings.Repeat(string(cl.r), end-x), cl.fg, cl.bg, cl.hasF, cl.hasB))
			} else {
				b.WriteString(strings.Repeat(string(cl.r), end-x))
			}
			x = end
		}
		if y < g.h-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// colorize 单字符着色（可含背景）。空前景用默认。
func colorize(s string, fg, bg lipgloss.Color, hasF, hasB bool) string {
	style := lipgloss.NewStyle()
	if hasF {
		style = style.Foreground(fg)
	}
	if hasB && bg != "" {
		style = style.Background(bg)
	}
	return style.Render(s)
}

// ---- 渲染原语（§4） ----

// SUB_EIGHTH 亚字符 1/8 块（§4.1）。
var subEighth = []rune{'▏', '▎', '▍', '▌', '▋', '▊', '▉'}

// progressBar 高精度进度条：末位亚字符收尾（§4.1 [MUST 精度]）。
// 返回填充/空字符的 rune 切片，长度恒 == width。fill/empty 为字形。
func progressBar(pct float64, width int, fill, empty rune) []rune {
	if width <= 0 {
		return nil
	}
	n := clampf(pct, 0, 1) * float64(width)
	full := int(n)
	frac := int((n - float64(full)) * 8)
	out := make([]rune, 0, width)
	for i := 0; i < full && i < width; i++ {
		out = append(out, fill)
	}
	if full < width && frac > 0 {
		out = append(out, subEighth[frac-1])
	}
	for len(out) < width {
		out = append(out, empty)
	}
	return out
}

// spark 八级 sparkline 字符（§4.2）。
var spark = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// sparkline 行内趋势（§4.2）：min-max 归一化，hi==lo 防护（全 0 → 最低档）。
func sparkline(vals []float64, width int) []rune {
	if width <= 0 {
		return nil
	}
	if len(vals) == 0 {
		return []rune(strings.Repeat("▁", width))
	}
	rs := resample(vals, width)
	lo, hi := rs[0], rs[0]
	for _, v := range rs[1:] {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	rng := hi - lo
	out := make([]rune, len(rs))
	for i, v := range rs {
		idx := 0
		if rng > 0 {
			idx = int((v - lo) / rng * 7.999)
		}
		out[i] = spark[idx]
	}
	return out
}

// resample 降采样/补齐到目标宽度（分段平均，§4.2）。
// 长度不足补 0；超长按块平均。
func resample(vals []float64, width int) []float64 {
	if width <= 0 {
		return nil
	}
	if len(vals) == 0 {
		out := make([]float64, width)
		return out
	}
	if len(vals) == width {
		return vals
	}
	out := make([]float64, width)
	if len(vals) < width {
		copy(out, vals)
		return out
	}
	// 降采样：均匀取 width 个代表点
	for i := 0; i < width; i++ {
		idx := int(float64(i) * float64(len(vals)) / float64(width))
		if idx >= len(vals) {
			idx = len(vals) - 1
		}
		out[i] = vals[idx]
	}
	return out
}

// areaChart 半块面积图（§4.3 [MUST 显式背景色]）。
// grid 上画 (x,y) 起 w×rows 的区域，vals 填充，fill=填充色，bg=背景色（必须显式）。
func areaChart(g *cellGrid, x, y, w, rows int, vals []float64, fill, bg lipgloss.Color) {
	if w <= 0 || rows <= 0 {
		return
	}
	levels := rows * 2 // 一个字符 = 2 纵向像素
	rs := resample(vals, w)
	lo, hi := rs[0], rs[0]
	for _, v := range rs[1:] {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	rng := hi - lo
	// 先铺背景（防闪烁，§8.1）
	g.fillBg(x, y, w, rows, bg)
	for i := 0; i < w && i < len(rs); i++ {
		lvl := 0
		if rng > 0 {
			lvl = int(float64(rs[i]-lo) / rng * float64(levels))
		}
		for r := 0; r < rows; r++ {
			loLv := 2 * (rows - 1 - r) // 该 cell 下半格层级
			hiLv := loLv + 1           // 上半格层级
			fHi, fLo := lvl > hiLv, lvl > loLv
			switch {
			case fHi && fLo:
				g.put(x+i, y+r, '█', fill, bg)
			case fHi:
				g.put(x+i, y+r, '▀', fill, bg)
			case fLo:
				g.put(x+i, y+r, '▄', fill, bg)
			default:
				g.put(x+i, y+r, ' ', fill, bg)
			}
		}
	}
}

// chunkMap 分片图（§4.4）：▓已完成 / ▒在途 / ░未取 / 空格未分配。
func chunkMap(g *cellGrid, x, y, w, rows, done, active, total int) {
	for r := 0; r < rows; r++ {
		for i := 0; i < w; i++ {
			idx := r*w + i
			ch, fg := rune(' '), colBg()
			switch {
			case idx >= total:
				ch, fg = ' ', colBg()
			case idx < done:
				ch, fg = '▓', colGreen()
			case idx < done+active:
				ch, fg = '▒', colAccent()
			default:
				ch, fg = '░', colMute()
			}
			g.put(x+i, y+r, ch, fg, colBg())
		}
	}
}

// clampf 钳制到 [0,1]。
func clampf(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// truncW 按显示宽度截断字符串（§8.3 [MUST]），超长末尾补 …（占 1 列）。
func truncW(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	w := 0
	var b strings.Builder
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if w+rw > maxW {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	if w < runewidth.StringWidth(s) {
		// 截断了：若空间足够再补 …，否则去掉末字符
		if w+1 <= maxW {
			return b.String() + "…"
		}
		rs := []rune(b.String())
		if len(rs) > 0 {
			return string(rs[:len(rs)-1]) + "…"
		}
	}
	return b.String()
}

// fmtPct 百分比格式化（不带 %，3 位右对齐）。
func fmtPct(frac float64) string {
	return fmt.Sprintf("%3d", int(clampf(frac, 0, 1)*100+0.5))
}

// 让 utf8 包不被误删（保留宽字符校验用）。
var _ = utf8.RuneLen
