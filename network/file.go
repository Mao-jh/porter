// file.go 实现 file:// 本地文件传输层（第 13 轮新增，DESIGN §2.3b）。
// 语义与 HTTP/FTP 路径同构（Fetcher 契约）：Probe 返回 (大小, true, nil)，
// FetchRange 下载 [start,end)（end=0 到 EOF），经 Mux 分发。
// 安全合规：无网络行为（不在 H-3 回环约束范围），但路径解析从严——
// host 必须为空或 localhost；仅接受绝对路径；拒绝 opaque/带 fragment 之外的部分按 URL 语义丢弃。
package network

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// FileTransport 本地文件传输层（无状态，可共享）。
type FileTransport struct{}

// NewFileTransport 构造 file:// 传输层。
func NewFileTransport() *FileTransport { return &FileTransport{} }

// parseFileURL 解析并校验 file:// URL，返回本地绝对路径。
func parseFileURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("unsupported scheme: %s (file)", u.Scheme)
	}
	if u.Opaque != "" {
		return "", fmt.Errorf("file: 不支持不带 // 的相对形式: %q", raw)
	}
	if h := strings.ToLower(u.Host); h != "" && h != "localhost" {
		return "", fmt.Errorf("file: 非本地主机 %q（仅接受空或 localhost）", u.Host)
	}
	p := u.Path
	// Windows 盘符形式：file:///C:/x → u.Path="/C:/x"，剥去前导斜杠归一为 "C:/x"。
	if runtime.GOOS == "windows" && len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	if p == "" {
		return "", fmt.Errorf("file: 缺少路径")
	}
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("file: 仅接受绝对路径: %q", p)
	}
	return p, nil
}

// Probe 探测本地文件：返回 (大小, true, nil)。目录与非存在路径均报错。
func (f *FileTransport) Probe(_ context.Context, urlStr string) (int64, bool, error) {
	p, err := parseFileURL(urlStr)
	if err != nil {
		return 0, false, err
	}
	info, err := os.Stat(p)
	if err != nil {
		return 0, false, err
	}
	if info.IsDir() {
		return 0, false, fmt.Errorf("file: %s 是目录", p)
	}
	return info.Size(), true, nil
}

// FetchRange 读取 [start,end) 写入 dst（end=0 表示到文件尾）。
// 本地读取无阻塞点，不支持取消中断（ctx 仅在入口检查一次）。
func (f *FileTransport) FetchRange(ctx context.Context, urlStr string, start, end int64, dst io.WriterAt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := parseFileURL(urlStr)
	if err != nil {
		return err
	}
	fh, err := os.Open(p)
	if err != nil {
		return err
	}
	defer fh.Close()
	if start > 0 {
		if _, err := fh.Seek(start, io.SeekStart); err != nil {
			return err
		}
	}
	expected := int64(-1)
	src := io.Reader(fh)
	if end > start {
		expected = end - start
		src = io.LimitReader(fh, expected) // 有界区间严格限长（与 HTTP/FTP 同约定）
	}
	return writeWriterAt(dst, src, expected)
}
