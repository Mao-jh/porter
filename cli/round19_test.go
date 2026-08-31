// round19_test.go 第 19 轮测试：-summary 速率/ETA 增强。
package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/Mao-jh/porter/persist"
)

// TestSummaryTracker_SpeedAndETA 两帧差分：速率与 ETA 计算正确。
func TestSummaryTracker_SpeedAndETA(t *testing.T) {
	tr := newSummaryTracker()
	t0 := time.Unix(1000, 0)
	var sb1 strings.Builder
	tr.renderAt(&sb1, []*persist.State{{
		ID: "a.bin", URL: "http://127.0.0.1/a", FileSize: 64 << 20, Done: 16 << 20, Status: "running",
	}}, t0)
	// 首帧无历史 → 速率/ETA 为 "-"
	if !strings.Contains(sb1.String(), "- ETA -") {
		t.Errorf("首帧应为 '-' 速率与 ETA: %q", sb1.String())
	}

	// 第二帧：10s 后 done 增加 16MiB → 速率 1.6MiB/s，剩余 32MiB → ETA 20s
	var sb2 strings.Builder
	tr.renderAt(&sb2, []*persist.State{{
		ID: "a.bin", URL: "http://127.0.0.1/a", FileSize: 64 << 20, Done: 32 << 20, Status: "running",
	}}, t0.Add(10*time.Second))
	out := sb2.String()
	if !strings.Contains(out, "1.6MiB/s") {
		t.Errorf("速率应为 1.6MiB/s: %q", out)
	}
	if !strings.Contains(out, "ETA 20s") {
		t.Errorf("ETA 应为 20s: %q", out)
	}
	if !strings.Contains(out, "50.0%") {
		t.Errorf("百分比应为 50.0%%: %q", out)
	}

	// 第三帧：done 回落（任务重启）→ 速率钳为 0（不出现负数），ETA 未知为 "-"
	var sb3 strings.Builder
	tr.renderAt(&sb3, []*persist.State{{
		ID: "a.bin", URL: "http://127.0.0.1/a", FileSize: 64 << 20, Done: 8 << 20, Status: "running",
	}}, t0.Add(20*time.Second))
	out3 := sb3.String()
	if strings.Contains(out3, "-MiB/s") || strings.Contains(out3, "-KiB/s") {
		t.Errorf("回落帧速率不应为负: %q", out3)
	}
	if !strings.Contains(out3, "0B/s ETA -") {
		t.Errorf("回落帧应为 0B/s 且 ETA 未知: %q", out3)
	}
}

// TestFormatETA 秒数 → 人类可读。
func TestFormatETA(t *testing.T) {
	cases := []struct{ in, want float64 }{{0, 0}, {30, 30}, {90, 90}, {3700, 3700}}
	for _, c := range cases {
		_ = c
	}
	if got := formatETA(30); got != "30s" {
		t.Errorf("formatETA(30) = %q, 期望 30s", got)
	}
	if got := formatETA(90); got != "1m 30s" {
		t.Errorf("formatETA(90) = %q, 期望 1m 30s", got)
	}
	if got := formatETA(3700); got != "1h 1m" {
		t.Errorf("formatETA(3700) = %q, 期望 1h 1m", got)
	}
	if got := formatETA(-1); got != "-" {
		t.Errorf("formatETA(-1) = %q, 期望 -", got)
	}
}

// TestHumanSpeed 速率格式化。
func TestHumanSpeed(t *testing.T) {
	if got := humanSpeed(1.5 * float64(1<<20)); got != "1.5MiB/s" {
		t.Errorf("humanSpeed = %q, 期望 1.5MiB/s", got)
	}
	if got := humanSpeed(500 * float64(1<<10)); got != "500.0KiB/s" {
		t.Errorf("humanSpeed = %q, 期望 500.0KiB/s", got)
	}
	if got := humanSpeed(123); got != "123B/s" {
		t.Errorf("humanSpeed = %q, 期望 123B/s", got)
	}
}

// TestHumanBytes 大小格式化（含 B 级与混合单位）。
func TestHumanBytes(t *testing.T) {
	if got := humanBytes(0, 100); got != "0/100B" {
		t.Errorf("humanBytes = %q", got)
	}
	if got := humanBytes(1<<10, 2<<10); got != "1.0/2.0KiB" {
		t.Errorf("humanBytes = %q", got)
	}
	if got := humanBytes(3<<20, 8<<20); got != "3.0/8.0MiB" {
		t.Errorf("humanBytes = %q", got)
	}
}

// TestSummaryTracker_Empty 空集：无输出且不 panic。
func TestSummaryTracker_Empty(t *testing.T) {
	var sb strings.Builder
	tr := newSummaryTracker()
	tr.renderAt(&sb, nil, time.Now())
	if sb.Len() != 0 {
		t.Errorf("空集应无输出: %q", sb.String())
	}
}

// 防止 persist 未使用（render 的 state 类型即 persist.State）
var _ = persist.ShardState{}
