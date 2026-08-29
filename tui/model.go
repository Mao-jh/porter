// Package tui 实现下载器的终端界面（Bubble Tea MVU 架构）。
//
// AI 第一用户设计要点：
//   - Model/Update/View 全部为纯函数式状态机，无锁、race-free by construction
//     （Bubble Tea 的 Cmd 在独立 goroutine 执行、Msg 单线程投递 Update）；
//   - 渲染结果是字符串，可 go test 断言，无需人眼验收；
//   - 引擎零改动：每任务一次 cli.RunMulti + 每任务独立 state 子目录
//     （避免多个 RunMulti 并发写同一 state.json 互相覆盖——core 现状约束）。
package tui

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/nymjin22/downloader/cli"
	"github.com/nymjin22/downloader/persist"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// TaskState 任务生命周期状态。
type TaskState int

const (
	StateQueued TaskState = iota
	StateRunning
	StatePaused
	StateDone
	StateFailed
)

func (s TaskState) String() string {
	switch s {
	case StateQueued:
		return "排队"
	case StateRunning:
		return "下载中"
	case StatePaused:
		return "已暂停"
	case StateDone:
		return "完成"
	case StateFailed:
		return "失败"
	}
	return "?"
}

// Task 单个下载任务的 UI 态。
type Task struct {
	URL    string
	Output string // 文件路径（同时是 persist 状态 ID）
	State  TaskState
	Err    error
	Size   int64
	Done   int64
	Speed  float64 // B/s（进度差分）

	lastDone int64
	lastAt   time.Time
	cancel   context.CancelFunc // 非 nil 表示引擎在跑
	doneCh   chan error         // 引擎完成事件（tick 轮询抽取，缓冲 1）
}

// start 启动引擎 goroutine：RunMulti 结束（成功/失败/取消）写入 doneCh。
// 任务即刻转为 Running（drainDone 只抽取 Running 态的完成事件，队列态会漏掉）。
func (t *Task) start(baseOpt cli.Options) {
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	t.State = StateRunning
	t.doneCh = make(chan error, 1)
	opt := baseOpt // 值拷贝
	opt.URLs = []string{t.URL}
	opt.Output = t.Output
	opt.StateDir = t.taskStateDir(baseOpt.StateDir)
	go func() { t.doneCh <- cli.RunMulti(ctx, &opt) }()
}

// taskStateDir 任务专属 state 子目录（每任务独立，避免 state.json 并发写冲突）。
func (t *Task) taskStateDir(root string) string {
	return filepath.Join(root, taskDirName(t.URL))
}

// taskDirName 用 URL 哈希命名：重启后同 URL 命中同一子目录（续传恢复键）。
func taskDirName(urlStr string) string {
	sum := sha1.Sum([]byte(urlStr))
	return "t" + hex.EncodeToString(sum[:])[:12]
}

// Model TUI 状态机。
type Model struct {
	tasks   []*Task
	cursor  int
	input   textinput.Model
	adding  bool
	baseOpt cli.Options // 共享旗标（StateDir 根 / Verify / Limit / Mode / Shards）
	outDir  string      // 新任务输出目录（空=当前目录）
	width   int
	height  int
	status  string
	errMsg  string
	// Selftest 由 --selftest 置位：全部任务到达终态后自动退出（无头验收）。
	Selftest bool
	// QuitReason 退出原因（selftest 用于判定退出码：ok/failed/user）。
	QuitReason string
	quitting   bool
}

// New 构造 Model。baseOpt 的 StateDir 为状态根目录。
func New(baseOpt cli.Options) Model {
	ti := textinput.New()
	ti.Placeholder = "http://127.0.0.1/file/x.bin"
	return Model{
		input:   ti,
		baseOpt: baseOpt,
		status:  "a:添加任务 ↑/↓:选择 p:暂停/继续 d:删除 q:退出",
	}
}

// SetOutputDir 设置新任务的输出目录（缺省当前目录）。
func (m *Model) SetOutputDir(dir string) { m.outDir = dir }

// Tasks 任务快照（selftest 判定与外部检查用）。
func (m Model) Tasks() []*Task { return m.tasks }

// AddTask 预置一个任务并立即启动（-url / selftest 用，等价于交互式添加+回车）。
func (m *Model) AddTask(raw string) error {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return fmt.Errorf("仅支持 http/https URL: %s", raw)
	}
	t := &Task{URL: raw, Output: deriveOutputName(raw, m.tasks), State: StateQueued}
	if m.outDir != "" {
		t.Output = filepath.Join(m.outDir, t.Output)
	}
	m.tasks = append(m.tasks, t)
	m.cursor = len(m.tasks) - 1

	opt := m.baseOpt
	opt.URLs = []string{t.URL}
	opt.Output = t.Output
	opt.StateDir = t.taskStateDir(m.baseOpt.StateDir)
	t.cancel, t.doneCh = nil, nil
	t.start(opt)
	return nil
}

// drainDone 非阻塞抽取各任务引擎的完成事件并做状态迁移（成功/取消→暂停/失败）。
func (m *Model) drainDone() {
	for _, t := range m.tasks {
		if t.doneCh == nil || t.State != StateRunning {
			continue
		}
		select {
		case err := <-t.doneCh:
			t.doneCh = nil
			t.cancel = nil
			switch {
			case err == nil:
				t.State = StateDone
				t.Err = nil
			case isCanceled(err):
				t.State = StatePaused // 引擎取消 → 进度已落盘，可续传
				t.Err = nil
			default:
				t.State = StateFailed
				t.Err = err
			}
		default:
		}
	}
}

// ---- 消息 ----

type tickMsg time.Time

type taskRemovedMsg struct{ output string }

// Init 启动进度轮询。
func (m Model) Init() tea.Cmd { return tickCmd() }

func tickCmd() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Update 状态转移（纯函数；所有引擎交互经 Cmd/Msg）。
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		m.refreshProgress()
		m.drainDone()
		cmds := []tea.Cmd{tickCmd()}
		if m.Selftest && !m.quitting && m.allSettled() {
			m.quitting = true
			m.QuitReason = m.verdict()
			cmds = append(cmds, tea.Quit)
		}
		return m, tea.Batch(cmds...)

	case taskRemovedMsg:
		for i, t := range m.tasks {
			if t.Output == msg.output {
				m.tasks = append(m.tasks[:i], m.tasks[i+1:]...)
				if m.cursor >= len(m.tasks) && m.cursor > 0 {
					m.cursor--
				}
				break
			}
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func isCanceled(err error) bool {
	if err == nil {
		return false
	}
	return err == context.Canceled || strings.Contains(err.Error(), "context canceled")
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.adding {
		switch msg.Type {
		case tea.KeyEnter:
			raw := strings.TrimSpace(m.input.Value())
			m.adding = false
			m.input.Blur()
			m.input.SetValue("")
			if raw == "" {
				return m, nil
			}
			if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
				m.errMsg = "仅支持 http/https URL"
				return m, nil
			}
			t := &Task{URL: raw, Output: deriveOutputName(raw, m.tasks), State: StateQueued}
			if m.outDir != "" {
				t.Output = filepath.Join(m.outDir, t.Output)
			}
			m.tasks = append(m.tasks, t)
			m.cursor = len(m.tasks) - 1
			m.errMsg = ""
			t.start(m.baseOpt)
			return m, nil
		case tea.KeyEsc:
			m.adding = false
			m.input.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	switch msg.Type {
	case tea.KeyCtrlC:
		m.quitting = true
		m.QuitReason = "user"
		m.cancelAll()
		return m, tea.Quit
	case tea.KeyUp:
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case tea.KeyDown:
		if m.cursor < len(m.tasks)-1 {
			m.cursor++
		}
		return m, nil
	}

	switch msg.String() {
	case "q":
		m.quitting = true
		m.QuitReason = "user"
		m.cancelAll()
		return m, tea.Quit
	case "a":
		m.adding = true
		m.input.Focus()
		m.input.SetValue("")
		return m, textinput.Blink
	case "p":
		if len(m.tasks) == 0 || m.cursor >= len(m.tasks) {
			return m, nil
		}
		t := m.tasks[m.cursor]
		switch t.State {
		case StateRunning: // 暂停：取消引擎（taskDoneMsg 会转为 paused）
			if t.cancel != nil {
				t.cancel()
			}
			m.status = "暂停中（等待引擎落盘）…"
		case StatePaused, StateFailed, StateQueued: // 继续/重试/启动
			t.State = StateRunning
			t.Err = nil
			t.start(m.baseOpt)
		}
	case "d":
		if len(m.tasks) == 0 || m.cursor >= len(m.tasks) {
			return m, nil
		}
		t := m.tasks[m.cursor]
		if t.cancel != nil {
			t.cancel()
		}
		dir := t.taskStateDir(m.baseOpt.StateDir)
		return m, func() tea.Msg {
			_ = os.RemoveAll(dir)
			return taskRemovedMsg{output: t.Output}
		}
	}
	return m, nil
}

func (m *Model) cancelAll() {
	for _, t := range m.tasks {
		if t.cancel != nil {
			t.cancel()
		}
	}
}

// allSettled selftest 终止条件：没有排队/下载中的任务。
func (m Model) allSettled() bool {
	for _, t := range m.tasks {
		if t.State == StateRunning || t.State == StateQueued {
			return false
		}
	}
	return true
}

func (m Model) verdict() string {
	for _, t := range m.tasks {
		if t.State != StateDone {
			return "failed"
		}
	}
	if len(m.tasks) > 0 {
		return "ok"
	}
	return "failed"
}

// refreshProgress 从各任务子目录的 state.json 直接读取最新进度。
// 注意：persist.Store 打开后缓存不自动刷新，轮询必须重读文件（core 现状约束）。
func (m *Model) refreshProgress() {
	now := time.Now()
	for _, t := range m.tasks {
		if t.State != StateRunning {
			continue
		}
		st, ok := readState(t.taskStateDir(m.baseOpt.StateDir), t.Output)
		if !ok {
			continue
		}
		t.Size = st.FileSize
		t.Done = st.Done
		if !t.lastAt.IsZero() {
			dt := now.Sub(t.lastAt).Seconds()
			if dt > 0 && st.Done >= t.lastDone {
				t.Speed = float64(st.Done-t.lastDone) / dt
			}
		}
		t.lastDone, t.lastAt = st.Done, now
	}
}

// readState 读取单个任务 state.json 中的指定条目。
func readState(stateDir, id string) (persist.State, bool) {
	b, err := os.ReadFile(filepath.Join(stateDir, "state.json"))
	if err != nil {
		return persist.State{}, false
	}
	var states map[string]persist.State
	if err := json.Unmarshal(b, &states); err != nil {
		return persist.State{}, false
	}
	st, ok := states[id]
	return st, ok
}

// RestoreTasks 启动时从状态根目录恢复历史任务（扫描各子目录 state.json）。
func (m *Model) RestoreTasks() {
	entries, err := os.ReadDir(m.baseOpt.StateDir)
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(m.baseOpt.StateDir, e.Name(), "state.json"))
		if err != nil {
			continue
		}
		var states map[string]persist.State
		if err := json.Unmarshal(b, &states); err != nil {
			continue
		}
		for _, st := range states {
			if seen[st.ID] || st.URL == "" {
				continue
			}
			seen[st.ID] = true
			state := StatePaused
			if st.Status == "done" {
				state = StateDone
			}
			m.tasks = append(m.tasks, &Task{
				URL: st.URL, Output: st.ID, State: state,
				Size: st.FileSize, Done: st.Done,
			})
		}
	}
}

// deriveOutputName 从 URL 推导输出文件名并做 Windows 非法字符清洗 + 去重。
func deriveOutputName(raw string, existing []*Task) string {
	base := ""
	if u, err := url.Parse(raw); err == nil {
		base = path.Base(u.Path)
	}
	if base == "" || base == "." || base == "/" {
		base = fmt.Sprintf("download-%d.bin", time.Now().UnixNano()%100000)
	}
	base = sanitizeName(base)
	taken := map[string]bool{}
	for _, t := range existing {
		taken[t.Output] = true
	}
	name := base
	for i := 2; taken[name]; i++ {
		ext := path.Ext(base)
		name = fmt.Sprintf("%s-%d%s", strings.TrimSuffix(base, ext), i, ext)
	}
	return name
}

func sanitizeName(s string) string {
	return strings.NewReplacer(
		`<`, `_`, `>`, `_`, `:`, `_`, `"`, `_`, `/`, `_`,
		`\`, `_`, `|`, `_`, `?`, `_`, `*`, `_`,
	).Replace(s)
}
