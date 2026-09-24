// go-wrk-desktop 是一个 HTTP 压测桌面应用，Go + Wails 写的。
//
// 把命令行工具 go-wrk 的能力搬到界面上：填好地址、并发数、时长就能跑，
// 跑的过程中实时看 QPS 和延迟曲线，跑完看百分位、状态码分布和延迟分布。
//
// 请求语义和 go-wrk 的命令行参数一一对应（-c/-d/-T/-M/-H/-body/-host/
// -no-c/-no-ka/-no-vr/-redir/-http/-cert/-key/-ca），另外补了 -n 请求数模式。
// 引擎实现见 engine.go，统计见 stats.go。
package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

// webviewDataPath WebView2 存缓存和 localStorage 的地方。
// Wails 默认用 %APPDATA%\<可执行文件名>.exe —— 目录名带 .exe 不好看，改成不带后缀的，
// 顺便和 configs.go 存配置用的是同一个目录。
// 取不到目录就返回空串，交回 Wails 的默认行为。
func webviewDataPath() string {
	dir, err := dataDir()
	if err != nil {
		return ""
	}
	return dir
}

func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:     "go-wrk-desktop",
		Width:     1280,
		Height:    840,
		MinWidth:  1000,
		MinHeight: 680,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		OnStartup:        app.startup,
		Windows: &windows.Options{
			WebviewUserDataPath: webviewDataPath(),
		},
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		log.Fatalf("启动失败：%v", err)
	}
}
