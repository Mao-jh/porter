// cookies.go 实现 Netscape cookie.txt 解析与按域注入（第 14 轮新增契约）。
// 对标 aria2 --load-cookies：解析经典 7 列格式，按请求域名匹配后合并进
// Cookie 头（与 -H "Cookie: ..." 透传共存：先透传值，后 cookie 文件值）。
// 简化边界（诚实声明）：不区分 path 与 secure 维度——凡域匹配即发送；
// cookie.txt 本就是用户主动提供的凭据文件，粒度收窄带来的风险可忽略。
package network

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Cookie 单条 cookie（域匹配用）。
type Cookie struct {
	Domain string // 匹配域（.example.com 与 example.com 等价）
	Name   string
	Value  string
}

// cookies 挂在 Transport 上的 cookie 集合（只读快照，替换整体）。
// 放在 Transport 结构体外以独立文件管理：mu 复用 Transport.mu。

// SetCookies 设置 cookie 集合（整体替换；nil 清空）。
func (t *Transport) SetCookies(cs []Cookie) {
	cp := make([]Cookie, len(cs))
	copy(cp, cs)
	t.mu.Lock()
	t.cookies = cp
	t.mu.Unlock()
}

// applyCookies 按 URL 域匹配并把 cookie 合并进请求 Cookie 头。
// 调用时机：透传头已应用之后。同名 cookie：文件值追加在透传值之后
//（服务端按 RFC 6265 取第一个出现者，透传 -H 优先——与 aria2 语义一致）。
func (t *Transport) applyCookies(req *http.Request, hdrs map[string]string) {
	t.mu.RLock()
	cs := t.cookies
	t.mu.RUnlock()
	if len(cs) == 0 {
		return
	}
	host := strings.ToLower(req.URL.Hostname())
	var matched []string
	for _, c := range cs {
		d := strings.TrimPrefix(strings.ToLower(c.Domain), ".")
		if d != "" && (host == d || strings.HasSuffix(host, "."+d)) {
			matched = append(matched, c.Name+"="+c.Value)
		}
	}
	if len(matched) == 0 {
		return
	}
	merged := matched
	if existing := hdrs["Cookie"]; existing != "" {
		merged = append([]string{existing}, matched...)
	}
	req.Header.Set("Cookie", strings.Join(merged, "; "))
}

// ParseNetscapeCookies 解析 Netscape cookie.txt（curl/wget/aria2 通用格式）：
//   - 注释行：以 "#" 开头（"#HttpOnly_" 前缀除外——它是真实 cookie 的标记）；
//   - 数据行：7 个 TAB 分隔字段 domain\tincludeSub\tpath\tsecure\texpiry\tname\tvalue；
//   - 畸形行跳过（不报错，尽量保留可用 cookie，与 curl 行为一致）。
//   - value 支持 base64 及任意非换行字符；域名为空或 name 为空的行跳过。
func ParseNetscapeCookies(data []byte) ([]Cookie, error) {
	var out []Cookie
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 {
			continue
		}
		// "#HttpOnly_" 前缀是社区约定的 HttpOnly 标记（curl 可读），剥离后为正常数据行
		if bytes.HasPrefix(line, []byte("#HttpOnly_")) {
			line = bytes.TrimPrefix(line, []byte("#HttpOnly_"))
		} else if line[0] == '#' {
			continue // 普通注释
		}
		fields := strings.Split(string(line), "\t")
		if len(fields) < 7 {
			// 容错：空白分隔的行不接受（TAB 是格式规范；空格分割有歧义风险）
			continue
		}
		domain := strings.TrimSpace(fields[0])
		name := strings.TrimSpace(fields[5])
		value := fields[6]
		if i := strings.IndexByte(value, '\t'); i >= 0 {
			// 超过 7 列：值内不应含 TAB（Netscape 格式无转义），取最后一列更安全
			value = value[strings.LastIndexByte(value, '\t')+1:]
		}
		value = strings.TrimSpace(value)
		if domain == "" || name == "" {
			continue
		}
		if _, err := strconv.ParseInt(strings.TrimSpace(fields[4]), 10, 64); err != nil {
			// expiry 非数字：仍接受（有的导出工具写 0 或留空）
			_ = err
		}
		out = append(out, Cookie{Domain: domain, Name: name, Value: value})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("network: cookie 文件中无有效条目")
	}
	return out, nil
}
