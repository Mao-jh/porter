package mcpserver_test

import (
	"testing"

	"github.com/Mao-jh/downloader/testserver"
)

// tsHelper 测试服务端包装（确定性模式内容，可复算 sha256）。
type tsHelper struct{ s *testserver.Server }

// NewForTest 启动环回测试服务端并预置测试文件。
func NewForTest(t *testing.T) *tsHelper {
	t.Helper()
	s, err := testserver.New(testserver.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("testserver.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	h := &tsHelper{s}
	h.mustCreate(t, "f.bin", 4<<20)
	h.mustCreate(t, "big.bin", 8<<20)
	return h
}

func (h *tsHelper) mustCreate(t *testing.T, name string, size int64) {
	t.Helper()
	if _, err := h.s.CreateFile(name, size); err != nil {
		t.Fatalf("CreateFile %s: %v", name, err)
	}
}

// FileURL 文件下载地址。
func (h *tsHelper) FileURL(name string) string { return h.s.FileURL(name) }

// ChecksumHex 服务端文件的 sha256（流式计算）。
func (h *tsHelper) ChecksumHex(name string) string {
	sum, err := h.s.Checksum(name)
	if err != nil {
		return ""
	}
	return sum
}

// LimitBytes 运行时限速（字节/秒）。
func (h *tsHelper) LimitBytes(n int64) { h.s.SetLimit(n) }
