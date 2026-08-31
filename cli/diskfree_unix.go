//go:build !windows

// diskfree_unix.go 磁盘剩余空间查询（Linux/macOS：syscall.Statfs，零依赖）。
package cli

import (
	"syscall"
)

// diskFreeBytes 返回 path 所在文件系统的可用字节数。
func diskFreeBytes(path string) (int64, error) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return 0, err
	}
	// Bavail 为普通用户可用块数（不是 Bfree，后者含 root 保留块）
	return int64(fs.Bavail) * int64(fs.Bsize), nil
}
