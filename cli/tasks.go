// tasks.go 实现 `porter tasks` 子命令（第 14 轮，B6）：列出持久化任务与历史。
// 数据源为 persist.Store（state.json），与下载引擎同一份状态——
// 断点续传的中间进度、done 历史、异常退出残留在此一目了然。
package cli

import (
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/Mao-jh/porter/persist"
)

// RunTasks 执行 tasks 子命令：按更新时间倒序列出所有任务状态。
func RunTasks(stateDir string) error {
	store, err := persist.Open(stateDir)
	if err != nil {
		return fmt.Errorf("打开任务状态目录失败: %w", err)
	}
	return listTasks(os.Stdout, store.All())
}

// listTasks 渲染任务列表（注入 Writer 便于测试）。
func listTasks(w interface{ Write([]byte) (int, error) }, states []*persist.State) error {
	if len(states) == 0 {
		_, err := fmt.Fprintln(w, "无任务记录")
		return err
	}
	sort.Slice(states, func(i, j int) bool { return states[i].UpdatedAt > states[j].UpdatedAt })
	_, err := fmt.Fprintf(w, "共 %d 个任务（按更新时间倒序）\n", len(states))
	if err != nil {
		return err
	}
	for _, st := range states {
		total := st.FileSize
		pct := 0.0
		if total > 0 {
			pct = float64(st.Done) / float64(total) * 100
			if pct > 100 {
				pct = 100
			}
		}
		size := fmt.Sprintf("%d/%dB", st.Done, total)
		if total >= 1<<20 || st.Done >= 1<<20 {
			size = fmt.Sprintf("%.1f/%.1fMiB", float64(st.Done)/(1<<20), float64(total)/(1<<20))
		} else if total >= 1<<10 || st.Done >= 1<<10 {
			size = fmt.Sprintf("%.1f/%.1fKiB", float64(st.Done)/(1<<10), float64(total)/(1<<10))
		}
		updated := time.Unix(0, st.UpdatedAt).Format("2006-01-02 15:04:05")
		fmt.Fprintf(w, "%-8s %s (%5.1f%%)  %s  %s\n", st.Status, size, pct, updated, st.URL)
		fmt.Fprintf(w, "         → %s\n", st.ID)
	}
	return nil
}
