package main

import (
	"embed"
	"flag"
	"log"

	"github.com/bernd/vibepit/experiments/desktop/backend"
	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	demo := flag.Bool("demo", false, "Use simulated sandbox and agent data")
	logDir := flag.String("stream-log-dir", "logs/claude", "Directory for Claude stream JSONL recordings")
	flag.Parse()
	if serverMode && !*demo {
		log.Fatal("Browser preview is demo-only. Run the native application for live sandbox access.")
	}
	var app *application.App
	service := backend.NewWorkspaces(*demo, func(event backend.Event) { app.Event.Emit("vibepit", event) }, *logDir)
	shell := newDesktopShell()
	services := append([]application.Service{application.NewService(service)}, shell.services()...)
	app = application.New(application.Options{
		Name:        "Vibepit Lab",
		Description: "Throwaway desktop experiment",
		Services:    services,
		Assets:      application.AssetOptions{Handler: application.BundledAssetFileServer(assets)},
		Mac:         application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: false},
		Linux:       application.LinuxOptions{DisableQuitOnLastWindowClosed: true},
		Windows:     application.WindowsOptions{DisableQuitOnLastWindowClosed: true},
		Server:      application.ServerOptions{Host: "127.0.0.1", Port: 9245},
		OnShutdown:  func() { shell.shutdown(); service.Shutdown() },
	})
	shell.setup(app, service)
	if !serverMode {
		if _, err := service.NewWindow(); err != nil {
			log.Fatal(err)
		}
	}
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
