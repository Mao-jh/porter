// retry.go 实现 `porter retry` 子命令（第 17 轮）：
// 读取持久化任务中 status!=done 的条目，逐个续传重跑（引擎字节级断点续传，
// 已写分片前缀直接复用）；done 任务跳过。对标 aria2 会话恢复与 IDM 队列重试的
// 运维面。串行执行保证确定性（错误聚合返回，单个失败不影响其余）。
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/Mao-jh/porter/hash"
	"github.com/Mao-jh/porter/network"
	"github.com/Mao-jh/porter/persist"
)

// ParseRetry 解析 retry 子命令参数（无 URL 位置参数；任务清单来自状态目录）。
func ParseRetry(args []string) (*Options, error) {
	fs := flag.NewFlagSet("retry", flag.ContinueOnError)
	var (
		stateDir = fs.String("state-dir", ".downloader", "任务状态持久化目录（须与下载时一致）")
		limit    = fs.Int64("limit", 0, "全局下载限速 字节/秒（0=不限）")
		proxy    = fs.String("proxy", "", "代理出口（http(s)/socks5；设置即视为允许出站）")
		ckFile   = fs.String("load-cookies", "", "Netscape cookie.txt 路径（按域匹配注入 Cookie 头）")
		verify   = fs.String("verify", "sha256", "校验算法: sha256|sha1|md5|none")
		summary  = fs.Bool("summary", false, "进度摘要")
		hdrs     headerList
	)
	fs.Var(&hdrs, "H", "透传请求头 \"Key: Value\"（可重复）")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	algo := hash.Algorithm(*verify)
	if algo == "none" {
		algo = ""
	}
	hm, err := headerMap(hdrs)
	if err != nil {
		return nil, err
	}
	return &Options{
		StateDir:   *stateDir,
		Limit:      *limit,
		Proxy:      *proxy,
		CookieFile: *ckFile,
		Verify:     algo,
		Summary:    *summary,
		Headers:    hm,
	}, nil
}

// RunRetry 逐个续传重跑状态目录中的未完成任务（status!=done 且 URL 非空）。
// 返回聚合错误：单个任务失败不影响其余任务继续重跑。
func RunRetry(ctx context.Context, opt *Options) error {
	if opt == nil || opt.StateDir == "" {
		return errors.New("retry: 缺少状态目录")
	}
	store, err := persist.Open(opt.StateDir)
	if err != nil {
		return fmt.Errorf("持久化打开失败: %w", err)
	}
	var pending []*persist.State
	for _, st := range store.All() {
		if st.Status != "done" && st.URL != "" {
			pending = append(pending, st)
		}
	}
	if len(pending) == 0 {
		fmt.Fprintln(os.Stderr, "retry: 没有未完成任务")
		return nil
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].UpdatedAt < pending[j].UpdatedAt })

	tr, _, err := buildTransport(opt, false)
	if err != nil {
		return err
	}
	tr.SetGlobalLimit(opt.Limit)
	if opt.Proxy != "" {
		fmt.Fprintf(os.Stderr, "[proxy] 出口 %s（远程目标已随代理显式放行）\n", opt.Proxy)
	}
	fetch := network.NewMux(tr, false)

	var errs []error
	for _, st := range pending {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		fmt.Fprintf(os.Stderr, "[retry] %s <- %s\n", st.ID, st.URL)
		if err := runOne(ctx, fetch, tr, opt, st.URL, st.ID, store); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", st.ID, err))
			continue
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("retry: %d/%d 个任务失败: %w", len(errs), len(pending), errors.Join(errs...))
	}
	fmt.Fprintf(os.Stderr, "retry: %d 个任务处理完毕\n", len(pending))
	return nil
}
