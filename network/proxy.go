// proxy.go 实现代理出口配置（第 14 轮新增契约，DESIGN §2.3）。
// 对标 aria2 --all-proxy：支持 http:// https:// socks5://（socks5 由 net/http
// 原生支持，无需自实现拨号——零依赖约束下的最短路径）。
// H-3 边界的产品决策：显式配置代理即视为显式允许出站流量（语义对齐 -allow-remote），
// 代理成为唯一出口——http.Transport 只拨号代理地址，目标主机不再被本端 DNS 校验。
package network

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// SetProxy 配置代理（http://host:port | https://... | socks5://...）。
// 设置后：
//   - 所有 http(s) 请求经该代理转发（CONNECT 隧道或绝对 URI）；
//   - allowRemote 自动置位（代理即显式出站同意，README 安全边界同步声明）；
//   - validateURL 对目标只做 scheme 白名单校验（DNS 解析交给代理）。
// 重复调用以最后一次为准；传空串返回错误（取消代理无意义——重建 Transport 即可）。
func (t *Transport) SetProxy(raw string) error {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Scheme == "" {
		return fmt.Errorf("network: 非法代理地址 %q（应为 http(s)://host:port 或 socks5://host:port）", raw)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return fmt.Errorf("network: 不支持的代理协议 %q（仅 http/https/socks5）", u.Scheme)
	}
	ht, ok := t.client.Transport.(*http.Transport)
	if !ok {
		return fmt.Errorf("network: 内部传输层类型异常，无法设置代理")
	}
	ht.Proxy = http.ProxyURL(u)
	t.mu.Lock()
	t.allowRemote = true // 代理 = 显式出站同意（文档化语义，README/USAGE 同步）
	t.proxySet = true
	t.mu.Unlock()
	return nil
}

// proxyConfigured 是否已配置代理（validateURL 用；跳过目标 DNS 断言）。
func (t *Transport) proxyConfigured() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.proxySet
}
