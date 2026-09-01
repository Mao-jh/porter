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
	"strconv"
	"strings"
	"time"

	"github.com/Mao-jh/porter/cli"
	"github.com/Mao-jh/porter/hash"
	"github.com/Mao-jh/porter/persist"

	"github.com/atotto/clipboard"
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
	ETA    int64   // 剩余秒数（0=未知；R21 起由速率推算）

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
	tasks    []*Task
	cursor   int
	input    textinput.Model
	adding   bool
	proxying bool // 代理输入模式（x 进入；Enter 生效，Esc 取消）
	baseOpt  cli.Options // 共享旗标（StateDir 根 / Verify / Limit / Mode / Shards / Proxy）
	outDir   string      // 新任务输出目录（空=当前目录）
	width    int
	height   int
	status   string
	errMsg   string
	// 设置面板（s 打开）：常用预设优先，自定义补全。
	settings      bool // 面板打开
	settingRow    int  // 0=限速 1=分片 2=校验 3=代理
	settingCustom bool // 自定义值输入中（复用 input）
	// Selftest 由 --selftest 置位：全部任务到达终态后自动退出（无头验收）。
	Selftest bool
	// QuitReason 退出原因（selftest 用于判定退出码：ok/failed/user）。
	QuitReason string
	quitting   bool
	// expandedErr 展开错误详情的任务索引（-1=无；点击失败行 toggle，R33）。
	expandedErr int
}

// ---- 设置面板档位（常用场景优先，末尾"自定义…"进输入） ----

// speedPresets 限速档位值（字节/秒；0=不限）。
var speedPresets = []int64{0, 1 << 20, 5 << 20, 10 << 20}

// speedLabels 与 speedPresets 平行，末尾恒为自定义入口。
var speedLabels = []string{"不限", "1MiB/s", "5MiB/s", "10MiB/s", "自定义…"}

// shardPresets 分片档位（0=自动）。
var shardPresets = []int{0, 1, 4, 8, 16}

var shardLabels = []string{"自动", "1", "4", "8", "16", "自定义…"}

// verifyPresets 校验算法档位（""=不校验）。
var verifyPresets = []string{"sha256", "sha1", "md5", ""}

var verifyLabels = []string{"sha256", "sha1", "md5", "不校验", "自定义…"}

// proxyPresets 代理档位（""=直连）。
var proxyPresets = []string{"", "http://127.0.0.1:7890", "socks5://127.0.0.1:1080"}

var proxyLabels = []string{"直连", "http://127.0.0.1:7890", "socks5://127.0.0.1:1080", "自定义…"}

// settingRowNames 设置面板行名。
var settingRowNames = []string{"限速", "分片", "校验", "代理"}

// urlPlaceholder 任务 URL 输入框占位。
const urlPlaceholder = "http://127.0.0.1/file/x.bin"

// New 构造 Model。baseOpt 的 StateDir 为状态根目录。
func New(baseOpt cli.Options) Model {
	ti := textinput.New()
	ti.Placeholder = urlPlaceholder
	return Model{
		input:       ti,
		baseOpt:     baseOpt,
		status:      "",
		expandedErr: -1,
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
				// H-3 安全边界拒绝（公网 URL 默认不放行）：给出可行动指引，
				// 否则用户只见「失败」与截断的行尾错误，无从下手。
				if m.baseOpt.Proxy == "" && isLoopbackRefusal(err) {
					m.errMsg = "目标被安全边界 (H-3) 拒绝（默认仅允许回环地址）。公网链接请按 x 配置代理出口，或启动时加 -proxy，然后按 p 重试。"
				}
			}
		default:
		}
	}
}

// isLoopbackRefusal 判断错误是否因 H-3 回环边界拒绝而起。
func isLoopbackRefusal(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "H-3") || strings.Contains(s, "not loopback") ||
		strings.Contains(s, "non-loopback") || strings.Contains(s, "非回环")
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

	case tea.MouseMsg:
		return m.handleMouse(msg)
	}
	return m, nil
}

// handleMouse 鼠标交互（R32 起）：
//   - 左键点击：命中行尾操作按钮（[暂停/继续] [删除]）→ 执行；否则命中任务行 → 选中；
//   - 滚轮：上下移动选中行（等价 ↑/↓）；
//   - 输入/设置面板内忽略鼠标（保持键盘为主，避免误触）。
//
// 热区 lastFrame 由 View 每帧重建（Bubble Tea 单线程事件循环串行访问，无锁安全）。
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.adding || m.proxying || m.settingCustom || m.settings {
		return m, nil
	}
	if tea.MouseEvent(msg).IsWheel() {
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			if m.cursor > 0 {
				m.cursor--
			}
		case tea.MouseButtonWheelDown:
			if m.cursor < len(m.tasks)-1 {
				m.cursor++
			}
		}
		return m, nil
	}
	// 只响应左键释放（完整的"点击"语义，避开按下即拖动）
	if msg.Action != tea.MouseActionRelease || msg.Button != tea.MouseButtonLeft {
		return m, nil
	}
	if z := hitZone(lastFrame.buttons, msg.X, msg.Y); z != nil {
		return m.activateZone(*z)
	}
	if z := hitZone(lastFrame.rows, msg.X, msg.Y); z != nil {
		return m.activateZone(*z)
	}
	return m, nil
}

// activateZone 按热区动作分发（与键盘 p/d 共用同一套任务操作，行为一致）。
func (m Model) activateZone(z clickZone) (tea.Model, tea.Cmd) {
	switch z.action {
	case "select":
		if z.rowIdx >= 0 && z.rowIdx < len(m.tasks) {
			m.cursor = z.rowIdx
			// 点击失败/出错行：展开/收起完整错误详情（R33）
			if m.tasks[z.rowIdx].Err != nil {
				if m.expandedErr == z.rowIdx {
					m.expandedErr = -1
				} else {
					m.expandedErr = z.rowIdx
				}
			}
		}
		return m, nil
	case "pause":
		return m.pauseTask(z.rowIdx)
	case "resume":
		return m.resumeTask(z.rowIdx)
	case "delete":
		return m.deleteTask(z.rowIdx)
	}
	return m, nil
}

// pauseTask 暂停任务（与键盘 p 的"暂停"分支同语义：取消引擎，落盘后转 paused）。
func (m Model) pauseTask(i int) (tea.Model, tea.Cmd) {
	if i < 0 || i >= len(m.tasks) {
		return m, nil
	}
	t := m.tasks[i]
	if t.State == StateRunning && t.cancel != nil {
		t.cancel()
		m.status = "暂停中（等待引擎落盘）…"
	}
	return m, nil
}

// resumeTask 继续/重试任务（paused/failed/queued → running，重启引擎）。
func (m Model) resumeTask(i int) (tea.Model, tea.Cmd) {
	if i < 0 || i >= len(m.tasks) {
		return m, nil
	}
	t := m.tasks[i]
	switch t.State {
	case StatePaused, StateFailed, StateQueued:
		t.State = StateRunning
		t.Err = nil
		t.start(m.baseOpt)
	}
	return m, nil
}

// deleteTask 删除任务：取消引擎、清理 state 子目录、移除列表项（与键盘 d 同语义）。
func (m Model) deleteTask(i int) (tea.Model, tea.Cmd) {
	if i < 0 || i >= len(m.tasks) {
		return m, nil
	}
	t := m.tasks[i]
	if t.cancel != nil {
		t.cancel()
	}
	dir := t.taskStateDir(m.baseOpt.StateDir)
	return m, func() tea.Msg {
		_ = os.RemoveAll(dir)
		return taskRemovedMsg{output: t.Output}
	}
}

func isCanceled(err error) bool {
	if err == nil {
		return false
	}
	return err == context.Canceled || strings.Contains(err.Error(), "context canceled")
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// 输入模式：URL 添加 / 代理配置 / 设置自定义值
	if m.adding || m.proxying || m.settingCustom {
		switch msg.Type {
		case tea.KeyEnter:
			raw := strings.TrimSpace(m.input.Value())
			switch {
			case m.proxying:
				return m.finishProxy(raw)
			case m.settingCustom:
				return m.finishCustom(raw)
			}
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
			m.adding, m.proxying, m.settingCustom = false, false, false
			m.input.Blur()
			m.input.SetValue("")
			m.input.Placeholder = urlPlaceholder
			return m, nil
		case tea.KeyCtrlV:
			if txt, ok := pasteText(); ok && txt != "" {
				m.input.SetValue(m.input.Value() + txt)
				m.input.CursorEnd()
			} else {
				m.errMsg = "剪贴板不可读（也可用终端粘贴：Ctrl+Shift+V 或右键）"
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	// 设置面板（无输入焦点时的导航）
	if m.settings {
		return m.handleSettingKey(msg)
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

	// 大小写兼容：Shift+a / Caps Lock 下 "A" 也匹配（Toast 键语义，大小写不敏感）。
	switch strings.ToLower(msg.String()) {
	case "q":
		m.quitting = true
		m.QuitReason = "user"
		m.cancelAll()
		return m, tea.Quit
	case "a":
		m.adding = true
		m.input.Focus()
		m.input.SetValue("")
		m.input.Placeholder = urlPlaceholder
		return m, textinput.Blink
	case "x":
		m.proxying = true
		m.input.Focus()
		m.input.SetValue("")
		m.input.Placeholder = "http://host:port 或 socks5://host:port（设置即视为允许公网出站）"
		return m, textinput.Blink
	case "s":
		m.settings = true
		m.settingRow = 0
		m.errMsg = ""
		return m, nil
	case "p":
		if len(m.tasks) == 0 || m.cursor >= len(m.tasks) {
			return m, nil
		}
		switch m.tasks[m.cursor].State {
		case StateRunning: // 暂停：取消引擎（drainDone 会转为 paused）
			return m.pauseTask(m.cursor)
		case StatePaused, StateFailed, StateQueued: // 继续/重试/启动
			return m.resumeTask(m.cursor)
		}
	case "d":
		if len(m.tasks) == 0 || m.cursor >= len(m.tasks) {
			return m, nil
		}
		return m.deleteTask(m.cursor)
	}
	return m, nil
}

// handleSettingKey 设置面板按键：↑/↓ 选择行，Enter/空格/→ 切换档位，
// 档位落到"自定义…"进入输入；q/Esc 关闭。
func (m Model) handleSettingKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quitting = true
		m.QuitReason = "user"
		m.cancelAll()
		return m, tea.Quit
	case tea.KeyUp:
		if m.settingRow > 0 {
			m.settingRow--
		}
		return m, nil
	case tea.KeyDown:
		if m.settingRow < len(settingRowNames)-1 {
			m.settingRow++
		}
		return m, nil
	case tea.KeyEnter:
		return m.cycleSetting()
	case tea.KeyEsc:
		m.settings = false
		m.status = ""
		return m, nil
	}
	switch msg.String() {
	case "q":
		m.settings = false
		m.status = ""
		return m, nil
	case " ", "right": // 空格/右箭头同样切换档位
		return m.cycleSetting()
	}
	return m, nil
}

// cycleSetting 当前行切到下一档；切到"自定义…"时进入自定义输入。
func (m Model) cycleSetting() (tea.Model, tea.Cmd) {
	// 找到当前值所在档位索引；不在预设（自定义值）→ 视为下一档起点
	next := 0
	switch m.settingRow {
	case 0: // 限速
		cur := indexOf(speedPresets, m.baseOpt.Limit)
		next = cur + 1
		if next < len(speedPresets) {
			m.baseOpt.Limit = speedPresets[next]
			return m, nil
		}
	case 1: // 分片
		cur := indexOf(shardPresets, m.baseOpt.Shards)
		next = cur + 1
		if next < len(shardPresets) {
			m.baseOpt.Shards = shardPresets[next]
			return m, nil
		}
	case 2: // 校验
		cur := indexOf(verifyPresets, string(m.baseOpt.Verify))
		next = cur + 1
		if next < len(verifyPresets) {
			m.baseOpt.Verify = hashAlgo(verifyPresets[next])
			return m, nil
		}
	case 3: // 代理
		cur := indexOf(proxyPresets, m.baseOpt.Proxy)
		next = cur + 1
		if next < len(proxyPresets) {
			m.baseOpt.Proxy = proxyPresets[next]
			if proxyPresets[next] == "" {
				m.errMsg = ""
			}
			return m, nil
		}
	}
	// 走到末尾 → 自定义输入
	m.settingCustom = true
	m.input.Focus()
	m.input.SetValue("")
	switch m.settingRow {
	case 0:
		m.input.Placeholder = "如 5M / 1024k / 1048576（0=不限）"
	case 1:
		m.input.Placeholder = "1..16（0=自动）"
	case 2:
		m.input.Placeholder = "sha256 / sha1 / md5 / none"
	case 3:
		m.input.Placeholder = "http://host:port / socks5://host:port（空=直连）"
	}
	return m, textinput.Blink
}

// indexOf 返回 v 在预设中的索引；不存在返回 -1。
func indexOf[T comparable](presets []T, v T) int {
	for i, p := range presets {
		if p == v {
			return i
		}
	}
	return -1
}

// finishCustom 提交设置自定义值（Enter）：按当前行校验并写入 baseOpt。
func (m Model) finishCustom(raw string) (tea.Model, tea.Cmd) {
	row := m.settingRow
	m.settingCustom = false
	m.input.Blur()
	m.input.SetValue("")
	m.input.Placeholder = urlPlaceholder
	switch row {
	case 0: // 限速
		v, err := parseSpeed(raw)
		if err != nil {
			m.errMsg = err.Error()
			return m, nil
		}
		m.baseOpt.Limit = v
		m.status = "限速已设为 " + speedLabel(v)
	case 1: // 分片
		v, err := parseShards(raw)
		if err != nil {
			m.errMsg = err.Error()
			return m, nil
		}
		m.baseOpt.Shards = v
		m.status = "分片已设为 " + shardLabel(v)
	case 2: // 校验
		algo := strings.ToLower(strings.TrimSpace(raw))
		switch algo {
		case "", "none":
			m.baseOpt.Verify = ""
		case "sha256", "sha1", "md5":
			m.baseOpt.Verify = hashAlgo(algo)
		default:
			m.errMsg = "校验算法: sha256 / sha1 / md5 / none"
			return m, nil
		}
		m.status = "校验已设为 " + verifyLabel(string(m.baseOpt.Verify))
	case 3: // 代理
		raw = strings.TrimSpace(raw)
		if raw != "" && !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") &&
			!strings.HasPrefix(raw, "socks5://") {
			m.errMsg = "代理格式: http://host:port / https://host:port / socks5://host:port（空=直连）"
			return m, nil
		}
		m.baseOpt.Proxy = raw
		if raw == "" {
			m.status = "代理已清除（直连）"
		} else {
			m.status = "代理出口 " + raw
		}
	}
	m.errMsg = ""
	return m, nil
}

// speedLabel / shardLabel / verifyLabel 值 → 显示标签（自定义值时回显原值）。
func speedLabel(v int64) string {
	if i := indexOf(speedPresets, v); i >= 0 {
		return speedLabels[i]
	}
	return humanBytes(v) + "/s"
}

func shardLabel(v int) string {
	if i := indexOf(shardPresets, v); i >= 0 {
		return shardLabels[i]
	}
	return fmt.Sprintf("%d", v)
}

func verifyLabel(v string) string {
	if i := indexOf(verifyPresets, v); i >= 0 {
		return verifyLabels[i]
	}
	return v
}

// hashAlgo 字符串 → hash.Algorithm（hash 包类型转换）。
func hashAlgo(s string) hash.Algorithm { return hash.Algorithm(s) }

// parseSpeed 解析限速输入：支持 k/K/M/G 后缀（二进制，如 5M=5MiB）；裸数字=字节/秒。
func parseSpeed(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, nil
	}
	mult := int64(1)
	switch s[len(s)-1] {
	case 'k', 'K':
		mult, s = 1<<10, s[:len(s)-1]
	case 'm', 'M':
		mult, s = 1<<20, s[:len(s)-1]
	case 'g', 'G':
		mult, s = 1<<30, s[:len(s)-1]
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("限速格式: 如 5M / 1024k / 1048576（0=不限）")
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("限速格式: 如 5M / 1024k / 1048576（0=不限）")
	}
	if v > (1<<63-1)/mult {
		return 0, fmt.Errorf("限速值过大")
	}
	return v * mult, nil
}

// parseShards 解析分片数：0=自动，1..16。
func parseShards(raw string) (int, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 || v > 16 {
		return 0, fmt.Errorf("分片数: 0..16（0=自动）")
	}
	return v, nil
}

// pasteText 读取系统剪贴板文本（可替换以便测试）。Windows 纯 syscall；
// Linux 需 xclip/wl-paste 等外部程序，缺失时返回 false（终端自带粘贴兜底）。
var pasteText = func() (string, bool) {
	txt, err := clipboard.ReadAll()
	if err != nil || txt == "" {
		return "", false
	}
	return txt, true
}

// finishProxy 提交代理配置（Enter）：空值清除代理；非空校验前缀并生效。
// 与 CLI -proxy 同语义：显式设置代理即视为允许公网出站（network 层自动放行）。
// 生效范围：之后新添加的任务与按 p 重试的任务（t.start 时读取 baseOpt）。
func (m Model) finishProxy(raw string) (tea.Model, tea.Cmd) {
	m.proxying = false
	m.input.Blur()
	m.input.SetValue("")
	m.input.Placeholder = urlPlaceholder
	if raw == "" {
		m.baseOpt.Proxy = ""
		m.status = "代理已清除（直连）"
		return m, nil
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") &&
		!strings.HasPrefix(raw, "socks5://") {
		m.errMsg = "代理格式: http://host:port 或 https://host:port 或 socks5://host:port"
		return m, nil
	}
	m.baseOpt.Proxy = raw
	m.errMsg = ""
	m.status = "代理出口 " + raw + "（新任务与按 p 重试生效）"
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
		// R21：ETA = 剩余字节 / 当前速率（已知大小且速率>0 时）
		t.ETA = 0
		if st.FileSize > 0 && t.Speed > 0 && st.Done < st.FileSize {
			secs := float64(st.FileSize-st.Done) / t.Speed
			if secs < float64(int64(1)<<62) { // 防溢出
				t.ETA = int64(secs)
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
