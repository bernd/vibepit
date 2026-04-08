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
		{ID: "session-1", Command: "/bin/bash", Status: "attached", ClientCount: 1, CreatedAt: now.Add(-10 * time.Minute)},
		{ID: "session-2", Command: "/bin/bash", Status: "detached", ClientCount: 0, CreatedAt: now.Add(-5 * time.Minute)},
		{ID: "session-3", Command: "/bin/bash", Status: "exited", ExitCode: 0, CreatedAt: now.Add(-30 * time.Minute), ExitedAt: now.Add(-2 * time.Minute)},
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
	assert.False(t, result.takeOver)
	assert.NotNil(t, cmd) // tea.Quit
}

func TestSelectorQuit(t *testing.T) {
	m := newSelectorModel(testSessions())
	updated, cmd := m.Update(keyPress("q"))
	result := updated.(selectorModel).result
	assert.Nil(t, result)
	assert.NotNil(t, cmd) // tea.Quit
}

func TestSelectorNavigateAndSelectDetached(t *testing.T) {
	m := newSelectorModel(testSessions())
	// Move down to session-2 (detached).
	updated, _ := m.Update(specialKeyPress(tea.KeyDown))
	m = updated.(selectorModel)
	assert.Equal(t, 1, m.cursor)

	// Select it.
	updated, cmd := m.Update(specialKeyPress(tea.KeyEnter))
	result := updated.(selectorModel).result
	require.NotNil(t, result)
	assert.Equal(t, "session-2", result.sessionID)
	assert.False(t, result.takeOver)
	assert.NotNil(t, cmd)
}

func TestSelectorExitedSessionNotSelectable(t *testing.T) {
	m := newSelectorModel(testSessions())
	// Move down to session-3 (exited).
	updated, _ := m.Update(specialKeyPress(tea.KeyDown))
	updated, _ = updated.(selectorModel).Update(specialKeyPress(tea.KeyDown))
	m = updated.(selectorModel)
	assert.Equal(t, 2, m.cursor)

	// Try to select — should not produce a result.
	updated, cmd := m.Update(specialKeyPress(tea.KeyEnter))
	result := updated.(selectorModel).result
	assert.Nil(t, result)
	assert.Nil(t, cmd)
}

func TestSelectorAttachedSessionPromptsTakeOver(t *testing.T) {
	m := newSelectorModel(testSessions())
	// session-1 is at cursor 0, already attached.
	updated, cmd := m.Update(specialKeyPress(tea.KeyEnter))
	m = updated.(selectorModel)
	assert.True(t, m.confirmTakeOver)
	assert.Nil(t, cmd) // no quit yet

	// Answer yes.
	updated, cmd = m.Update(keyPress("y"))
	result := updated.(selectorModel).result
	require.NotNil(t, result)
	assert.Equal(t, "session-1", result.sessionID)
	assert.True(t, result.takeOver)
	assert.NotNil(t, cmd)
}

func TestSelectorTakeOverDeclined(t *testing.T) {
	m := newSelectorModel(testSessions())
	updated, _ := m.Update(specialKeyPress(tea.KeyEnter))
	m = updated.(selectorModel)
	assert.True(t, m.confirmTakeOver)

	// Answer no.
	updated, cmd := m.Update(keyPress("n"))
	m = updated.(selectorModel)
	assert.False(t, m.confirmTakeOver)
	assert.Nil(t, m.result)
	assert.Nil(t, cmd)
}

func TestSelectorNewSessionOption(t *testing.T) {
	m := newSelectorModel(testSessions())
	// Move cursor to the "new session" option (index 3).
	for range 3 {
		updated, _ := m.Update(specialKeyPress(tea.KeyDown))
		m = updated.(selectorModel)
	}
	assert.Equal(t, 3, m.cursor)

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
	// Should be clamped to itemCount-1 = 3.
	assert.Equal(t, 3, m.cursor)
}

func TestSelectorViewContainsSessionInfo(t *testing.T) {
	m := newSelectorModel(testSessions())
	view := m.View().Content
	assert.Contains(t, view, "session-1")
	assert.Contains(t, view, "session-2")
	assert.Contains(t, view, "session-3")
	assert.Contains(t, view, "[new session]")
	assert.Contains(t, view, "attached")
	assert.Contains(t, view, "detached")
	assert.Contains(t, view, "exited")
	assert.Contains(t, view, "not selectable")
}

func TestFormatStatus(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name     string
		info     session.SessionInfo
		contains string
	}{
		{
			name:     "attached",
			info:     session.SessionInfo{Status: "attached", ClientCount: 2},
			contains: "2 client(s) attached",
		},
		{
			name:     "detached",
			info:     session.SessionInfo{Status: "detached", CreatedAt: now.Add(-5 * time.Minute)},
			contains: "detached",
		},
		{
			name:     "exited",
			info:     session.SessionInfo{Status: "exited", ExitCode: 1, ExitedAt: now.Add(-3 * time.Minute)},
			contains: "exited (1)",
		},
		{
			name:     "unknown",
			info:     session.SessionInfo{Status: "unknown"},
			contains: "unknown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatStatus(tt.info)
			assert.Contains(t, result, tt.contains)
		})
	}
}
