// Command downloader 是下载工具的可执行入口（阶段1：Linux 二进制；阶段2：.exe）。
// 构建：GOFLAGS=-mod=readonly GOPROXY=off CGO_ENABLED=0 go build -ldflags="-s -w" ./cmd/porter
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Mao-jh/porter/cli"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "用法: downloader <url> [url2 ...] [-o output] [-n shards] [-limit bps] [-H \"K: V\"] [-mode default|max] [-verify sha256]")
		os.Exit(2)
	}
	opt, err := cli.Parse(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "参数错误:", err)
		os.Exit(2)
	}
	if err := cli.Run(ctx, opt); err != nil {
		fmt.Fprintln(os.Stderr, "下载失败:", err)
		os.Exit(1)
	}
}
