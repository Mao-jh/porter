// Package discover 实现「链接发现」：从 HTTP 页面、FTP 目录、浏览器书签
// 与 BitTorrent 种子/磁力中提取可用下载链接。
//
// 设计约束（与核心引擎一致）：零第三方依赖，纯标准库；出站统一经
// network.Transport（H-3 回环边界、UA 自标识、代理/Cookie 透传）。
//
// 输出约定：所有发现结果均去重、绝对化（相对 URL 解析为完整 URL），
// 可直接写入 urls.txt 交给 porter -i 批量消费。
package discover

import (
	"net/url"
	"strings"
)

// ExtFilter 扩展名过滤（小写、带点前缀，如 ".mp4"）；nil 表示不过滤。
type ExtFilter []string

// Match 判断 name 是否匹配任一允许扩展名（大小写不敏感）。
func (f ExtFilter) Match(name string) bool {
	if len(f) == 0 {
		return true
	}
	n := strings.ToLower(name)
	for _, e := range f {
		if strings.HasSuffix(n, strings.ToLower(e)) {
			return true
		}
	}
	return false
}

// schemeOK 仅保留可下载的 URL 协议。
func schemeOK(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "ftp", "ftps":
		return true
	}
	return false
}

// Dedupe 保持顺序去重。
func Dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
