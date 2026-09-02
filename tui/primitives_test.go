package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// ---- 渲染原语（P0，方案 §4） ----

// TestProgressBarExactWidth 亚字符进度条长度精确 == width（§10 P0 验收）。
func TestProgressBarExactWidth(t *testing.T) {
	for _, pct := range []float64{0, 0.01, 0.353, 0.5, 0.999, 1.0} {
		b := progressBar(pct, 62, '█', '░')
		if len(b) != 62 {
			t.Fatalf("progressBar(%.3f,62) 长度=%d want 62", pct, len(b))
		}
	}
}

// TestProgressBarSubEighth 亚字符收尾：35.3% → 21 整 + 亚字符。
func TestProgressBarSubEighth(t *testing.T) {
	b := progressBar(0.353, 62, '█', '░')
	// 0.353*62 = 21.886 → 21 整 + 7/8 亚字符（▉）
	got := string(b)
	if !strings.HasPrefix(got, strings.Repeat("█", 21)) {
		t.Fatalf("整块数错误: %q", got)
	}
	rs := []rune(got)
	if rs[21] != '▉' {
		t.Fatalf("亚字符应 ▉（21.886 → frac 7）: %q", got)
	}
	if rs[22] != '░' {
		t.Fatalf("亚字符后应为空字符: %q", got)
	}
	if len(rs) != 62 {
		t.Fatalf("总长=%d want 62", len(rs))
	}
}

// TestProgressBarClamp 越界钳制。
func TestProgressBarClamp(t *testing.T) {
	if got := string(progressBar(-0.5, 5, '█', '░')); got != strings.Repeat("░", 5) {
		t.Fatalf("负值应全空: %q", got)
	}
	if got := string(progressBar(1.5, 5, '█', '░')); got != strings.Repeat("█", 5) {
		t.Fatalf(">1 应全满: %q", got)
	}
}

// TestSparklineUniform hi==lo 除零防护（§4.2）：全同值 → 高度归一（无除零崩溃）。
func TestSparklineUniform(t *testing.T) {
	s := sparkline([]float64{5, 5, 5}, 6)
	if len(s) != 6 {
		t.Fatalf("长度=%d want 6", len(s))
	}
	// 无除零崩溃即通过（sparkline 不能 panic）
	if len(sparkline(nil, 8)) != 8 {
		t.Fatal("空值应补满宽度")
	}
}

// TestSparklineTrend 单调上升 → 高度递增。
func TestSparklineTrend(t *testing.T) {
	s := string(sparkline([]float64{1, 2, 3, 4, 5, 6, 7, 8}, 8))
	// 8 档各取一：▁▂▃▄▅▆▇█
	if s != "▁▂▃▄▅▆▇█" {
		t.Fatalf("单调上升 sparkline=%q", s)
	}
}

// TestResample 降采样到目标宽度。
func TestResample(t *testing.T) {
	got := resample([]float64{1, 2, 3, 4}, 2)
	if len(got) != 2 {
		t.Fatal("降采样宽度错误")
	}
	if got[0] != 1 || got[1] != 3 {
		t.Fatalf("降采样取值错误: %v", got)
	}
	if len(resample([]float64{1}, 5)) != 5 {
		t.Fatal("补 0 宽度错误")
	}
}

// TestAreaChartExplicitBG 面积图区域显式写背景色（§4.3 [MUST]）。
func TestAreaChartExplicitBG(t *testing.T) {
	g := newCellGrid(10, 5)
	areaChart(g, 1, 1, 8, 3, []float64{0, 10, 20, 30, 40, 50, 60, 100}, colAccent(), colPanel())
	// 区域内所有 cell 必须 hasB（防差分闪烁）
	for y := 1; y < 4; y++ {
		for x := 1; x < 9; x++ {
			if !g.c[y*g.w+x].hasB {
				t.Fatalf("面积图 cell(%d,%d) 未显式背景色", x, y)
			}
		}
	}
	// 区域外不受影响
	if g.c[0].hasB {
		t.Fatal("区域外不应有背景")
	}
}

// TestChunkMap 分片图三段渲染 + 背景显式。
func TestChunkMap(t *testing.T) {
	g := newCellGrid(10, 1)
	chunkMap(g, 0, 0, 10, 1, 3, 2, 6)
	if g.c[0].r != '▓' || g.c[2].r != '▓' {
		t.Fatal("已完成分片应 ▓")
	}
	if g.c[3].r != '▒' || g.c[4].r != '▒' {
		t.Fatal("在途分片应 ▒")
	}
	if g.c[5].r != '░' {
		t.Fatal("未取分片应 ░")
	}
	if g.c[6].r != ' ' {
		t.Fatal("超出 total 应空白")
	}
	for x := 0; x < 6; x++ {
		if !g.c[x].hasB {
			t.Fatalf("分片 cell(%d) 未显式背景", x)
		}
	}
}

// TestGridString 网格拼接：相邻同色合并、宽字符占 2 列。
func TestGridString(t *testing.T) {
	g := newCellGrid(6, 2)
	g.put(0, 0, '█', colAccent(), colPanel())
	g.put(1, 0, '█', colAccent(), colPanel())
	g.put(2, 0, '文', colTxt(), colPanel()) // 宽字符
	g.putStr(3, 1, "ab", colTxt(), colBg())
	got := g.String()
	lines := strings.Split(got, "\n")
	if lipgloss.Width(lines[0]) != 6 {
		t.Fatalf("首行显示宽度=%d want 6: %q", lipgloss.Width(lines[0]), lines[0])
	}
}

// TestTruncW 按显示宽度截断 + 补…（§8.3）。
func TestTruncW(t *testing.T) {
	// 超宽：截断 + 省略号占末列（§8.3 末尾补…）
	if got := truncW("hello world", 5); got != "hell…" {
		t.Fatalf("截断= %q", got)
	}
	if got := truncW("hello world", 8); got != "hello w…" {
		t.Fatalf("补…= %q", got)
	}
	// 截断到正好宽度不补 …
	if got := truncW("hello", 5); got != "hello" {
		t.Fatalf("正好宽度不应截断: %q", got)
	}
	if got := truncW("中文文件名很长", 5); lipgloss.Width(got) > 5 {
		t.Fatalf("宽字符截断越界: %q (w=%d)", got, lipgloss.Width(got))
	}
	if got := truncW("abc", 10); got != "abc" {
		t.Fatalf("未超宽不应截断: %q", got)
	}
}

// TestSanitizeGlyphs 禁用字符置换（§3.2）：⏸→‖ ✗→✕ ⏳→⋯。
func TestSanitizeGlyphs(t *testing.T) {
	cases := map[string]string{
		"⏸ 暂停": "‖ 暂停",
		"✗":    "✕",
		"⏳ 剩余": "⋯ 剩余",
		"⟳":    "↻",
		"正常文本": "正常文本",
	}
	for in, want := range cases {
		if got := sanitizeGlyphs(in); got != want {
			t.Errorf("sanitizeGlyphs(%q)=%q want %q", in, got, want)
		}
	}
	// 禁用字符集零残留（§11 验收项）
	for _, r := range "⏸⏵⟳⧗⌛⏳✗" {
		if sanitizeGlyphs(string(r)) == string(r) {
			t.Errorf("禁用字符 %q 未被置换", r)
		}
	}
}

// TestStateDict 状态字典：图标/文案/色 与 §3.4 一致。
func TestStateDict(t *testing.T) {
	cases := []struct {
		s    TaskState
		icon string
		word string
	}{
		{StateRunning, "▼", "下载中"},
		{StatePaused, "‖", "已暂停"},
		{StateDone, "✓", "已完成"},
		{StateFailed, "✕", "失败"},
		{StateQueued, "⋯", "排队"},
	}
	for _, c := range cases {
		if got := stateIcon(c.s); got != c.icon {
			t.Errorf("stateIcon(%v)=%q want %q", c.s, got, c.icon)
		}
		if got := stateWord(c.s); got != c.word {
			t.Errorf("stateWord(%v)=%q want %q", c.s, got, c.word)
		}
	}
	// 状态色无硬编码（真彩色下为 token 值）
	colorLevel = ColorTrue
	if stateColor(StateRunning) != colAccent() {
		t.Fatal("running 色应为 ACCENT")
	}
	if stateColor(StateFailed) != colRed() {
		t.Fatal("failed 色应为 RED")
	}
}
