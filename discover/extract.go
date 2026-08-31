// extract.go 通用文本 URL 提取：从任意文本/文件内容中抓取 http(s)/ftp(s) 链接。
// 与 bookmarks.go 不同——不假设 HTML 结构，面向日志、列表页、聊天记录等混杂文本。
package discover

import (
	"net/url"
	"regexp"
	"strings"
)

// urlRe 匹配完整 URL token（不含结尾的引号/括号/标点）。
// 注意：不能在此处排除非 ASCII（(?i) 下 RE2 对字符类做 case-fold 闭包，
// s↔ſ、k↔K 等折叠会让 ASCII 被误排除）——中文文本紧邻的剥离在代码层完成。
var urlRe = regexp.MustCompile(`(?i)(https?|ftps?)://[^\s<>"'()\[\]{}\x{3000}]+`)

// ExtractURLs 从文本提取去重后的可用 URL。
// 尾随标点清理：URL 后紧跟的 . , ; : ! ? 与中文标点视为文本标点剥离；
// 非 ASCII 尾字符（中文文本紧邻 URL 时）同样剥离。
func ExtractURLs(text string) []string {
	raw := urlRe.FindAllString(text, -1)
	seen := map[string]struct{}{}
	out := make([]string, 0, len(raw))
	for _, u := range raw {
		u = strings.TrimRight(u, ".,;:!?。，；：！？")
		for len(u) > 0 && u[len(u)-1] >= 0x80 {
			u = u[:len(u)-1] // UTF-8 多字节序列每个字节均 ≥0x80，逐个剥除即整字符
		}
		u = strings.TrimRight(u, ".,;:!?。，；：！？")
		if _, err := url.Parse(u); err != nil || !schemeOK(u) {
			continue
		}
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out
}
