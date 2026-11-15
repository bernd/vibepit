//go:build server

package main

import (
	"github.com/bernd/vibepit/experiments/desktop/backend"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// Browser preview deliberately has no tray, native windows, or OS notifications.
type desktopShell struct{}

func newDesktopShell() *desktopShell                  { return &desktopShell{} }
func (*desktopShell) services() []application.Service { return nil }
func (*desktopShell) setup(_ *application.App, workspaces *backend.Workspaces) {
	workspaces.Hooks = backend.DesktopHooks{EnableNotifications: func() (string, error) { return "Native notifications are unavailable in browser preview", nil }}
}
func (*desktopShell) shutdown() {}
