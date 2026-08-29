// Command spike3 是 G-A 对照组：lxn/walk 原生控件（1000 行 ListBox）实测。
package main

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
)

func main() {
	rows := make([]string, 1000)
	for i := range rows {
		rows[i] = fmt.Sprintf("task-%04d http://127.0.0.1/file/blob-%04d.bin", i, i)
	}
	var mw *walk.MainWindow
	if err := (MainWindow{
		AssignTo: &mw,
		Title:    "spike3-walk-list",
		MinSize:  Size{Width: 720, Height: 480},
		Layout:   VBox{},
		Children: []Widget{
			ListBox{Model: rows},
		},
	}).Create(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	time.AfterFunc(12*time.Second, func() {
		mw.Synchronize(func() { mw.Close() })
	})
	mw.Run()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fmt.Fprintf(os.Stderr, "[spike3] go_heap_inuse=%.1fMB go_sys=%.1fMB\n",
		float64(m.HeapInuse)/1e6, float64(m.Sys)/1e6)
}
