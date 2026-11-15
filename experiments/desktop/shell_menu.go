//go:build !server

package main

import (
	"fmt"
	"log"
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Unmodified Ctrl keys belong to terminal applications. macOS uses Command;
// elsewhere app-level shortcuts use Ctrl+Shift, including clipboard actions.
func menuShortcut(key string) string {
	if runtime.GOOS == "darwin" {
		return "Cmd+" + key
	}
	return "Ctrl+Shift+" + key
}

func (s *desktopShell) applicationMenu() *application.Menu {
	menu := application.NewMenu()
	if runtime.GOOS == "darwin" {
		menu.AddRole(application.AppMenu)
	}
	file := menu.AddSubmenu("File")
	file.Add("New window").SetAccelerator(menuShortcut("n")).OnClick(func(*application.Context) {
		go func() {
			if _, err := s.workspaces.NewWindow(); err != nil {
				log.Printf("New window: %v", err)
			}
		}()
	})
	file.Add("Hide window").SetAccelerator(menuShortcut("w")).OnClick(func(*application.Context) {
		s.withActiveWindow(func(id string) { s.hideNativeWindow(s.windows[id]) })
	})
	file.AddSeparator()
	file.Add("Quit Vibepit Lab").SetAccelerator(menuShortcut("q")).OnClick(func(*application.Context) { s.quit() })
	edit := menu.AddSubmenu("Edit")
	for _, item := range []struct{ label, command, key string }{
		{"Undo", "undo", "z"}, {"Redo", "redo", "y"},
		{"Cut", "cut", "x"}, {"Copy", "copy", "c"}, {"Paste", "paste", "v"}, {"Select All", "select-all", "a"},
	} {
		if item.command == "cut" || item.command == "select-all" {
			edit.AddSeparator()
		}
		shortcut := menuShortcut(item.key)
		if runtime.GOOS == "darwin" && item.command == "redo" {
			shortcut = "Cmd+Shift+z"
		}
		edit.Add(item.label).SetAccelerator(shortcut).OnClick(func(*application.Context) { s.sendMenuCommand(item.command) })
	}
	view := menu.AddSubmenu("View")
	for i, item := range []struct{ label, command string }{{"Terminal", "view-terminal"}, {"Claude", "view-claude"}, {"Claude debug", "view-debug"}} {
		view.Add(item.label).SetAccelerator(menuShortcut(fmt.Sprint(i + 1))).OnClick(func(*application.Context) { s.sendMenuCommand(item.command) })
	}
	windows := menu.AddSubmenu("Window")
	windows.Add("Minimize").OnClick(func(*application.Context) { s.withActiveWindow(func(id string) { s.windows[id].Minimise() }) })
	windows.Add("Show all windows").OnClick(func(*application.Context) { s.enqueue(s.showAllWindows) })
	windows.AddSeparator()
	s.windowMenu = windows.AddSubmenu("Workspaces")
	return menu
}

// GTK menus can temporarily own focus. Remember the last focused project window
// so actions still target it when Window.Current reports no webview.
func (s *desktopShell) withActiveWindow(action func(string)) {
	s.enqueue(func() {
		id := s.activeWindow
		application.InvokeSync(func() {
			if window := s.app.Window.Current(); window != nil {
				id = window.Name()
			}
		})
		if s.windows[id] != nil {
			action(id)
		}
	})
}
func (s *desktopShell) sendMenuCommand(command string) {
	s.withActiveWindow(func(id string) {
		s.app.Event.Emit("vibepit:command", map[string]string{"workspace": id, "command": command})
	})
}
func (s *desktopShell) refreshWindowMenu() {
	s.windowMenu.Clear()
	for _, info := range s.workspaces.ListWindows() {
		id := info.ID
		s.windowMenu.Add(info.Title).OnClick(func(*application.Context) {
			s.enqueue(func() {
				if err := s.openWindow(id); err != nil {
					log.Printf("Open workspace: %v", err)
				}
			})
		})
	}
	s.windowMenu.Update()
}

func (s *desktopShell) readClipboard() (string, error) {
	text, ok := s.app.Clipboard.Text()
	if !ok {
		return "", fmt.Errorf("clipboard does not contain readable text")
	}
	return text, nil
}
func (s *desktopShell) writeClipboard(text string) error {
	if !s.app.Clipboard.SetText(text) {
		return fmt.Errorf("could not write to the system clipboard")
	}
	return nil
}
