// Command testserver 启动本地环回（127.0.0.1）测试服务端，供端到端验证使用。
// 用法：
//
//	testserver -dir DIR -name big.bin -size 67108864 -addr 127.0.0.1:54321
//
// 启动后打印文件路径与下载 URL（stdout），Ctrl+C 或 kill 退出。
// 内容为确定性偏移相关模式填充，可通过 PatternFill 复算或直接比对 sha256。
// 默认监听随机端口；-addr 可固定（如 127.0.0.1:54321），便于脚本/文档示例直接引用。
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/Mao-jh/porter/testserver"
)

func main() {
	dir := flag.String("dir", "", "测试文件目录（默认临时目录）")
	name := flag.String("name", "file.bin", "测试文件名")
	size := flag.Int64("size", 10<<20, "测试文件字节数")
	limit := flag.Int64("limit", 0, "限速（字节/秒，0=不限）；用于复现下载中途中断")
	useFTP := flag.Bool("ftp", false, "同时启动 FTP 服务端（额外打印 ftp:// URL）")
	extra := flag.String("extra", "", "附加测试文件 \"名称:字节数[,名称2:字节数2...]\"（如 master 变体需要的 tiny.bin）")
	addr := flag.String("addr", "", "监听地址（默认 127.0.0.1:0 随机端口；固定如 127.0.0.1:54321 供脚本复用）")
	flag.Parse()

	cfg := testserver.Config{LimitBytesPerSec: *limit, Addr: *addr}
	if *dir != "" {
		abs, err := filepath.Abs(*dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "目录解析失败:", err)
			os.Exit(2)
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "创建目录失败:", err)
			os.Exit(2)
		}
		cfg.Dir = abs
	} else {
		// 无 -dir 时预先建临时目录并写入 cfg.Dir：New(cfg) 是值传递，其内部
		// 兜底 MkdirTemp 只改副本，传回不了——若此处不提前建，HTTP 与 FTP
		// 会各建各的临时目录（FTP 拿到空目录，SIZE/RETR 全 550）。
		tmp, err := os.MkdirTemp("", "dltest")
		if err != nil {
			fmt.Fprintln(os.Stderr, "创建临时目录失败:", err)
			os.Exit(2)
		}
		cfg.Dir = tmp
	}
	s, err := testserver.New(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "启动服务端失败:", err)
		os.Exit(1)
	}
	path, err := s.CreateFile(*name, *size)
	if err != nil {
		fmt.Fprintln(os.Stderr, "创建测试文件失败:", err)
		os.Exit(1)
	}
	fmt.Printf("file=%s\nsize=%d\nurl=%s\n", path, *size, s.FileURL(*name))

	if *extra != "" { // 附加测试文件（HLS 主列表低码率变体等）
		for _, item := range strings.Split(*extra, ",") {
			kv := strings.SplitN(strings.TrimSpace(item), ":", 2)
			if len(kv) != 2 {
				continue
			}
			n, err := strconv.ParseInt(kv[1], 10, 64)
			if err != nil || n < 0 {
				continue
			}
			if _, err := s.CreateFile(kv[0], n); err != nil {
				fmt.Fprintf(os.Stderr, "创建附加文件 %s 失败: %v\n", kv[0], err)
				os.Exit(1)
			}
		}
	}

	if *useFTP {
		ftp, err := testserver.NewFTPServer(cfg.Dir, *limit)
		if err != nil {
			fmt.Fprintln(os.Stderr, "启动 FTP 服务端失败:", err)
			os.Exit(1)
		}
		fmt.Printf("ftpurl=%s\n", ftp.FileURL(*name))
		defer ftp.Close()
	}

	// 阻塞至收到退出信号（服务端在 goroutine 中持续运行）
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c
	_ = s.Close()
}
