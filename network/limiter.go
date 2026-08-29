package network

import (
	"context"
	"io"
	"sync"
	"time"
)

// rateLimiter 全局平滑限速器（多连接共享）。
// 算法：按字节平滑排班（leaky bucket 节奏）——每个读取者在互斥锁下预约
// 自己的时间窗后睡眠等待。天然 FIFO 公平、无突发、多连接严格共享总配额。
// 复用方（transport）在响应体读取路径调用 acquire。
type rateLimiter struct {
	mu           sync.Mutex
	next         time.Time // 下一个可用时刻
	nanosPerByte float64   // 每字节应占用的纳秒数
}

// newRateLimiter 构造限速器；bytesPerSec 必须 > 0。
func newRateLimiter(bytesPerSec int64) *rateLimiter {
	return &rateLimiter{
		nanosPerByte: float64(time.Second) / float64(bytesPerSec),
	}
}

// acquire 预约 n 字节的发送窗并阻塞到该窗到来；ctx 取消时提前返回 ctx.Err()。
func (l *rateLimiter) acquire(ctx context.Context, n int) error {
	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now // 空闲期不积累突发配额
	}
	wait := l.next.Sub(now)
	l.next = l.next.Add(time.Duration(float64(n) * l.nanosPerByte))
	l.mu.Unlock()

	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// throttledReader 以全局限速节奏读取响应体（H-2 语义不受影响：仍是流式小缓冲）。
type throttledReader struct {
	ctx context.Context
	r   io.Reader
	l   *rateLimiter
}

func (tr *throttledReader) Read(p []byte) (int, error) {
	n, err := tr.r.Read(p)
	if n > 0 {
		if aerr := tr.l.acquire(tr.ctx, n); aerr != nil {
			return n, aerr
		}
	}
	return n, err
}
