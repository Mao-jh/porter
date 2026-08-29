// Command spike6 是 G-A Spike：Bubble Tea 千行列表实测（TUI 选型验证）。
// 无头运行（输出到文件），静置采样后自动退出。
package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type spikeModel struct {
	rows []string
}

func (m spikeModel) Init() tea.Cmd { return nil }

func (m spikeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case quitMsg:
		return m, tea.Quit
	}
	return m, nil
}

type quitMsg struct{}

func (m spikeModel) View() string {
	var b strings.Builder
	b.WriteString("Spike-6: bubbletea 1000-row list (headless)\n")
	for _, r := range m.rows {
		b.WriteString(r)
		b.WriteByte('\n')
	}
	return b.String()
}

func main() {
	rows := make([]string, 1000)
	for i := range rows {
		rows[i] = fmt.Sprintf("task-%04d http://127.0.0.1/file/blob-%04d.bin", i, i)
	}
	f, err := os.Create("spike6.out")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()

	p := tea.NewProgram(spikeModel{rows: rows}, tea.WithOutput(f), tea.WithInput(nil))
	time.AfterFunc(10*time.Second, func() { p.Send(quitMsg{}) })
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fmt.Fprintf(os.Stderr, "[spike6] go_heap_inuse=%.1fMB go_sys=%.1fMB\n",
		float64(m.HeapInuse)/1e6, float64(m.Sys)/1e6)
}
