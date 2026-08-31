// 抗劣化下载单元测试：progress 汇总（含在途）、retryForever 退避重试、
// watchQuality 慢速/停滞判定。
package cli

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mao-jh/porter/io"
	"github.com/Mao-jh/porter/scheduler"
)

// TestProgressIncludesInFlight 验证 progress() 统计在途 attempt 的已写前缀。
func TestProgressIncludesInFlight(t *testing.T) {
	sf, err := io.OpenSparse(t.TempDir()+"/p.bin", 10<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer sf.Abort()
	d := newDownloader(nil, "http://x/", sf, &scheduler.Plan{FileSize: 10 << 20})
	d.prog[0] = &shardProgress{start: 0, end: 5 << 20}
	d.prog[0].record(0, 1<<20) // 已完成 1MiB
	at := &attempt{t: rangeTask{shardIdx: 1, start: 5 << 20, end: 10 << 20}}
	at.written = new(atomic.Int64)
	at.written.Store(2 << 20) // 在途已写 2MiB
	d.attempts[at] = struct{}{}
	if got := d.progress(); got != 3<<20 {
		t.Errorf("progress 应 3MiB，got %d", got)
	}
}

// TestRetryForever 验证无限重试：前 2 次失败后成功。
func TestRetryForever(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := 0
	err := retryForever(ctx, func() error {
		n++
		if n < 3 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("应成功，got %v", err)
	}
	if n != 3 {
		t.Errorf("应尝试 3 次，got %d", n)
	}
}

// TestRetryForeverCancel 验证取消时退出（进度持久化后重启续传）。
func TestRetryForeverCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 已取消
	start := time.Now()
	err := retryForever(ctx, func() error { return errors.New("boom") })
	if err == nil {
		t.Fatal("应返回错误")
	}
	if time.Since(start) > 2*time.Second {
		t.Error("取消后应立即退出，不应等待退避")
	}
}

// TestWatchQualitySlow 验证慢速判定：注入低速进度，30s 窗口后 fail。
func TestWatchQualitySlow(t *testing.T) {
	sf, err := io.OpenSparse(t.TempDir()+"/p.bin", 100<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer sf.Abort()
	d := newDownloader(nil, "http://x/", sf, &scheduler.Plan{FileSize: 100 << 20})
	d.minRate = 1 << 20 // 阈值 1MiB/s
	d.prog[0] = &shardProgress{start: 0, end: 100 << 20}
	// 每 2s 只推进 100KB（远低于阈值）
	stop := make(chan struct{})
	go func() {
		tk := time.NewTicker(2 * time.Second)
		defer tk.Stop()
		var cur int64
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				cur += 100 << 10
				d.mu.Lock()
				d.prog[0].record(0, cur)
				d.mu.Unlock()
			}
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.watchQuality(ctx, make(chan struct{}))
		close(done)
	}()
	// 慢速判定应在 ~30s 后触发（容忍 40s 上限）
	select {
	case <-done:
		cancel()
		close(stop)
	case <-time.After(45 * time.Second):
		cancel()
		close(stop)
		t.Fatal("慢速判定未在 45s 内触发")
	}
	d.mu.Lock()
	failed := d.failed != nil
	d.mu.Unlock()
	if !failed {
		t.Fatal("应 fail（慢速）")
	}
}

// TestWatchQualityFast 验证快速下载不误判。
func TestWatchQualityFast(t *testing.T) {
	sf, err := io.OpenSparse(t.TempDir()+"/p.bin", 100<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer sf.Abort()
	d := newDownloader(nil, "http://x/", sf, &scheduler.Plan{FileSize: 100 << 20})
	d.minRate = 1 << 20 // 1MiB/s
	d.prog[0] = &shardProgress{start: 0, end: 100 << 20}
	// 快速推进：一次性记录全部（模拟下载完成），监测不应 fail
	d.prog[0].record(0, 100<<20)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		d.watchQuality(ctx, make(chan struct{}))
		close(done)
	}()
	// 等 4 个采样周期（8s）后确认未 fail
	time.Sleep(8 * time.Second)
	d.mu.Lock()
	failed := d.failed != nil
	d.mu.Unlock()
	cancel()
	<-done
	if failed {
		t.Fatalf("快速下载不应被判慢速: %v", d.failed)
	}
}
