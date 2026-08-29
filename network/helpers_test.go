package network

import (
	"testing"

	"github.com/Mao-jh/downloader/testserver"
)

// loopbackServer 包装 testserver 供网络层测试使用（仅测试路径引用，生产不依赖）。
type loopbackServer struct{ s *testserver.Server }

// startTestServer 启动本地环回测试服务端。
func startTestServer(t *testing.T) (*loopbackServer, func()) {
	t.Helper()
	s, err := testserver.New(testserver.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("testserver.New: %v", err)
	}
	return &loopbackServer{s}, func() { _ = s.Close() }
}

// CreateFile 创建确定性模式填充的测试文件，返回名称。
func (l *loopbackServer) CreateFile(t *testing.T, name string, size int64) string {
	t.Helper()
	if _, err := l.s.CreateFile(name, size); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	return name
}

// URL 返回文件下载地址。
func (l *loopbackServer) URL(name string) string { return l.s.FileURL(name) }
