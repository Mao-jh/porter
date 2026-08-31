//go:build windows

// diskfree_windows.go 磁盘剩余空间查询（Windows：kernel32!GetDiskFreeSpaceExW，
// 经 stdlib syscall.NewLazyDLL 直调——零第三方依赖，符合 B-1/H-4）。
package cli

import (
	"syscall"
	"unsafe"
)

var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procGetDiskFreeSpaceEx = kernel32.NewProc("GetDiskFreeSpaceExW")
)

// diskFreeBytes 返回 path 所在卷的可用字节数。
func diskFreeBytes(path string) (int64, error) {
	ptr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free, total, avail uint64
	r1, _, e := procGetDiskFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(ptr)),
		uintptr(unsafe.Pointer(&free)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&avail)),
	)
	if r1 == 0 {
		return 0, e
	}
	return int64(avail), nil
}
