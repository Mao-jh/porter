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
	if !strings.Contains(v, "x:代理出口") {
		t.Fatalf("帮助行应含 x:代理出口\n---\n%s", v)
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
