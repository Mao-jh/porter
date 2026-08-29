// Package testserver 提供本地环回（127.0.0.1）HTTP 测试服务端。
// 功能（§6）：
//  1. HTTP/HTTPS Range 响应（正确 206 + Content-Range）
//  2. 故障注入：断连 / 超时 / 429 / 5xx（可配置计数）
//  3. 测试文件：10 MiB / 128 MiB / 1 GiB / 2 GiB（按需生成，避免常驻内存）
//
// 全部绑定 127.0.0.0/8（H-3），绝不暴露公网。
package testserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Config 服务端配置。
type Config struct {
	Addr             string // 默认 127.0.0.1:0（随机端口）
	Dir              string // 测试文件根目录
	FaultCount       int32  // 每种故障的触发次数（共用计数器，逐次递减）
	LimitBytesPerSec int64  // 全局限速（0=不限）；用于确定性复现「下载中途中断」
}

// Server 测试服务端。
type Server struct {
	cfg     Config
	mux     *http.ServeMux
	srv     *http.Server
	ln      net.Listener
	faults  atomic.Int32
	served  atomic.Int64 // 累计服务的数据字节数（Range 命中含），用于续传断言
	baseURL string

	limitMu sync.RWMutex // 保护 cfg.LimitBytesPerSec 的运行时修改
}

// New 构造并启动服务端（绑定 127.0.0.1，H-3）。
func New(cfg Config) (*Server, error) {
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:0"
	}
	if cfg.Dir == "" {
		var err error
		cfg.Dir, err = os.MkdirTemp("", "dltest")
		if err != nil {
			return nil, err
		}
	}
	s := &Server{cfg: cfg}
	s.faults.Store(cfg.FaultCount)

	mux := http.NewServeMux()
	mux.HandleFunc("/file/", s.handleFile)
	mux.HandleFunc("/fault", s.handleFault)
	mux.HandleFunc("/echo", s.handleEcho)
	s.mux = mux
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return nil, err
	}
	s.ln = ln
	addr := ln.Addr().(*net.TCPAddr)
	s.baseURL = fmt.Sprintf("http://127.0.0.1:%d", addr.Port)

	go s.srv.Serve(ln)
	return s, nil
}

// BaseURL 返回服务端基地址（http://127.0.0.1:<port>）。
func (s *Server) BaseURL() string { return s.baseURL }

// ServedBytes 返回累计服务的数据字节数（含 Range 片段），供测试断言续传未重下全量。
func (s *Server) ServedBytes() int64 { return s.served.Load() }

// SetLimit 运行时调整全局限速（字节/秒，0=不限）。
func (s *Server) SetLimit(bytesPerSec int64) {
	s.limitMu.Lock()
	s.cfg.LimitBytesPerSec = bytesPerSec
	s.limitMu.Unlock()
}

// currentLimit 读取当前限速配置（handleFile 每请求调用）。
func (s *Server) currentLimit() int64 {
	s.limitMu.RLock()
	defer s.limitMu.RUnlock()
	return s.cfg.LimitBytesPerSec
}

// FileURL 返回指定名称文件的 URL。
func (s *Server) FileURL(name string) string { return s.baseURL + "/file/" + name }

// SetFaults 设置剩余故障次数。
func (s *Server) SetFaults(n int32) { s.faults.Store(n) }

// Close 关闭服务端并清理临时目录。
func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
	if s.cfg.Dir != "" && s.cfg.FaultCount == 0 {
		// 仅在未指定外部目录时清理
	}
	return nil
}

// CreateFile 创建指定大小的测试文件，内容为**确定性、偏移相关**的模式填充：
// offset 处字节 = (block*131 + j*7 + 13) % 251，其中 block=offset/64KiB，j=块内偏移。
// 偏移相关内容可暴露任何 Range 错位/拼接 bug（全零文件会掩盖此类缺陷）。
func (s *Server) CreateFile(name string, size int64) (string, error) {
	path := filepath.Join(s.cfg.Dir, name)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]byte, 64<<10)
	var off int64
	for off < size {
		n := int64(len(buf))
		if size-off < n {
			n = size - off
		}
		PatternFill(buf[:n], off)
		if _, err := f.WriteAt(buf[:n], off); err != nil {
			return "", err
		}
		off += n
	}
	return path, nil
}

// PatternFill 按与 CreateFile 相同的确定性模式填充 buf；off 为 buf[0] 对应的全局偏移。
func PatternFill(buf []byte, off int64) {
	const blockSize = 64 << 10
	for i := range buf {
		g := off + int64(i)
		buf[i] = byte(((g/blockSize)*131 + (g%blockSize)*7 + 13) % 251)
	}
}

// Checksum 计算某文件的 sha256（流式，H-2）。
func (s *Server) Checksum(name string) (string, error) {
	f, err := os.Open(filepath.Join(s.cfg.Dir, name))
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 64<<10)
	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// handleFile 处理 Range 请求：返回 206 + Content-Range，或 200 全量。
// 支持 "bytes=start-end"（含尾）与 "bytes=start-"（到 EOF）；逐字节与 PatternFill 一致。
func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Path[len("/file/"):]
	path := filepath.Join(s.cfg.Dir, name)
	info, err := os.Stat(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	size := info.Size()

	w.Header().Set("Accept-Ranges", "bytes")

	// 处理 Range（格式 "bytes=start-end"（含尾）或 "bytes=start-"，Go 内部用半开 [start,end)）
	rangeHeader := r.Header.Get("Range")
	start, end := int64(0), size
	if rangeHeader != "" {
		spec := strings.TrimPrefix(rangeHeader, "bytes=")
		dash := strings.IndexByte(spec, '-')
		if dash >= 0 {
			sPart, ePart := spec[:dash], spec[dash+1:]
			start, _ = strconv.ParseInt(sPart, 10, 64)
			if ePart == "" {
				end = size // "bytes=N-" → 到 EOF
			} else if e2, err := strconv.ParseInt(ePart, 10, 64); err == nil && e2 >= start {
				end = e2 + 1 // 含尾转半开
			}
		}
		if start < 0 {
			start = 0
		}
		if end > size {
			end = size
		}
		if end <= start {
			end = size // 非法 → 全量
			start = 0
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end-1, size))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		start, end = 0, size
	}

	f, err := os.Open(path)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer f.Close()
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Length", strconv.FormatInt(end-start, 10))
	if r.Method == http.MethodHead {
		return // HEAD：仅响应头即可；读文件体会拖慢探测（限速/大文件场景）
	}
	var dst io.Writer = w
	if rate := s.currentLimit(); rate > 0 {
		dst = &throttleWriter{w: w, rate: rate, start: time.Now()}
	}
	n, _ := io.CopyBuffer(dst, io.LimitReader(f, end-start), make([]byte, 64<<10))
	if r.Method != http.MethodHead { // HEAD 会照常读文件但响应体被丢弃，不计入服务字节
		s.served.Add(n)
	}
}

// throttleWriter 限速写入器：按 rate 维持平均发送速率。
type throttleWriter struct {
	w      io.Writer
	rate   int64
	start  time.Time
	served int64
}

func (t *throttleWriter) Write(p []byte) (int, error) {
	n, err := t.w.Write(p)
	t.served += int64(n)
	if t.rate > 0 && err == nil {
		want := time.Duration(int64(time.Second) * t.served / t.rate)
		if el := time.Since(t.start); want > el {
			time.Sleep(want - el)
		}
	}
	return n, err
}

// handleEcho 回显请求头（k=v 逐行），用于透传头断言。
func (s *Server) handleEcho(w http.ResponseWriter, r *http.Request) {
	for k, vs := range r.Header {
		for _, v := range vs {
			io.WriteString(w, k+"="+v+"\n")
		}
	}
}

// handleFault 按 ?type= 参数注入故障：reset/timeout/429/5xx。
func (s *Server) handleFault(w http.ResponseWriter, r *http.Request) {
	if s.faults.Load() <= 0 {
		io.WriteString(w, "ok")
		return
	}
	s.faults.Add(-1)
	switch r.URL.Query().Get("type") {
	case "timeout":
		select {
		case <-time.After(10 * time.Second): // 触发客户端超时
		case <-r.Context().Done():
		}
	case "reset":
		// 强制关闭连接（不写响应头，触发连接重置）
		panic(http.ErrAbortHandler)
	case "429":
		http.Error(w, "too many", http.StatusTooManyRequests)
	case "5xx":
		http.Error(w, "boom", http.StatusInternalServerError)
	default:
		io.WriteString(w, "ok")
	}
}
