// Command spike4 是 G-A2 PoC：Fyne 千行虚拟列表实测（AI 为第一用户的选型验证）。
// 行为：打开 720x480 窗口（widget.List 虚拟化 1000 行），静置 12 秒（空闲期采样），
// 自动退出。Fyne 需要 cgo（仅 gui module，CGO_ENABLED=1）。
package main

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/widget"
)

func main() {
	a := app.NewWithID("downloader.spike4")
	w := a.NewWindow("spike4-fyne-list")
	w.Resize(fyne.NewSize(720, 480))

	rows := make([]string, 1000)
	for i := range rows {
		rows[i] = fmt.Sprintf("task-%04d http://127.0.0.1/file/blob-%04d.bin", i, i)
	}
	list := widget.NewList(
		func() int { return len(rows) },
		func() fyne.CanvasObject { return widget.NewLabel("template") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			o.(*widget.Label).SetText(rows[i])
		},
	)
	w.SetContent(list)

	// 空闲 12 秒后退出（采样窗口在静置期）
	time.AfterFunc(12*time.Second, func() { a.Quit() })
	w.ShowAndRun()

	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fmt.Fprintf(os.Stderr, "[spike4] go_heap_inuse=%.1fMB go_sys=%.1fMB\n",
		float64(m.HeapInuse)/1e6, float64(m.Sys)/1e6)
}
