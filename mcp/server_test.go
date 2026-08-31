package mcpserver_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	mcpserver "github.com/Mao-jh/porter/mcp"
)

// newSession 组装内存传输上的 MCP 会话（server=被测工具集，client=模拟 AI 客户端）。
func newSession(t *testing.T, cfg mcpserver.Config) *mcp.ClientSession {
	t.Helper()
	srv := mcpserver.NewToolServer(cfg)
	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(context.Background(), st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cl := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := cl.Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// callTool 调用工具并断言无 isError。
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		b, _ := json.Marshal(res.Content)
		t.Fatalf("%s 返回 isError: %s", name, b)
	}
	out := map[string]any{}
	if res.StructuredContent != nil {
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("%s structured content 解析失败: %v", name, err)
		}
	}
	return out
}

// statusState 从 status 工具输出提取指定输出的状态。
func statusState(out map[string]any, output string) (string, map[string]any) {
	tasks, _ := out["tasks"].([]any)
	for _, tk := range tasks {
		m, _ := tk.(map[string]any)
		if m == nil {
			continue
		}
		if m["output"] == output {
			s, _ := m["state"].(string)
			return s, m
		}
	}
	return "", nil
}

// TestMCP_DownloadLifecycle 全生命周期：start → status 轮询至 done → 文件与 sha256 一致。
func TestMCP_DownloadLifecycle(t *testing.T) {
	s := NewForTest(t)
	cfg := mcpserver.Config{
		StateRoot: filepath.Join(t.TempDir(), "state"),
		Verify:    "sha256",
	}
	cs := newSession(t, cfg)

	outDir := t.TempDir()
	start := callTool(t, cs, "download_start", map[string]any{
		"url": s.FileURL("f.bin"), "output_dir": outDir,
	})
	if id, _ := start["task_id"].(string); id == "" {
		t.Fatalf("start 应返回 task_id: %v", start)
	}
	output, _ := start["output"].(string)

	// 轮询至完成（引擎 500ms 落盘节奏 + MCP 调用往返）
	deadline := time.Now().Add(60 * time.Second)
	for {
		st, info := statusState(callTool(t, cs, "download_status", map[string]any{}), output)
		if st == "done" {
			break
		}
		if st == "failed" {
			t.Fatalf("任务失败: %v", info)
		}
		if time.Now().After(deadline) {
			t.Fatalf("60s 未完成: %v", info)
		}
		time.Sleep(300 * time.Millisecond)
	}

	// 文件与校验
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("输出文件缺失: %v", err)
	}
	sum := sha256.Sum256(got)
	if hex.EncodeToString(sum[:]) != s.ChecksumHex("f.bin") {
		t.Fatal("sha256 不一致")
	}

	// 历史恢复：list_tasks 应包含该任务
	list := callTool(t, cs, "list_tasks", map[string]any{})
	if st, _ := statusState(list, output); st != "done" {
		t.Fatalf("list_tasks 状态=%s want done", st)
	}
}

// TestMCP_CancelLifecycle 取消：限速下载 → cancel → 状态转 paused（可续传）。
func TestMCP_CancelLifecycle(t *testing.T) {
	s := NewForTest(t)
	s.LimitBytes(1 << 20) // 1MiB/s/连接 → 8MiB 文件下载需数秒
	cfg := mcpserver.Config{StateRoot: filepath.Join(t.TempDir(), "state"), Verify: ""}
	cs := newSession(t, cfg)

	outDir := t.TempDir()
	start := callTool(t, cs, "download_start", map[string]any{
		"url": s.FileURL("big.bin"), "output_dir": outDir,
	})
	id, _ := start["task_id"].(string)
	output, _ := start["output"].(string)

	time.Sleep(1 * time.Second) // 让下载先跑一段
	cancelled := callTool(t, cs, "download_cancel", map[string]any{"task_id": id})
	if c, _ := cancelled["cancelled"].(bool); !c {
		t.Fatalf("cancel 应成功: %v", cancelled)
	}

	// 轮询至 paused
	deadline := time.Now().Add(30 * time.Second)
	for {
		st, _ := statusState(callTool(t, cs, "download_status", map[string]any{"task_id": id}), output)
		if st == "paused" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("取消后未进入 paused")
		}
		time.Sleep(300 * time.Millisecond)
	}

	// 续传：重新 start 同 URL → 走引擎断点续传 → done
	callTool(t, cs, "download_start", map[string]any{
		"url": s.FileURL("big.bin"), "output_dir": outDir,
	})
	deadline = time.Now().Add(120 * time.Second)
	for {
		st, info := statusState(callTool(t, cs, "download_status", map[string]any{}), output)
		if st == "done" {
			break
		}
		if st == "failed" {
			t.Fatalf("续传失败: %v", info)
		}
		if time.Now().After(deadline) {
			t.Fatalf("续传 120s 未完成: %v", info)
		}
		time.Sleep(500 * time.Millisecond)
	}
	if info, err := os.Stat(output); err != nil || info.Size() != 8<<20 {
		t.Fatalf("续传产物异常: %v %v", info, err)
	}
}

// TestMCP_RejectsBadInput 非法输入走 isError 文本结果（AI 可读），不崩服务。
// 第 12 轮起 ftp/ftps 已入白名单（引擎 Mux 分发），此处以未知协议验证拒绝路径。
func TestMCP_RejectsBadInput(t *testing.T) {
	cfg := mcpserver.Config{StateRoot: filepath.Join(t.TempDir(), "state")}
	cs := newSession(t, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "download_start",
		Arguments: map[string]any{"url": "gopher://127.0.0.1/x"},
	})
	if err != nil {
		t.Fatalf("调用本身不应失败: %v", err)
	}
	if !res.IsError {
		t.Fatal("gopher URL 应返回 isError 结果")
	}
}

// TestMCP_ProxyAndCookies 第 15 轮：代理 + Cookie 文件配置接线——下载经代理出口
// 成功完成（代理命中计数 >0），Cookie 文件正常加载不中断链路。
func TestMCP_ProxyAndCookies(t *testing.T) {
	s := NewForTest(t)

	// 转发代理（绝对 URI 转发；H-3 回环内，测试目标=同机 testserver）
	var proxied int64
	px := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&proxied, 1)
		out, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		for k, vs := range r.Header {
			for _, v := range vs {
				out.Header.Add(k, v)
			}
		}
		resp, err := http.DefaultClient.Do(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer px.Close()

	ck := filepath.Join(t.TempDir(), "cookies.txt")
	if err := os.WriteFile(ck, []byte("127.0.0.1\tFALSE\t/\tFALSE\t1893456000\tsid\tmcp-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := mcpserver.Config{
		StateRoot:  filepath.Join(t.TempDir(), "state"),
		Verify:     "",
		Proxy:      px.URL,
		CookieFile: ck,
	}
	cs := newSession(t, cfg)

	outDir := t.TempDir()
	start := callTool(t, cs, "download_start", map[string]any{
		"url": s.FileURL("f.bin"), "output_dir": outDir,
	})
	output, _ := start["output"].(string)

	deadline := time.Now().Add(60 * time.Second)
	for {
		st, info := statusState(callTool(t, cs, "download_status", map[string]any{}), output)
		if st == "done" {
			break
		}
		if st == "failed" {
			t.Fatalf("任务失败: %v", info)
		}
		if time.Now().After(deadline) {
			t.Fatalf("60s 未完成: %v", info)
		}
		time.Sleep(300 * time.Millisecond)
	}
	if atomic.LoadInt64(&proxied) == 0 {
		t.Fatal("请求未经过代理出口")
	}
	if _, err := os.Stat(output); err != nil {
		t.Fatalf("输出文件缺失: %v", err)
	}
}
