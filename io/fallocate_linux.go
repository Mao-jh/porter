//go:build linux

// Package io 的 Linux 专用 fallocate 实现。
// 仅在 Linux 构建时编译；Windows (GOOS=windows) 交叉编译使用 fallocate_stub.go 回退到 Truncate。
package io

import (
	"os"
	"syscall"
)

// fallocate 通过 syscall.Fallocate 预分配磁盘空间（Linux fallocate(2)）。
// mode=0：实际分配；off=0 从文件起始；len=size 分配 size 字节。
// 任何错误视为"不支持"，由调用方（allocate）回退到 Truncate。
func fallocate(f *os.File, size int64) error {
	if size <= 0 {
		return nil
	}
	return syscall.Fallocate(int(f.Fd()), 0, 0, size)
}
