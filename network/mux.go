// mux.go 实现协议无关的下载接口与 scheme 分发（第 12 轮新增契约，DESIGN §2.3b；
// 第 13 轮扩展 file://）。
// Fetcher 与 *Transport.FetchRange/Probe 同构：cli 引擎面向接口编程，
// http/https → Transport，ftp/ftps → FTPTransport，file → FileTransport，
// 其余 scheme 在入口拒绝（HLS/Metalink 属 http(s) 内容形态，由 cli 在此层之上包装）。
// 既有契约不变（只增不改）：NewTransport/SetFaults/FetchRange 原样保留。
package network

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// urlScheme 提取 URL scheme（小写）；解析失败返回空串（后续按不支持处理）。
func urlScheme(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Scheme)
}

// validateScheme 校验 URL 语法与 scheme 白名单，返回人类可读错误。
// AI-first：区分「URL 语法非法」与「scheme 不支持」，避免误导性报错
//（如 http://[::1 解析失败时 scheme 为空串，若只报 unsupported scheme 会让
// 使用者误以为是协议问题而非 URL 本身写错）。
func validateScheme(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return fmt.Errorf("URL 语法非法: %q（无法解析）", raw)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "ftp", "ftps", "file":
		return nil
	default:
		return fmt.Errorf("unsupported scheme: %q (支持: http/https/ftp/ftps/file)", strings.ToLower(u.Scheme))
	}
}

// Fetcher 协议无关的下载接口（cli 引擎依赖的最小协议面）。
type Fetcher interface {
	// Probe 探测资源：返回 (大小, 是否支持 Range, 错误)；size=0 表示未知（流式）。
	Probe(ctx context.Context, urlStr string) (int64, bool, error)
	// FetchRange 下载 [start,end) 写入 dst；end=0 表示到 EOF；start=end=0 为完整下载。
	FetchRange(ctx context.Context, urlStr string, start, end int64, dst io.WriterAt) error
}

// Mux 按 scheme 分发到对应传输层。全局限速配额跨协议共享：
// FTP 复用 HTTP Transport 的 rateLimiter（同一令牌池，聚合速率仍受 -limit 约束）。
type Mux struct {
	httpT *Transport
	ftpT  *FTPTransport
	fileT *FileTransport
}

// NewMux 构造协议分发器。allowRemote 仅作用于 FTP 侧拨号（HTTP 侧由传入的
// httpT 自身配置决定，保持既有 H-3 语义与测试注入路径不变）。
func NewMux(httpT *Transport, allowRemote bool) *Mux {
	return &Mux{httpT: httpT, ftpT: NewFTPTransport(allowRemote), fileT: NewFileTransport()}
}

// Probe 按 scheme 分发探测。
func (m *Mux) Probe(ctx context.Context, urlStr string) (int64, bool, error) {
	scheme := urlScheme(urlStr)
	switch scheme {
	case "http", "https":
		return m.httpT.Probe(ctx, urlStr)
	case "ftp", "ftps":
		return m.ftpT.Probe(ctx, urlStr)
	case "file":
		return m.fileT.Probe(ctx, urlStr)
	default:
		return 0, false, validateScheme(urlStr)
	}
}

// FetchRange 按 scheme 分发区间下载；FTP 侧注入共享限速器。
func (m *Mux) FetchRange(ctx context.Context, urlStr string, start, end int64, dst io.WriterAt) error {
	switch urlScheme(urlStr) {
	case "http", "https":
		return m.httpT.FetchRange(ctx, urlStr, start, end, dst)
	case "ftp", "ftps":
		return m.ftpT.fetchRange(ctx, urlStr, start, end, m.httpT.sharedLimiter(), dst)
	case "file":
		return m.fileT.FetchRange(ctx, urlStr, start, end, dst)
	default:
		return validateScheme(urlStr)
	}
}
