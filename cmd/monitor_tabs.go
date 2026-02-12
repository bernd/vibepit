package cmd

import (
	"strings"

	"github.com/bernd/vibepit/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type tabbedMonitorScreen struct {
	tabs      []string
	activeTab int
	screens   []tui.Screen
}

func newTabbedMonitorScreen(network *monitorScreen, telemetry *telemetryScreen) *tabbedMonitorScreen {
	return &tabbedMonitorScreen{
		tabs:    []string{"Network", "Telemetry"},
		screens: []tui.Screen{network, telemetry},
	}
}

func (t *tabbedMonitorScreen) activeScreen() tui.Screen {
	return t.screens[t.activeTab]
}

func (t *tabbedMonitorScreen) Update(msg tea.Msg, w *tui.Window) (tui.Screen, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "tab":
			t.activeTab = (t.activeTab + 1) % len(t.tabs)
			return t, nil
		case "1":
			t.activeTab = 0
			return t, nil
		case "2":
			t.activeTab = 1
			return t, nil
		}
	}

	screen, cmd := t.activeScreen().Update(msg, w)
	t.screens[t.activeTab] = screen
	return t, cmd
}

func (t *tabbedMonitorScreen) View(w *tui.Window) string {
	tabBar := t.renderTabBar()
	content := t.activeScreen().View(w)
	return tabBar + "\n" + content
}

func (t *tabbedMonitorScreen) renderTabBar() string {
	activeStyle := lipgloss.NewStyle().Foreground(tui.ColorCyan).Bold(true)
	inactiveStyle := lipgloss.NewStyle().Foreground(tui.ColorField)

	var parts []string
	for i, name := range t.tabs {
		if i == t.activeTab {
			parts = append(parts, activeStyle.Render("["+name+"]"))
		} else {
			parts = append(parts, inactiveStyle.Render(" "+name+" "))
		}
	}
	return strings.Join(parts, "  ")
}

func (t *tabbedMonitorScreen) FooterKeys(w *tui.Window) []tui.FooterKey {
	keys := t.activeScreen().FooterKeys(w)
	keys = append(keys, tui.FooterKey{Key: "tab", Desc: "switch tab"})
	return keys
}

func (t *tabbedMonitorScreen) FooterStatus(w *tui.Window) string {
	return t.activeScreen().FooterStatus(w)
}
