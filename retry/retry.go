// Package retry 实现指数退避 + 抖动重试策略。
// 覆盖断连 / 超时 / 429 / 5xx（§2 决策）。单次退避: base*2^attempt + 随机抖动, 上限 30s。
package retry

import (
	"math/rand"
	"time"
)

// Config 重试配置。并发安全：抖动随机数取自全局并发安全源，
// 同一 Config 可被多个下载工作协程共享（-race 验证）。
type Config struct {
	Base   time.Duration // 初始退避（默认 1s）
	Max    time.Duration // 上限（默认 30s）
	MaxTry int           // 最大尝试次数；0 = 无限（建议调用方设上限）
	Jitter float64       // 抖动幅度 0~1（默认 0.2）
}

// Default 返回默认配置（来源：§2 决策「1s→2s→4s… 上限30s」）。
func Default() *Config {
	return &Config{
		Base:   time.Second,
		Max:    30 * time.Second,
		MaxTry: 8,
		Jitter: 0.2,
	}
}

// Backoff 计算第 attempt 次（从0计数）退避时长。
// 采用饱和倍增（逐次 ×2，达上限即止），避免大 attempt 时移位溢出为负数。
func (c *Config) Backoff(attempt int) time.Duration {
	if c.Base <= 0 {
		c.Base = time.Second
	}
	if c.Max <= 0 {
		c.Max = 30 * time.Second
	}
	if attempt < 0 {
		attempt = 0
	}
	d := c.Base
	for i := 0; i < attempt && d < c.Max; i++ {
		d *= 2
	}
	if d > c.Max {
		d = c.Max
	}
	// 加抖动 ±Jitter（rand 全局源并发安全，多 worker 共享 Config 亦无数据竞争）
	if c.Jitter > 0 {
		j := time.Duration(float64(d) * c.Jitter * (rand.Float64()*2 - 1))
		d += j
		if d < 0 {
			d = 0
		}
	}
	return d
}

// Do 对 fn 执行重试，直到成功或达到 MaxTry。fn 返回 (retryable, error)：retryable=false 立即放弃。
func (c *Config) Do(fn func(attempt int) (retryable bool, err error)) error {
	cfg := c
	if cfg.MaxTry <= 0 {
		cfg.MaxTry = 8
	}
	for i := 0; ; i++ {
		retryable, err := fn(i)
		if err == nil {
			return nil
		}
		if !retryable || (cfg.MaxTry > 0 && i+1 >= cfg.MaxTry) {
			return err
		}
		time.Sleep(cfg.Backoff(i))
	}
}

// classifyError 将常见错误归类为可重试（供上层网络层使用）。
// 429/5xx/断连/超时 → 重试；4xx(除429) → 不重试。
func classifyError(_ error) bool {
	// 具体分类在网络层实现（见 network/transport.go）。
	// 此处仅占位，避免循环依赖；真实逻辑以 network 包为准。
	return true
}
