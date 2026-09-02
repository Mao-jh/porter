package tui

// 缩放冒烟（§8.4 resize 稳健）：遍历极端尺寸渲染三布局，检测 panic/越界。
import (
	"strings"
	"testing"

	"github.com/Mao-jh/porter/cli"
	"github.com/charmbracelet/lipgloss"
)

type errTest string

func (e errTest) Error() string { return string(e) }

func TestResizeSmoke(t *testing.T) {
	sizes := [][2]int{
		{40, 10}, {60, 15}, {80, 20}, {99, 24}, {100, 25}, {110, 28},
		{120, 30}, {130, 32}, {140, 34}, {150, 36}, {160, 40}, {200, 50},
		{80, 30}, {120, 20}, {140, 15}, {200, 12},
	}
	for _, sz := range sizes {
		for _, l := range []LayoutID{LayoutA, LayoutB, LayoutC} {
			m := New(cli.Options{Verify: ""})
			m.width, m.height = sz[0], sz[1]
			m.layout, m.layoutAuto = l, false
			m.tasks = []*Task{
				{URL: "http://x/中文文件名很长很长很长很长很长.bin", Output: "中文文件名很长很长很长很长很长.bin",
					State: StateRunning, Proto: "http", Size: 10 << 30, Done: 3 << 30, Speed: 1 << 20, ETA: 7, speedRing: newSpeedRing(60)},
				{URL: "u", Output: "b.bin", State: StatePaused, Proto: "http", Size: 1 << 20, Done: 1 << 19, speedRing: newSpeedRing(60)},
				{URL: "u", Output: "c.bin", State: StateFailed, Proto: "http", Err: errTest("boom\nline2"), speedRing: newSpeedRing(60)},
				{URL: "u", Output: "d.bin", State: StateDone, Proto: "http", Size: 100, Done: 100, speedRing: newSpeedRing(60)},
			}
			m.tasks[0].speedRing.append(1 << 20)
			m.globalSpeed = newSpeedRing(120)
			m.globalSpeed.append(1 << 20)
			var v string
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("PANIC layout=%v size=%dx%d: %v", l, sz[0], sz[1], r)
					}
				}()
				v = m.View()
			}()
			// 输出绝不超出实际终端宽（含降级后布局）
			for _, line := range strings.Split(v, "\n") {
				if lipgloss.Width(line) > sz[0] {
					t.Errorf("layout=%v size=%dx%d 行越界（宽 %d > %d）: %q", l, sz[0], sz[1], lipgloss.Width(line), sz[0], line)
				}
			}
		}
	}
}

// TestResizeKeepsContent 尺寸变化后关键内容仍渲染（防过度降级丢信息）。
func TestResizeKeepsContent(t *testing.T) {
	cases := []struct {
		layout LayoutID
		w, h   int
		wants  []string
	}{
		{LayoutB, 120, 30, []string{"队列", "吞吐", "分片", "下载中"}},
		{LayoutB, 100, 20, []string{"队列", "下载中"}}, // 高度不足：面积图/分片图降级隐藏
		{LayoutC, 120, 35, []string{"总速度", "任务", "已下载", "队列剩余"}},
		{LayoutC, 140, 40, []string{"总速度", "进度", "速度"}},
	}
	for _, c := range cases {
		m := New(cli.Options{Verify: ""})
		m.width, m.height = c.w, c.h
		m.layout, m.layoutAuto = c.layout, false
		m.tasks = []*Task{{URL: "u", Output: "run.bin", State: StateRunning,
			Size: 1 << 20, Done: 1 << 19, Speed: 1 << 20}}
		m.globalSpeed = newSpeedRing(120)
		m.globalSpeed.append(1 << 20)
		m.cursor = 0
		v := m.View()
		for _, want := range c.wants {
			if !strings.Contains(v, want) {
				t.Errorf("layout=%v size=%dx%d 缺少 %q", c.layout, c.w, c.h, want)
			}
		}
	}
}
