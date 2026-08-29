// Command spike5 是 G-A2 对照组：Wails v2（WebView2）+ 1000 行 DOM 列表实测。
// 无 npm：go:embed 静态 HTML，纯 go build（Windows 端无需 cgo）。
package main

import (
	"context"
	"embed"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed index.html
var assets embed.FS

func main() {
	err := wails.Run(&options.App{
		Title:       "spike5-wails-list",
		Width:       720,
		Height:      480,
		AssetServer: &assetserver.Options{Assets: assets},
		OnStartup: func(ctx context.Context) {
			time.AfterFunc(12*time.Second, func() {
				wruntime.Quit(ctx)
			})
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fmt.Fprintf(os.Stderr, "[spike5] go_heap_inuse=%.1fMB go_sys=%.1fMB\n",
		float64(m.HeapInuse)/1e6, float64(m.Sys)/1e6)
}
