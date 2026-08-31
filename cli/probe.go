// probe.go 实现 `porter probe` 子命令（第 16 轮，对标 wget --spider）：
// 只探测不下载——输出目标资源的大小、Range 支持与服务端建议文件名（key=value
// 格式，脚本友好），供脚本/人工决策使用。支持 -proxy / -load-cookies / -H。
// 第 17 轮：抽出 buildProbeTransport / ProbeURL，供 MCP download_probe 工具复用
//（同一传输构建与协议分发路径，CLI 与 MCP 探测语义同源）。
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Mao-jh/porter/network"
)

// buildTransport 构造下载/探测共用传输层（proxy / cookie / headers），
// 返回 (传输层, 已加载 cookie 条数, 错误)。allowRemote 透传 MCP 的产品开关；
// RunMulti/RunProbe 恒为 false（H-3，代理是 CLI 的唯一出站开关）。
func buildTransport(opt *Options, allowRemote bool) (*network.Transport, int, error) {
	var tr *network.Transport
	if allowRemote {
		tr = network.NewTransport(true) // MCP 显式产品开关
	} else {
		tr = newTransport() // 测试可替换注入故障（H-3 生产路径恒 false）
	}
	if len(opt.Headers) > 0 {
		tr.SetHeaders(opt.Headers)
	}
	if opt.Proxy != "" {
		if err := tr.SetProxy(opt.Proxy); err != nil {
			return nil, 0, fmt.Errorf("代理配置失败: %w", err)
		}
	}
	n := 0
	if opt.CookieFile != "" {
		data, err := os.ReadFile(opt.CookieFile)
		if err != nil {
			return nil, 0, fmt.Errorf("读取 cookie 文件失败: %w", err)
		}
		cs, err := network.ParseNetscapeCookies(data)
		if err != nil {
			return nil, 0, fmt.Errorf("解析 cookie 文件失败: %w", err)
		}
		tr.SetCookies(cs)
		n = len(cs)
	}
	return tr, n, nil
}

// ProbeURL 探测单个 URL：返回 (大小, Range 支持, 服务端建议名, 错误)。
// name 仅 http(s) 且有 Content-Disposition 时非空。供 porter probe 与 MCP
// download_probe 共用（同一传输构建 + Mux 分发 + CD 查询路径）。
func ProbeURL(ctx context.Context, proxy, cookieFile string, allowRemote bool,
	headers map[string]string, urlStr string) (size int64, ranged bool, name string, err error) {
	opt := &Options{Proxy: proxy, CookieFile: cookieFile, Headers: headers}
	tr, _, err := buildTransport(opt, allowRemote)
	if err != nil {
		return 0, false, "", err
	}
	fetch := network.NewMux(tr, allowRemote)
	size, ranged, err = fetch.Probe(ctx, urlStr)
	if err != nil {
		return 0, false, "", err
	}
	if startsWithAny(urlStr, "http://", "https://") {
		name = tr.ContentFilename(ctx, urlStr)
	}
	return size, ranged, name, nil
}

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
	tr, _, err := buildTransport(opt, false)
	if err != nil {
		return err
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
