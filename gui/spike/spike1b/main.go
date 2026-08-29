// Command spike1b 是 Spike-1 对照组：Gio 空窗口（无任何控件），测运行时地板。
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
)

func main() {
	go func() {
		w := &app.Window{}
		w.Option(app.Title("spike1b-empty"), app.Size(unit.Dp(720), unit.Dp(480)))
		time.AfterFunc(12*time.Second, func() { w.Perform(system.ActionClose) })
		err := run(w)
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		fmt.Fprintf(os.Stderr, "[spike1b] go_heap_inuse=%.1fMB go_sys=%.1fMB exit=%v\n",
			float64(m.HeapInuse)/1e6, float64(m.Sys)/1e6, err)
		os.Exit(0)
	}()
	app.Main()
}

func run(w *app.Window) error {
	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			layout.Stack{}.Layout(gtx)
			e.Frame(gtx.Ops)
		}
	}
}
