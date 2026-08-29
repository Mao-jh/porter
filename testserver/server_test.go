package testserver

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/nymjin22/downloader/hash"
)

// TestServer_Range206 验证 Range 请求返回 206 与正确 Content-Range（§6）。
func TestServer_Range206(t *testing.T) {
	s, err := New(Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	if _, err := s.CreateFile("seg.bin", 1<<20); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, s.FileURL("seg.bin"), nil)
	req.Header.Set("Range", "bytes=1024-2047") // 1 KiB
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("应返回 206, got %d", resp.StatusCode)
	}
	want := "bytes 1024-2047/1048576"
	if got := resp.Header.Get("Content-Range"); got != want {
		t.Errorf("Content-Range=%q want %q", got, want)
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) != 1024 {
		t.Errorf("返回字节数=%d want 1024", len(body))
	}
}

// TestServer_Resume_SHA256 正常下载 + 断点续传后 sha256 一致（S-3 前置校验）。
func TestServer_Resume_SHA256(t *testing.T) {
	s, err := New(Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	size := int64(10 << 20) // 10 MiB
	if _, err := s.CreateFile("big.bin", size); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	expected, err := s.Checksum("big.bin")
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}

	// 模拟两段 Range 下载拼接（断点续传的等价操作）
	seg1, _ := fetchRange(t, s.FileURL("big.bin"), 0, size/2)
	seg2, _ := fetchRange(t, s.FileURL("big.bin"), size/2, size)
	combined := bytes.NewReader(append(seg1, seg2...))
	got, err := hash.Sum(combined, hash.SHA256)
	if err != nil {
		t.Fatalf("hash.Sum: %v", err)
	}
	if got != expected {
		t.Errorf("拼接后 sha256 不一致 (断点续传前提校验失败)\ngot  %s\nwant %s", got, expected)
	}
}

// TestServer_FaultInjection 故障注入：429 / 5xx 触发对应状态码；reset 关闭连接。
func TestServer_FaultInjection(t *testing.T) {
	s, err := New(Config{Dir: t.TempDir(), FaultCount: 3})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	s.SetFaults(3)
	for _, tp := range []string{"429", "5xx", "reset"} {
		resp, err := http.Get(s.baseURL + "/fault?type=" + tp)
		if err != nil {
			// reset 类型由服务端 panic(abort) 关闭连接，客户端收 error 属正常
			if tp != "reset" {
				t.Logf("type=%s client err=%v (可接受)", tp, err)
			}
			continue
		}
		resp.Body.Close()
		if tp == "429" && resp.StatusCode != 429 {
			t.Errorf("429 期望状态码 429, got %d", resp.StatusCode)
		}
		if tp == "5xx" && resp.StatusCode != 500 {
			t.Errorf("5xx 期望 500, got %d", resp.StatusCode)
		}
	}
	if s.faults.Load() != 0 {
		t.Errorf("故障计数器应为 0, got %d", s.faults.Load())
	}
}

// fetchRange 拉取 [start,end) 区间字节（半开，与服务端约定一致）。
func fetchRange(t *testing.T, urlStr string, start, end int64) ([]byte, error) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, urlStr, nil)
	req.Header.Set("Range", "bytes="+itoa(start)+"-"+itoa(end-1))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// itoa 避免 strconv 依赖噪音（保持本文件轻量）。
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
