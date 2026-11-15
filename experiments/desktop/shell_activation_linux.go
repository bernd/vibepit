//go:build linux && !server

package main

import (
	"fmt"
	"strings"

	"github.com/ebitengine/purego"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func bindGTKSymbol(name string, function any) error {
	symbol, err := purego.Dlsym(purego.RTLD_DEFAULT, name)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", name, err)
	}
	purego.RegisterFunc(function, symbol)
	return nil
}

func setWindowActivationToken(window uintptr, token string) error {
	if strings.ContainsRune(token, '\x00') {
		return fmt.Errorf("invalid window activation token")
	}
	var getDisplay func(uintptr) uintptr
	if err := bindGTKSymbol("gtk_widget_get_display", &getDisplay); err != nil {
		return err
	}
	var waylandDisplayType func() uintptr
	if err := bindGTKSymbol("gdk_wayland_display_get_type", &waylandDisplayType); err == nil {
		var isType func(uintptr, uintptr) int32
		if err := bindGTKSymbol("g_type_check_instance_is_a", &isType); err != nil {
			return err
		}
		display := getDisplay(window)
		if isType(display, waylandDisplayType()) != 0 {
			// Setting the window startup ID activates an already-mapped Wayland surface
			// immediately. Use the display ID so the following present consumes it once.
			// This public GTK3/4 API is deprecated in GTK4 but still needed for this path.
			var setDisplayID func(uintptr, string)
			if err := bindGTKSymbol("gdk_wayland_display_set_startup_notification_id", &setDisplayID); err != nil {
				return err
			}
			setDisplayID(display, token)
			return nil
		}
	}
	var setStartupID func(uintptr, string)
	if err := bindGTKSymbol("gtk_window_set_startup_id", &setStartupID); err != nil {
		return err
	}
	setStartupID(window, token)
	return nil
}

func restoreNativeWindow(window *application.WebviewWindow, token string) error {
	var result error
	application.InvokeSync(func() {
		if token != "" {
			handle := window.NativeWindow()
			if handle == nil {
				result = fmt.Errorf("notification window is unavailable")
				return
			}
			if result = setWindowActivationToken(uintptr(handle), token); result != nil {
				return
			}
		}
		// Focus calls gtk_window_present on Linux, which shows/unminimises too.
		// Keep this adjacent to the token assignment on the GTK main thread.
		window.Focus()
	})
	return result
}
