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
		case "probe": // 探测资源：size / ranged / name / final_url（不下载）
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
		case "meta": // 查看响应头：状态行 + key: value（对标 curl -I）
			opt, err := cli.Parse(os.Args[2:])
			if err != nil {
				fmt.Fprintln(os.Stderr, "参数错误:", err)
				os.Exit(2)
			}
			if err := cli.RunMeta(ctx, opt); err != nil {
				fmt.Fprintln(os.Stderr, "meta 失败:", err)
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
		case "find": // 抓取页面提取可下载链接
			if err := cli.RunFind(ctx, os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "find 失败:", err)
				os.Exit(1)
			}
			return
		case "ls": // FTP 目录列取
			if err := cli.RunLS(ctx, os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "ls 失败:", err)
				os.Exit(1)
			}
			return
		case "bookmarks": // 解析浏览器书签导出 HTML
			if err := cli.RunBookmarks(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "bookmarks 失败:", err)
				os.Exit(1)
			}
			return
		case "extract": // 从文本提取 URL
			if err := cli.RunExtract(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "extract 失败:", err)
				os.Exit(1)
			}
			return
		case "torrent": // 解析 .torrent / 磁力链接
			if err := cli.RunTorrent(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "torrent 失败:", err)
				os.Exit(1)
			}
			return
		case "info": // 媒体信息预览
			if err := cli.RunInfo(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "info 失败:", err)
				os.Exit(1)
			}
			return
		case "transcode": // ffmpeg 转码
			if err := cli.RunTranscode(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "transcode 失败:", err)
				os.Exit(1)
			}
			return
		case "organize": // 按类型归类整理
			if err := cli.RunOrganize(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "organize 失败:", err)
				os.Exit(1)
			}
			return
		case "scrub": // 广告/垃圾文件移入 .trash（文件级去广告）
			if err := cli.RunClean(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "scrub 失败:", err)
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
  porter meta <url> [-proxy URL] [-load-cookies file] [-H "K: V"]   # 查看响应头（curl -I 对标）
  porter retry [-state-dir DIR] [-limit bps] [-proxy URL] [-load-cookies file]  # 续传重跑未完成任务
  链接发现:
  porter find <page-url> [-ext mp4,mkv] [-probe] [-depth N] [-out urls.txt]     # 页面提取下载链接
  porter ls <ftp-url> [-l] [-r]                                                 # FTP 目录列取/递归
  porter bookmarks <bookmarks.html> [-out urls.txt]                             # 浏览器书签导出
  porter extract <file|-> [-out urls.txt]                                        # 文本中提取 URL
  porter torrent <file.torrent|magnet:...>                                       # 种子/磁力解析（含 WebSeed）
  下载后处理:
  porter info <file>                                                            # 媒体信息预览（时长/分辨率/编码）
  porter transcode <file> -to mp3|mp4|... [-crf N] [-out dir]                    # ffmpeg 转码（需系统 ffmpeg）
  porter organize <dir> [-dry-run] [-dedupe]                                     # 按类型归类整理
  porter scrub <dir> [-dry-run]                                                 # 广告/垃圾文件移入 .trash`)
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
