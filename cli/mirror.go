// mirror.go 抗劣化下载的镜像切换层（第 23 轮）。
// 语义：主源失败（断连/超时/慢速/停滞/5xx）→ 按序尝试镜像候选；
// 切换基于 runOne 的字节级续传——失败源的 .part 进度已持久化，
// 镜像大小一致时从断点继续，不一致时自动全新下载（runOne 内部守卫）。
// 与 Metalink failover 的关系：Metalink 是「探测期选源」；-mirror 是
// 「运行期换源」，两者正交可叠加（Metalink 选定后仍可挂 -mirror 兜底）。
// -retry-forever：全部候选失败后按 1s→30s 饱和退避从头重试（覆盖探测失败），
// 直到成功或用户取消（ctx 取消时进度已持久化，重启续传）。
package cli

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/Mao-jh/porter/network"
	"github.com/Mao-jh/porter/persist"
)

// runOneWithMirrors 执行单个下载任务，失败时按序切换镜像。
// 全部候选失败才返回错误（聚合最后一次错误）；用户取消（ctx.Err）不切换。
func runOneWithMirrors(ctx context.Context, fetch network.Fetcher, tr *network.Transport,
	opt *Options, urlStr, output string, store *persist.Store) error {

	attempt := func() error {
		if len(opt.Mirrors) == 0 {
			return runOne(ctx, fetch, tr, opt, urlStr, output, store)
		}
		candidates := make([]string, 0, 1+len(opt.Mirrors))
		candidates = append(candidates, urlStr)
		candidates = append(candidates, opt.Mirrors...)
		var lastErr error
		for i, u := range candidates {
			lastErr = runOne(ctx, fetch, tr, opt, u, output, store)
			if lastErr == nil {
				return nil
			}
			if ctx.Err() != nil {
				return lastErr // 用户取消：不再切换
			}
			if i == len(candidates)-1 {
				return lastErr
			}
			fmt.Fprintf(os.Stderr, "[mirror] 候选 %s 失败: %v\n", u, lastErr)
			fmt.Fprintf(os.Stderr, "[mirror] 切换 %s（进度已持久化，续传语义保持）\n", candidates[i+1])
		}
		return lastErr
	}
	if !opt.RetryForever {
		return attempt()
	}
	return retryForever(ctx, attempt)
}

// retryForever 无限退避重试（1s→30s 饱和 ±20% 抖动；与 network 层重试同节奏）。
// 覆盖探测失败（runOne 探测在分片重试循环之外，此处兜底全链路）。
func retryForever(ctx context.Context, fn func() error) error {
	backoff := time.Second
	for {
		err := fn()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return err // Ctrl-C / kill：进度已持久化，重启续传
		}
		jitter := time.Duration(float64(backoff) * 0.2 * (rand.Float64()*2 - 1))
		sleep := backoff + jitter
		if sleep < 0 {
			sleep = 0
		}
		fmt.Fprintf(os.Stderr, "[retry-forever] %v 后重试（%v）\n", sleep.Round(time.Millisecond), err)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(sleep):
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}
