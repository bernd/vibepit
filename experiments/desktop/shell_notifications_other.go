//go:build !linux && !server

package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

func newPlatformNotifications() nativeNotifications { return notifications.New() }
func restoreNativeWindow(window *application.WebviewWindow, _ string) error {
	window.Show()
	window.UnMinimise()
	window.Focus()
	return nil
}
