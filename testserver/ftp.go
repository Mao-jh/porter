// ftp.go 提供极简回环 FTP 测试服务端（测试基础设施，H-3 仅绑定 127.0.0.1）。
// 与 HTTP 服务端共用同一文件目录（CreateFile 产物），供跨协议 sha256 比对。
// 支持命令：USER/PASS/TYPE/EPSV/PASV/SIZE/REST/RETR/QUIT——覆盖客户端全部路径。
// 路径包含检查防目录穿越；数据传输可限速（复现「下载中途中断」场景）。
package testserver

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FTPServer 回环 FTP 服务端。
type FTPServer struct {
	dir  string
	rate int64 // 数据传输限速（字节/秒，0=不限）

	ln      net.Listener
	mu      sync.Mutex
	closed  bool
	session sync.WaitGroup
}

// NewFTPServer 启动 FTP 服务端（127.0.0.1 随机端口）。
// dir 为空时自动创建临时目录（与 HTTP 服务端同兜底，修复空 -dir 全 550）。
func NewFTPServer(dir string, rate int64) (*FTPServer, error) {
	if dir == "" {
		var err error
		dir, err = os.MkdirTemp("", "dlftp")
		if err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &FTPServer{dir: dir, rate: rate, ln: ln}
	s.session.Add(1)
	go s.acceptLoop()
	return s, nil
}

// URL 返回服务端基地址（ftp://127.0.0.1:<port>）。
func (s *FTPServer) URL() string { return "ftp://" + s.ln.Addr().String() }

// FileURL 返回指定名称文件的 FTP URL。
func (s *FTPServer) FileURL(name string) string { return s.URL() + "/" + name }

// Close 停止接受新连接并等待会话退出。
func (s *FTPServer) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	_ = s.ln.Close()
	s.session.Wait()
	return nil
}

func (s *FTPServer) acceptLoop() {
	defer s.session.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.session.Add(1)
		go func() {
			defer s.session.Done()
			s.serveConn(conn)
		}()
	}
}

// resolve 将 FTP 路径解析为目录内文件路径（防目录穿越）。
func (s *FTPServer) resolve(p string) (string, error) {
	rel := filepath.FromSlash(strings.TrimPrefix(p, "/"))
	cp := filepath.Clean(filepath.Join(s.dir, rel))
	root := filepath.Clean(s.dir)
	if cp != root && !strings.HasPrefix(cp, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("路径越界")
	}
	return cp, nil
}

// writeDirListing 将目录内容写入数据连接：mlsd=true 用 RFC 3659 facts 行，
// 否则用 Unix 风格 LIST（权限/大小/时间）。
func (s *FTPServer) writeDirListing(dc net.Conn, dir string, mlsd bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(dc)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if mlsd {
			kind := "file"
			if e.IsDir() {
				kind = "dir"
			}
			fmt.Fprintf(bw, "type=%s;size=%d;modify=%s; %s\r\n",
				kind, info.Size(), info.ModTime().Format("20060102150405"), e.Name())
		} else {
			perm := "-rw-r--r--"
			if e.IsDir() {
				perm = "drwxr-xr-x"
			}
			fmt.Fprintf(bw, "%s 1 porter porter %12d %s %s\r\n",
				perm, info.Size(), info.ModTime().Format("Jan _2 15:04"), e.Name())
		}
	}
	return bw.Flush()
}

// serveConn 处理一条控制连接（每连接独立会话）。
func (s *FTPServer) serveConn(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	bw := bufio.NewWriter(conn)
	write := func(format string, args ...any) {
		fmt.Fprintf(bw, format+"\r\n", args...)
		bw.Flush()
	}
	write("220 Porter test FTP ready")

	var rest int64
	var dataLn net.Listener
	defer func() {
		if dataLn != nil {
			dataLn.Close()
		}
	}()

	for {
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch strings.ToUpper(fields[0]) {
		case "USER":
			write("331 any password ok")
		case "PASS":
			write("230 logged in")
		case "TYPE":
			write("200 Type set")
		case "QUIT":
			write("221 bye")
			return
		case "EPSV":
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				write("425 cannot open data connection")
				continue
			}
			if dataLn != nil {
				dataLn.Close()
			}
			dataLn = ln
			write("229 Entering Extended Passive Mode (|||%d|)", ln.Addr().(*net.TCPAddr).Port)
		case "PASV":
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				write("425 cannot open data connection")
				continue
			}
			if dataLn != nil {
				dataLn.Close()
			}
			dataLn = ln
			port := ln.Addr().(*net.TCPAddr).Port
			write("227 Entering Passive Mode (127,0,0,1,%d,%d)", port/256, port%256)
		case "SIZE":
			if len(fields) < 2 {
				write("501 missing path")
				continue
			}
			path, rerr := s.resolve(fields[1])
			if rerr != nil {
				write("550 access denied")
				continue
			}
			if info, err := os.Stat(path); err == nil {
				write("213 %d", info.Size())
			} else {
				write("550 no such file")
			}
		case "REST":
			if len(fields) < 2 {
				write("501 missing offset")
				continue
			}
			n, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil || n < 0 {
				write("501 invalid offset")
				continue
			}
			rest = n
			write("350 resting at %d", n)
		case "MLSD", "LIST": // 目录列取（porter ls 用；数据连接发送，226 结束）
			path := s.dir
			if len(fields) >= 2 && fields[1] != "-a" {
				if p, rerr := s.resolve(fields[1]); rerr == nil {
					if info, err := os.Stat(p); err == nil && info.IsDir() {
						path = p
					}
				}
			}
			if dataLn == nil {
				write("425 no data connection")
				continue
			}
			write("150 Opening data connection")
			dc, err := dataLn.Accept()
			if err == nil {
				_ = s.writeDirListing(dc, path, strings.ToUpper(fields[0]) == "MLSD")
				dc.Close()
			}
			write("226 Directory send OK")
		case "RETR":
			path := ""
			if len(fields) >= 2 {
				path = fields[1]
			}
			resolved, rerr := s.resolve(path)
			if rerr != nil {
				write("550 access denied")
				continue
			}
			f, err := os.Open(resolved)
			if err != nil {
				write("550 no such file")
				continue
			}
			write("150 opening data connection")
			ln := dataLn
			if ln == nil {
				write("425 no passive listener")
				f.Close()
				continue
			}
			dataLn = nil // 一次性监听器：本次传输后废弃
			_ = conn.SetReadDeadline(time.Time{}) // 传输期间解除控制连接读超时
			ok := s.serveData(ln, f, rest)
			f.Close()
			if ok {
				write("226 transfer complete")
			} else {
				write("426 transfer aborted")
			}
			rest = 0 // RFC 959：传输成功后 REST 复位
		default:
			write("502 not implemented")
		}
	}
}

// serveData 等待数据连接并发送文件 [rest,EOF)（限速可选）。返回传输是否完整。
func (s *FTPServer) serveData(ln net.Listener, f *os.File, rest int64) bool {
	if tl, ok := ln.(*net.TCPListener); ok {
		_ = tl.SetDeadline(time.Now().Add(10 * time.Second))
	}
	data, err := ln.Accept()
	if err != nil {
		return false
	}
	defer data.Close()
	if _, err := f.Seek(rest, io.SeekStart); err != nil {
		return false
	}
	var dst io.Writer = data
	var src io.Reader = f
	if s.rate > 0 {
		dst = &throttleWriter{w: data, rate: s.rate, start: time.Now()}
		// (*os.File).WriteTo（Go 1.22 sendfile 快路径）会绕过 dst 包装直写 socket，
		// 必须剥离 WriterTo 才能让 CopyBuffer 走缓冲循环、限速真正生效。
		src = noWriteTo{f}
	}
	n, err := io.CopyBuffer(dst, src, make([]byte, 64<<10))
	if err != nil {
		return false
	}
	return n > 0 || rest == 0 // 有字节发出，或空文件的完整传输
}

// noWriteTo 仅暴露 Read 的 Reader（剥离 WriterTo 快路径，见 serveData 注释）。
type noWriteTo struct{ r io.Reader }

func (n noWriteTo) Read(p []byte) (int, error) { return n.r.Read(p) }
