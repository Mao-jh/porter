// 设计语言层：配色 token / 语义色 / 终端颜色能力检测与降级（方案 §3.1、§9.1）。
//
// 规则：所有 UI 颜色一律经本文件取值，禁止硬编码色值。
// 语义色（ACCENT/GREEN/YELLOW/RED/PURPLE/CYAN）只用于语义，不得挪作装饰。
package tui

import (
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ColorLevel 终端颜色能力（检测一次，全 UI 生效）。
type ColorLevel int

const (
	Color16   ColorLevel = iota // 仅 16 色（老 conhost）
	Color256                    // 256 色
	ColorTrue                   // truecolor / 24bit
)

// colorLevel 全局颜色能力（init 时探测一次）。测试可覆盖。
var colorLevel ColorLevel

func init() { colorLevel = detectColorLevel() }

// detectColorLevel 按 §9.1 规则探测：COLORTERM 含 truecolor/24bit → True；
// TERM 含 256color → 256；否则 16。
func detectColorLevel() ColorLevel {
	ct := strings.ToLower(os.Getenv("COLORTERM"))
	switch {
	case strings.Contains(ct, "truecolor"), strings.Contains(ct, "24bit"):
		return ColorTrue
	}
	term := strings.ToLower(os.Getenv("TERM"))
	switch {
	case strings.Contains(term, "truecolor"), strings.Contains(term, "24bit"):
		return ColorTrue
	case strings.Contains(term, "256color"):
		return Color256
	}
	return Color16
}

// ---- 基础色板（§3.1） ----

var (
	bgTok     = "#0A0E14" // 终端底色
	panelTok  = "#151B24" // 面板 / 卡片底
	panel2Tok = "#1C2431" // 交替行 / 选中行
	panel3Tok = "#222B39" // 表头 / 强选中
	borderTok = "#2A3441" // 非聚焦边框
	focusTok  = "#4C9AFF" // 聚焦边框 / 主色
	txtTok    = "#E6EDF3" // 主文字（文件名）
	dimTok    = "#8A96A5" // 次要信息
	muteTok   = "#4E5A6B" // 弱化 / 分隔线

	accentTok = "#4C9AFF" // 下载中 / 进度填充 / ▼
	greenTok  = "#3FB950" // 完成 / ✓
	yellowTok = "#D29922" // 暂停 / 限速 / ‖
	redTok    = "#F85149" // 失败 / 限速线 / ✕
	purpleTok = "#BC8CFF" // 做种 / BT / ⇡
	cyanTok   = "#39C5CF" // 数值 / 图表 / 坐标轴
)

// 16 色降级映射（§9.1 表）。值 = ANSI 色号。
var fade16 = map[string]int{
	accentTok: 4, focusTok: 12,
	greenTok: 2, yellowTok: 3, redTok: 1, purpleTok: 5, cyanTok: 6,
	txtTok: 7, dimTok: 8, muteTok: 0, bgTok: 0,
	panelTok: 0, panel2Tok: 0, panel3Tok: 0, borderTok: 0,
}

// c 将 hex token 按当前颜色能力转为 lipgloss.Color（256/true 用原值，16 色映射）。
func c(hex string) lipgloss.Color {
	if colorLevel == Color16 {
		if n, ok := fade16[hex]; ok {
			return lipgloss.Color(strconv.Itoa(n))
		}
	}
	return lipgloss.Color(hex)
}

// 基础 token 暴露给布局层
var (
	colBG     = func() lipgloss.Color { return c(bgTok) }
	colBg     = func() lipgloss.Color { return c(bgTok) } // 与参考实现命名对齐（背景底）
	colPanel  = func() lipgloss.Color { return c(panelTok) }
	colPanel2 = func() lipgloss.Color { return c(panel2Tok) }
	colPanel3 = func() lipgloss.Color { return c(panel3Tok) }
	colBorder = func() lipgloss.Color { return c(borderTok) }
	colFocus  = func() lipgloss.Color { return c(focusTok) }
	colTxt    = func() lipgloss.Color { return c(txtTok) }
	colDim    = func() lipgloss.Color { return c(dimTok) }
	colMute   = func() lipgloss.Color { return c(muteTok) }
)

// 语义色
var (
	colAccent = func() lipgloss.Color { return c(accentTok) }
	colGreen  = func() lipgloss.Color { return c(greenTok) }
	colYellow = func() lipgloss.Color { return c(yellowTok) }
	colRed    = func() lipgloss.Color { return c(redTok) }
	colPurple = func() lipgloss.Color { return c(purpleTok) }
	colCyan   = func() lipgloss.Color { return c(cyanTok) }
)

// st 快速构造着色 style。
func st(fg lipgloss.Color) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(fg)
}

// stBold 粗体着色。
func stBold(fg lipgloss.Color) lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(fg)
}

// stBg 背景着色。
func stBg(bg lipgloss.Color) lipgloss.Style {
	return lipgloss.NewStyle().Background(bg)
}

// stFgBg 前景+背景着色（选中行 / 徽章）。
func stFgBg(fg, bg lipgloss.Color) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(fg).Background(bg)
}
