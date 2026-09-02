package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mao-jh/porter/cli"
	"github.com/Mao-jh/porter/persist"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func newTestModel(t *testing.T) Model {
	t.Helper()
	dir := t.TempDir()
	return New(cli.Options{StateDir: dir, Verify: ""})
}

func keyRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// TestAddTaskFlow a → 输入 → Enter：任务入列并返回引擎启动 Cmd。
func TestAddTaskFlow(t *testing.T) {
	m := newTestModel(t)
	km, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = km.(Model)
	if !m.adding {
		t.Fatal("按 a 后应进入添加模式")
	}
	m.input.SetValue("http://127.0.0.1/dir/big.bin")
	km, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = km.(Model)
	if m.adding {
		t.Fatal("Enter 后应退出添加模式")
	}
	if len(m.tasks) != 1 {
		t.Fatalf("任务数=%d want 1", len(m.tasks))
	}
	task := m.tasks[0]
	if task.URL != "http://127.0.0.1/dir/big.bin" || task.Output != "big.bin" {
		t.Fatalf("任务字段错误: %+v", task)
	}
	if task.State != StateRunning {
		t.Fatalf("启动后应 running, got %v", task.State)
	}
	if cmd != nil {
		t.Fatal("新机制下启动经 t.start（doneCh），不应有启动 Cmd")
	}
	if task.doneCh == nil || task.cancel == nil {
		t.Fatal("Enter 后引擎应已启动（doneCh/cancel 就位）")
	}
	// 停引擎并等待其完全退出：否则 goroutine 异步创建 state 子目录会与 TempDir 清理竞态
	task.cancel()
	select {
	case <-task.doneCh:
	case <-time.After(10 * time.Second):
		t.Fatal("引擎未在 10s 内退出")
	}
}

// TestAddTaskRejectsNonHTTP 非 http(s) URL 拒绝且不崩溃。
func TestAddTaskRejectsNonHTTP(t *testing.T) {
	m := newTestModel(t)
	km, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = km.(Model)
	m.input.SetValue("ftp://127.0.0.1/x")
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = km.(Model)
	if len(m.tasks) != 0 {
		t.Fatal("非法 URL 不应入列")
	}
	if m.errMsg == "" {
		t.Fatal("应设置错误提示")
	}
}

// TestDeriveOutputName 输出名推导：清洗、去重、空路径回退。
func TestDeriveOutputName(t *testing.T) {
	existing := []*Task{{Output: "big.bin"}}
	if got := deriveOutputName("http://127.0.0.1/d/big.bin", existing); got != "big-2.bin" {
		t.Fatalf("got %s want big-2.bin", got)
	}
	if got := deriveOutputName("http://127.0.0.1/d/a:b*c.bin", nil); got != "a_b_c.bin" {
		t.Fatalf("非法字符未清洗: %s", got)
	}
	if got := deriveOutputName("http://127.0.0.1/", nil); !strings.HasSuffix(got, ".bin") {
		t.Fatalf("空路径应回退 download-N.bin, got %s", got)
	}
}

// TestTaskDoneTransitions 引擎完成事件三态迁移：成功/取消→暂停/失败。
func TestTaskDoneTransitions(t *testing.T) {
	m := newTestModel(t)
	tasks := []*Task{
		{URL: "u1", Output: "a", State: StateRunning},
		{URL: "u2", Output: "b", State: StateRunning},
		{URL: "u3", Output: "c", State: StateRunning},
	}
	for i, tk := range tasks {
		tk.doneCh = make(chan error, 1)
		switch i {
		case 0:
			tk.doneCh <- nil
		case 1:
			tk.doneCh <- context.Canceled
		case 2:
			tk.doneCh <- os.ErrPermission
		}
	}
	m.tasks = tasks
	km, _ := m.Update(tickMsg(time.Now())) // tick 抽取 doneCh 并迁移状态
	m = km.(Model)
	if m.tasks[0].State != StateDone {
		t.Errorf("成功应 done, got %v", m.tasks[0].State)
	}
	if m.tasks[1].State != StatePaused {
		t.Errorf("取消应 paused（可续传）, got %v", m.tasks[1].State)
	}
	if m.tasks[2].State != StateFailed {
		t.Errorf("失败应 failed, got %v", m.tasks[2].State)
	}
}

// TestResumeAfterPause 暂停后的任务按 p 重新入队并返回启动 Cmd。
func TestResumeAfterPause(t *testing.T) {
	m := newTestModel(t)
	m.tasks = []*Task{{URL: "http://127.0.0.1/a.bin", Output: "a.bin", State: StatePaused}}
	m.cursor = 0
	km, cmd := m.Update(keyRunes("p"))
	m = km.(Model)
	if m.tasks[0].State != StateRunning {
		t.Fatalf("继续后应 running, got %v", m.tasks[0].State)
	}
	if cmd != nil {
		t.Fatal("新机制下继续经 t.start，不应有启动 Cmd")
	}
	if m.tasks[0].doneCh == nil || m.tasks[0].cancel == nil {
		t.Fatal("继续后引擎应已启动")
	}
	// 停引擎并等待其完全退出（避免 goroutine 与 TempDir 清理竞态）
	m.tasks[0].cancel()
	select {
	case <-m.tasks[0].doneCh:
	case <-time.After(10 * time.Second):
		t.Fatal("引擎未在 10s 内退出")
	}
}

// TestDeleteTask deleteTask 逻辑：移除行并清理 state 子目录（确认流见 TestDeleteConfirmFlow）。
func TestDeleteTask(t *testing.T) {
	m := newTestModel(t)
	tk := &Task{URL: "http://127.0.0.1/a.bin", Output: "a.bin", State: StatePaused}
	sub := tk.taskStateDir(m.baseOpt.StateDir)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "state.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.tasks = []*Task{tk}
	m.cursor = 0
	km, cmd := m.deleteTask(0)
	if cmd == nil {
		t.Fatal("deleteTask 应返回清理 Cmd")
	}
	msg := cmd()
	km, _ = m.Update(msg)
	m = km.(Model)
	if len(m.tasks) != 0 {
		t.Fatal("任务应被移除")
	}
	if _, err := os.Stat(sub); !os.IsNotExist(err) {
		t.Fatal("state 子目录应被清理")
	}
}

// TestRefreshProgressAndSpeed 进度轮询读 state.json 并差分出速度。
func TestRefreshProgressAndSpeed(t *testing.T) {
	dir := t.TempDir()
	tk := &Task{URL: "http://x/f.bin", Output: "f.bin", State: StateRunning}
	sub := tk.taskStateDir(dir) // 任务状态在哈希子目录（与引擎一致）
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := persist.Open(sub)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(&persist.State{ID: "f.bin", URL: "http://x/f.bin", FileSize: 1000, Done: 400, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	m := newTestModel(t)
	m.baseOpt.StateDir = dir
	m.tasks = []*Task{tk}
	// 第一帧：建立基线
	m.refreshProgress()
	if tk.Done != 400 || tk.Size != 1000 {
		t.Fatalf("进度未读取: done=%d size=%d", tk.Done, tk.Size)
	}
	// 引擎推进进度（第二次落盘）
	if err := store.Put(&persist.State{ID: "f.bin", URL: "http://x/f.bin", FileSize: 1000, Done: 900, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	tk.lastAt = tk.lastAt.Add(-500 * time.Millisecond) // 模拟 0.5s 间隔
	m.refreshProgress()
	if tk.Done != 900 {
		t.Fatalf("进度应更新到 900, got %d", tk.Done)
	}
	if tk.Speed <= 0 {
		t.Fatalf("速度差分应 >0, got %v", tk.Speed)
	}
	// R21：ETA = 剩余字节 / 速率（公式接线断言；数值由差分决定）
	if want := int64(float64(tk.Size-tk.Done) / tk.Speed); tk.ETA != want {
		t.Errorf("ETA = %d, 期望 %d", tk.ETA, want)
	}
}

// TestReadStatesViaEngineFormat 与引擎 flushState 的真实 JSON 格式互通。
func TestReadStatesViaEngineFormat(t *testing.T) {
	dir := t.TempDir()
	store, err := persist.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	shards := []persist.ShardState{{Start: 0, End: 100, Done: 40}}
	if err := store.Put(&persist.State{
		ID: filepath.Join(dir, "x.bin"), URL: "http://x", FileSize: 100,
		Done: 40, Status: "running", Shards: shards,
	}); err != nil {
		t.Fatal(err)
	}
	st, ok := readState(dir, filepath.Join(dir, "x.bin"))
	if !ok {
		t.Fatal("应能读出引擎写入的状态")
	}
	if len(st.Shards) != 1 || st.Shards[0].Done != 40 {
		t.Fatalf("shard 进度丢失: %+v", st)
	}
	// JSON 原文可解析（防格式漂移）
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil || len(m) != 1 {
		t.Fatal("state.json 结构异常")
	}
}

// TestRestoreTasks 从磁盘恢复历史任务列表。
func TestRestoreTasks(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "tabc123")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := persist.Open(sub)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Put(&persist.State{ID: "done.bin", URL: "http://x/done.bin", FileSize: 10, Done: 10, Status: "done"})
	_ = store.Put(&persist.State{ID: "part.bin", URL: "http://x/part.bin", FileSize: 100, Done: 30, Status: "running"})

	m := newTestModel(t)
	m.baseOpt.StateDir = root
	m.RestoreTasks()
	if len(m.tasks) != 2 {
		t.Fatalf("应恢复 2 任务, got %d", len(m.tasks))
	}
	states := map[string]TaskState{}
	for _, tk := range m.tasks {
		states[tk.Output] = tk.State
	}
	if states["done.bin"] != StateDone || states["part.bin"] != StatePaused {
		t.Fatalf("恢复状态错误: %+v", states)
	}
}

// TestViewAssertions View 字符串断言（渲染即验收，布局 A 语义）。
func TestViewAssertions(t *testing.T) {
	m := newTestModel(t)
	m.tasks = []*Task{
		{URL: "u", Output: "ok.bin", State: StateDone, Size: 100, Done: 100},
		{URL: "u", Output: "run.bin", State: StateRunning, Size: 200, Done: 50, Speed: 1024, ETA: 90},
	}
	v := m.View()
	for _, want := range []string{"ok.bin", "run.bin", "✓", "▼", "100B / 100B", "1.0KB/s", "█", "1m 30s 剩余"} {
		if !strings.Contains(v, want) {
			t.Errorf("View 缺少 %q\n---\n%s", want, v)
		}
	}
	// 帮助信息不再常驻：默认视图不含键位罗列（§11 验收）
	if strings.Contains(v, "a:添加  x:代理  s:设置") {
		t.Errorf("帮助不应常驻多行罗列\n---\n%s", v)
	}
}

// TestHumanBytes 字节格式化。
func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0: "0B", 512: "512B", 1024: "1.0KB", 1536: "1.5KB",
		1048576: "1.0MB", 5 * 1024 * 1024 * 1024: "5.0GB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d)=%s want %s", in, got, want)
		}
	}
}

// TestSelftestVerdict selftest 终止判定。
func TestSelftestVerdict(t *testing.T) {
	m := newTestModel(t)
	m.Selftest = true
	if !m.allSettled() || m.verdict() != "failed" {
		t.Fatal("空任务列表应视为 settled 且快速失败（selftest 防空跑）")
	}
	m.tasks = []*Task{
		{URL: "u1", Output: "a", State: StateDone},
		{URL: "u2", Output: "b", State: StateDone},
	}
	if !m.allSettled() || m.verdict() != "ok" {
		t.Fatal("全 done 应 settled 且 verdict=ok")
	}
	// paused/failed 都属于"未全部完成"→ failed（selftest 退出码 1）
	m.tasks[1].State = StatePaused
	if m.verdict() != "failed" {
		t.Fatal("含 paused 应 verdict=failed")
	}
	m.tasks[1].State = StateFailed
	if m.verdict() != "failed" {
		t.Fatal("含 failed 应 verdict=failed")
	}
}

// TestUppercaseKeys 大写 A 同样进入添加模式（Shift/Caps Lock 兼容）。
func TestUppercaseKeys(t *testing.T) {
	m := newTestModel(t)
	// 大写 A（tea.KeyRunes 携带 'A'，String()="A"，原实现不匹配导致无响应）
	km, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})
	m = km.(Model)
	if !m.adding {
		t.Fatal("大写 A 应进入添加模式")
	}
	// Esc 退出后，大写 X 进入代理模式
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = km.(Model)
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")})
	m = km.(Model)
	if !m.proxying {
		t.Fatal("大写 X 应进入代理输入模式")
	}
}

// TestProxyFlow x → 输入代理 → Enter：baseOpt.Proxy 生效；空值清除。
func TestProxyFlow(t *testing.T) {
	m := newTestModel(t)
	km, _ := m.Update(keyRunes("x"))
	m = km.(Model)
	if !m.proxying {
		t.Fatal("按 x 应进入代理输入模式")
	}
	m.input.SetValue("http://127.0.0.1:7890")
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = km.(Model)
	if m.proxying {
		t.Fatal("Enter 后应退出代理输入模式")
	}
	if m.baseOpt.Proxy != "http://127.0.0.1:7890" {
		t.Fatalf("代理未生效: %q", m.baseOpt.Proxy)
	}
	if m.status == "" {
		t.Fatal("设置代理应有状态提示")
	}
	// 非法格式拒绝
	km, _ = m.Update(keyRunes("x"))
	m = km.(Model)
	m.input.SetValue("ftp://bad")
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = km.(Model)
	if m.baseOpt.Proxy != "http://127.0.0.1:7890" {
		t.Fatal("非法代理不应覆盖已有配置")
	}
	if m.errMsg == "" {
		t.Fatal("非法代理应提示格式")
	}
	// 空值清除代理
	km, _ = m.Update(keyRunes("x"))
	m = km.(Model)
	m.input.SetValue("")
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = km.(Model)
	if m.baseOpt.Proxy != "" {
		t.Fatalf("空值应清除代理, got %q", m.baseOpt.Proxy)
	}
}

// TestH3RefusalHint H-3 拒绝 → 失败并给出可行动指引（未配代理时）。
func TestH3RefusalHint(t *testing.T) {
	m := newTestModel(t)
	tk := &Task{URL: "http://example.com/f.bin", Output: "f.bin", State: StateRunning}
	tk.doneCh = make(chan error, 1)
	tk.doneCh <- fmt.Errorf("探测资源失败: host example.com resolves to non-loopback 93.184.216.34 (H-3)")
	m.tasks = []*Task{tk}
	km, _ := m.Update(tickMsg(time.Now()))
	m = km.(Model)
	if m.tasks[0].State != StateFailed {
		t.Fatalf("应失败, got %v", m.tasks[0].State)
	}
	if !strings.Contains(m.errMsg, "H-3") || !strings.Contains(m.errMsg, "x") {
		t.Fatalf("应给出含 x 指引的 H-3 提示, got %q", m.errMsg)
	}
	// 非 H-3 失败不产生该提示
	m2 := newTestModel(t)
	tk2 := &Task{URL: "http://127.0.0.1/f.bin", Output: "f2.bin", State: StateRunning}
	tk2.doneCh = make(chan error, 1)
	tk2.doneCh <- fmt.Errorf("探测资源失败: connection refused")
	m2.tasks = []*Task{tk2}
	km2, _ := m2.Update(tickMsg(time.Now()))
	m2 = km2.(Model)
	if strings.Contains(m2.errMsg, "H-3") {
		t.Fatalf("非 H-3 错误不应触发 H-3 提示: %q", m2.errMsg)
	}
}

// TestViewHelpLine 底部键帽含操作键；代理/限速 overlay；H-3 失败行尾短标签。
func TestViewHelpLine(t *testing.T) {
	m := newTestModel(t)
	v := m.View()
	// 底部键帽：单行、含主操作键（§5.0 footer，全中文说明）
	for _, want := range []string{"a", "添加", "/", "过滤", "?", "帮助", "q", "退出"} {
		if !strings.Contains(v, want) {
			t.Fatalf("底部键帽应含 %q\n---\n%s", want, v)
		}
	}
	// 代理 overlay
	km, _ := m.Update(keyRunes("x"))
	m = km.(Model)
	v = m.View()
	if !strings.Contains(v, "代理") || !strings.Contains(v, "socks5://") {
		t.Fatalf("代理 overlay 应渲染\n---\n%s", v)
	}
}

// TestH3FailureTag H-3 失败行尾短标签。
func TestH3FailureTag(t *testing.T) {
	m := newTestModel(t)
	m.tasks = []*Task{{URL: "u", Output: "f.bin", State: StateFailed,
		Err: fmt.Errorf("探测资源失败: host example.com resolves to non-loopback (H-3)")}}
	v := m.View()
	if !strings.Contains(v, "H-3") {
		t.Fatalf("H-3 失败行尾应显示短标签\n---\n%s", v)
	}
}

// TestSettingsFlow 设置面板：s 打开 → 档位切换 → 自定义输入 → Esc 关闭。
func TestSettingsFlow(t *testing.T) {
	m := newTestModel(t)
	km, _ := m.Update(keyRunes("s"))
	m = km.(Model)
	if !m.settings || m.settingRow != 0 {
		t.Fatal("按 s 应打开设置面板并定位限速行")
	}
	// 限速档位循环：0(不限) → 1MiB/s
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = km.(Model)
	if m.baseOpt.Limit != 1<<20 {
		t.Fatalf("限速应切到 1MiB/s, got %d", m.baseOpt.Limit)
	}
	// 继续切到 5MiB/s → 10MiB/s → 自定义输入
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = km.(Model)
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = km.(Model)
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = km.(Model)
	if !m.settingCustom {
		t.Fatal("切过全部档位后应进入自定义输入")
	}
	m.input.SetValue("5M")
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = km.(Model)
	if m.settingCustom {
		t.Fatal("自定义提交后应退出输入")
	}
	if m.baseOpt.Limit != 5<<20 {
		t.Fatalf("自定义限速 5M 应=5MiB, got %d", m.baseOpt.Limit)
	}
	// 分片行：↓ → Enter → 1 片
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = km.(Model)
	if m.settingRow != 1 {
		t.Fatalf("↓ 应到分片行, got %d", m.settingRow)
	}
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = km.(Model)
	if m.baseOpt.Shards != 1 {
		t.Fatalf("分片应切到 1, got %d", m.baseOpt.Shards)
	}
	// 校验行：先置 sha256（预设档 0），↓ → Enter → sha1
	m.baseOpt.Verify = "sha256"
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = km.(Model)
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = km.(Model)
	if string(m.baseOpt.Verify) != "sha1" {
		t.Fatalf("校验应切到 sha1, got %q", m.baseOpt.Verify)
	}
	// 代理行：↓ → Enter → http://127.0.0.1:7890
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = km.(Model)
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = km.(Model)
	if m.baseOpt.Proxy != "http://127.0.0.1:7890" {
		t.Fatalf("代理应切到预设, got %q", m.baseOpt.Proxy)
	}
	// q 关闭
	km, _ = m.Update(keyRunes("q"))
	m = km.(Model)
	if m.settings {
		t.Fatal("q 应关闭设置面板")
	}
}

// TestSettingsRender 设置面板渲染断言（档位高亮/自定义值回显）。
func TestSettingsRender(t *testing.T) {
	m := newTestModel(t)
	km, _ := m.Update(keyRunes("s"))
	m = km.(Model)
	v := m.View()
	for _, want := range []string{"限速", "分片", "校验", "代理", "自定义…", "[不限]", "q/Esc:关闭"} {
		if !strings.Contains(v, want) {
			t.Errorf("设置面板缺少 %q\n---\n%s", want, v)
		}
	}
	// 自定义值回显：设一个预设外的限速再打开设置
	m2 := newTestModel(t)
	m2.baseOpt.Limit = 3 << 20
	m2.baseOpt.Proxy = "socks5://127.0.0.1:9999"
	km, _ = m2.Update(keyRunes("s"))
	m2 = km.(Model)
	v = m2.View()
	if !strings.Contains(v, "[自定义…]") || !strings.Contains(v, "3.0MB/s") {
		t.Fatalf("自定义值应高亮自定义档并回显\n---\n%s", v)
	}
}

// TestPasteCtrlV Ctrl+V 从剪贴板读入输入框（替换 pasteText 隔离系统剪贴板）。
// 打开添加时剪贴板不可读 → 不自动填（自动读取见 TestAutoPasteClipboardURL）。
func TestPasteCtrlV(t *testing.T) {
	orig := pasteText
	defer func() { pasteText = orig }()

	m := newTestModel(t)
	pasteText = func() (string, bool) { return "", false } // 打开时剪贴板不可读
	km, _ := m.Update(keyRunes("a"))
	m = km.(Model)
	if m.input.Value() != "" {
		t.Fatalf("剪贴板不可读时按 a 不应自动填, got %q", m.input.Value())
	}
	pasteText = func() (string, bool) { return "https://example.com/a.bin", true }
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	m = km.(Model)
	if got := m.input.Value(); got != "https://example.com/a.bin" {
		t.Fatalf("Ctrl+V 应粘贴剪贴板内容, got %q", got)
	}
	// 剪贴板不可读 → 提示且不清空已有输入
	pasteText = func() (string, bool) { return "", false }
	m.input.SetValue("http://127.0.0.1/x")
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	m = km.(Model)
	if m.input.Value() != "http://127.0.0.1/x" {
		t.Fatal("剪贴板不可读时不应改动输入")
	}
	if !strings.Contains(m.errMsg, "剪贴板") {
		t.Fatalf("应有剪贴板提示, got %q", m.errMsg)
	}
}

// TestAutoPasteClipboardURL IDM 式：按 a 打开添加任务时自动读取剪贴板 URL 填入输入框。
func TestAutoPasteClipboardURL(t *testing.T) {
	orig := pasteText
	defer func() { pasteText = orig }()

	// 剪贴板是 URL → 自动填入
	m := newTestModel(t)
	pasteText = func() (string, bool) { return "  https://example.com/a.bin  ", true }
	km, _ := m.Update(keyRunes("a"))
	m = km.(Model)
	if !m.adding {
		t.Fatal("按 a 应进入添加模式")
	}
	if got := m.input.Value(); got != "https://example.com/a.bin" {
		t.Fatalf("剪贴板 URL 应自动填入（去首尾空白）, got %q", got)
	}
	// 剪贴板是普通文本 → 不自动填
	m2 := newTestModel(t)
	pasteText = func() (string, bool) { return "随便一段文字不是URL", true }
	km, _ = m2.Update(keyRunes("a"))
	m2 = km.(Model)
	if got := m2.input.Value(); got != "" {
		t.Fatalf("非 URL 剪贴板不应自动填, got %q", got)
	}
	// 剪贴板不可读 → 不自动填
	m3 := newTestModel(t)
	pasteText = func() (string, bool) { return "", false }
	km, _ = m3.Update(keyRunes("a"))
	m3 = km.(Model)
	if got := m3.input.Value(); got != "" {
		t.Fatalf("剪贴板不可读不应自动填, got %q", got)
	}
}

// TestLooksLikeURL 剪贴板 URL 判定（http/https 前缀，与添加校验一致）。
func TestLooksLikeURL(t *testing.T) {
	cases := map[string]bool{
		"http://x/a.bin":      true,
		"https://x.com/a":     true,
		"  https://x.com  ":   true,
		"magnet:?xt=urn:btih": false, // 添加任务暂不支持 magnet
		"不是链接":                false,
		"":                    false,
	}
	for in, want := range cases {
		if got := looksLikeURL(strings.TrimSpace(in)); got != want {
			t.Errorf("looksLikeURL(%q)=%v want %v", in, got, want)
		}
	}
}

// TestParseSpeed 限速解析：后缀/裸数字/非法。
func TestParseSpeed(t *testing.T) {
	cases := map[string]int64{
		"": 0, "0": 0, "5M": 5 << 20, "5m": 5 << 20,
		"1024k": 1 << 20, "1G": 1 << 30, "1048576": 1 << 20,
	}
	for in, want := range cases {
		got, err := parseSpeed(in)
		if err != nil || got != want {
			t.Errorf("parseSpeed(%q)=%d,%v want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"abc", "-1", "5X", "9999999999999999999999"} {
		if _, err := parseSpeed(bad); err == nil {
			t.Errorf("parseSpeed(%q) 应报错", bad)
		}
	}
}

// TestParseShards 分片解析。
func TestParseShards(t *testing.T) {
	if v, err := parseShards(""); err != nil || v != 0 {
		t.Fatalf("空应=0(自动), got %d,%v", v, err)
	}
	if v, err := parseShards("8"); err != nil || v != 8 {
		t.Fatalf("8 应=8, got %d,%v", v, err)
	}
	for _, bad := range []string{"129", "-1", "abc"} {
		if _, err := parseShards(bad); err == nil {
			t.Errorf("parseShards(%q) 应报错", bad)
		}
	}
	// 新档位上限内的合法值
	for _, ok := range []string{"16", "32", "128"} {
		if _, err := parseShards(ok); err != nil {
			t.Errorf("parseShards(%q) 应合法: %v", ok, err)
		}
	}
}

// ---- 鼠标交互（R32 起，按钮系统移除后仅行选择） ----

func mouseClick(x, y int) tea.MouseMsg {
	return tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: x, Y: y}
}

// TestViewTaskIcons 布局 A 状态图标渲染：running=▼ done=✓ paused=‖ failed=✕。
func TestViewTaskIcons(t *testing.T) {
	m := newTestModel(t)
	m.tasks = []*Task{
		{URL: "u", Output: "run.bin", State: StateRunning},
		{URL: "u", Output: "ok.bin", State: StateDone},
		{URL: "u", Output: "pa.bin", State: StatePaused},
		{URL: "u", Output: "bad.bin", State: StateFailed},
	}
	v := m.View()
	for _, want := range []string{"▼", "✓", "‖", "✕"} {
		if !strings.Contains(v, want) {
			t.Errorf("View 缺少状态图标 %q\n---\n%s", want, v)
		}
	}
	// 禁用字符零残留（§11 验收）
	for _, r := range "⏸⏵⟳⧗⌛⏳✗" {
		if strings.ContainsRune(v, r) {
			t.Errorf("View 含禁用字符 %q", r)
		}
	}
}

// TestLayoutASelection 布局 A 选中态：行首指示条 █ + PANEL2 底色，行位按 4 步进。
func TestLayoutASelection(t *testing.T) {
	m := newTestModel(t)
	m.tasks = []*Task{
		{URL: "u", Output: "a.bin", State: StateRunning},
		{URL: "u", Output: "b.bin", State: StateDone},
	}
	m.cursor = 0
	m.View()
	// 选中行热区 y=3，次行 y=7（item 步进 4，§5.1）
	z0 := findZoneAt(lastFrame.rows, "select", 0)
	z1 := findZoneAt(lastFrame.rows, "select", 1)
	if z0 == nil || z1 == nil {
		t.Fatal("应记录两行选中热区")
	}
	if z0.y != 3 || z1.y != 7 {
		t.Fatalf("item 步进错误: y0=%d y1=%d（want 3/7）", z0.y, z1.y)
	}
	// 选中行渲染含状态图标 + 文件名
	v := m.View()
	if !strings.Contains(v, "a.bin") || !strings.Contains(v, "▼") {
		t.Fatalf("布局 A 应渲染选中任务\n---\n%s", v)
	}
}

// findZoneAt 返回指定 rowIdx 且 action 匹配的热区（测试辅助）。
func findZoneAt(zones []clickZone, action string, rowIdx int) *clickZone {
	for i := range zones {
		if zones[i].action == action && zones[i].rowIdx == rowIdx {
			return &zones[i]
		}
	}
	return nil
}

// TestMouseSelectRow 点击任务行 → 选中。
func TestMouseSelectRow(t *testing.T) {
	m := newTestModel(t)
	m.tasks = []*Task{
		{URL: "u", Output: "a.bin", State: StateDone},
		{URL: "u", Output: "b.bin", State: StateDone},
		{URL: "u", Output: "c.bin", State: StateDone},
	}
	m.cursor = 0
	m.View()
	var rowY, rowX int
	found := false
	for _, rz := range lastFrame.rows {
		if rz.rowIdx == 1 {
			rowY, rowX, found = rz.y, rz.start+1, true
			break
		}
	}
	if !found {
		t.Fatal("应记录第 2 行选中热区")
	}
	km, _ := m.Update(mouseClick(rowX, rowY))
	m = km.(Model)
	if m.cursor != 1 {
		t.Fatalf("点击第 2 行应选中, got %d", m.cursor)
	}
	// 点击第 0 行
	for _, rz := range lastFrame.rows {
		if rz.rowIdx == 0 {
			km, _ = m.Update(mouseClick(rz.start+1, rz.y))
			m = km.(Model)
			break
		}
	}
	if m.cursor != 0 {
		t.Fatalf("点击第 1 行应选中, got %d", m.cursor)
	}
}

// TestMouseWheel 滚轮上下移动选中行（含边界）。
func TestMouseWheel(t *testing.T) {
	m := newTestModel(t)
	m.tasks = []*Task{
		{URL: "u", Output: "a.bin", State: StateDone},
		{URL: "u", Output: "b.bin", State: StateDone},
		{URL: "u", Output: "c.bin", State: StateDone},
	}
	m.cursor = 1
	km, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	m = km.(Model)
	if m.cursor != 0 {
		t.Fatalf("滚轮上应选中上一行, got %d", m.cursor)
	}
	km, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	m = km.(Model)
	if m.cursor != 0 {
		t.Fatal("顶部滚轮上应保持 0")
	}
	km, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	m = km.(Model)
	km, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	m = km.(Model)
	km, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	m = km.(Model)
	if m.cursor != 2 {
		t.Fatalf("底部滚轮下应停在最后一行, got %d", m.cursor)
	}
}

// TestDeleteConfirmFlow 键盘 d → 确认 overlay → y 确认删除；n/Esc 取消。
func TestDeleteConfirmFlow(t *testing.T) {
	m := newTestModel(t)
	tk := &Task{URL: "http://127.0.0.1/a.bin", Output: "a.bin", State: StateDone}
	sub := tk.taskStateDir(m.baseOpt.StateDir)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "state.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.tasks = []*Task{tk}
	m.cursor = 0

	// 按 d → 进入确认，不直接删除
	km, cmd := m.Update(keyRunes("d"))
	m = km.(Model)
	if cmd != nil {
		t.Fatal("d 应只弹确认 overlay，不应立即删除")
	}
	if m.confirming == nil {
		t.Fatal("d 应打开确认 overlay")
	}
	// n 取消 → 不删除
	km, _ = m.Update(keyRunes("n"))
	m = km.(Model)
	if m.confirming != nil || len(m.tasks) != 1 {
		t.Fatal("n 应取消确认且保留任务")
	}
	// 再 d → y 确认删除
	km, _ = m.Update(keyRunes("d"))
	m = km.(Model)
	km, cmd = m.Update(keyRunes("y"))
	m = km.(Model)
	if cmd == nil {
		t.Fatal("y 应返回删除 Cmd")
	}
	msg := cmd()
	km, _ = m.Update(msg)
	m = km.(Model)
	if len(m.tasks) != 0 {
		t.Fatal("y 确认后任务应被移除")
	}
	if _, err := os.Stat(sub); !os.IsNotExist(err) {
		t.Fatal("state 子目录应被清理")
	}
}

// TestLimitFlow l → 输入限速 → Enter 生效；Esc 取消。
func TestLimitFlow(t *testing.T) {
	m := newTestModel(t)
	km, _ := m.Update(keyRunes("l"))
	m = km.(Model)
	if !m.limiting {
		t.Fatal("按 l 应进入限速输入")
	}
	v := m.View()
	if !strings.Contains(v, "限速") {
		t.Fatalf("限速 overlay 应渲染\n---\n%s", v)
	}
	// 输入 5M
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("5")})
	m = km.(Model)
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("M")})
	m = km.(Model)
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = km.(Model)
	if m.limiting {
		t.Fatal("Enter 后应退出限速输入")
	}
	if m.baseOpt.Limit != 5<<20 {
		t.Fatalf("限速应=5MiB, got %d", m.baseOpt.Limit)
	}
}

// TestHelpOverlay ? 打开帮助 overlay，q/Esc 关闭。
func TestHelpOverlay(t *testing.T) {
	m := newTestModel(t)
	km, _ := m.Update(keyRunes("?"))
	m = km.(Model)
	if !m.helpOpen {
		t.Fatal("按 ? 应打开帮助")
	}
	v := m.View()
	if !strings.Contains(v, "帮助") || !strings.Contains(v, "a  添加任务") {
		t.Fatalf("帮助 overlay 应含键位说明\n---\n%s", v)
	}
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = km.(Model)
	if m.helpOpen {
		t.Fatal("Esc 应关闭帮助")
	}
}

// TestFilterFlow / → 输入过滤 → Enter 应用；Esc 清空。
func TestFilterFlow(t *testing.T) {
	m := newTestModel(t)
	m.tasks = []*Task{
		{URL: "u", Output: "alpha.bin", State: StateDone},
		{URL: "u", Output: "beta.bin", State: StateDone},
	}
	km, _ := m.Update(keyRunes("/"))
	m = km.(Model)
	if !m.filtering {
		t.Fatal("按 / 应进入过滤输入")
	}
	m.input.SetValue("alpha")
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = km.(Model)
	if m.filtering {
		t.Fatal("Enter 后应退出过滤输入")
	}
	if m.filter != "alpha" {
		t.Fatalf("filter=%q want alpha", m.filter)
	}
	// 布局 A 只显示匹配任务
	v := m.View()
	if !strings.Contains(v, "alpha.bin") || strings.Contains(v, "beta.bin") {
		t.Fatalf("过滤后应只显示 alpha\n---\n%s", v)
	}
	// Esc 清空过滤
	km, _ = m.Update(keyRunes("/"))
	m = km.(Model)
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = km.(Model)
	if m.filter != "" {
		t.Fatalf("Esc 应清空过滤, got %q", m.filter)
	}
}

// TestLayoutSwitch Tab/1/2/3 布局切换（§5.0 手动覆盖）。
func TestLayoutSwitch(t *testing.T) {
	m := newTestModel(t)
	// 默认自动布局；1/2/3 直达并固定
	km, _ := m.Update(keyRunes("2"))
	m = km.(Model)
	if m.layout != LayoutB || m.layoutAuto {
		t.Fatalf("按 2 应切到布局 B 且手动固定, got %v auto=%v", m.layout, m.layoutAuto)
	}
	km, _ = m.Update(keyRunes("3"))
	m = km.(Model)
	if m.layout != LayoutC {
		t.Fatalf("按 3 应切到布局 C, got %v", m.layout)
	}
	// Tab 循环
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = km.(Model)
	if m.layout != LayoutA {
		t.Fatalf("Tab 从 C 应回 A, got %v", m.layout)
	}
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = km.(Model)
	if m.layout != LayoutB {
		t.Fatalf("Tab 从 A 应到 B, got %v", m.layout)
	}
}

// TestLayoutAutoSwitch 宽度自动切换布局（§5.0）。
func TestLayoutAutoSwitch(t *testing.T) {
	m := newTestModel(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 25})
	m2 := m
	if autoLayout(80) != LayoutA || autoLayout(120) != LayoutB || autoLayout(160) != LayoutC {
		t.Fatal("autoLayout 阈值错误")
	}
	_ = m2
	// 收到宽度消息后自动选布局
	km, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = km.(Model)
	if m.layout != LayoutB {
		t.Fatalf("宽 120 应自动选 B, got %v", m.layout)
	}
	km, _ = m.Update(tea.WindowSizeMsg{Width: 170, Height: 35})
	m = km.(Model)
	if m.layout != LayoutC {
		t.Fatalf("宽 170 应自动选 C, got %v", m.layout)
	}
}

// TestLayoutBRender 布局 B 右栏详情：主进度条/面积图/分片图/元数据不越界。
func TestLayoutBRender(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 120, 30
	m.layout, m.layoutAuto = LayoutB, false
	m.tasks = []*Task{
		{URL: "http://x/big.bin", Output: "big.bin", State: StateRunning,
			Size: 10 << 20, Done: 3 << 20, Speed: 1 << 20, ETA: 7},
	}
	m.tasks[0].speedRing = newSpeedRing(60)
	m.tasks[0].speedRing.append(1 << 20)
	m.cursor = 0
	v := m.View()
	for _, want := range []string{"队列", "big.bin", "吞吐", "分片", "下载中", "1.0MB/s"} {
		if !strings.Contains(v, want) {
			t.Errorf("布局 B 缺少 %q\n---\n%s", want, v)
		}
	}
	// 每行显示宽度不超过 120（无越界）
	for _, line := range strings.Split(v, "\n") {
		if lipgloss.Width(line) > 120 {
			t.Errorf("布局 B 行越界（宽 %d > 120）: %q", lipgloss.Width(line), line)
		}
	}
}

// TestQueueExcludesDone 已完成任务不出现在下载队列（活动区），仅在"已完成"分组显示。
func TestQueueExcludesDone(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 120, 30
	m.layout, m.layoutAuto = LayoutB, false
	m.tasks = []*Task{
		{URL: "u", Output: "run-a.bin", State: StateRunning},
		{URL: "u", Output: "run-b.bin", State: StateRunning},
		{URL: "u", Output: "done-c.bin", State: StateDone},
	}
	m.cursor = 0
	lines := strings.Split(m.View(), "\n")
	leftCol := func(l string) string {
		if len(l) > 56 {
			return l[:56] // 中缝 x=56，左栏部分
		}
		return l
	}
	// 活动区屏 y=4..16：不得出现已完成任务
	for i := 4; i <= 16; i++ {
		if strings.Contains(lines[i], "done-c.bin") {
			t.Fatalf("已完成任务不应出现在队列活动区（y=%d）: %q", i, lines[i])
		}
	}
	// 活动区：running 任务应显示
	if !strings.Contains(lines[4], "run-a.bin") {
		t.Fatalf("活动区应显示下载中任务: %q", lines[4])
	}
	// 已完成区屏 y=21..26：应显示已完成任务
	found := false
	for i := 21; i <= 26; i++ {
		if strings.Contains(leftCol(lines[i]), "done-c.bin") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("已完成任务应出现在已完成分组")
	}
	// 已完成区（左栏）不得出现下载中任务（右栏元数据可能显示选中任务，不在此检查）
	for i := 21; i <= 26; i++ {
		if strings.Contains(leftCol(lines[i]), "run-a.bin") || strings.Contains(leftCol(lines[i]), "run-b.bin") {
			t.Fatalf("已完成区不应含下载中任务（y=%d）: %q", i, leftCol(lines[i]))
		}
	}
}

// TestLayoutCRender 布局 C 仪表盘：统计卡/吞吐图/表格/事件日志。
func TestLayoutCRender(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 120, 35
	m.layout, m.layoutAuto = LayoutC, false
	m.tasks = []*Task{
		{URL: "http://x/big.bin", Output: "big.bin", State: StateRunning,
			Size: 10 << 20, Done: 3 << 20, Speed: 1 << 20, ETA: 7},
	}
	m.globalSpeed.append(1 << 20)
	m.cursor = 0
	v := m.View()
	for _, want := range []string{"总速度", "任务", "已下载", "队列剩余", "big.bin", "1.0MB/s"} {
		if !strings.Contains(v, want) {
			t.Errorf("布局 C 缺少 %q\n---\n%s", want, v)
		}
	}
	for _, line := range strings.Split(v, "\n") {
		if lipgloss.Width(line) > 120 {
			t.Errorf("布局 C 行越界（宽 %d > 120）: %q", lipgloss.Width(line), line)
		}
	}
}

// TestFooterSingleLineKeycaps 底部键帽单行且 ≤9 个（§7.1 [MUST]）。
func TestFooterSingleLineKeycaps(t *testing.T) {
	m := newTestModel(t)
	m.tasks = []*Task{{URL: "u", Output: "run.bin", State: StateRunning}}
	m.cursor = 0
	caps := m.footerKeycaps()
	if len(caps) == 0 || len(caps) > 9 {
		t.Fatalf("底部键帽数=%d（要求 7–9 个）", len(caps))
	}
	v := m.View()
	lines := strings.Split(v, "\n")
	// footer 为末两行：分隔线 + 单行键帽（不应多行罗列）
	footer := lines[len(lines)-1]
	if strings.Contains(footer, "\n") {
		t.Fatal("底部键帽必须单行")
	}
}

// TestMouseIgnoredInInputMode 输入模式下鼠标点击不应触发动作（防误触）。
func TestMouseIgnoredInInputMode(t *testing.T) {
	m := newTestModel(t)
	km, _ := m.Update(keyRunes("a"))
	m = km.(Model)
	m.View()
	km, _ = m.Update(mouseClick(3, 1))
	m = km.(Model)
	if !m.adding {
		t.Fatal("输入模式下鼠标点击不应退出添加模式")
	}
}

// ---- P0 增强（R33：全局汇总 / 失败优先 / 错误展开） ----

// TestSummaryLine 全局汇总行：活动数（running+queued）/ 总速 / 总进度。
// 进度仅统计已知大小任务（size>0），全部未知 → "--"。
func TestSummaryLine(t *testing.T) {
	m := newTestModel(t)
	m.tasks = []*Task{
		{URL: "u", Output: "run.bin", State: StateRunning, Size: 1000, Done: 250, Speed: 2048},
		{URL: "u", Output: "que.bin", State: StateQueued, Size: 1000, Done: 0},
		{URL: "u", Output: "ok.bin", State: StateDone, Size: 1000, Done: 1000},
	}
	got := m.summaryLine()
	// 活动 = running+queued = 2；总速 = 2048B/s = 2.0KB/s；
	// 总进度 = 所有已知大小任务 (250+0+1000)/(1000+1000+1000) = 41%（queued 也计入总量）
	for _, want := range []string{"活动 2", "2.0KB/s", "41%"} {
		if !strings.Contains(got, want) {
			t.Errorf("汇总行缺少 %q: %s", want, got)
		}
	}
	// 未知大小任务不拖累进度；有已知大小即算百分比
	m2 := newTestModel(t)
	m2.tasks = []*Task{
		{URL: "u", Output: "run.bin", State: StateRunning, Size: 0, Done: 0, Speed: 1024},
		{URL: "u", Output: "ok.bin", State: StateDone, Size: 100, Done: 100},
	}
	if got := m2.summaryLine(); !strings.Contains(got, "100%") {
		t.Fatalf("有已知大小任务应算进度: %s", got)
	}
	// 全部未知大小 → "--"
	m3 := newTestModel(t)
	m3.tasks = []*Task{{URL: "u", Output: "run.bin", State: StateRunning, Size: 0, Done: 0}}
	if got := m3.summaryLine(); !strings.Contains(got, "--") {
		t.Fatalf("全部未知大小应显示 --%%: %s", got)
	}
	// 无任务时 View 不渲染汇总行
	m4 := newTestModel(t)
	if strings.Contains(m4.View(), "活动 ") {
		t.Fatal("无任务不应渲染汇总行")
	}
}

// TestTaskOrder 显示排序：下载中 > 错误 > 暂停 > 排队 > 完成（§6.2）；
// 同状态稳定保序；仅影响显示层，Model 内任务顺序与 cursor 语义不变。
func TestTaskOrder(t *testing.T) {
	tasks := []*Task{
		{Output: "done", State: StateDone},
		{Output: "fail1", State: StateFailed},
		{Output: "paused", State: StatePaused},
		{Output: "run", State: StateRunning},
		{Output: "queued", State: StateQueued},
		{Output: "fail2", State: StateFailed},
	}
	order := taskOrder(tasks)
	want := []int{3, 1, 5, 2, 4, 0} // run fail1 fail2 paused queued done（同状态保原序）
	if len(order) != len(want) {
		t.Fatalf("长度=%d want %d", len(order), len(want))
	}
	for i, w := range want {
		if order[i] != w {
			t.Fatalf("order[%d]=%d want %d（全序 %v）", i, order[i], w, order)
		}
	}
	// View 渲染后 Model 内任务顺序不得改变
	m := newTestModel(t)
	m.tasks = tasks
	m.View()
	if m.tasks[0].Output != "done" || m.tasks[1].State != StateFailed {
		t.Fatal("taskOrder 不应改动 Model 内任务顺序")
	}
}

// TestLayoutA5Tasks 布局 A 在 100×25 显示 5 个任务无错位（§10 P1 验收）。
func TestLayoutA5Tasks(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 100, 25
	m.layout, m.layoutAuto = LayoutA, false
	for i := 0; i < 6; i++ {
		m.tasks = append(m.tasks, &Task{URL: "u", Output: fmt.Sprintf("f%d.bin", i),
			State: StateRunning, Size: 1000, Done: int64(100 * i)})
	}
	m.cursor = 0
	v := m.View()
	for _, want := range []string{"f0.bin", "f1.bin", "f2.bin", "f3.bin", "f4.bin"} {
		if !strings.Contains(v, want) {
			t.Errorf("布局 A 应显示 %q\n---\n%s", want, v)
		}
	}
	// 第 6 个超出一屏不显示
	if strings.Contains(v, "f5.bin") {
		t.Error("100×25 一屏最多 5 个任务，f5 不应显示")
	}
	// 每行不越界
	for _, line := range strings.Split(v, "\n") {
		if lipgloss.Width(line) > 100 {
			t.Errorf("布局 A 行越界（宽 %d > 100）: %q", lipgloss.Width(line), line)
		}
	}
	// 进度条宽 62（§5.1）
	rows := strings.Split(v, "\n")
	foundBar := false
	for _, line := range rows {
		if strings.Contains(line, "█") && strings.Contains(line, "░") {
			foundBar = true
			break
		}
	}
	if !foundBar {
		t.Error("布局 A 应渲染亚字符进度条")
	}
}

// TestErrorExpandCollapse 点击失败行展开完整错误详情（单行化），再点收起。
// 点击无错误行不展开；展开行自身点击同样收起（同一 toggle 路径）。
func TestErrorExpandCollapse(t *testing.T) {
	// 错误含换行且超长：行尾短标签被 trunc(40) 截断，展开行显示完整单行化内容
	fullErr := "探测资源失败:\n" + strings.Repeat("z", 60)
	m := newTestModel(t)
	m.tasks = []*Task{
		{URL: "u", Output: "a.bin", State: StateFailed, Err: fmt.Errorf("%s", fullErr)},
		{URL: "u", Output: "b.bin", State: StateDone},
	}
	m.cursor = 0
	m.View()
	if m.expandedErr != -1 {
		t.Fatal("初始 expandedErr 应为 -1")
	}
	// 点击失败行 → 展开
	z := findZoneAt(lastFrame.rows, "select", 0)
	if z == nil {
		t.Fatal("应记录失败行热区")
	}
	km, _ := m.Update(mouseClick(z.start+1, z.y))
	m = km.(Model)
	if m.expandedErr != 0 {
		t.Fatalf("点击失败行应展开, got %d", m.expandedErr)
	}
	v := m.View()
	if !strings.Contains(v, strings.Repeat("z", 60)) {
		t.Fatalf("展开行应显示完整错误（换行压成空格、超长不截断）\n---\n%s", v)
	}
	// 再点同一行 → 收起
	m.View()
	z = findZoneAt(lastFrame.rows, "select", 0)
	if z == nil {
		t.Fatal("收起路径也应能找到行热区")
	}
	km, _ = m.Update(mouseClick(z.start+1, z.y))
	m = km.(Model)
	if m.expandedErr != -1 {
		t.Fatalf("再点应收起, got %d", m.expandedErr)
	}
	if v := m.View(); strings.Contains(v, strings.Repeat("z", 60)) {
		t.Fatalf("收起后不应再显示完整错误（仅行尾截断短标签）\n---\n%s", v)
	}
	// 点击无错误行不展开
	m2 := newTestModel(t)
	m2.tasks = []*Task{
		{URL: "u", Output: "ok.bin", State: StateDone},
		{URL: "u", Output: "fail.bin", State: StateFailed, Err: fmt.Errorf("boom")},
	}
	m2.cursor = 0
	m2.View()
	z = findZoneAt(lastFrame.rows, "select", 0)
	if z == nil {
		t.Fatal("应记录 done 行热区")
	}
	km, _ = m2.Update(mouseClick(z.start+1, z.y))
	m2 = km.(Model)
	if m2.expandedErr != -1 {
		t.Fatalf("点击无错误行不应展开, got %d", m2.expandedErr)
	}
}

// ---- R34：原型三步法定稿（协议差异化 + 30/70 阈值 + 底部操作栏） ----

// TestDetectProto URL → 协议标签。
func TestDetectProto(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1/a.bin":    "http",
		"https://x.com/b.torrent":   "http",
		"MAGNET:?xt=urn:btih:abc":   "magnet",
		" magnet:?xt=urn:btih:def ": "magnet",
	}
	for in, want := range cases {
		if got := detectProto(in); got != want {
			t.Errorf("detectProto(%q)=%q want %q", in, got, want)
		}
	}
}

// TestProtoTagsAndInfo 协议差异化：标签一眼区分（HTTP/BT/磁力）。
func TestProtoTagsAndInfo(t *testing.T) {
	m := newTestModel(t)
	m.tasks = []*Task{
		{URL: "u", Output: "http.bin", State: StateRunning, Proto: "http",
			Size: 2 << 30, Done: 58 * (2 << 30) / 100, Speed: 3.4 * (1 << 20), ETA: 250},
		{URL: "u", Output: "bt.bin", State: StateRunning, Proto: "bt",
			Size: 1 << 30, Done: 12 * (1 << 30) / 100, Peers: 45, Seeds: 8},
		{URL: "u", Output: "mag.bin", State: StateRunning, Proto: "magnet", Meta: "解析中…"},
		{URL: "u", Output: "bad.bin", State: StateFailed, Proto: "http",
			Err: fmt.Errorf("探测资源失败:\nconnection refused")},
	}
	v := m.View()
	for _, want := range []string{
		"HTTP", "BT", // 协议标签（http/bt）
		"▼", "✕", // 状态图标
		"3.4MB/s", "4m 10s 剩余", // HTTP 速度+剩余
	} {
		if !strings.Contains(v, want) {
			t.Errorf("View 缺少 %q\n---\n%s", want, v)
		}
	}
	if strings.Contains(v, "\nconnection refused") || strings.Contains(v, "\n探测资源失败") {
		t.Fatal("错误含换行会断行破坏热区 y 坐标，必须单行化")
	}
}

// TestBarColorThresholds 状态 → 进度条色（§6.2 语义色系统）。
func TestBarColorThresholds(t *testing.T) {
	colorLevel = ColorTrue
	cases := []struct {
		state TaskState
		want  lipgloss.Color
	}{
		{StateRunning, colAccent()},
		{StatePaused, colYellow()},
		{StateDone, colGreen()},
		{StateFailed, colRed()},
	}
	for _, c := range cases {
		if got := barColorOf(&Task{State: c.state}); got != c.want {
			t.Errorf("barColorOf(%v) 色=%v want %v", c.state, got, c.want)
		}
	}
}

// TestFooterKeycapsContext 底部键帽跟随选中任务状态：running=含 pause，done=含 open。
func TestFooterKeycapsContext(t *testing.T) {
	m := newTestModel(t)
	m.tasks = []*Task{
		{URL: "u", Output: "run.bin", State: StateRunning},
		{URL: "u", Output: "ok.bin", State: StateDone},
	}
	// 选中 running → 键帽含暂停 不含打开
	m.cursor = 0
	labels := map[string]bool{}
	for _, c := range m.footerKeycaps() {
		labels[c.label] = true
	}
	if !labels[CapPause] {
		t.Fatal("选中 running 时底栏应含暂停键帽")
	}
	// 选中 done → 键帽含打开
	m.cursor = 1
	labels = map[string]bool{}
	for _, c := range m.footerKeycaps() {
		labels[c.label] = true
	}
	if !labels[CapOpen] {
		t.Fatal("选中 done 时底栏应含打开键帽")
	}
}

// TestFooterFollowsCursorAfterSelect 点击行改变选中 → 底栏键帽随之切换。
func TestFooterFollowsCursorAfterSelect(t *testing.T) {
	m := newTestModel(t)
	m.tasks = []*Task{
		{URL: "u", Output: "run.bin", State: StateRunning},
		{URL: "u", Output: "ok.bin", State: StateDone},
	}
	m.cursor = 0
	m.View()
	// 点击第 2 行（done）→ cursor 移到 done
	rz := findZoneAt(lastFrame.rows, "select", 1)
	if rz == nil {
		t.Fatal("应记录第 2 行热区")
	}
	km, _ := m.Update(mouseClick(rz.start+1, rz.y))
	m = km.(Model)
	if m.cursor != 1 {
		t.Fatalf("点击应选中第 2 行, got %d", m.cursor)
	}
	labels := map[string]bool{}
	for _, c := range m.footerKeycaps() {
		labels[c.label] = true
	}
	if !labels[CapOpen] {
		t.Fatal("选中 done 后底栏应含打开键帽")
	}
}
