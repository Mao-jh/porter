// envelope_test.go Agent-First CLI 契约层测试（对应《面向 AI 的 CLI 上下文工程最佳实践》第 4/9 章）：
//   - 统一封套：schemaVersion/type/ok/data/errors/meta 字段稳定、ok 与 errors 互斥；
//   - 结构化错误：code/retryable/next_actions 可解析，确定性模式不依赖模型猜；
//   - MDJSON 行级封套可逐行解析（管道可恢复）；
//   - probe/meta/tasks 的 --output json 输出与 MCP 字段同源。
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Mao-jh/porter/network"
	"github.com/Mao-jh/porter/persist"
	"github.com/Mao-jh/porter/testserver"
)

// testCaptureStdout 捕获 os.Stdout（与 round20_test 同模式）。
func testCaptureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	_ = w.Close()
	buf := make([]byte, 1<<20)
	n, _ := r.Read(buf)
	return string(buf[:n])
}

// TestEnvelopeJSONShape 成功封套：字段名与 ok/errors 互斥。
func TestEnvelopeJSONShape(t *testing.T) {
	env := OKEnv("probe.list", []string{"a"})
	var buf bytes.Buffer
	if e := Emit(&buf, OutputJSON, env); e != nil {
		t.Fatal(e)
	}
	var m map[string]any
	if e := json.Unmarshal(buf.Bytes(), &m); e != nil {
		t.Fatalf("JSON 不可解析: %v\n%s", e, buf.String())
	}
	if m["schemaVersion"] != ContractVersion {
		t.Errorf("schemaVersion = %v", m["schemaVersion"])
	}
	if m["ok"] != true {
		t.Errorf("ok = %v", m["ok"])
	}
	if _, hasErr := m["errors"]; hasErr {
		t.Errorf("成功封套不应有 errors 字段: %s", buf.String())
	}
	// 失败封套：ok=false 且 errors 非空
	aev := ErrEnv("error", []AppError{{Code: CodeInternal, Retryable: false, Message: "x"}})
	buf.Reset()
	if e := Emit(&buf, OutputJSON, aev); e != nil {
		t.Fatal(e)
	}
	if e := json.Unmarshal(buf.Bytes(), &m); e != nil {
		t.Fatal(e)
	}
	if m["ok"] != false {
		t.Errorf("错误封套 ok 应为 false: %s", buf.String())
	}
	errs, _ := m["errors"].([]any)
	if len(errs) != 1 {
		t.Errorf("errors 应为 1 条: %s", buf.String())
	}
	f := errs[0].(map[string]any)
	if f["code"] != CodeInternal || f["retryable"] != false {
		t.Errorf("错误字段不符: %v", f)
	}
}

// TestNDJSONRowExpansion NDJSON 每元素一行封套，每行可独立解析。
func TestNDJSONRowExpansion(t *testing.T) {
	env := OKEnv("tasks.list", []*persist.State{{ID: "a", Status: "done"}, {ID: "b", Status: "running"}})
	var buf bytes.Buffer
	if e := Emit(&buf, OutputNDJSON, env); e != nil {
		t.Fatal(e)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("期望 2 行，实际 %d: %s", len(lines), buf.String())
	}
	for _, ln := range lines {
		var row map[string]any
		if e := json.Unmarshal([]byte(ln), &row); e != nil {
			t.Fatalf("行不可解析: %v (%s)", e, ln)
		}
		typ, _ := row["type"].(string)
		if !strings.HasSuffix(typ, ".row") {
			t.Errorf("行 type 应为 *.row: %s", ln)
		}
	}
}

// TestClassify_H3Loopback H-3 回环拒绝 → permission_denied + next_actions 精确命令。
func TestClassify_H3Loopback(t *testing.T) {
	ae := Classify(errors.New("host 10.0.0.1 resolves to non-loopback, refused (H-3)"), "porter probe")
	if ae.Code != CodePermission {
		t.Errorf("code = %q, 期望 %q", ae.Code, CodePermission)
	}
	if ae.Retryable {
		t.Error("权限拒绝不应 retryable")
	}
	if len(ae.NextActions) == 0 {
		t.Errorf("应有可执行 next_actions: %+v", ae)
	}
	if !strings.Contains(ae.NextActions[0].Command, "-proxy") {
		t.Errorf("next_action 应含 -proxy 放行命令: %+v", ae.NextActions)
	}
}

// TestClassify_Deterministic 确定性模式：取消/参数/429/未找到。
func TestClassify_Deterministic(t *testing.T) {
	cases := []struct {
		err  error
		code string
		retr bool
	}{
		{context.Canceled, CodeCancelled, false},
		{errors.New("参数错误: -o 指向已存在目录"), CodeInvalidArgument, false},
		{errors.New("非法 -verify: md6（应为 sha256|sha1|md5|none）"), CodeInvalidArgument, false},
		{errors.New("任务未找到: xxx"), CodeNotFound, false},
		{errors.New("HTTP 429 Too Many Requests"), CodeRateLimited, true},
	}
	for _, c := range cases {
		ae := Classify(c.err, "porter x")
		if ae.Code != c.code || ae.Retryable != c.retr {
			t.Errorf("Classify(%v) = %+v, 期望 code=%s retryable=%v", c.err, ae, c.code, c.retr)
		}
	}
}

// TestRunProbeJSON probe --output json：封套 + 字段名（与 MCP download_probe 同源）。
func TestRunProbeJSON(t *testing.T) {
	ts, err := testserver.New(testserver.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	if _, err := ts.CreateFile("jp.bin", 1<<20); err != nil {
		t.Fatal(err)
	}
	old := newTransport
	newTransport = func() *network.Transport { return network.NewTransport(false) }
	defer func() { newTransport = old }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out := testCaptureStdout(t, func() {
		if e := RunProbe(ctx, &Options{URLs: []string{ts.FileURL("jp.bin")}, OutMode: OutputJSON}); e != nil {
			t.Fatalf("RunProbe(json): %v", e)
		}
	})
	for _, want := range []string{`"ok":true`, `"type":"probe.list"`, `"size_bytes":1048576`, `"schemaVersion":"1"`} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺 %s:\n%s", want, out)
		}
	}
}

// TestRunProbeJSON_Mixed 部分失败：ok=false 且 data 保留成功项 + errors 携带失败项。
func TestRunProbeJSON_Mixed(t *testing.T) {
	ts, err := testserver.New(testserver.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	if _, err := ts.CreateFile("ok.bin", 1<<20); err != nil {
		t.Fatal(err)
	}
	old := newTransport
	newTransport = func() *network.Transport { return network.NewTransport(false) }
	defer func() { newTransport = old }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var gotErr error
	out := testCaptureStdout(t, func() {
		gotErr = RunProbe(ctx, &Options{URLs: []string{ts.FileURL("ok.bin"), "http://10.0.0.1/x"}, OutMode: OutputJSON})
	})
	if gotErr == nil {
		t.Fatal("部分失败应返回错误")
	}
	if !strings.Contains(out, `"ok":false`) {
		t.Errorf("部分失败 ok 应为 false:\n%s", out)
	}
	if !strings.Contains(out, `"url":"`+ts.FileURL("ok.bin")+`"`) {
		t.Errorf("data 应保留成功项:\n%s", out)
	}
	if !strings.Contains(out, `"code":"permission_denied"`) {
		t.Errorf("errors 应含 permission_denied:\n%s", out)
	}
}

// TestRunMetaJSON meta --output json：headers 为 {key:[values]}。
func TestRunMetaJSON(t *testing.T) {
	ts, err := testserver.New(testserver.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	if _, err := ts.CreateFile("jm.bin", 2<<20); err != nil {
		t.Fatal(err)
	}
	old := newTransport
	newTransport = func() *network.Transport { return network.NewTransport(false) }
	defer func() { newTransport = old }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out := testCaptureStdout(t, func() {
		if e := RunMeta(ctx, &Options{URLs: []string{ts.FileURL("jm.bin")}, OutMode: OutputJSON}); e != nil {
			t.Fatalf("RunMeta(json): %v", e)
		}
	})
	for _, want := range []string{`"type":"meta.list"`, `"status":"HTTP/1.1 200 OK"`, `"headers":{`} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺 %s:\n%s", want, out)
		}
	}
}

// TestRunTasksJSON tasks --output json：含 hash 字段（校验和回填链路）。
func TestRunTasksJSON(t *testing.T) {
	dir := t.TempDir()
	store, err := persist.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(&persist.State{ID: "out.bin", URL: "http://127.0.0.1:1/a.bin", FileSize: 10, Done: 10, Status: "done", UpdatedAt: time.Now().UnixNano()}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetHash("out.bin", "abc123"); err != nil {
		t.Fatal(err)
	}
	out := testCaptureStdout(t, func() {
		if e := RunTasks(dir, OutputJSON); e != nil {
			t.Fatalf("RunTasks(json): %v", e)
		}
	})
	for _, want := range []string{`"ok":true`, `"type":"tasks.list"`, `"hash":"abc123"`, `"status":"done"`} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺 %s:\n%s", want, out)
		}
	}
}

// TestParseOutputMode --output 枚举严格校验（拒绝未知枚举）。
func TestParseOutputMode(t *testing.T) {
	for _, ok := range []string{"table", "json", "ndjson", ""} {
		if _, err := ParseOutputMode(ok); err != nil {
			t.Errorf("ParseOutputMode(%q) 应通过: %v", ok, err)
		}
	}
	for _, bad := range []string{"yaml", "xml", "jsonl"} {
		if _, err := ParseOutputMode(bad); err == nil {
			t.Errorf("ParseOutputMode(%q) 应拒绝", bad)
		}
	}
}

// TestParse_OutputFlag 解析：--output json 落到 Options.OutMode；非法值拒绝。
func TestParse_OutputFlag(t *testing.T) {
	opt, err := Parse([]string{"http://127.0.0.1:1/a.bin", "--output", "ndjson"})
	if err != nil {
		t.Fatal(err)
	}
	if opt.OutMode != OutputNDJSON {
		t.Errorf("OutMode = %q, 期望 ndjson", opt.OutMode)
	}
	// 缺省 table
	opt2, err := Parse([]string{"http://127.0.0.1:1/a.bin"})
	if err != nil {
		t.Fatal(err)
	}
	if opt2.OutMode != OutputTable {
		t.Errorf("缺省 OutMode = %q, 期望 table", opt2.OutMode)
	}
	if _, err := Parse([]string{"http://127.0.0.1:1/a.bin", "--output", "yaml"}); err == nil {
		t.Error("非法 --output 应拒绝")
	}
}