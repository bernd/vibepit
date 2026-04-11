package sshd

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bernd/vibepit/session"
)

// selectorResult holds the outcome of the session selector.
type selectorResult struct {
	sessionID string // empty means "new session"
}

// selectorModel is a Bubble Tea model for choosing a session to attach to.
type selectorModel struct {
	sessions []session.SessionInfo
	cursor   int
	result   *selectorResult
}

func newSelectorModel(sessions []session.SessionInfo) selectorModel {
	return selectorModel{
		sessions: sessions,
		cursor:   0,
	}
}

func (m selectorModel) Init() tea.Cmd {
	return nil
}

func (m selectorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.handleNormalKey(msg)
	}
	return m, nil
}

// itemCount returns total items: sessions + "new session" option.
func (m selectorModel) itemCount() int {
	return len(m.sessions) + 1
}

func (m selectorModel) handleNormalKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		// Quit without selecting.
		return m, tea.Quit

	case "n":
		// Shortcut for new session.
		m.result = &selectorResult{}
		return m, tea.Quit

	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}

	case "down", "j":
		if m.cursor < m.itemCount()-1 {
			m.cursor++
		}

	case "enter":
		// "New session" is the last item.
		if m.cursor == len(m.sessions) {
			m.result = &selectorResult{}
			return m, tea.Quit
		}
		m.result = &selectorResult{sessionID: m.sessions[m.cursor].ID}
		return m, tea.Quit
	}
	return m, nil
}

func (m selectorModel) View() tea.View {
	var b strings.Builder

	b.WriteString("Sessions:\n\n")

	for i, info := range m.sessions {
		cursor := "  "
		if i == m.cursor {
			cursor = "> "
		}
		status := formatStatus(info)
		line := fmt.Sprintf("%s%-14s %-12s %s", cursor, info.ID, info.Command, status)
		b.WriteString(line)
		b.WriteString("\n")
	}

	// "New session" option.
	cursor := "  "
	if m.cursor == len(m.sessions) {
		cursor = "> "
	}
	fmt.Fprintf(&b, "%s[new session]\n", cursor)

	b.WriteString("\nj/k or arrows to move, enter to select, n for new, q to quit")

	return tea.NewView(b.String())
}

func formatStatus(info session.SessionInfo) string {
	created := time.Since(info.CreatedAt).Truncate(time.Second)
	detached := time.Since(info.DetachedAt).Truncate(time.Second)
	return fmt.Sprintf("created %s ago, detached %s ago", created, detached)
}
