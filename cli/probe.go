// probe.go 实现 `porter probe` 子命令（第 16 轮，对标 wget --spider）：
// 只探测不下载——输出目标资源的大小、Range 支持与服务端建议文件名（key=value
// 格式，脚本友好），供脚本/人工决策使用。支持 -proxy / -load-cookies / -H。
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Mao-jh/porter/network"
)

// RunProbe 探测每个 URL 并打印：
//
//	url=<原 URL>
//	size=<字节数，0=未知>
//	ranged=<true|false>
//	name=<服务端建议文件名>（仅 http(s) 且有 Content-Disposition 时输出）
//
// 任一 URL 探测失败即返回聚合错误（退出码 1），其余 URL 仍继续探测。
func RunProbe(ctx context.Context, opt *Options) error {
	if opt == nil || len(opt.URLs) == 0 {
		return errors.New("probe: 未提供 URL")
	}
	tr := newTransport()
	if len(opt.Headers) > 0 {
		tr.SetHeaders(opt.Headers)
	}
	if opt.Proxy != "" {
		if err := tr.SetProxy(opt.Proxy); err != nil {
			return fmt.Errorf("代理配置失败: %w", err)
		}
	}
	if opt.CookieFile != "" {
		data, err := os.ReadFile(opt.CookieFile)
		if err != nil {
			return fmt.Errorf("读取 cookie 文件失败: %w", err)
		}
		cs, err := network.ParseNetscapeCookies(data)
		if err != nil {
			return fmt.Errorf("解析 cookie 文件失败: %w", err)
		}
		tr.SetCookies(cs)
	}
	fetch := network.NewMux(tr, false)

	var errs []error
	for _, u := range opt.URLs {
		size, ranged, err := fetch.Probe(ctx, u)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", u, err))
			continue
		}
		fmt.Fprintf(os.Stdout, "url=%s\nsize=%d\nranged=%v\n", u, size, ranged)
		if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
			if name := tr.ContentFilename(ctx, u); name != "" {
				fmt.Fprintf(os.Stdout, "name=%s\n", name)
			}
		}
	}
	return errors.Join(errs...)
}
