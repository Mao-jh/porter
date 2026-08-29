// Package io 实现下载 IO 写入层：稀疏文件预分配、固定 64KB 环形缓冲、原子落盘。
// 设计目标（§2 决策「IO 保住内存红线」）：
//   - 固定 64 KB 环形缓冲，禁止将整个文件读入 []byte；
//   - 使用 io.CopyBuffer / bufio.Reader 显式传 buffer；
//   - 稀疏文件预分配（fallocate），避免运行时碎片与峰值内存爬升。
package io

import (
	"errors"
	"io"
	"os"
	"sync"
)

// RingBuffer 固定容量环形缓冲（生产者=网络读取，消费者=磁盘写入）。
// 容量固定为 64 KB（§2 决策），整个生命周期内不增长 → 保住 H-2 稳态内存。
type RingBuffer struct {
	buf    []byte
	mu     sync.Mutex
	r, w   int  // 读/写指针
	len    int  // 当前数据量
	closed bool // Close 后阻塞的 Read/Write 立即返回错误
	cond   *sync.Cond
}

// NewRingBuffer 构造容量为 size 的环形缓冲。默认 64 KiB。
func NewRingBuffer(size int) *RingBuffer {
	if size <= 0 {
		size = 64 << 10 // 64 KiB
	}
	r := &RingBuffer{buf: make([]byte, size)}
	r.cond = sync.NewCond(&r.mu)
	return r
}

// Capacity 返回固定容量。
func (r *RingBuffer) Capacity() int { return cap(r.buf) }

// Write 写入数据，缓冲区满时阻塞（背压，防止网络快于磁盘导致内存膨胀）。
// Close 后返回 io.ErrClosedPipe。
func (r *RingBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for n < len(p) {
		for r.len == len(r.buf) {
			if r.closed {
				return n, io.ErrClosedPipe
			}
			r.cond.Wait() // 满则阻塞（背压）
		}
		space := len(r.buf) - r.len
		avail := len(p) - n
		if space > avail {
			space = avail
		}
		// 分两段拷贝（环形环绕）
		w := (r.w + r.len) % len(r.buf)
		cnt := space
		if w+cnt > len(r.buf) {
			cnt = len(r.buf) - w
		}
		copy(r.buf[w:w+cnt], p[n:n+cnt])
		r.len += cnt
		n += cnt
		r.cond.Broadcast()
	}
	return n, nil
}

// Read 读出数据到 p，无数据时阻塞。Close 后缓冲排空即返回 io.ErrClosedPipe。
func (r *RingBuffer) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for r.len == 0 {
		if r.closed {
			return 0, io.ErrClosedPipe
		}
		r.cond.Wait()
	}
	n := 0
	for n < len(p) && r.len > 0 {
		cnt := r.len
		if cnt > len(p)-n {
			cnt = len(p) - n
		}
		if r.r+cnt > len(r.buf) {
			cnt = len(r.buf) - r.r
		}
		copy(p[n:n+cnt], r.buf[r.r:r.r+cnt])
		r.r = (r.r + cnt) % len(r.buf)
		r.len -= cnt
		n += cnt
		r.cond.Broadcast()
	}
	return n, nil
}

// Close 关闭缓冲：阻塞中的 Read/Write 及后续调用返回 io.ErrClosedPipe。
// 用于消费者/生产者异常退出时解除对端阻塞，避免 goroutine 泄漏。
func (r *RingBuffer) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.cond.Broadcast()
}

// ---------------------------------------------------------------------------
// SparseFile：稀疏文件 + 预分配
// ---------------------------------------------------------------------------

// SparseFile 封装一个支持稀疏写入与原子提交的文件。
type SparseFile struct {
	path    string
	tmpPath string
	f       *os.File
	size    int64
}

// OpenSparse 创建（或断点续传时重新打开）目标文件的 .part 临时文件，并按 size 预分配空间。
// 关键语义：**绝不截断已有内容** —— 重启续传时 .part 中已下载字节必须保留（S-3）。
// 已有文件大于 size 时收缩到 size（清理上一次更大任务的残留）；小于 size 时扩展并预分配。
func OpenSparse(path string, size int64) (*SparseFile, error) {
	if path == "" {
		return nil, errors.New("io: empty path")
	}
	tmp := path + ".part"
	f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	sf := &SparseFile{path: path, tmpPath: tmp, f: f, size: size}
	if size > 0 {
		if info, err := f.Stat(); err == nil && info.Size() > size {
			if err := f.Truncate(size); err != nil {
				_ = f.Close()
				return nil, err
			}
		}
		if err := sf.allocate(size); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	return sf, nil
}

// allocate 预分配磁盘空间（稀疏，不实际写入块）。
func (sf *SparseFile) allocate(size int64) error {
	// fallocate 在 Linux 上实现稀疏预分配；不支持时回退到 Truncate。
	if err := fallocate(sf.f, size); err == nil {
		return nil
	}
	return sf.f.Truncate(size)
}

// WriteAt 在指定偏移写入数据（支持随机写入，供分片并行落盘）。
func (sf *SparseFile) WriteAt(p []byte, off int64) (int, error) {
	return sf.f.WriteAt(p, off)
}

// Commit 将 .part 原子重命名为最终文件（rename 在同一文件系统内为原子操作）。
func (sf *SparseFile) Commit() error {
	if err := sf.f.Sync(); err != nil {
		return err
	}
	if err := sf.f.Close(); err != nil {
		return err
	}
	return os.Rename(sf.tmpPath, sf.path)
}

// Abort 放弃下载，删除临时文件。
func (sf *SparseFile) Abort() {
	_ = sf.f.Close()
	_ = os.Remove(sf.tmpPath)
}

// Close 关闭句柄但保留 .part 文件（区别于 Abort 的删除）。
// 用于「进程崩溃」语义模拟与句柄交接：重启后 OpenSparse 重开同一文件续传。
func (sf *SparseFile) Close() error {
	return sf.f.Close()
}

// fallocate 的具体实现由构建标签隔离（见同包）：
//   - fallocate_linux.go (//go:build linux) → syscall.Fallocate（稀疏预分配）
//   - fallocate_stub.go  (//go:build !linux) → f.Truncate（Windows 交叉编译回退）
// 任何错误视为"不支持"，由调用方（allocate）回退到 Truncate（见 allocate 方法）。
// 此处不重复声明，避免与 tagged 实现冲突。

// CopyBuffer 使用显式固定 buffer 将 src 全部拷贝到 dst，避免内部分配大 buffer。
// 这是对标准 io.CopyBuffer 的封装，明确传 buffer 以符合 §2 IO 决策。
func CopyBuffer(dst io.Writer, src io.Reader, buf []byte) (int64, error) {
	if len(buf) == 0 {
		buf = make([]byte, 64<<10) // 64 KiB 固定
	}
	return io.CopyBuffer(dst, src, buf)
}
