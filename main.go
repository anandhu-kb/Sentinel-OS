package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

// The //go:embed directive is a compile-time instruction that bundles the entire
// frontend folder into the binary itself. This makes Sentinel-OS a single,
// self-contained executable — no separate web server or file serving needed.
//
// Python analogy: similar to Django's STATICFILES_DIRS, but instead of pointing
// to a disk location at runtime, the files are physically embedded into the
// compiled binary at build time by the Go compiler.
//
//go:embed all:frontend
var assets embed.FS

func main() {
	// NewApp() is our constructor — returns a pointer to the initialized App struct.
	// Analogy: instantiating the core Django application object before serve().
	app := NewApp()

	err := wails.Run(&options.App{
		Title:     "Sentinel-OS",
		Width:     1280,
		Height:    800,
		MinWidth:  960,
		MinHeight: 640,

		// AssetServer wires the embedded filesystem to Wails' internal WebView.
		// In `wails dev` mode this is bypassed — Wails serves directly from disk.
		// In `wails build` mode, the embedded FS is used exclusively.
		AssetServer: &assetserver.Options{
			Assets: assets,
		},

		// Background color shown while the WebView is still loading.
		// Matches our dark-mode UI: #0D1117 (GitHub dark background).
		BackgroundColour: &options.RGBA{R: 13, G: 17, B: 23, A: 255},

		// Lifecycle hooks — Wails calls these at the correct moments.
		// Go functions are assigned here as first-class values (like Python lambdas).
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,

		// Bind registers struct instances. Every PUBLIC method on *App becomes
		// callable from JavaScript as:
		//   window.go.main.App.MethodName(args) → returns a Promise
		// Private methods (lowercase) are never exposed.
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		log.Fatalf("Error starting Sentinel-OS: %v", err)
	}
}
