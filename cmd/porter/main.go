// Command porter 是 Porter 下载器的 CLI 入口。
// 子命令：tasks（列出持久化任务）；默认动作：下载。
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

	// 子命令：porter tasks [-state-dir DIR] —— 列出持久化任务与历史（第 14 轮）
	if len(os.Args) >= 2 && (os.Args[1] == "tasks" || os.Args[1] == "status") {
		dir := ".downloader"
		for i := 2; i+1 < len(os.Args); i++ {
			if os.Args[i] == "-state-dir" {
				dir = os.Args[i+1]
			}
		}
		if err := cli.RunTasks(dir); err != nil {
			fmt.Fprintln(os.Stderr, "tasks 失败:", err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, `用法:
  porter <url> [url2 ...] [-o output] [-n shards] [-j jobs] [-i urls.txt] [-limit bps]
         [-proxy http(s)://host:port|socks5://] [-load-cookies cookies.txt] [-summary]
         [-H "K: V"] [-mode default|max] [-verify sha256] [-state-dir DIR]
  porter tasks [-state-dir DIR]   # 列出持久化任务与历史`)
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
