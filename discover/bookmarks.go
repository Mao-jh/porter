// bookmarks.go Netscape 书签 HTML 解析（Firefox/Chrome「导出书签」通用格式）。
// 从 <A HREF="..."> 提取全部链接，保留书签标题供 -o 命名输出使用。
package discover

import (
	"regexp"
	"sort"
	"strings"
)

// aTagRe 匹配 <A HREF="...">Title</A>；HREF 大小写不敏感、值可为单双引号或裸。
var aTagRe = regexp.MustCompile(`(?is)<\s*a\b[^>]*\bhref\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))[^>]*>`)

// Bookmark 书签条目。
type Bookmark struct {
	URL   string
	Title string
}

// ParseBookmarks 解析 Netscape 书签 HTML，返回去重后的书签列表（保持出现顺序）。
// 非书签文件（无 <A HREF>）返回空列表——调用方据此判定格式。
func ParseBookmarks(data []byte) []Bookmark {
	text := string(data)
	var out []Bookmark
	seen := map[string]struct{}{}
	for _, m := range aTagRe.FindAllSubmatchIndex(data, -1) {
		raw := ""
		for _, gi := range [3]int{2, 4, 6} { // 三个捕获组中非空者
			if m[gi] >= 0 {
				raw = string(data[m[gi]:m[gi+1]])
				break
			}
		}
		raw = strings.TrimSpace(raw)
		if raw == "" || !schemeOK(raw) {
			continue
		}
		if _, ok := seen[raw]; ok {
			continue
		}
		seen[raw] = struct{}{}
		out = append(out, Bookmark{URL: raw, Title: titleAround(text, m[1])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out
}

// titleAround 提取标签闭合后到 </A> 前的标题文本（hrefEnd 为完整 <A ...> 匹配的结束位）。
func titleAround(text string, hrefEnd int) string {
	rest := text[hrefEnd:]
	if j := strings.IndexByte(rest, '<'); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}
