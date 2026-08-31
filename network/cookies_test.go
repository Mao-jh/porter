// cookies_test.go Netscape cookie 解析与按域注入测试（第 14 轮 A3）。
package network

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestParseNetscapeCookies 解析：注释/HttpOnly 前缀/畸形行跳过。
func TestParseNetscapeCookies(t *testing.T) {
	data := []byte("" +
		"# Netscape HTTP Cookie File\n" +
		"# https://curl.se/docs/http-cookies.html\n" +
		"\n" +
		".example.com\tTRUE\t/\tFALSE\t1893456000\tsid\tabc123\n" +
		"#HttpOnly_.example.com\tTRUE\t/\tTRUE\t1893456000\ttoken\tt0p\n" +
		"127.0.0.1\tFALSE\t/\tFALSE\t1893456000\tlocal\tx=1\n" +
		"broken-line-no-tabs\n" +
		"\texpiry-name-value\n" +
		".example.com\tTRUE\t/\tFALSE\tnot-a-number\tloose\tok\n")
	cs, err := ParseNetscapeCookies(data)
	if err != nil {
		t.Fatalf("ParseNetscapeCookies: %v", err)
	}
	if len(cs) != 4 {
		t.Fatalf("应解析出 4 条, got %d: %+v", len(cs), cs)
	}
	want := map[string]string{"sid": "abc123", "token": "t0p", "local": "x=1", "loose": "ok"}
	for _, c := range cs {
		if want[c.Name] != c.Value {
			t.Errorf("cookie %s=%s, 期望 %s=%s", c.Name, c.Value, c.Name, want[c.Name])
		}
	}
}

// TestParseNetscapeCookies_Empty 全无效内容应报错而非静默空集。
func TestParseNetscapeCookies_Empty(t *testing.T) {
	if _, err := ParseNetscapeCookies([]byte("# only comments\n\n")); err == nil {
		t.Error("无有效条目应报错")
	}
}

// TestSetCookies_DomainMatchAndMerge 域匹配 + 与 -H 透传 Cookie 合并（透传优先）。
func TestSetCookies_DomainMatchAndMerge(t *testing.T) {
	ts, cleanup := startTestServer(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tr := NewTransport(false)
	tr.SetHeaders(map[string]string{"Cookie": "sentinel=1"})
	tr.SetCookies([]Cookie{
		{Domain: "127.0.0.1", Name: "sid", Value: "abc"},
		{Domain: ".example.com", Name: "remote", Value: "zzz"},
	})
	data, err := tr.getBounded(ctx, ts.s.BaseURL()+"/echo", 4096)
	if err != nil {
		t.Fatalf("getBounded: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, "Cookie=sentinel=1; sid=abc") {
		t.Errorf("回环域 cookie 应合并注入（透传在前）, body:\n%s", body)
	}
	if strings.Contains(body, "remote=") {
		t.Error("非匹配域 cookie 不应注入")
	}

	// 带点域匹配子域；此处目标 host=127.0.0.1，只有精确/后缀匹配生效
	tr2 := NewTransport(false)
	tr2.SetCookies([]Cookie{{Domain: "0.0.1", Name: "suffix", Value: "hit"}})
	data2, err := tr2.getBounded(ctx, ts.s.BaseURL()+"/echo", 4096)
	if err != nil {
		t.Fatalf("getBoundored: %v", err)
	}
	if !strings.Contains(string(data2), "Cookie=suffix=hit") {
		t.Errorf("后缀匹配应生效, body:\n%s", string(data2))
	}
}

// TestSetCookies_Cleared nil 清空后不再注入。
func TestSetCookies_Cleared(t *testing.T) {
	ts, cleanup := startTestServer(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tr := NewTransport(false)
	tr.SetCookies([]Cookie{{Domain: "127.0.0.1", Name: "sid", Value: "abc"}})
	tr.SetCookies(nil)
	data, err := tr.getBounded(ctx, ts.s.BaseURL()+"/echo", 4096)
	if err != nil {
		t.Fatalf("getBounded: %v", err)
	}
	if strings.Contains(string(data), "Cookie=sid=") {
		t.Error("清空后不应注入 cookie")
	}
}

// TestParseContentDisposition RFC 6266/5987 文件名解析用例。
func TestParseContentDisposition(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`attachment; filename="report 2026.pdf"`, "report 2026.pdf"},
		{`attachment; filename=plain.bin`, "plain.bin"},
		{`attachment; filename="a\"b.bin"`, `a"b.bin`},
		{`attachment; filename*=UTF-8''%E6%8A%A5%E5%91%8A.pdf`, "报告.pdf"},
		{`attachment; filename="fallback.zip"; filename*=UTF-8''%E4%BC%98%E5%85%88.zip`, "优先.zip"},
		{`inline`, ""},
		{`attachment; size=100`, ""},
	}
	for _, c := range cases {
		if got := parseContentDisposition(c.in); got != c.want {
			t.Errorf("parseContentDisposition(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

// TestContentFilename 端到端：/cd 端点 HEAD → CD 文件名。
func TestContentFilename(t *testing.T) {
	ts, cleanup := startTestServer(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tr := NewTransport(false)
	if got := tr.ContentFilename(ctx, ts.s.BaseURL()+"/cd/setup%20v2.exe"); got != "setup v2.exe" {
		t.Errorf("CD 文件名 = %q, 期望 %q", got, "setup v2.exe")
	}
	// filename* 原样形态
	if got := tr.ContentFilename(ctx, ts.s.BaseURL()+"/cd/x?raw="+"attachment%3B+filename*%3DUTF-8''%E6%96%87%E4%BB%B6.bin"); got != "文件.bin" {
		t.Errorf("filename* 解析 = %q, 期望 文件.bin", got)
	}
	// 无 CD 头 → 空串
	if got := tr.ContentFilename(ctx, ts.s.FileURL("nofile.bin")); got != "" {
		t.Errorf("无 CD 应返回空串, got %q", got)
	}
	// 非回环拒绝（无代理）→ 空串
	if got := tr.ContentFilename(ctx, "https://example.com/x"); got != "" {
		t.Errorf("非回环应返回空串, got %q", got)
	}
}
