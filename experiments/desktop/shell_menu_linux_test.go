//go:build linux && !server

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplicationMenuLeavesTerminalEditingKeysAlone(t *testing.T) {
	menu := newDesktopShell().applicationMenu()
	assert.Nil(t, menu.FindByLabel("Delete"), "Backspace must reach the terminal")
	for _, tc := range []struct{ label, shortcut string }{
		{"Copy", "Ctrl+Shift+C"}, {"Paste", "Ctrl+Shift+V"}, {"Cut", "Ctrl+Shift+X"},
		{"Select All", "Ctrl+Shift+A"}, {"Undo", "Ctrl+Shift+Z"}, {"Redo", "Ctrl+Shift+Y"},
		{"New window", "Ctrl+Shift+N"}, {"Hide window", "Ctrl+Shift+W"}, {"Quit Vibepit Lab", "Ctrl+Shift+Q"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			item := menu.FindByLabel(tc.label)
			require.NotNil(t, item)
			assert.Equal(t, tc.shortcut, item.GetAccelerator(), "unshifted Ctrl chords belong to the shell")
		})
	}
	for _, label := range []string{"Terminal", "Claude", "Claude debug", "Show all windows", "Minimize"} {
		require.NotNil(t, menu.FindByLabel(label))
	}
}
