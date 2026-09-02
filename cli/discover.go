// discover.go 实现链接发现子命令（第 23 轮，链接发现三件套之一）：
//   find      抓取 HTTP 页面提取可下载链接（-ext 过滤 / -probe 探测大小 / -depth 递归）
//   ls        FTP 目录列取（-l 长格式 / -r 递归收集全站清单）
//   bookmarks 解析 Netscape 书签 HTML（Firefox/Chrome 导出）
//   extract   从任意文本/文件提取 URL（日志、列表页、剪贴板）
//   torrent   解析 .torrent / 磁力链接，输出元数据与 WebSeed 直链
//
// 安全边界与主链路一致：默认仅回环目标（H-3），-proxy 显式允许出站。
// 输出均为脚本友好格式（每行一 URL 或 key=value），可直接喂给 porter -i。
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/Mao-jh/porter/discover"
	"github.com/Mao-jh/porter/network"
)

// parseDiscoverFlags 解析发现子命令共用旗标（-proxy / -load-cookies / -H）。
func parseDiscoverFlags(fs *flag.FlagSet, args []string, posLimit int) (proxy, cookie string, headers map[string]string, pos []string, err error) {
	// AI-first：与 cli.Parse 同策略，吞掉 flag 包自动 usage 打印（避免大段帮助
	// 文本淹没真实错误）；调用方单行输出错误。
	fs.SetOutput(io.Discard)
	args = flagFirst(args)
	pProxy := fs.String("proxy", "", "代理出口（设置即视为允许出站）")
	pCookie := fs.String("load-cookies", "", "Netscape cookie.txt")
	var hdrs headerList
	fs.Var(&hdrs, "H", "透传请求头 \"Key: Value\"")
	if err := fs.Parse(flagFirst(args)); err != nil {
		return "", "", nil, nil, err
	}
	hm, err := headerMap(hdrs)
	if err != nil {
		return "", "", nil, nil, err
	}
	pos = fs.Args()
	if posLimit >= 0 && len(pos) > posLimit {
		return "", "", nil, nil, fmt.Errorf("位置参数过多（最多 %d 个）", posLimit)
	}
	return *pProxy, *pCookie, hm, pos, nil
}

// isBoolFlag 判断发现子命令中的布尔旗标（不带值）。
func isBoolFlag(a string) bool {
	name := strings.TrimLeft(a, "-")
	switch name {
	case "probe", "l", "r", "dry-run", "dedupe", "quiet":
		return true
	}
	return false
}

// flagFirst 把旗标参数集中到位置参数之前（flag 包在首个位置参数后停止解析；
// 所有子命令共用，保证 "子命令 <url> -ext x" 形态可用）。
func flagFirst(args []string) []string {
	var flagArgs, posArgs []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) > 1 && a[0] == '-' {
			flagArgs = append(flagArgs, a)
			if !isBoolFlag(a) && i+1 < len(args) && !(len(args[i+1]) > 1 && args[i+1][0] == '-') {
				flagArgs = append(flagArgs, args[i+1])
				i++
			}
			continue
		}
		posArgs = append(posArgs, a)
	}
	return append(flagArgs, posArgs...)
}

// --- find ---------------------------------------------------------

// RunFind 抓取页面提取链接。输出每行一个 URL；-probe 时附加 "  size=NNN"。
func RunFind(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("find", flag.ContinueOnError)
	ext := fs.String("ext", "", "扩展名过滤（逗号分隔，如 \"mp4,mkv\"；空=全部）")
	probe := fs.Bool("probe", false, "对每个链接 HEAD 探测大小并追加到输出")
	out := fs.String("out", "", "输出文件（默认 stdout；可作 -i 列表文件）")
	depth := fs.Int("depth", 1, "递归抓取深度（1=仅首页；2=首页+其链接的页面）")
	max := fs.Int64("max", 0, "单页抓取上限字节（默认 8MiB）")
	proxy, cookie, headers, pos, err := parseDiscoverFlags(fs, args, 1)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("find: 需要页面 URL（如 porter find http://127.0.0.1:54321/page/）")
	}
	pageURL := pos[0]
	if !startsWithAny(pageURL, "http://", "https://") {
		return fmt.Errorf("find: 仅支持 http(s) 页面（%s）", pageURL)
	}
	filter := discover.ExtFilter(nil)
	if *ext != "" {
		for _, e := range strings.Split(*ext, ",") {
			e = strings.TrimSpace(e)
			if e != "" {
				filter = append(filter, "."+strings.TrimPrefix(e, "."))
			}
		}
	}
	tr, _, err := buildTransport(&Options{Proxy: proxy, CookieFile: cookie, Headers: headers}, false)
	if err != nil {
		return err
	}

	w := io.Writer(os.Stdout)
	closer := io.Closer(nil)
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return fmt.Errorf("find: 创建输出文件失败: %w", err)
		}
		w, closer = f, f
	}
	defer func() {
		if closer != nil {
			_ = closer.Close()
		}
	}()

	// pageSeen：抓取过的页面去重；linkSeen：输出过的资源链接去重（两者独立，
	// 避免「页面链接先被输出、递归时又被跳过」的死锁）。
	pageSeen := map[string]struct{}{}
	linkSeen := map[string]struct{}{}
	queue := []string{pageURL}
	var fetchErrs []error
	for d := 0; d < *depth && len(queue) > 0; d++ {
		level := queue
		queue = nil
		for _, u := range level {
			if _, ok := pageSeen[u]; ok {
				continue
			}
			pageSeen[u] = struct{}{}
			hits, err := discover.FindLinksInPage(ctx, tr, u, *max, filter)
			if err != nil {
				fmt.Fprintf(os.Stderr, "find: 抓取 %s 失败: %v\n", u, err)
				fetchErrs = append(fetchErrs, fmt.Errorf("抓取 %s 失败: %w", u, err))
				continue
			}
			for _, link := range hits.Links {
				if _, ok := linkSeen[link]; ok {
					continue
				}
				linkSeen[link] = struct{}{}
				if *probe {
					size, _, _, perr := ProbeURL(ctx, proxy, cookie, false, headers, link)
					if perr != nil {
						fmt.Fprintf(w, "%s  size=ERR(%v)\n", link, perr)
						continue
					}
					fmt.Fprintf(w, "%s  size=%d\n", link, size)
				} else {
					fmt.Fprintln(w, link)
				}
				if *depth > 1 && startsWithAny(link, "http://", "https://") {
					queue = append(queue, link)
				}
			}
		}
	}
	// AI-first：页面抓取失败必须反映到退出码（此前静默 continue 返回 nil，
	// AI 从 exit=0 无法得知整站发现失败）。部分成功仍返回错误（errors.Join）。
	return errors.Join(fetchErrs...)
}

// --- ls -----------------------------------------------------------

// RunLS 列取 FTP 目录。默认每行一个条目名；-l 长格式；-r 递归收集全部文件。
func RunLS(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	long := fs.Bool("l", false, "长格式（类型 大小 修改时间 名称）")
	recursive := fs.Bool("r", false, "递归列出子目录内全部文件")
	maxDepth := fs.Int("max-depth", 8, "递归最大深度（防环）")
	proxy, _, _, pos, err := parseDiscoverFlags(fs, args, 1)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("ls: 需要 FTP 目录 URL（如 porter ls ftp://127.0.0.1:54321/）")
	}
	dirURL := pos[0]
	if !startsWithAny(dirURL, "ftp://", "ftps://") {
		return fmt.Errorf("ls: 仅支持 ftp(s) 目录（%s）", dirURL)
	}
	if proxy != "" {
		return fmt.Errorf("ls: FTP 目录列取暂不支持代理出口")
	}
	ftp := network.NewFTPTransport(false)

	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		if depth > *maxDepth {
			return nil
		}
		if !strings.HasSuffix(dir, "/") {
			dir += "/"
		}
		entries, err := ftp.ListDir(ctx, dir)
		if err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
		for _, e := range entries {
			if *long {
				kind := "-"
				if e.IsDir {
					kind = "d"
				}
				mt := ""
				if !e.ModTime.IsZero() {
					mt = e.ModTime.Format("2006-01-02 15:04")
				}
				fmt.Fprintf(os.Stdout, "%s %12d %s  %s%s\n", kind, e.Size, mt, dir, e.Name)
			} else {
				fmt.Fprintln(os.Stdout, dir+e.Name)
			}
			if e.IsDir && *recursive {
				if err := walk(dir+e.Name, depth+1); err != nil {
					fmt.Fprintf(os.Stderr, "ls: %v\n", err)
				}
			}
		}
		return nil
	}
	return walk(dirURL, 0)
}

// --- bookmarks ----------------------------------------------------

// RunBookmarks 解析 Netscape 书签 HTML，输出去重 URL 列表。
func RunBookmarks(args []string) error {
	fs := flag.NewFlagSet("bookmarks", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // AI-first：吞 flag 包 usage 打印
	out := fs.String("out", "", "输出文件（默认 stdout；可作 -i 列表文件）")
	if err := fs.Parse(flagFirst(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("bookmarks: 需要书签 HTML 文件路径")
	}
	data, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("bookmarks: 读取失败: %w", err)
	}
	bms := discover.ParseBookmarks(data)
	if len(bms) == 0 {
		return fmt.Errorf("bookmarks: 未解析到任何书签链接（文件可能不是 Netscape 书签导出）")
	}
	w := io.Writer(os.Stdout)
	closer := io.Closer(nil)
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		w, closer = f, f
		defer func() { _ = closer.Close() }()
	}
	for _, b := range bms {
		fmt.Fprintln(w, b.URL)
	}
	fmt.Fprintf(os.Stderr, "bookmarks: 解析 %d 条书签\n", len(bms))
	return nil
}

// --- extract ------------------------------------------------------

// RunExtract 从文件（- 或空=stdin）提取 URL，每行一个。
func RunExtract(args []string) error {
	fs := flag.NewFlagSet("extract", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // AI-first：吞 flag 包 usage 打印
	out := fs.String("out", "", "输出文件（默认 stdout）")
	if err := fs.Parse(flagFirst(args)); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("extract: 最多一个输入文件（- 表示 stdin）")
	}
	var data []byte
	var err error
	if fs.NArg() == 0 || fs.Arg(0) == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(fs.Arg(0))
	}
	if err != nil {
		return fmt.Errorf("extract: 读取失败: %w", err)
	}
	urls := discover.ExtractURLs(string(data))
	w := io.Writer(os.Stdout)
	closer := io.Closer(nil)
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		w, closer = f, f
		defer func() { _ = closer.Close() }()
	}
	for _, u := range urls {
		fmt.Fprintln(w, u)
	}
	fmt.Fprintf(os.Stderr, "extract: 提取 %d 条链接\n", len(urls))
	return nil
}

// --- torrent ------------------------------------------------------

// RunTorrent 解析 .torrent 或磁力链接并输出元数据。
func RunTorrent(args []string) error {
	fs := flag.NewFlagSet("torrent", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // AI-first：吞 flag 包 usage 打印
	if err := fs.Parse(flagFirst(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("torrent: 需要 .torrent 文件路径或 magnet: 链接")
	}
	in := fs.Arg(0)
	if strings.HasPrefix(in, "magnet:") {
		m, err := discover.ParseMagnet(in)
		if err != nil {
			return err
		}
		fmt.Printf("kind=magnet\ninfo_hash=%s\n", m.InfoHash)
		if m.Name != "" {
			fmt.Printf("name=%s\n", m.Name)
		}
		for _, tr := range m.Trackers {
			fmt.Printf("tracker=%s\n", tr)
		}
		fmt.Printf("hint=%s\n", m.Hint())
		return nil
	}
	data, err := os.ReadFile(in)
	if err != nil {
		return fmt.Errorf("torrent: 读取失败: %w", err)
	}
	t, err := discover.ParseTorrent(data)
	if err != nil {
		return err
	}
	fmt.Printf("kind=torrent\nname=%s\ninfo_hash=%s\n", t.Name, t.InfoHash)
	if t.Length >= 0 {
		fmt.Printf("length=%d\n", t.Length)
	} else {
		var total int64
		for _, f := range t.Files {
			total += f.Length
		}
		fmt.Printf("files=%d\ntotal_length=%d\n", len(t.Files), total)
		for _, f := range t.Files {
			fmt.Printf("file=%s  length=%d\n", f.Path, f.Length)
		}
	}
	for _, a := range t.Announce {
		fmt.Printf("announce=%s\n", a)
	}
	for _, ws := range t.WebSeeds {
		fmt.Printf("webseed=%s\n", ws)
	}
	if len(t.WebSeeds) > 0 {
		fmt.Printf("hint=WebSeed 直链可直接下载: porter %s -o %s\n",
			t.WebSeeds[0], quoteName(t.Name))
	} else {
		fmt.Printf("hint=无 WebSeed：需 BT 客户端完成对等下载，porter 可接力其结果\n")
	}
	return nil
}

// quoteName 输出名净化（去路径分隔符）。
func quoteName(name string) string {
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.TrimSpace(name)
	if name == "" {
		return "out"
	}
	return name
}

// urlFileName URL 转文件名的安全函数（供 ls 输出下载清单时提示）。
func urlFileName(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return "out"
	}
	return path.Base(p.Path)
}
