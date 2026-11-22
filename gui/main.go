// Command xs-hub-gui is the CrossStream hub desktop app: an unprivileged
// window that controls xs-hubd over its IPC socket.
package main

import (
	"embed"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"runtime"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"

	"github.com/zyndata/cross-stream/internal/ipc"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed packaging/crossstream-hub.png
var icon []byte

var version = "dev"

func main() {
	sock := flag.String("socket", envOr("XS_HUB_SOCKET", ipc.DefaultSocket), "xs-hubd IPC socket")
	debug := flag.Bool("debug", false, "debug logging")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("xs-hub-gui", version)
		return
	}
	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	webkitEnv()
	app := NewApp(*sock, log)
	err := wails.Run(&options.App{
		Title:     "CrossStream Hub",
		Width:     1360,
		Height:    820,
		MinWidth:  900,
		MinHeight: 600,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:     app.startup,
		OnBeforeClose: app.beforeClose,
		Bind:          []any{app},
		Linux: &linux.Options{
			Icon:             icon,
			ProgramName:      "crossstream-hub",
			WebviewGpuPolicy: linux.WebviewGpuPolicyNever,
		},
	})
	if err != nil {
		log.Error("gui", "err", err)
		os.Exit(1)
	}
}

// webkitEnv turns WebKitGTK's DMA-BUF renderer off before the webview starts
// (Linux; a value the user set wins): with GPU compositing off as well, the
// web process no longer crashed in the libEGL destructor at exit
// (decisions.md phase 18).
func webkitEnv() {
	if runtime.GOOS == "linux" && os.Getenv("WEBKIT_DISABLE_DMABUF_RENDERER") == "" {
		_ = os.Setenv("WEBKIT_DISABLE_DMABUF_RENDERER", "1")
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
