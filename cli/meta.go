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

// RunMeta 打印每个 URL 的状态行与响应头：
//
//	HTTP/1.1 200 OK
//	Accept-Ranges: bytes
//	Content-Length: 12345
//	...
//
// 任一 URL 失败即返回聚合错误（退出码 1），其余 URL 仍继续。
func RunMeta(ctx context.Context, opt *Options) error {
	if opt == nil || len(opt.URLs) == 0 {
		return errors.New("meta: 未提供 URL")
	}
	tr, _, err := buildTransport(opt, false)
	if err != nil {
		return err
	}

	var errs []error
	for _, u := range opt.URLs {
		status, hdr, err := tr.Meta(ctx, u)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", u, err))
			continue
		}
		fmt.Fprintf(os.Stdout, "%s %s\n", u, status)
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
	return errors.Join(errs...)
}
