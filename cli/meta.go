// meta.go 实现 `porter meta` 子命令（第 21 轮，对标 curl -I / wget --spider）：
// 只查看响应头不下载——输出状态行 + 全部响应头（key: value，canonical 形式），
// 供脚本/人工确认 Content-Length / Accept-Ranges / Content-Disposition / ETag 等。
// 支持 -proxy / -load-cookies / -H。
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
)

// RunMeta 查看每个 URL 的响应头：
//   - table（默认）：状态行 + key: value（curl -I 对标，脚本友好）；
//   - json|ndjson：统一封套（type=meta.list），data 为逐 URL {url,status,headers}。
// 任一 URL 失败即返回聚合错误（退出码 1），其余 URL 仍继续。
func RunMeta(ctx context.Context, opt *Options) error {
	if opt == nil || len(opt.URLs) == 0 {
		return errors.New("meta: 未提供 URL")
	}
	tr, _, err := buildTransport(opt, false)
	if err != nil {
		return err
	}

	type metaItem struct {
		URL     string              `json:"url"`
		Status  string              `json:"status"`
		Headers map[string][]string `json:"headers"`
	}
	items := make([]metaItem, 0, len(opt.URLs))
	var errs []error
	for _, u := range opt.URLs {
		status, hdr, err := tr.Meta(ctx, u)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", u, err))
			continue
		}
		item := metaItem{URL: u, Status: status, Headers: hdr}
		if opt.outMode() == OutputTable {
			fmt.Fprintf(os.Stdout, "%s %s\n", u, item.Status)
			keys := make([]string, 0, len(hdr))
			for k := range hdr {
				keys = append(keys, k)
			}
			sort.Strings(keys) // 稳定输出，脚本友好
			for _, k := range keys {
				for _, v := range hdr[k] {
					fmt.Fprintf(os.Stdout, "%s: %s\n", k, v)
				}
			}
		}
		items = append(items, item)
	}
	if opt.outMode() != OutputTable {
		env := OKEnv("meta.list", items)
		env.Meta.Command = "porter meta"
		for _, e := range errs {
			env.Errors = append(env.Errors, Classify(e, "porter meta"))
		}
		if len(env.Errors) > 0 {
			env.OK = false
		}
		_ = Emit(os.Stdout, opt.outMode(), env)
	}
	return errors.Join(errs...)
}
