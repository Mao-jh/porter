package retry

import (
	"errors"
	"testing"
	"time"
)

func TestBackoff_Exponential(t *testing.T) {
	c := Default()
	c.Jitter = 0 // 关闭抖动便于断言
	c.Max = 30 * time.Second
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
	}
	for _, c2 := range cases {
		got := c.Backoff(c2.attempt)
		if got != c2.want {
			t.Errorf("attempt=%d Backoff=%v want %v", c2.attempt, got, c2.want)
		}
	}
}

func TestBackoff_CappedAtMax(t *testing.T) {
	c := Default()
	c.Jitter = 0
	c.Max = 5 * time.Second
	got := c.Backoff(100) // 远大于上限
	if got > c.Max {
		t.Errorf("退避 %v 超过上限 %v", got, c.Max)
	}
}

func TestBackoff_JitterWithinRange(t *testing.T) {
	c := Default()
	c.Jitter = 0.2
	c.Base = time.Second
	c.Max = time.Hour
	// attempt 上限取 30：1s<<30 未溢出 int64 纳秒（更大倍增由饱和逻辑截断到 Max）
	for i := 0; i <= 30; i++ {
		d := c.Backoff(i)
		nominal := time.Duration(float64(time.Second) * float64(uint64(1)<<uint(i)))
		if nominal > c.Max {
			nominal = c.Max
		}
		lo := time.Duration(float64(nominal) * 0.8)
		hi := time.Duration(float64(nominal) * 1.2)
		if d < lo || d > hi {
			t.Errorf("抖动越界: %v not in [%v,%v]", d, lo, hi)
		}
	}
}

// TestBackoff_SaturatesNoOverflow 大 attempt 下饱和倍增：恒等于 Max 且不为负（溢出回归）。
func TestBackoff_SaturatesNoOverflow(t *testing.T) {
	c := Default()
	c.Jitter = 0 // 关闭抖动以断言饱和值
	for _, attempt := range []int{31, 62, 63, 64, 100, 1000} {
		d := c.Backoff(attempt)
		if d != 30*time.Second {
			t.Errorf("attempt=%d Backoff=%v want 30s（饱和）", attempt, d)
		}
		if d < 0 {
			t.Errorf("attempt=%d Backoff 为负: %v（移位溢出）", attempt, d)
		}
	}
}

func TestDo_RetriesThenSucceeds(t *testing.T) {
	c := Default()
	c.MaxTry = 5
	c.Base = time.Millisecond // 加速测试
	attempts := 0
	err := c.Do(func(i int) (bool, error) {
		attempts++
		if i < 2 {
			return true, errors.New("transient")
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("应成功, got %v", err)
	}
	if attempts != 3 {
		t.Fatalf("尝试次数=%d want 3", attempts)
	}
}

func TestDo_NonRetryableStops(t *testing.T) {
	c := Default()
	c.MaxTry = 5
	attempts := 0
	err := c.Do(func(i int) (bool, error) {
		attempts++
		return false, errors.New("fatal") // 不可重试
	})
	if err == nil {
		t.Fatal("应返回错误")
	}
	if attempts != 1 {
		t.Fatalf("应立即停止, attempts=%d", attempts)
	}
}
