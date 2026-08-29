package io

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestRingBuffer_FixedCapacity 缓冲容量固定，不随数据增长（H-2 稳态内存前提）。
// 生产者写入远超容量的数据，消费者并发排空：数据不丢失、缓冲不扩容。
func TestRingBuffer_FixedCapacity(t *testing.T) {
	rb := NewRingBuffer(64 << 10) // 64 KiB
	if rb.Capacity() != 64<<10 {
		t.Fatalf("容量应为 64KiB, got %d", rb.Capacity())
	}
	big := bytes.Repeat([]byte("x"), 1<<20) // 1 MiB（远超容量）
	var got bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // 消费者并发排空
		defer wg.Done()
		tmp := make([]byte, 4<<10)
		for {
			n, err := rb.Read(tmp)
			got.Write(tmp[:n])
			if err != nil {
				return
			}
		}
	}()
	if _, err := rb.Write(big); err != nil {
		t.Fatalf("write: %v", err)
	}
	rb.Close()
	wg.Wait()
	if got.Len() != len(big) {
		t.Fatalf("消费字节数=%d want %d（数据丢失）", got.Len(), len(big))
	}
	if rb.Capacity() != 64<<10 {
		t.Fatalf("缓冲不应扩容, cap=%d", rb.Capacity())
	}
}

// TestRingBuffer_ConcurrentProducerConsumer 并发生产-消费不丢数据、不 panic。
func TestRingBuffer_ConcurrentProducerConsumer(t *testing.T) {
	rb := NewRingBuffer(4 << 10)
	var wg sync.WaitGroup
	data := bytes.Repeat([]byte("abcdefgh"), 512) // 4 KiB
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := rb.Write(data); err != nil {
			t.Errorf("write: %v", err)
		}
		rb.Close() // 写毕关闭，消费者读完排空数据后收到 ErrClosedPipe
	}()
	got := make([]byte, 0, len(data))
	tmp := make([]byte, 1<<10)
	for len(got) < len(data) {
		n, err := rb.Read(tmp)
		got = append(got, tmp[:n]...)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
	}
	wg.Wait()
	if !bytes.Equal(got, data) {
		t.Fatalf("数据不一致: got %d bytes want %d", len(got), len(data))
	}
}

// TestRingBuffer_BackpressureBlocks 满缓冲时 Write 阻塞（背压）；Read 解除阻塞。
func TestRingBuffer_BackpressureBlocks(t *testing.T) {
	rb := NewRingBuffer(1024)
	if _, err := rb.Write(bytes.Repeat([]byte("a"), 1024)); err != nil { // 写满
		t.Fatalf("fill: %v", err)
	}
	started := make(chan struct{})
	blocked := make(chan struct{})
	go func() {
		close(started) // 标记写协程已启动
		_, _ = rb.Write([]byte("overflow"))
		close(blocked)
	}()
	<-started
	select {
	case <-blocked:
		t.Fatal("Write 在缓冲已满时应阻塞（背压失效）")
	case <-time.After(100 * time.Millisecond):
		// 期望：仍在阻塞
	}
	if _, err := rb.Read(make([]byte, 8)); err != nil {
		t.Fatalf("read: %v", err)
	}
	select {
	case <-blocked:
		// Read 后 Write 完成（背压解除）
	case <-time.After(time.Second):
		t.Fatal("Read 后 Write 应解除阻塞")
	}
}

// TestRingBuffer_CloseUnblocksClose 关闭后：阻塞的 Write 立即解除并返回 ErrClosedPipe；
// 缓冲中已写入的数据仍可排空读取，读尽后 Read 返回 ErrClosedPipe。
func TestRingBuffer_CloseUnblocksClose(t *testing.T) {
	rb := NewRingBuffer(64)
	errCh := make(chan error, 1)
	go func() {
		_, err := rb.Write(bytes.Repeat([]byte("x"), 128)) // 阻塞中
		errCh <- err
	}()
	time.Sleep(50 * time.Millisecond)
	rb.Close()
	select {
	case err := <-errCh:
		if err != io.ErrClosedPipe {
			t.Fatalf("应返回 ErrClosedPipe, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close 应解除阻塞的 Write")
	}
	total := 0
	for {
		n, err := rb.Read(make([]byte, 128))
		total += n
		if err != nil {
			if err != io.ErrClosedPipe {
				t.Fatalf("排空后应返回 ErrClosedPipe, got %v", err)
			}
			break
		}
	}
	if total == 0 {
		t.Fatal("关闭前已入缓冲的数据应仍可读出")
	}
}

// TestSparseFile_CommitAtomic 写入 + Commit 后文件存在且内容正确，.part 被清理。
func TestSparseFile_CommitAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.bin")
	sf, err := OpenSparse(path, 1024)
	if err != nil {
		t.Fatalf("OpenSparse: %v", err)
	}
	payload := []byte("hello-downloader")
	if _, err := sf.WriteAt(payload, 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	if err := sf.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	// 最终文件存在
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read final: %v", err)
	}
	if !bytes.Equal(got[:len(payload)], payload) {
		t.Fatalf("内容不一致: %q", got[:len(payload)])
	}
	// .part 已清理
	if _, err := os.Stat(path + ".part"); !os.IsNotExist(err) {
		t.Fatalf(".part 应已清理")
	}
}

// TestSparseFile_ReopenPreservesContent 断点续传核心前提：重新打开 .part 不截断已有内容（S-3）。
func TestSparseFile_ReopenPreservesContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.bin")
	{
		sf, err := OpenSparse(path, 4096)
		if err != nil {
			t.Fatalf("OpenSparse: %v", err)
		}
		if _, err := sf.WriteAt([]byte("first-half-data"), 0); err != nil {
			t.Fatalf("WriteAt: %v", err)
		}
		// 模拟进程崩溃：仅关闭句柄（Close 保留 .part；Abort 会删除文件，不能用于崩溃模拟）
		if err := sf.Close(); err != nil {
			t.Fatalf("sf.Close: %v", err)
		}
	}
	// 重启：重新打开同一 .part，先前内容必须保留
	sf2, err := OpenSparse(path, 4096)
	if err != nil {
		t.Fatalf("重开 OpenSparse: %v", err)
	}
	defer sf2.Abort()
	f, err := os.Open(path + ".part")
	if err != nil {
		t.Fatalf("open .part: %v", err)
	}
	defer f.Close()
	buf := make([]byte, len("first-half-data"))
	if _, err := f.ReadAt(buf, 0); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if string(buf) != "first-half-data" {
		t.Fatalf("续传内容被截断: %q", string(buf))
	}
	// 大小守卫：.part 应为声明的 size
	if info, _ := os.Stat(path + ".part"); info.Size() != 4096 {
		t.Fatalf(".part 应为 %d 字节, got %d", 4096, info.Size())
	}
}

// TestCopyBuffer_UsesExplicitBuffer 验证 CopyBuffer 使用固定 64KiB buffer（不分配大块）。
func TestCopyBuffer_UsesExplicitBuffer(t *testing.T) {
	src := bytes.NewReader(bytes.Repeat([]byte("z"), 2<<10))
	var dst bytes.Buffer
	buf := make([]byte, 64<<10) // 显式 64KiB
	n, err := CopyBuffer(&dst, src, buf)
	if err != nil && err != io.EOF {
		t.Fatalf("CopyBuffer: %v", err)
	}
	if n != 2<<10 {
		t.Fatalf("拷贝字节数=%d want %d", n, 2<<10)
	}
}
