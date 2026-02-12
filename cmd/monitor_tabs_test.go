package cmd

import (
	"testing"

	"github.com/bernd/vibepit/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

func makeTabbedSetup() (*tabbedMonitorScreen, *tui.Window) {
	network := newMonitorScreen(&SessionInfo{
		SessionID:  "test123",
		ProjectDir: "/test",
	}, nil)
	telemetry := newTelemetryScreen(nil)
	tabbed := newTabbedMonitorScreen(network, telemetry)

	header := &tui.HeaderInfo{ProjectDir: "/test", SessionID: "test123"}
	w := tui.NewWindow(header, tabbed)
	w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return tabbed, w
}

func TestTabbedMonitorScreen_TabSwitch(t *testing.T) {
	t.Run("starts on network tab", func(t *testing.T) {
		tabbed, _ := makeTabbedSetup()
		assert.Equal(t, 0, tabbed.activeTab)
	})

	t.Run("tab key switches to telemetry", func(t *testing.T) {
		tabbed, w := makeTabbedSetup()
		tabbed.Update(tea.KeyMsg{Type: tea.KeyTab}, w)
		assert.Equal(t, 1, tabbed.activeTab)
	})

	t.Run("tab key wraps back to network", func(t *testing.T) {
		tabbed, w := makeTabbedSetup()
		tabbed.Update(tea.KeyMsg{Type: tea.KeyTab}, w)
		tabbed.Update(tea.KeyMsg{Type: tea.KeyTab}, w)
		assert.Equal(t, 0, tabbed.activeTab)
	})

	t.Run("number 1 switches to network", func(t *testing.T) {
		tabbed, w := makeTabbedSetup()
		tabbed.activeTab = 1
		tabbed.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}}, w)
		assert.Equal(t, 0, tabbed.activeTab)
	})

	t.Run("number 2 switches to telemetry", func(t *testing.T) {
		tabbed, w := makeTabbedSetup()
		tabbed.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}}, w)
		assert.Equal(t, 1, tabbed.activeTab)
	})

	t.Run("view contains tab bar", func(t *testing.T) {
		tabbed, w := makeTabbedSetup()
		view := tabbed.View(w)
		assert.Contains(t, view, "Network")
		assert.Contains(t, view, "Telemetry")
	})

	t.Run("footer includes tab hint", func(t *testing.T) {
		tabbed, w := makeTabbedSetup()
		keys := tabbed.FooterKeys(w)
		descs := footerKeyDescs(keys)
		assert.Contains(t, descs, "switch tab")
	})
}
