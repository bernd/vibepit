package sshd

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bernd/vibepit/session"
	"github.com/bernd/vibepit/tui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testSessions() []session.SessionInfo {
	now := time.Now()
	return []session.SessionInfo{
		{ID: "session-1", Command: "/bin/bash", Status: "detached", ClientCount: 0, CreatedAt: now.Add(-10 * time.Minute), DetachedAt: now.Add(-30 * time.Second)},
		{ID: "session-2", Command: "/bin/bash", Status: "detached", ClientCount: 0, CreatedAt: now.Add(-5 * time.Minute), DetachedAt: now.Add(-10 * time.Second)},
	}
}

func testWindow(s *selectorScreen) *tui.Window {
	return tui.NewWindow(&tui.HeaderInfo{}, s)
}

func keyPress(s string) tea.Msg {
	return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
}

func specialKeyPress(code rune) tea.Msg {
	return tea.KeyPressMsg{Code: code}
}

func TestSelectorNewSessionShortcut(t *testing.T) {
	s := newSelectorScreen(testSessions())
	s.VpHeight = 20
	w := testWindow(s)
	_, cmd := s.Update(keyPress("n"), w)
	require.NotNil(t, s.result)
	assert.Empty(t, s.result.sessionID)
	assert.NotNil(t, cmd) // tea.Quit
}

func TestSelectorQuit(t *testing.T) {
	s := newSelectorScreen(testSessions())
	s.VpHeight = 20
	w := testWindow(s)
	_, cmd := s.Update(keyPress("q"), w)
	assert.Nil(t, s.result)
	assert.NotNil(t, cmd) // tea.Quit
}

func TestSelectorNavigateAndSelect(t *testing.T) {
	s := newSelectorScreen(testSessions())
	s.VpHeight = 20
	w := testWindow(s)

	// Move down to session-2.
	s.Update(specialKeyPress(tea.KeyDown), w)
	assert.Equal(t, 1, s.Pos)

	// Select it.
	_, cmd := s.Update(specialKeyPress(tea.KeyEnter), w)
	require.NotNil(t, s.result)
	assert.Equal(t, "session-2", s.result.sessionID)
	assert.NotNil(t, cmd)
}

func TestSelectorNewSessionOption(t *testing.T) {
	s := newSelectorScreen(testSessions())
	s.VpHeight = 20
	w := testWindow(s)

	// Move cursor to the "new session" option (index 2).
	for range 2 {
		s.Update(specialKeyPress(tea.KeyDown), w)
	}
	assert.Equal(t, 2, s.Pos)

	_, cmd := s.Update(specialKeyPress(tea.KeyEnter), w)
	require.NotNil(t, s.result)
	assert.Empty(t, s.result.sessionID)
	assert.NotNil(t, cmd)
}

func TestSelectorCursorBounds(t *testing.T) {
	s := newSelectorScreen(testSessions())
	s.VpHeight = 20
	w := testWindow(s)

	// cursor starts at 0, pressing up should not go negative.
	s.Update(specialKeyPress(tea.KeyUp), w)
	assert.Equal(t, 0, s.Pos)

	// Move to the end.
	for range 10 {
		s.Update(specialKeyPress(tea.KeyDown), w)
	}
	// Should be clamped to itemCount-1 = 2.
	assert.Equal(t, 2, s.Pos)
}

func TestSelectorViewContainsSessionInfo(t *testing.T) {
	s := newSelectorScreen(testSessions())
	s.VpHeight = 20
	w := testWindow(s)
	view := s.View(w)
	assert.Contains(t, view, "session-1")
	assert.Contains(t, view, "session-2")
	assert.Contains(t, view, "[new session]")
	assert.Contains(t, view, "detached")
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{2*time.Hour + 15*time.Minute, "2h 15m"},
		{25*time.Hour + 30*time.Minute, "1d 1h"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, formatDuration(tt.d))
		})
	}
}
