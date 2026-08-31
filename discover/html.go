// html.go HTTP 页面链接提取：轻量扫描 HTML 标签属性（零第三方依赖）。
// 覆盖 <a href> / <img src> / <video src> / <source src> / <iframe src>；
// 排除页面内部资源（<script src> / <link href> / <style> 内联）以免污染下载候选。
package discover

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// pageFetcher 页面抓取接口（实现为 *network.Transport；测试可注入假实现）。
type pageFetcher interface {
	Get(ctx context.Context, urlStr string, max int64) ([]byte, error)
}

// maxPageBytes 页面抓取上限（8 MiB：防内存滥用，正常页面远小于此）。
const maxPageBytes = 8 << 20

// tagRe 匹配 <tag ...> 开标签（tag 为捕获组 1；不跨行，避免吞掉 <script> 块内内容）。
var tagRe = regexp.MustCompile(`(?is)<\s*([a-zA-Z][a-zA-Z0-9]*)\b[^>]*>`)

// attrRe 提取属性键值：键可为 href/src，值可为引号包裹或裸 token。
var attrRe = regexp.MustCompile(`(?is)\b(href|src)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)

// hrefOnly 声明只取 href 的标签（<a>/<link> 语义）；其余按 src。
var hrefOnly = map[string]bool{"a": true, "link": true, "area": true}

// skipTags 声明其资源属性不产出下载候选（页面内部资源）。
var skipTags = map[string]bool{"script": true, "style": true, "link": true}

// PageHits 页面链接提取结果。
type PageHits struct {
	Base    string   // 页面最终 URL（供相对链接解析）
	Links   []string // 去重后的可下载链接（绝对 URL）
	Ignored int      // 被过滤条数（内部资源/协议不符/重复）
}

// FindLinksInPage 抓取页面并提取可下载链接。
// max 为页面大小上限（0 用默认 8MiB）；filter 扩展名过滤（nil=不过滤）。
func FindLinksInPage(ctx context.Context, f pageFetcher, pageURL string, max int64, filter ExtFilter) (*PageHits, error) {
	if max <= 0 {
		max = maxPageBytes
	}
	body, err := f.Get(ctx, pageURL, max)
	if err != nil {
		return nil, err
	}
	return ParsePageLinks(body, pageURL, filter), nil
}

// ParsePageLinks 从页面字节解析链接（页面 URL 用于绝对化）。
func ParsePageLinks(body []byte, pageURL string, filter ExtFilter) *PageHits {
	hits := &PageHits{}
	base := pageURL
	if u, err := url.Parse(pageURL); err == nil {
		// <base href> 优先
		for _, m := range tagRe.FindAllSubmatch(body, -1) {
			if strings.EqualFold(string(m[1]), "base") {
				if raw := attrValue(tagAttrs(m[0], m[1]), "href"); raw != "" {
					if bu, err := u.Parse(raw); err == nil && bu.IsAbs() {
						base = bu.String()
					}
				}
				break
			}
		}
		hits.Base = base
	} else {
		hits.Base = pageURL
	}
	bu, _ := url.Parse(base)

	seen := map[string]struct{}{}
	for _, m := range tagRe.FindAllSubmatch(body, -1) {
		tag := strings.ToLower(string(m[1]))
		attrs := tagAttrs(m[0], m[1])
		attrName := "src"
		if hrefOnly[tag] {
			attrName = "href"
		}
		if skipTags[tag] {
			continue
		}
		// 页面内部锚点（#xxx）与 onclick 等伪链接不产生资源
		for _, raw := range attrValues(attrs, attrName) {
			raw = strings.TrimSpace(raw)
			if raw == "" || strings.HasPrefix(raw, "#") {
				hits.Ignored++
				continue
			}
			abs := resolve(raw, bu)
			if abs == "" {
				hits.Ignored++
				continue
			}
			// 过滤明显非资源的扩展名（.js/.css/.woff 等页面支撑物）
			low := strings.ToLower(abs)
			if strings.HasSuffix(low, ".js") || strings.HasSuffix(low, ".css") ||
				strings.HasSuffix(low, ".woff") || strings.HasSuffix(low, ".woff2") ||
				strings.HasSuffix(low, ".svg") {
				hits.Ignored++
				continue
			}
			if !filter.Match(abs) {
				hits.Ignored++
				continue
			}
			if _, ok := seen[abs]; ok {
				hits.Ignored++
				continue
			}
			seen[abs] = struct{}{}
			hits.Links = append(hits.Links, abs)
		}
	}
	sort.Strings(hits.Links)
	return hits
}

// resolve 把原始链接相对页面基址绝对化；返回 "" 表示不可用。
func resolve(raw string, base *url.URL) string {
	if u, err := url.Parse(raw); err == nil {
		if u.IsAbs() {
			if schemeOK(u.String()) {
				return u.String()
			}
			return ""
		}
		if base != nil {
			abs := base.ResolveReference(u)
			if abs.IsAbs() && schemeOK(abs.String()) {
				return abs.String()
			}
		}
	}
	return ""
}

// tagAttrs 从完整标签匹配（m[0]=`<tag attrs>`）中提取属性子串。
func tagAttrs(full, tag []byte) string {
	start := len(tag) + 1 // 跳过 "<tag"
	if start >= len(full) {
		return ""
	}
	s := string(full[start:])
	if i := strings.LastIndexByte(s, '>'); i >= 0 {
		s = s[:i]
	}
	return s
}

// attrValue 从属性串中提取首个指定键的值。
func attrValue(attrs, key string) string {
	vs := attrValues(attrs, key)
	if len(vs) == 0 {
		return ""
	}
	return vs[0]
}

// attrValues 提取属性串中指定键的全部值。
func attrValues(attrs, key string) []string {
	var out []string
	for _, m := range attrRe.FindAllStringSubmatch(attrs, -1) {
		if !strings.EqualFold(m[1], key) {
			continue
		}
		v := m[2]
		if v == "" {
			v = m[3]
		}
		if v == "" {
			v = m[4]
		}
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// Validate 供测试断言解析器行为。
func (h *PageHits) String() string {
	return fmt.Sprintf("base=%s links=%d ignored=%d", h.Base, len(h.Links), h.Ignored)
}
