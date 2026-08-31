// Command porter 是 Porter 下载器的 CLI 入口。
// 子命令：tasks（列出持久化任务）/ rm（删除任务）/ clean（清理完成记录）/
// probe（探测资源，不下载）；默认动作：下载。
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

	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "tasks", "status": // 列出持久化任务与历史
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
		case "rm": // 删除指定任务（拒绝运行中且有 .part 的任务）
			dir, ids := subArgs(os.Args[2:])
			removed, refused, err := cli.RemoveTasks(dir, ids, false)
			if err != nil {
				fmt.Fprintln(os.Stderr, "rm 失败:", err)
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "已删除 %d 个任务\n", removed)
			for _, r := range refused {
				fmt.Fprintln(os.Stderr, "跳过:", r)
			}
			if len(refused) > 0 {
				os.Exit(1)
			}
			return
		case "clean": // 清理全部 status=done 的完成记录
			dir, _ := subArgs(os.Args[2:])
			removed, refused, err := cli.RemoveTasks(dir, nil, true)
			if err != nil {
				fmt.Fprintln(os.Stderr, "clean 失败:", err)
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "已清理 %d 个完成记录\n", removed)
			for _, r := range refused {
				fmt.Fprintln(os.Stderr, "跳过:", r)
			}
			if len(refused) > 0 {
				os.Exit(1)
			}
			return
		case "probe": // 探测资源：size / ranged / name（不下载）
			opt, err := cli.Parse(os.Args[2:])
			if err != nil {
				fmt.Fprintln(os.Stderr, "参数错误:", err)
				os.Exit(2)
			}
			if err := cli.RunProbe(ctx, opt); err != nil {
				fmt.Fprintln(os.Stderr, "探测失败:", err)
				os.Exit(1)
			}
			return
		case "retry": // 续传重跑状态目录中的未完成任务
			opt, err := cli.ParseRetry(os.Args[2:])
			if err != nil {
				fmt.Fprintln(os.Stderr, "参数错误:", err)
				os.Exit(2)
			}
			if err := cli.RunRetry(ctx, opt); err != nil {
				fmt.Fprintln(os.Stderr, "retry 失败:", err)
				os.Exit(1)
			}
			return
		}
	}

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, `用法:
  porter <url> [url2 ...] [-o output] [-n shards] [-j jobs] [-i urls.txt] [-limit bps]
         [-proxy http(s)://host:port|socks5://] [-load-cookies cookies.txt] [-summary]
         [-H "K: V"] [-mode default|max] [-verify sha256] [-state-dir DIR]
  子命令:
  porter tasks [-state-dir DIR]      # 列出持久化任务与历史
  porter rm <id>... [-state-dir DIR] # 删除指定任务（拒绝运行中且有 .part 的任务）
  porter clean [-state-dir DIR]      # 清理全部 status=done 的完成记录
  porter probe <url> [-proxy URL] [-load-cookies file] [-H "K: V"]  # 只探测不下载
  porter retry [-state-dir DIR] [-limit bps] [-proxy URL] [-load-cookies file]  # 续传重跑未完成任务`)
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

// subArgs 解析子命令参数：提取 -state-dir 值，其余非旗标参数为任务 ID。
func subArgs(args []string) (dir string, ids []string) {
	dir = ".downloader"
	for i := 0; i < len(args); i++ {
		if args[i] == "-state-dir" && i+1 < len(args) {
			dir = args[i+1]
			i++
			continue
		}
		ids = append(ids, args[i])
	}
	return dir, ids
}
