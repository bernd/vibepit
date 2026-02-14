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
	events := newTelemetryScreen(nil)
	metrics := newMetricsScreen(nil)
	tabbed := newTabbedMonitorScreen(network, events, metrics)

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

	t.Run("tab key cycles through tabs", func(t *testing.T) {
		tabbed, w := makeTabbedSetup()
		tabbed.Update(tea.KeyMsg{Type: tea.KeyTab}, w)
		assert.Equal(t, 1, tabbed.activeTab)

		tabbed.Update(tea.KeyMsg{Type: tea.KeyTab}, w)
		assert.Equal(t, 2, tabbed.activeTab)

		tabbed.Update(tea.KeyMsg{Type: tea.KeyTab}, w)
		assert.Equal(t, 0, tabbed.activeTab) // wraps
	})

	t.Run("number keys select tabs directly", func(t *testing.T) {
		tabbed, w := makeTabbedSetup()

		tabbed.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}}, w)
		assert.Equal(t, 1, tabbed.activeTab)

		tabbed.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}}, w)
		assert.Equal(t, 2, tabbed.activeTab)

		tabbed.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}}, w)
		assert.Equal(t, 0, tabbed.activeTab)
	})

	t.Run("view contains all tab names", func(t *testing.T) {
		tabbed, w := makeTabbedSetup()
		view := tabbed.View(w)
		assert.Contains(t, view, "Network")
		assert.Contains(t, view, "Events")
		assert.Contains(t, view, "Metrics")
	})

	t.Run("footer includes tab hint", func(t *testing.T) {
		tabbed, w := makeTabbedSetup()
		keys := tabbed.FooterKeys(w)
		descs := footerKeyDescs(keys)
		assert.Contains(t, descs, "switch tab")
	})
}
