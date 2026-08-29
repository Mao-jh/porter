// Command spike1 是 G-A Spike-1：Gio 千行列表空窗口实测。
// 行为：打开窗口（含 1000 行任务列表样式的静态数据），静置 6 秒（空闲期），
// 之后自动关闭。测量脚本在空闲期采样工作集/Private Bytes/CPU。
package main

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"gioui.org/app"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

const idleDuration = 12 * time.Second

func main() {
	go func() {
		w := &app.Window{}
		w.Option(app.Title("spike1-gio-list"), app.Size(unit.Dp(720), unit.Dp(480)))
		// 关闭必须由独立 goroutine 触发：空闲期无 FrameEvent，事件循环内无法感知时间流逝
		time.AfterFunc(idleDuration, func() { w.Perform(system.ActionClose) })
		err := run(w)
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		fmt.Fprintf(os.Stderr, "[spike1] go_heap_inuse=%.1fMB go_sys=%.1fMB exit=%v\n",
			float64(m.HeapInuse)/1e6, float64(m.Sys)/1e6, err)
		os.Exit(0)
	}()
	app.Main()
}

func run(w *app.Window) error {
	th := material.NewTheme()
	list := &widget.List{List: layout.List{Axis: layout.Vertical}}
	rows := make([]string, 1000)
	for i := range rows {
		rows[i] = fmt.Sprintf("task-%04d http://127.0.0.1/file/blob-%04d.bin", i, i)
	}

	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			material.Caption(th, "Spike-1: 1000-row list, idle after first frame").Layout(gtx)
			list.Layout(gtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions {
				return material.Body1(th, rows[i]).Layout(gtx)
			})
			e.Frame(gtx.Ops)
		}
	}
}
