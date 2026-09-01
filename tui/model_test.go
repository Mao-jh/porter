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

// TestDeleteTask d 删除：移除行并清理 state 子目录。
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
	km, cmd := m.Update(keyRunes("d"))
	if cmd == nil {
		t.Fatal("d 应返回清理 Cmd")
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

// TestViewAssertions View 字符串断言（渲染即验收）。
func TestViewAssertions(t *testing.T) {
	m := newTestModel(t)
	m.tasks = []*Task{
		{URL: "u", Output: "ok.bin", State: StateDone, Size: 100, Done: 100},
		{URL: "u", Output: "run.bin", State: StateRunning, Size: 200, Done: 50, Speed: 1024, ETA: 90},
	}
	v := m.View()
	for _, want := range []string{"ok.bin", "run.bin", "完成", "下载中", "100B/100B", "1.0KB/s", "█", "ETA 1m 30s"} {
		if !strings.Contains(v, want) {
			t.Errorf("View 缺少 %q\n---\n%s", want, v)
		}
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

// TestViewHelpLine 帮助行常驻且含 x；H-3 失败行尾为短标签。
func TestViewHelpLine(t *testing.T) {
	m := newTestModel(t)
	v := m.View()
	if !strings.Contains(v, "s:设置") || !strings.Contains(v, "x:代理") {
		t.Fatalf("帮助行应含 s:设置 与 x:代理\n---\n%s", v)
	}
	// 代理输入行
	km, _ := m.Update(keyRunes("x"))
	m = km.(Model)
	v = m.View()
	if !strings.Contains(v, "代理出口>") {
		t.Fatalf("代理输入模式应渲染输入行\n---\n%s", v)
	}
	// H-3 失败行尾短标签
	m2 := newTestModel(t)
	m2.tasks = []*Task{{URL: "u", Output: "f.bin", State: StateFailed,
		Err: fmt.Errorf("探测资源失败: host example.com resolves to non-loopback (H-3)")}}
	v = m2.View()
	if !strings.Contains(v, "安全边界拒绝(H-3)") {
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
func TestPasteCtrlV(t *testing.T) {
	orig := pasteText
	defer func() { pasteText = orig }()
	pasteText = func() (string, bool) { return "https://example.com/a.bin", true }

	m := newTestModel(t)
	km, _ := m.Update(keyRunes("a"))
	m = km.(Model)
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
	for _, bad := range []string{"17", "-1", "abc"} {
		if _, err := parseShards(bad); err == nil {
			t.Errorf("parseShards(%q) 应报错", bad)
		}
	}
}

// ---- 鼠标交互（R32 起） ----

// findZone 返回第一个匹配 action 的热区（测试辅助）。
func findZone(zones []clickZone, action string) *clickZone {
	for i := range zones {
		if zones[i].action == action {
			return &zones[i]
		}
	}
	return nil
}

func mouseClick(x, y int) tea.MouseMsg {
	return tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: x, Y: y}
}

// TestViewRowButtons 行尾按钮渲染：running → [暂停][删除]，done → 仅 [删除]。
func TestViewRowButtons(t *testing.T) {
	m := newTestModel(t)
	m.tasks = []*Task{
		{URL: "u", Output: "run.bin", State: StateRunning},
		{URL: "u", Output: "ok.bin", State: StateDone},
	}
	v := m.View()
	if !strings.Contains(v, "[暂停]") || !strings.Contains(v, "[删除]") {
		t.Fatalf("running 行应含 [暂停] [删除]\n---\n%s", v)
	}
	if strings.Contains(v, "[继续]") {
		t.Fatal("running 行不应含 [继续]")
	}
}

// TestButtonPosFixed 按钮区固定在最左侧：不同任务（参数/进度差异巨大）的按钮 x 坐标一致，
// 不随文件名/进度条/字节数等参数左右跳动（R32 用户反馈的跳动问题回归测试）。
func TestButtonPosFixed(t *testing.T) {
	m := newTestModel(t)
	m.tasks = []*Task{
		{URL: "u", Output: "a.bin", State: StateRunning, Size: 100, Done: 1, Speed: 1, ETA: 90},
		{URL: "u", Output: "very-long-filename-xxxxxxxxxxxx.bin", State: StateRunning,
			Size: 10 << 30, Done: 5 << 30, Speed: 9 << 20, ETA: 3600},
		{URL: "u", Output: "c.bin", State: StateDone, Size: 100, Done: 100},
	}
	m.View()
	// 两个 running 行的 [暂停] 按钮 x 应恒为 4（左边框1 + padding1 + cursor2）
	for _, want := range []struct {
		rowIdx int
		x      int
	}{{0, 4}, {1, 4}} {
		z := findZoneAt(lastFrame.buttons, "pause", want.rowIdx)
		if z == nil {
			t.Fatalf("rowIdx=%d 应有 [暂停] 按钮", want.rowIdx)
		}
		if z.start != want.x {
			t.Errorf("rowIdx=%d [暂停] x=%d want %d（按钮应固定在行首，不随参数跳动）", want.rowIdx, z.start, want.x)
		}
	}
	// running 行（双按钮）的 [删除] 恒为 11（4 + 6 + 空格 1）；done 行（单按钮）恒为 4
	for _, want := range []struct {
		rowIdx int
		x      int
	}{{0, 11}, {1, 11}, {2, 4}} {
		z := findZoneAt(lastFrame.buttons, "delete", want.rowIdx)
		if z == nil {
			t.Fatalf("rowIdx=%d 应有 [删除] 按钮", want.rowIdx)
		}
		if z.start != want.x {
			t.Errorf("rowIdx=%d [删除] x=%d want %d", want.rowIdx, z.start, want.x)
		}
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

// TestMousePauseResumeButton 点击 [暂停] → 引擎取消落盘 → paused；再点 [继续] → running。
func TestMousePauseResumeButton(t *testing.T) {
	m := newTestModel(t)
	km, _ := m.Update(keyRunes("a"))
	m = km.(Model)
	m.input.SetValue("http://127.0.0.1/dir/pause.bin")
	km, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = km.(Model)
	if len(m.tasks) != 1 || m.tasks[0].cancel == nil {
		t.Fatal("任务应已启动")
	}
	// 渲染并点 [暂停]
	m.View()
	z := findZone(lastFrame.buttons, "pause")
	if z == nil {
		t.Fatal("running 任务应渲染 [暂停] 按钮")
	}
	km, _ = m.Update(mouseClick(z.start+1, z.y))
	m = km.(Model)
	// 等待引擎退出（只读确认，值放回 channel 交给 drainDone 消费，保证走真实状态迁移路径）
	select {
	case err := <-m.tasks[0].doneCh:
		m.tasks[0].doneCh <- err
	case <-time.After(10 * time.Second):
		t.Fatal("引擎未在 10s 内退出")
	}
	m.drainDone()
	if m.tasks[0].State != StatePaused {
		t.Fatalf("点 [暂停] 后应 paused, got %v", m.tasks[0].State)
	}
	// 点 [继续] 重启
	m.View()
	z = findZone(lastFrame.buttons, "resume")
	if z == nil {
		t.Fatal("paused 任务应渲染 [继续] 按钮")
	}
	km, _ = m.Update(mouseClick(z.start+1, z.y))
	m = km.(Model)
	if m.tasks[0].State != StateRunning {
		t.Fatalf("点 [继续] 后应 running, got %v", m.tasks[0].State)
	}
	// 清理：取消并等待引擎退出
	m.tasks[0].cancel()
	select {
	case <-m.tasks[0].doneCh:
	case <-time.After(10 * time.Second):
		t.Fatal("引擎未在 10s 内退出")
	}
}

// TestMouseDeleteButton 点击选中行 [删除] → 移除任务（含 cursor 回退）。
func TestMouseDeleteButton(t *testing.T) {
	m := newTestModel(t)
	m.tasks = []*Task{
		{URL: "u", Output: "a.bin", State: StateDone},
		{URL: "u", Output: "b.bin", State: StateDone},
	}
	m.cursor = 0
	m.View()
	// 取选中行（rowIdx==0）的删除按钮
	var z *clickZone
	for i := range lastFrame.buttons {
		if lastFrame.buttons[i].action == "delete" && lastFrame.buttons[i].rowIdx == 0 {
			z = &lastFrame.buttons[i]
			break
		}
	}
	if z == nil {
		t.Fatal("应渲染 [删除] 按钮")
	}
	km, cmd := m.Update(mouseClick(z.start+1, z.y))
	m = km.(Model)
	if cmd == nil {
		t.Fatal("点 [删除] 应返回 taskRemovedMsg Cmd")
	}
	msg := cmd()
	tm, ok := msg.(taskRemovedMsg)
	if !ok {
		t.Fatalf("Cmd 应产生 taskRemovedMsg, got %T", msg)
	}
	km, _ = m.Update(tm)
	m = km.(Model)
	if len(m.tasks) != 1 || m.tasks[0].Output != "b.bin" {
		t.Fatalf("删除 a.bin 后应剩 b.bin, got %d 个: %+v", len(m.tasks), m.tasks)
	}
	if m.cursor != 0 {
		t.Fatalf("删除后 cursor 应保持 0, got %d", m.cursor)
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

// TestTaskOrder 显示排序：失败 → 暂停 → 进行中/排队 → 完成；
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
	want := []int{1, 5, 2, 3, 4, 0} // fail1 fail2 paused run queued done（同状态保原序）
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
		"http://127.0.0.1/a.bin": "http",
		"https://x.com/b.torrent": "http",
		"MAGNET:?xt=urn:btih:abc": "magnet",
		" magnet:?xt=urn:btih:def ": "magnet",
	}
	for in, want := range cases {
		if got := detectProto(in); got != want {
			t.Errorf("detectProto(%q)=%q want %q", in, got, want)
		}
	}
}

// TestProtoTagsAndInfo 协议差异化：标签一眼区分，信息列按协议渲染
// （HTTP=速度+ETA / BT=连接+做种 / 磁力=解析状态）。
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
		"HTTP", "BT", "磁力", // 协议标签
		"●下载中", "●失败", // 状态色点+字
		"45 连接 8 做种", // BT 信息列
		"解析中…",       // 磁力信息列
		"3.4MB/s", "ETA 4m 10s", // HTTP 速度+剩余（humanBytes 用 KB/MB 进制）
		"探测资源失败: connect", // 错误单行化截断（\n 压成空格，不断行）
	} {
		if !strings.Contains(v, want) {
			t.Errorf("View 缺少 %q\n---\n%s", want, v)
		}
	}
	if strings.Contains(v, "\nconnection refused") || strings.Contains(v, "\n探测资源失败") {
		t.Fatal("错误含换行会断行破坏热区 y 坐标，必须单行化")
	}
}

// TestBarColorThresholds 进度条三色阈值（R34 定稿）：<30% 红 / <70% 黄 / ≥70% 绿。
func TestBarColorThresholds(t *testing.T) {
	cases := []struct {
		frac float64
		want lipgloss.Color // ANSI 256 色索引
	}{
		{0.00, "9"}, {0.29, "9"}, // 红
		{0.30, "11"}, {0.69, "11"}, // 黄
		{0.70, "10"}, {1.00, "10"}, // 绿
	}
	for _, c := range cases {
		got := barColor(c.frac).GetForeground()
		if got != c.want {
			t.Errorf("barColor(%.2f) 色=%v want %v", c.frac, got, c.want)
		}
	}
}

// footerY 底栏按钮所在终端行（热区中 y 最大的一组 = 底部操作栏）。
func footerY(buttons []clickZone) int {
	maxY := -1
	for _, z := range buttons {
		if z.y > maxY {
			maxY = z.y
		}
	}
	return maxY
}

// TestFooterButtons 底部操作栏（R34）：按钮跟随选中任务状态，x 恒 2/10 固定列位。
func TestFooterButtons(t *testing.T) {
	m := newTestModel(t)
	m.tasks = []*Task{
		{URL: "u", Output: "run.bin", State: StateRunning},
		{URL: "u", Output: "bad.bin", State: StateFailed, Err: fmt.Errorf("boom")},
		{URL: "u", Output: "ok.bin", State: StateDone},
	}
	m.cursor = 0 // 选中 running → 底栏 [暂停] [移除]
	m.View()
	fy := footerY(lastFrame.buttons)
	if fy < 0 {
		t.Fatal("应有底栏按钮热区")
	}
	pause := findZoneAtY(lastFrame.buttons, "pause", 0, fy)
	if pause == nil {
		t.Fatal("选中 running 时底栏应有 [暂停]")
	}
	if pause.start != 2 {
		t.Errorf("底栏 [暂停] x=%d want 2（固定列位，不随内容跳动）", pause.start)
	}
	del := findZoneAtY(lastFrame.buttons, "delete", 0, fy)
	if del == nil || del.start != 9 {
		t.Errorf("底栏 [移除] 缺失或 x 错误: %+v", del)
	}
	// 切到 failed → 底栏 [继续] [移除]
	m.cursor = 1
	m.View()
	fy = footerY(lastFrame.buttons)
	if findZoneAtY(lastFrame.buttons, "resume", 1, fy) == nil {
		t.Fatal("选中 failed 时底栏应有 [继续]（重试语义）")
	}
	// 切到 done → 底栏仅 [移除]，无 [暂停]/[继续]（行首按钮仍按各自状态渲染，此处只看底栏热区）
	m.cursor = 2
	m.View()
	fy = footerY(lastFrame.buttons)
	if findZoneAtY(lastFrame.buttons, "pause", 2, fy) != nil {
		t.Fatal("选中 done 时底栏不应有 [暂停] 热区")
	}
	if findZoneAtY(lastFrame.buttons, "resume", 2, fy) != nil {
		t.Fatal("选中 done 时底栏不应有 [继续] 热区")
	}
	if findZoneAtY(lastFrame.buttons, "delete", 2, fy) == nil {
		t.Fatal("选中 done 时底栏应有 [移除] 热区")
	}
}

// findZoneAtY 指定 y、action、rowIdx 的热区（底栏按钮专用；行首按钮 y 不同）。
func findZoneAtY(zones []clickZone, action string, rowIdx, y int) *clickZone {
	for i := range zones {
		if zones[i].action == action && zones[i].rowIdx == rowIdx && zones[i].y == y {
			return &zones[i]
		}
	}
	return nil
}

// TestMouseFooterDelete 点击底栏 [移除] → 状态转移（taskRemovedMsg 移除任务）。
func TestMouseFooterDelete(t *testing.T) {
	m := newTestModel(t)
	m.tasks = []*Task{
		{URL: "u", Output: "a.bin", State: StateDone},
		{URL: "u", Output: "b.bin", State: StateFailed, Err: fmt.Errorf("boom")},
	}
	m.cursor = 1 // 选中 failed
	m.View()
	fy := footerY(lastFrame.buttons)
	z := findZoneAtY(lastFrame.buttons, "delete", 1, fy)
	if z == nil {
		t.Fatal("底栏应有 [移除] 热区")
	}
	km, cmd := m.Update(mouseClick(z.start+1, z.y))
	m = km.(Model)
	if cmd == nil {
		t.Fatal("底栏 [移除] 应返回删除 Cmd")
	}
	km, _ = m.Update(cmd())
	m = km.(Model)
	if len(m.tasks) != 1 || m.tasks[0].Output != "a.bin" {
		t.Fatalf("点击底栏 [移除] 后应删除选中任务, got %d 个", len(m.tasks))
	}
}

// TestFooterFollowsCursorAfterSelect 点击行改变选中 → 底栏按钮随之切换。
func TestFooterFollowsCursorAfterSelect(t *testing.T) {
	m := newTestModel(t)
	m.tasks = []*Task{
		{URL: "u", Output: "run.bin", State: StateRunning},
		{URL: "u", Output: "ok.bin", State: StateDone},
	}
	m.cursor = 0
	m.View()
	// 点击第 2 行（done）→ cursor 移到 done → 底栏只剩 [移除]
	rz := findZoneAt(lastFrame.rows, "select", 1)
	if rz == nil {
		t.Fatal("应记录第 2 行热区")
	}
	km, _ := m.Update(mouseClick(rz.start+1, rz.y))
	m = km.(Model)
	if m.cursor != 1 {
		t.Fatalf("点击应选中第 2 行, got %d", m.cursor)
	}
	m.View()
	fy := footerY(lastFrame.buttons)
	if findZoneAtY(lastFrame.buttons, "pause", 1, fy) != nil {
		t.Fatalf("选中 done 后底栏不应有 [暂停] 热区")
	}
	if findZoneAtY(lastFrame.buttons, "delete", 1, fy) == nil {
		t.Fatal("选中 done 后底栏应有 [移除] 热区")
	}
}

