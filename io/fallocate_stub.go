//go:build !linux

// Package io 的非 Linux（含 Windows 交叉编译）fallocate 回退实现。
// Windows 不支持 fallocate(2)，直接 Truncate 预分配；稀疏语义降级，不影响正确性（H-2 仍由固定缓冲保证）。
package io

import "os"

// fallocate 回退实现：直接 Truncate 预分配。稀疏语义不保证，但功能正确。
func fallocate(f *os.File, size int64) error {
	if size <= 0 {
		return nil
	}
	return f.Truncate(size)
}
