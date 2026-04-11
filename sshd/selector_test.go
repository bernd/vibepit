package sshd

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bernd/vibepit/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testSessions() []session.SessionInfo {
	now := time.Now()
	return []session.SessionInfo{
		{ID: "session-1", Command: "/bin/bash", Status: "detached", ClientCount: 0, CreatedAt: now.Add(-10 * time.Minute)},
		{ID: "session-2", Command: "/bin/bash", Status: "detached", ClientCount: 0, CreatedAt: now.Add(-5 * time.Minute)},
	}
}

func keyPress(s string) tea.Msg {
	return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
}

func specialKeyPress(code rune) tea.Msg {
	return tea.KeyPressMsg{Code: code}
}

func TestSelectorNewSessionShortcut(t *testing.T) {
	m := newSelectorModel(testSessions())
	updated, cmd := m.Update(keyPress("n"))
	result := updated.(selectorModel).result
	require.NotNil(t, result)
	assert.Empty(t, result.sessionID)
	assert.NotNil(t, cmd) // tea.Quit
}

func TestSelectorQuit(t *testing.T) {
	m := newSelectorModel(testSessions())
	updated, cmd := m.Update(keyPress("q"))
	result := updated.(selectorModel).result
	assert.Nil(t, result)
	assert.NotNil(t, cmd) // tea.Quit
}

func TestSelectorNavigateAndSelect(t *testing.T) {
	m := newSelectorModel(testSessions())
	// Move down to session-2.
	updated, _ := m.Update(specialKeyPress(tea.KeyDown))
	m = updated.(selectorModel)
	assert.Equal(t, 1, m.cursor)

	// Select it.
	updated, cmd := m.Update(specialKeyPress(tea.KeyEnter))
	result := updated.(selectorModel).result
	require.NotNil(t, result)
	assert.Equal(t, "session-2", result.sessionID)
	assert.NotNil(t, cmd)
}

func TestSelectorNewSessionOption(t *testing.T) {
	m := newSelectorModel(testSessions())
	// Move cursor to the "new session" option (index 2).
	for range 2 {
		updated, _ := m.Update(specialKeyPress(tea.KeyDown))
		m = updated.(selectorModel)
	}
	assert.Equal(t, 2, m.cursor)

	updated, cmd := m.Update(specialKeyPress(tea.KeyEnter))
	result := updated.(selectorModel).result
	require.NotNil(t, result)
	assert.Empty(t, result.sessionID)
	assert.NotNil(t, cmd)
}

func TestSelectorCursorBounds(t *testing.T) {
	m := newSelectorModel(testSessions())
	// cursor starts at 0, pressing up should not go negative.
	updated, _ := m.Update(specialKeyPress(tea.KeyUp))
	m = updated.(selectorModel)
	assert.Equal(t, 0, m.cursor)

	// Move to the end.
	for range 10 {
		updated, _ = m.Update(specialKeyPress(tea.KeyDown))
		m = updated.(selectorModel)
	}
	// Should be clamped to itemCount-1 = 2.
	assert.Equal(t, 2, m.cursor)
}

func TestSelectorViewContainsSessionInfo(t *testing.T) {
	m := newSelectorModel(testSessions())
	view := m.View().Content
	assert.Contains(t, view, "session-1")
	assert.Contains(t, view, "session-2")
	assert.Contains(t, view, "[new session]")
	assert.Contains(t, view, "detached")
}

func TestFormatStatus(t *testing.T) {
	now := time.Now()
	info := session.SessionInfo{Status: "detached", CreatedAt: now.Add(-5 * time.Minute)}
	result := formatStatus(info)
	assert.Contains(t, result, "detached")
}
