package cmd

import (
	"fmt"
	"testing"
	"time"

	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testStubScreen struct{}

func (s *testStubScreen) Update(msg tea.Msg, w *tui.Window) (tui.Screen, tea.Cmd) { return s, nil }
func (s *testStubScreen) View(w *tui.Window) string                               { return "" }
func (s *testStubScreen) FooterKeys(w *tui.Window) []tui.FooterKey                { return nil }
func (s *testStubScreen) FooterStatus(w *tui.Window) string                        { return "" }

func makeSessions(n int) []ctr.ProxySession {
	var sessions []ctr.ProxySession
	for i := range n {
		sessions = append(sessions, ctr.ProxySession{
			ContainerID: fmt.Sprintf("container%d", i),
			SessionID:   fmt.Sprintf("session%d-abcdef12", i),
			ControlPort: fmt.Sprintf("%d", 3129+i),
			ProjectDir:  fmt.Sprintf("/home/user/project%d", i),
			StartedAt:   time.Now().Add(-time.Duration(i+1) * time.Hour),
		})
	}
	return sessions
}

func makeSessionTestSetup(n int) (*sessionScreen, *tui.Window) {
	sessions := makeSessions(n)
	s := newSessionScreen(sessions, nil)
	header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "selector"}
	w := tui.NewWindow(header, s)
	w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return s, w
}

func TestFormatUptime(t *testing.T) {
	tests := []struct {
		name     string
		started  time.Time
		now      time.Time
		expected string
	}{
		{"seconds only", time.Date(2026, 1, 1, 12, 0, 30, 0, time.UTC), time.Date(2026, 1, 1, 12, 0, 45, 0, time.UTC), "< 1m"},
		{"minutes only", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), time.Date(2026, 1, 1, 12, 7, 0, 0, time.UTC), "7m"},
		{"hours and minutes", time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC), time.Date(2026, 1, 1, 12, 13, 0, 0, time.UTC), "2h 13m"},
		{"days and hours", time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC), time.Date(2026, 1, 3, 15, 0, 0, 0, time.UTC), "2d 5h"},
		{"exactly one hour", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC), "1h 0m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatUptime(tt.started, tt.now)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSessionScreen_Navigation(t *testing.T) {
	t.Run("j moves down", func(t *testing.T) {
		s, w := makeSessionTestSetup(5)
		assert.Equal(t, 0, s.Pos)
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}, w)
		assert.Equal(t, 1, s.Pos)
	})

	t.Run("k moves up from pos 1", func(t *testing.T) {
		s, w := makeSessionTestSetup(5)
		s.Pos = 1
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}, w)
		assert.Equal(t, 0, s.Pos)
	})

	t.Run("G jumps to end", func(t *testing.T) {
		s, w := makeSessionTestSetup(5)
		assert.Equal(t, 0, s.Pos)
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}}, w)
		assert.Equal(t, 4, s.Pos)
	})
}

func TestSessionScreen_Selection(t *testing.T) {
	t.Run("enter selects session and quits in standalone mode", func(t *testing.T) {
		s, w := makeSessionTestSetup(3)
		s.Pos = 1
		_, cmd := s.Update(tea.KeyMsg{Type: tea.KeyEnter}, w)
		require.NotNil(t, s.selected)
		assert.Equal(t, "/home/user/project1", s.selected.ProjectDir)
		assert.Equal(t, "session1-abcdef12", s.selected.SessionID)
		assert.Equal(t, "3130", s.selected.ControlPort)
		require.NotNil(t, cmd, "should return tea.Quit cmd")
	})

	t.Run("enter calls onSelect callback when set", func(t *testing.T) {
		sessions := makeSessions(3)
		var gotInfo *SessionInfo
		stub := &testStubScreen{}
		s := newSessionScreen(sessions, func(info *SessionInfo) (tui.Screen, tea.Cmd) {
			gotInfo = info
			return stub, nil
		})
		header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "selector"}
		w := tui.NewWindow(header, s)
		w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

		s.Pos = 2
		screen, _ := s.Update(tea.KeyMsg{Type: tea.KeyEnter}, w)
		require.NotNil(t, gotInfo, "onSelect callback should have been called")
		assert.Equal(t, "/home/user/project2", gotInfo.ProjectDir)
		assert.Equal(t, "session2-abcdef12", gotInfo.SessionID)
		assert.Equal(t, stub, screen, "returned screen should be the stub")
	})
}

func TestSessionScreen_Footer(t *testing.T) {
	s, w := makeSessionTestSetup(3)
	keys := s.FooterKeys(w)
	descs := footerKeyDescs(keys)
	assert.Contains(t, descs, "select")
	assert.Contains(t, descs, "navigate")
}

func TestSessionScreen_View(t *testing.T) {
	_, w := makeSessionTestSetup(3)
	view := w.View()
	assert.Contains(t, view, "/home/user/project0")
	assert.Contains(t, view, "/home/user/project1")
	assert.Contains(t, view, "/home/user/project2")
}

func TestSessionScreen_ErrorMsg(t *testing.T) {
	s, w := makeSessionTestSetup(3)
	s.Update(sessionErrorMsg{err: fmt.Errorf("connection refused")}, w)
	require.Error(t, w.Err())
	assert.Contains(t, w.Err().Error(), "connection refused")
}

func TestSessionScreen_OnSelectError(t *testing.T) {
	sessions := makeSessions(3)
	onSelect := func(info *SessionInfo) (tui.Screen, tea.Cmd) {
		return nil, func() tea.Msg { return sessionErrorMsg{err: fmt.Errorf("connect failed")} }
	}
	s := newSessionScreen(sessions, onSelect)
	header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "selector"}
	w := tui.NewWindow(header, s)
	w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

	s.Pos = 0
	newScreen, cmd := s.Update(tea.KeyMsg{Type: tea.KeyEnter}, w)
	// Should stay on session screen
	assert.Equal(t, s, newScreen)
	// Should have a command that will deliver the error
	assert.NotNil(t, cmd)
}
