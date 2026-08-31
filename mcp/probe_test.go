// probe_test.go 第 17 轮：MCP download_probe 工具测试。
package mcpserver_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	mcpserver "github.com/Mao-jh/porter/mcp"
)

// TestMCP_DownloadProbe 探测：普通文件（size/ranged）与 CD 命名端点。
func TestMCP_DownloadProbe(t *testing.T) {
	h := NewForTest(t)
	cs := newSession(t, mcpserver.Config{StateRoot: t.TempDir()})

	out := callTool(t, cs, "download_probe", map[string]any{"url": h.FileURL("f.bin")})
	if int64(out["size_bytes"].(float64)) != 4<<20 {
		t.Errorf("size_bytes = %v, 期望 %d", out["size_bytes"], 4<<20)
	}
	if out["ranged"] != true {
		t.Errorf("ranged = %v, 期望 true", out["ranged"])
	}

	out = callTool(t, cs, "download_probe", map[string]any{"url": h.s.BaseURL() + "/cd/setup%20v2.exe"})
	if name, _ := out["name"].(string); name != "setup v2.exe" {
		t.Errorf("name = %q, 期望 setup v2.exe", name)
	}

	// R20：重定向 → final_url 指向最终地址；无重定向 → 缺省不输出
	redir := h.s.BaseURL() + "/redirect?to=" + h.FileURL("f.bin")
	out = callTool(t, cs, "download_probe", map[string]any{"url": redir})
	if fin, _ := out["final_url"].(string); fin != h.FileURL("f.bin") {
		t.Errorf("final_url = %q, 期望 %q", fin, h.FileURL("f.bin"))
	}
	out = callTool(t, cs, "download_probe", map[string]any{"url": h.FileURL("f.bin")})
	if _, has := out["final_url"]; has {
		t.Errorf("无重定向不应输出 final_url: %v", out)
	}
}

// TestMCP_DownloadProbe_Errors 非法 scheme / 非回环（默认配置）→ isError。
func TestMCP_DownloadProbe_Errors(t *testing.T) {
	cs := newSession(t, mcpserver.Config{StateRoot: t.TempDir()})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, u := range []string{"gopher://127.0.0.1/x", "http://10.0.0.1/x"} {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "download_probe", Arguments: map[string]any{"url": u}})
		if err != nil {
			t.Fatalf("%s: %v", u, err)
		}
		if !res.IsError {
			b, _ := json.Marshal(res.Content)
			t.Errorf("%s 应返回 isError: %s", u, b)
		}
	}
}
