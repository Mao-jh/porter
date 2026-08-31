// round18_test.go 第 18 轮测试：磁盘空间预检 / -o - 流式输出。
package cli

import (
	"bytes"
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mao-jh/porter/hash"
	"github.com/Mao-jh/porter/network"
	"github.com/Mao-jh/porter/testserver"
)

// TestDiskFreeBytes 查询可用空间：应 > 0 且无错误。
func TestDiskFreeBytes(t *testing.T) {
	free, err := diskFreeBytes(t.TempDir())
	if err != nil {
		t.Fatalf("diskFreeBytes: %v", err)
	}
	if free <= 0 {
		t.Errorf("可用空间应 > 0, got %d", free)
	}
}

// TestPreflightDisk_Enough 小文件空间充足 → 通过。
func TestPreflightDisk_Enough(t *testing.T) {
	if err := preflightDisk(filepath.Join(t.TempDir(), "x.bin"), 1<<20); err != nil {
		t.Errorf("1MiB 应通过: %v", err)
	}
}

// TestPreflightDisk_NotEnough 天文数字大小 → 快速失败。
func TestPreflightDisk_NotEnough(t *testing.T) {
	err := preflightDisk(filepath.Join(t.TempDir(), "x.bin"), math.MaxInt64)
	if err == nil {
		t.Fatal("MaxInt64 大小应判定空间不足")
	}
	if !strings.Contains(err.Error(), "磁盘空间不足") {
		t.Errorf("错误信息应含'磁盘空间不足': %v", err)
	}
}

// TestPreflightDisk_PartDeduction 已有 .part 折算所需空间（续传场景）。
func TestPreflightDisk_PartDeduction(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "x.bin")
	if err := os.WriteFile(out+".part", bytes.Repeat([]byte{0}, 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	// 需要 size - part = 1MiB，但 .part 已占 1MiB 时仅需补 1MiB → 仍应通过
	if err := preflightDisk(out, 2<<20); err != nil {
		t.Errorf("part 折算后应通过: %v", err)
	}
	// .part 尺寸 >= size（异常态）→ 无需额外空间
	if err := preflightDisk(out, 1<<20); err != nil {
		t.Errorf(".part 已满尺寸应直接通过: %v", err)
	}
}

// TestPreflightDisk_SkipZero 未知大小（0）跳过预检。
func TestPreflightDisk_SkipZero(t *testing.T) {
	if err := preflightDisk(filepath.Join(t.TempDir(), "x.bin"), 0); err != nil {
		t.Errorf("size=0 应跳过: %v", err)
	}
}

// TestRun_StreamStdout -o - 流式：stdout 内容与源文件 sha256 一致。
func TestRun_StreamStdout(t *testing.T) {
	ts, err := testserver.New(testserver.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	if _, err := ts.CreateFile("s.bin", 3<<20); err != nil {
		t.Fatal(err)
	}
	want, err := ts.Checksum("s.bin")
	if err != nil {
		t.Fatal(err)
	}

	old := newTransport
	newTransport = func() *network.Transport { return network.NewTransport(false) }
	defer func() { newTransport = old }()

	// 捕获 stdout：管道缓冲有限（64KiB），必须并发读取避免写端阻塞死锁
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	var buf bytes.Buffer
	readDone := make(chan struct{})
	go func() {
		_, _ = buf.ReadFrom(r)
		close(readDone)
	}()

	tmp := t.TempDir()
	opt := &Options{
		URL:      ts.FileURL("s.bin"),
		URLs:     []string{ts.FileURL("s.bin")},
		Output:   "-",
		Verify:   hash.SHA256,
		StateDir: filepath.Join(tmp, "state"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runErr := Run(ctx, opt)

	_ = w.Close()
	os.Stdout = orig
	<-readDone
	if runErr != nil {
		t.Fatalf("Run(-o -): %v", runErr)
	}
	if int64(buf.Len()) != 3<<20 {
		t.Fatalf("stdout 长度 %d != %d", buf.Len(), 3<<20)
	}
	sum := sha256Hex(buf.Bytes())
	if sum != want {
		t.Errorf("流式输出 sha256 不符: got %s want %s", sum, want)
	}
}

// TestRun_StreamHLS -o - 支持 HLS 内容形态（虚拟映射流式输出）。
func TestRun_StreamHLS(t *testing.T) {
	ts, err := testserver.New(testserver.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	if _, err := ts.CreateFile("h.bin", 3<<20); err != nil {
		t.Fatal(err)
	}
	want, _ := ts.Checksum("h.bin")

	old := newTransport
	newTransport = func() *network.Transport { return network.NewTransport(false) }
	defer func() { newTransport = old }()

	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	var buf bytes.Buffer
	readDone := make(chan struct{})
	go func() {
		_, _ = buf.ReadFrom(r)
		close(readDone)
	}()

	tmp := t.TempDir()
	opt := &Options{
		URL:      ts.BaseURL() + "/hls/h.bin.m3u8",
		URLs:     []string{ts.BaseURL() + "/hls/h.bin.m3u8"},
		Output:   "-",
		Verify:   "",
		StateDir: filepath.Join(tmp, "state"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runErr := Run(ctx, opt)

	_ = w.Close()
	os.Stdout = orig
	<-readDone
	if runErr != nil {
		t.Fatalf("Run(HLS -o -): %v", runErr)
	}
	sum := sha256Hex(buf.Bytes())
	if sum != want {
		t.Errorf("HLS 流式输出 sha256 不符: got %s want %s", sum, want)
	}
}

// TestValidateStreamOutput -o - 参数约束：多 URL / -n 分片 → 报错。
func TestValidateStreamOutput(t *testing.T) {
	if err := validateStreamOutput(&Options{Output: "-", URLs: []string{"a", "b"}}); err == nil {
		t.Error("多 URL + -o - 应报错")
	}
	if err := validateStreamOutput(&Options{Output: "-", URLs: []string{"a"}, Shards: 4}); err == nil {
		t.Error("-n 分片 + -o - 应报错")
	}
	if err := validateStreamOutput(&Options{Output: "-", URLs: []string{"a"}}); err != nil {
		t.Errorf("单 URL 无分片应通过: %v", err)
	}
	if err := validateStreamOutput(&Options{Output: "x.bin", URLs: []string{"a"}}); err != nil {
		t.Errorf("普通输出不受影响: %v", err)
	}
}

func sha256Hex(b []byte) string {
	s, err := hash.Sum(bytes.NewReader(b), hash.SHA256)
	if err != nil {
		return ""
	}
	return s
}
