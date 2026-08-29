package network

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fileURL 构造三斜杠形式 file:/// URL（Windows 盘符与 Unix 通用）。
func fileURL(p string) string {
	return "file:///" + strings.TrimPrefix(filepath.ToSlash(p), "/")
}

func TestParseFileURL(t *testing.T) {
	t.Parallel()
	// 应当拒绝：非本地 host / opaque 相对形式 / 错误 scheme
	for _, raw := range []string{
		"file://other/x.bin",
		"file:relative.bin",
		"http://127.0.0.1/x",
		"ftp://127.0.0.1/x",
	} {
		if _, err := parseFileURL(raw); err == nil {
			t.Errorf("parseFileURL(%q) 应拒绝", raw)
		}
	}
	// 应当接受：平台正确的绝对路径（host 空 / localhost 两种形式）
	slash := filepath.ToSlash(t.TempDir())
	if !strings.HasPrefix(slash, "/") {
		slash = "/" + slash // Windows 盘符补前导斜杠 → file:///C:/...
	}
	for _, raw := range []string{"file://" + slash + "/x.bin", "file://localhost" + slash + "/x.bin"} {
		if _, err := parseFileURL(raw); err != nil {
			t.Errorf("parseFileURL(%q) err=%v", raw, err)
		}
	}
	// Windows 语义：/tmp/x.bin 缺盘符不是绝对路径 → 拒绝
	if runtime.GOOS == "windows" {
		if _, err := parseFileURL("file:///tmp/x.bin"); err == nil {
			t.Error("Windows 下 /tmp 路径应拒绝")
		}
	}
}

// fileSinkWriterAt 单区间落位 sink：Fetcher 契约规定 FetchRange 从 dst 逻辑偏移 0
// 顺序写入（物理偏移由上层适配器叠加），故每个区间用独立 sink 收集。
type fileSinkWriterAt struct{ buf []byte }

func (w *fileSinkWriterAt) WriteAt(p []byte, off int64) (int, error) {
	if end := off + int64(len(p)); end > int64(len(w.buf)) {
		nb := make([]byte, end)
		copy(nb, w.buf)
		w.buf = nb
	}
	copy(w.buf[off:], p)
	return len(p), nil
}

func TestFileTransport_ProbeAndFetchRange(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "data.bin")
	content := make([]byte, 3<<20)
	for i := range content { // 偏移相关内容，可暴露错位
		content[i] = byte(i*7 + 13)
	}
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	u := fileURL(src)

	ft := NewFileTransport()
	size, ranged, err := ft.Probe(context.Background(), u)
	if err != nil || !ranged || size != int64(len(content)) {
		t.Fatalf("Probe: size=%d ranged=%v err=%v", size, ranged, err)
	}

	// 目录与不存在路径
	if _, _, err := ft.Probe(context.Background(), fileURL(dir)); err == nil {
		t.Error("目录 Probe 应报错")
	}
	if _, _, err := ft.Probe(context.Background(), fileURL(filepath.Join(dir, "nope.bin"))); err == nil {
		t.Error("不存在路径 Probe 应报错")
	}

	// 乱序分片读取再按序重组（模拟引擎多分片调度）
	fetch := func(s, e int64) []byte {
		t.Helper()
		w := &fileSinkWriterAt{}
		if err := ft.FetchRange(context.Background(), u, s, e, w); err != nil {
			t.Fatalf("FetchRange [%d,%d): %v", s, e, err)
		}
		return w.buf
	}
	mid := fetch(2<<20, 3<<20)
	head := fetch(0, 1<<20)
	tail := fetch(1<<20, 2<<20)
	var got []byte
	got = append(got, head...)
	got = append(got, tail...)
	got = append(got, mid...)
	if !bytes.Equal(got, content) {
		t.Fatalf("重组内容不一致: got %d bytes", len(got))
	}

	// end=0 → 到 EOF
	if rest := fetch(1<<20, 0); !bytes.Equal(rest, content[1<<20:]) {
		t.Fatalf("open-ended 内容不一致: got %d bytes", len(rest))
	}
}
