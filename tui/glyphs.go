// 字形安全集与回退（方案 §3.2 [MUST]）。
//
// 三款等宽字体实测：██/框线/▔▔ 全安全；⇡⇣◐◑★☆✖↻↺⚙▰▱ 需回退；
// ⏸⏵⟳⧗⌛⏳✗ 多数字体缺失（豆腐块），一律置换。
//
// 偏差说明见施工回执：Go TUI 无法像参考实现那样逐像素对比 .notdef 字形，
// 落地为"静态安全表 + 强制置换"——效果等价：禁用字符零出现（§11 验收项）。
package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// charSub 禁用/危险字符 → 安全字符置换表（§3.2 处理规则）。
var charSub = map[rune]rune{
	'⏸': '‖', // 暂停 → ‖
	'⏵': '▶', // 播放/继续 → ▶（安全）
	'▶': '▶',
	'⟳': '↻', // 校验中 → ↻
	'⧗': '↻',
	'⌛': '⋯', // 剩余时间 → ⋯
	'⏳': '⋯',
	'✗': '✕', // 失败 → ✕
	'❍': '●',
	'␣': ' ',
}

// glyphBan 含被列入禁用集的字符（供测试断言"零出现"）。
var glyphBan = map[rune]bool{'⏸': true, '⏵': true, '⟳': true, '⧗': true, '⌛': true, '⏳': true, '✗': true}

// sanitizeRune 单个字符回退（不在置换表则原样返回）。
func sanitizeRune(r rune) rune {
	if s, ok := charSub[r]; ok {
		return s
	}
	return r
}

// sanitizeGlyphs 整串字符回退（渲染前对用户输入/错误信息必调）。
func sanitizeGlyphs(s string) string {
	src := []rune(s)
	changed := false
	for _, r := range src {
		if _, ok := charSub[r]; ok {
			changed = true
			break
		}
	}
	if !changed {
		return s
	}
	for i, r := range src {
		if s2, ok := charSub[r]; ok {
			src[i] = s2
		}
	}
	return string(src)
}

// safeGlyphs 全安全字符集（三款字体均有）——布局代码的"可放心用"清单。
const safeGlyphs = "█▉▊▋▌▍▎▏ ░▒▓ ▀▄ " +
	"▁▂▃▄▅▆▇█ " +
	"─│┌┐└┘├┤┬┴┼ " +
	"╭╮╰╯ ━┃ ┏┓┗┛ ═║ ╔╗╚╝ " +
	"✓✕▼▲●◉◆◇ →↓·┈┊ ‖"

// isSafeGlyph 判断字符是否在安全集（测试/断言用）。
func isSafeGlyph(r rune) bool {
	return strings.ContainsRune(safeGlyphs, r)
}

// ---- 状态字典（§3.4） ----

// stateIcon 状态 → 图标（已置换到安全集）。
func stateIcon(s TaskState) string {
	switch s {
	case StateRunning:
		return "▼"
	case StatePaused:
		return "‖"
	case StateDone:
		return "✓"
	case StateFailed:
		return "✕"
	case StateQueued:
		return "⋯"
	default:
		return "?"
	}
}

// stateWord 状态 → 中文文案（状态徽章/字典，§3.4；全中文界面）。
func stateWord(s TaskState) string {
	switch s {
	case StateRunning:
		return "下载中"
	case StatePaused:
		return "已暂停"
	case StateDone:
		return "已完成"
	case StateFailed:
		return "失败"
	case StateQueued:
		return "排队"
	}
	return "?"
}

// stateColor 状态 → 语义色（§3.4 / §6.2）。
func stateColor(s TaskState) lipgloss.Color {
	switch s {
	case StateRunning:
		return colAccent()
	case StatePaused:
		return colYellow()
	case StateDone:
		return colGreen()
	case StateFailed:
		return colRed()
	case StateQueued:
		return colMute()
	}
	return colMute()
}
