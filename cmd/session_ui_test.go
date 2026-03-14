package cmd

import (
	"fmt"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/tui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testStubScreen struct{}

func (s *testStubScreen) Update(msg tea.Msg, w *tui.Window) (tui.Screen, tea.Cmd) { return s, nil }
func (s *testStubScreen) View(w *tui.Window) string                               { return "" }
func (s *testStubScreen) FooterKeys(w *tui.Window) []tui.FooterKey                { return nil }
func (s *testStubScreen) FooterStatus(w *tui.Window) string                       { return "" }

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
	s := newSessionScreen(sessions, nil, nil)
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
		s.Update(tea.KeyPressMsg{Code: 'j', Text: "j"}, w)
		assert.Equal(t, 1, s.Pos)
	})

	t.Run("k moves up from pos 1", func(t *testing.T) {
		s, w := makeSessionTestSetup(5)
		s.Pos = 1
		s.Update(tea.KeyPressMsg{Code: 'k', Text: "k"}, w)
		assert.Equal(t, 0, s.Pos)
	})

	t.Run("G jumps to end", func(t *testing.T) {
		s, w := makeSessionTestSetup(5)
		assert.Equal(t, 0, s.Pos)
		s.Update(tea.KeyPressMsg{Code: 'G', Text: "G"}, w)
		assert.Equal(t, 4, s.Pos)
	})
}

func TestSessionScreen_Selection(t *testing.T) {
	t.Run("enter selects session and quits in standalone mode", func(t *testing.T) {
		s, w := makeSessionTestSetup(3)
		s.Pos = 1
		_, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter}, w)
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
		}, nil)
		header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "selector"}
		w := tui.NewWindow(header, s)
		w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

		s.Pos = 2
		screen, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter}, w)
		assert.Equal(t, s, screen, "screen should not switch until async callback returns")
		require.NotNil(t, cmd)
		require.Nil(t, gotInfo, "onSelect callback should run asynchronously via tea.Cmd")

		msg := cmd()
		screen, nextCmd := s.Update(msg, w)
		assert.Nil(t, nextCmd)
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
	view := w.View().Content
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
	s := newSessionScreen(sessions, onSelect, nil)
	header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "selector"}
	w := tui.NewWindow(header, s)
	w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

	s.Pos = 0
	newScreen, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter}, w)
	// Should stay on session screen while async callback is pending
	assert.Equal(t, s, newScreen)
	require.NotNil(t, cmd)

	// Process async select result
	newScreen, cmd = s.Update(cmd(), w)
	assert.Equal(t, s, newScreen)
	require.NotNil(t, cmd, "callback error command should be bubbled up")

	// Process callback error message
	s.Update(cmd(), w)
	require.Error(t, w.Err())
	assert.Contains(t, w.Err().Error(), "connect failed")
}

func TestSessionScreen_Polling(t *testing.T) {
	t.Run("tick triggers session poll", func(t *testing.T) {
		sessions := makeSessions(3)
		pollCalled := false
		pollFn := func() ([]ctr.ProxySession, error) {
			pollCalled = true
			return sessions, nil
		}
		s := newSessionScreen(sessions, nil, pollFn)
		header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "selector"}
		w := tui.NewWindow(header, s)
		w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

		_, cmd := s.Update(tui.TickMsg{}, w)
		require.NotNil(t, cmd, "tick should return poll command")
		assert.True(t, s.pollInFlight, "pollInFlight should be set")

		msg := cmd()
		result, ok := msg.(sessionPollResultMsg)
		require.True(t, ok)
		assert.Len(t, result.sessions, 3)
		assert.NoError(t, result.err)
		assert.True(t, pollCalled, "pollSessions callback should have been called")
	})

	t.Run("no double poll while in-flight", func(t *testing.T) {
		sessions := makeSessions(3)
		s := newSessionScreen(sessions, nil, func() ([]ctr.ProxySession, error) {
			return sessions, nil
		})
		header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "selector"}
		w := tui.NewWindow(header, s)
		w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

		s.Update(tui.TickMsg{}, w)
		_, cmd := s.Update(tui.TickMsg{}, w)
		assert.Nil(t, cmd, "should not start second poll while in-flight")
	})

	t.Run("poll result updates session list", func(t *testing.T) {
		sessions := makeSessions(3)
		s := newSessionScreen(sessions, nil, nil)
		s.pollInFlight = true
		header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "selector"}
		w := tui.NewWindow(header, s)
		w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

		newSessions := makeSessions(5)
		s.Update(sessionPollResultMsg{sessions: newSessions}, w)
		assert.Len(t, s.sessions, 5)
		assert.Equal(t, 5, s.ItemCount)
		assert.False(t, s.pollInFlight)
	})

	t.Run("cursor clamped when list shrinks", func(t *testing.T) {
		sessions := makeSessions(5)
		s := newSessionScreen(sessions, nil, nil)
		s.pollInFlight = true
		header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "selector"}
		w := tui.NewWindow(header, s)
		w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
		s.Pos = 3

		smaller := makeSessions(2)
		s.Update(sessionPollResultMsg{sessions: smaller}, w)
		assert.Equal(t, 1, s.Pos, "cursor should clamp to last item")
	})

	t.Run("empty list sets cursor to 0", func(t *testing.T) {
		sessions := makeSessions(3)
		s := newSessionScreen(sessions, nil, nil)
		s.pollInFlight = true
		s.Pos = 2
		header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "selector"}
		w := tui.NewWindow(header, s)
		w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

		s.Update(sessionPollResultMsg{sessions: nil}, w)
		assert.Len(t, s.sessions, 0)
		assert.Equal(t, 0, s.Pos)
		assert.Equal(t, 0, s.ItemCount)
	})

	t.Run("poll error shows in footer and continues polling", func(t *testing.T) {
		sessions := makeSessions(3)
		s := newSessionScreen(sessions, nil, func() ([]ctr.ProxySession, error) {
			return nil, fmt.Errorf("docker unavailable")
		})
		s.pollInFlight = true
		header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "selector"}
		w := tui.NewWindow(header, s)
		w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

		s.Update(sessionPollResultMsg{err: fmt.Errorf("docker unavailable")}, w)
		assert.Error(t, w.Err())
		assert.Contains(t, w.Err().Error(), "docker unavailable")
		assert.False(t, s.pollInFlight, "should allow next poll")
	})

	t.Run("repeated identical errors are suppressed", func(t *testing.T) {
		sessions := makeSessions(3)
		s := newSessionScreen(sessions, nil, func() ([]ctr.ProxySession, error) {
			return nil, fmt.Errorf("docker unavailable")
		})
		header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "selector"}
		w := tui.NewWindow(header, s)
		w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

		s.pollInFlight = true
		s.Update(sessionPollResultMsg{err: fmt.Errorf("docker unavailable")}, w)
		assert.Error(t, w.Err())

		s.pollInFlight = true
		s.Update(sessionPollResultMsg{err: fmt.Errorf("docker unavailable")}, w)
		assert.Error(t, w.Err())

		s.pollInFlight = true
		s.Update(sessionPollResultMsg{sessions: makeSessions(2)}, w)
		assert.NoError(t, w.Err())
	})

	t.Run("no polling without pollSessions callback", func(t *testing.T) {
		s, w := makeSessionTestSetup(3)
		_, cmd := s.Update(tui.TickMsg{}, w)
		assert.Nil(t, cmd, "should not poll without callback")
	})
}
