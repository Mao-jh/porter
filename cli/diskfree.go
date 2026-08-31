// diskfree.go 下载前磁盘空间预检（第 18 轮，对标 IDM/wget 的早期失败）：
// 目标文件所在卷剩余空间不足时快速失败，避免下载到一半才因磁盘满而中止。
// 策略（诚实边界）：
//   - 仅已知大小（size>0）且非流式输出（-o -）时执行；
//   - 续传场景按「已存在的 .part 大小」折算所需空间；
//   - 查询失败（跨平台差异/权限）降级为 stderr 警告，不阻断下载。
package cli

import (
	"fmt"
	"os"
	"path/filepath"
)

// preflightDisk 校验 output 所在目录有足够空间容纳 size 字节（含 .part 已有量折算）。
// 返回错误表示空间不足（调用方直接失败）；查询失败时仅警告并放行。
func preflightDisk(output string, size int64) error {
	if size <= 0 {
		return nil
	}
	needed := size
	if st, err := os.Stat(output + ".part"); err == nil && st.Size() < size {
		needed -= st.Size() // 续传：已落盘部分不需要额外空间
	} else if err == nil {
		needed = 0 // .part 已满尺寸（异常态），无需额外空间
	}
	if needed <= 0 {
		return nil
	}
	dir := filepath.Dir(output)
	free, err := diskFreeBytes(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[warn] 磁盘空间查询失败（%s），跳过预检: %v\n", dir, err)
		return nil
	}
	if free < needed {
		return fmt.Errorf("磁盘空间不足: 需要 %d 字节（含 .part 已存 %d），卷可用 %d（%s）",
			needed, size-needed, free, dir)
	}
	return nil
}
