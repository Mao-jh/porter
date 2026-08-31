// stream.go 实现 `-o -` 流式输出模式（第 18 轮，对标 curl -o - / wget -O -）：
// 单 URL 单连接顺序下载、直接写 stdout，适合管道消费（porter URL -o - | ...）。
//
// 诚实的限制（与同类工具一致的取舍）：
//   - 强制单连接顺序流（stdout 不可寻址 → 无分片并行）；
//   - 无断点续传（不写持久化状态）、无完成后校验（内容已流向管道无法回读）；
//   - Metalink/HLS 内容形态仍生效（failover/选流/AES-128 解密在传输层完成）。
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/Mao-jh/porter/network"
)

// runStream 流式下载到 stdout。dlFetch 已完成内容形态包装（HLS/Metalink）。
func runStream(ctx context.Context, dlFetch network.Fetcher, urlStr string) error {
	out := &stdoutWriter{}
	if err := dlFetch.FetchRange(ctx, urlStr, 0, 0, out); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "[stream] 已输出 %d 字节（流式模式：跳过校验与断点续传）\n", out.n)
	return nil
}

// stdoutWriter 把顺序 WriteAt 映射为 stdout 写入（忽略逻辑偏移，统计字节数）。
type stdoutWriter struct{ n int64 }

func (w *stdoutWriter) WriteAt(p []byte, off int64) (int, error) {
	n, err := os.Stdout.Write(p)
	w.n += int64(n)
	return n, err
}

// validateStreamOutput 校验 -o - 流式模式的参数约束（RunMulti 入口调用）。
func validateStreamOutput(opt *Options) error {
	if opt.Output != "-" {
		return nil
	}
	if len(opt.URLs) != 1 {
		return errors.New("-o - 流式模式仅支持单个 URL")
	}
	if opt.Shards > 0 {
		return errors.New("-o - 流式模式不支持 -n 分片（stdout 不可寻址，强制单连接顺序输出）")
	}
	return nil
}
