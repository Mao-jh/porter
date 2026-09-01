// Command downloader-tui 是下载器的终端界面入口。
//
// 用法：
//
//	downloader-tui [-state-dir DIR] [-limit bps] [-verify algo] [-mode default|max] [-n shards]
//	               [-url URL]... [-out DIR] [-selftest]
//
// 交互：a 添加任务 / ↑↓ 选择 / p 暂停·继续 / d 删除 / x 配置代理出口 / q 退出。
// 安全边界 (H-3)：默认仅允许回环地址；公网链接需显式代理（-proxy 或界面内按 x），
// 设置代理即视为允许出站。
// --selftest：无头模式（预置 -url 自动下载，全部终态后自动退出，退出码 0=全部成功）。
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Mao-jh/porter/cli"
	"github.com/Mao-jh/porter/hash"
	"github.com/Mao-jh/porter/scheduler"
	"github.com/Mao-jh/porter/tui"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	var (
		stateDir = flag.String("state-dir", ".downloader-tui", "状态根目录（每任务一个子目录）")
		limit    = flag.Int64("limit", 0, "全局下载限速 字节/秒（0=不限）")
		verify   = flag.String("verify", "sha256", "校验算法: sha256|sha1|md5|none")
		mode     = flag.String("mode", "default", "CPU 模式: default|max")
		shards   = flag.Int("n", 0, "每任务分片数（0=自动）")
		outDir   = flag.String("out", "", "输出目录（缺省当前目录）")
		proxy    = flag.String("proxy", "", "代理出口（http(s)://host:port 或 socks5://host:port；设置即视为允许出站）")
		ckFile   = flag.String("load-cookies", "", "Netscape cookie.txt 路径（按域匹配注入 Cookie 头）")
		urls     urlList
		selftest = flag.Bool("selftest", false, "无头自检：预置 URL 自动下载，全部终态后退出")
	)
	flag.Var(&urls, "url", "预置任务 URL（可重复，配合 -selftest 或直接启动）")
	flag.Parse()

	base := cli.Options{
		StateDir:   *stateDir,
		Limit:      *limit,
		Shards:     *shards,
		Proxy:      *proxy,
		CookieFile: *ckFile,
	}
	switch *verify {
	case "none":
		base.Verify = ""
	default:
		base.Verify = hash.Algorithm(*verify)
	}
	switch *mode {
	case "max", "maxperf":
		base.Mode = scheduler.ModeMaxPerf
	default:
		base.Mode = scheduler.ModeDefault
	}

	// 引擎的 stderr 输出（[verify]/完成行）重定向到日志文件，避免污染终端界面
	_ = os.MkdirAll(*stateDir, 0o755)
	logf, err := os.OpenFile(filepath.Join(*stateDir, "engine.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err == nil {
		defer logf.Close()
		os.Stderr = logf
	}

	if *outDir != "" {
		_ = os.MkdirAll(*outDir, 0o755)
	}

	m := tui.New(base)
	m.Selftest = *selftest
	if *outDir != "" {
		m.SetOutputDir(*outDir)
	}
	m.RestoreTasks()
	for _, u := range urls {
		if err := m.AddTask(u); err != nil {
			fmt.Fprintln(os.Stderr, "预置任务失败:", err)
			os.Exit(2)
		}
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	if *selftest {
		// 无头：键盘无输入，输出丢弃
		p = tea.NewProgram(m, tea.WithInput(nil), tea.WithOutput(io.Discard))
	}
	final, err := p.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tui:", err)
		os.Exit(1)
	}
	if *selftest {
		tm, ok := final.(tui.Model)
		if !ok || tm.QuitReason != "ok" {
			fmt.Fprintln(os.Stderr, "selftest failed:", quitDetail(tm, ok))
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "selftest ok")
	}
}

func quitDetail(m tui.Model, ok bool) string {
	if !ok {
		return "final model 类型异常"
	}
	var b strings.Builder
	b.WriteString(m.QuitReason)
	for _, t := range m.Tasks() {
		fmt.Fprintf(&b, " [%s %s]", t.Output, t.State)
	}
	return b.String()
}

// urlList 收集可重复 -url。
type urlList []string

func (u *urlList) String() string     { return strings.Join(*u, ",") }
func (u *urlList) Set(v string) error { *u = append(*u, v); return nil }
