# Monitor Session Detector Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Automatically show a session selector when a monitored session disappears, and allow manual session switching via Esc.

**Architecture:** Add disconnect detection to `monitorScreen` (3s timer via tick counting), add live-refresh polling to `sessionScreen`, and wire both together in `monitor.go` via `onBack`/`onSelect`/`pollSessions` callbacks. `ControlClient` gets a `Close()` method for resource cleanup on transitions.

**Tech Stack:** Go, BubbleTea (charmbracelet/bubbletea), Lipgloss, testify

---

### Task 1: Add `Close()` to `ControlClient`

**Files:**
- Modify: `cmd/control.go:15-35`
- Test: `cmd/control_test.go`

**Step 1: Write the failing test**

Add to `cmd/control_test.go`:

```go
func TestControlClient_Close(t *testing.T) {
	log := proxy.NewLogBuffer(100)
	api := proxy.NewControlAPI(log, nil, nil, nil)
	client := testControlClient(t, api)

	// Should not panic and should be safe to call multiple times.
	client.Close()
	client.Close()
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run TestControlClient_Close -v`
Expected: FAIL — `client.Close undefined`

**Step 3: Write minimal implementation**

Add to `cmd/control.go` after the `NewControlClient` function:

```go
// Close releases idle connections held by the underlying HTTP transport.
func (c *ControlClient) Close() {
	c.http.CloseIdleConnections()
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./cmd/ -run TestControlClient_Close -v`
Expected: PASS

**Step 5: Commit**

```
git add cmd/control.go cmd/control_test.go
git commit -m "Add Close method to ControlClient"
```

---

### Task 2: Add `onBack` field and `Esc` key to `monitorScreen`

**Files:**
- Modify: `cmd/monitor_ui.go:34-50` (struct + constructor)
- Modify: `cmd/monitor_ui.go:112-134` (Update key handling)
- Modify: `cmd/monitor_ui.go:229-249` (FooterKeys)
- Test: `cmd/monitor_ui_test.go`

**Step 1: Write the failing test for Esc**

Add to `cmd/monitor_ui_test.go`:

```go
func TestMonitorScreen_EscReturnsSessionScreen(t *testing.T) {
	s, w := makeTestSetup(5)
	stub := &testStubScreen{}
	s.onBack = func() tui.Screen { return stub }

	screen, _ := s.Update(tea.KeyMsg{Type: tea.KeyEscape}, w)
	assert.Equal(t, stub, screen, "Esc should return the onBack screen")
}

func TestMonitorScreen_EscWithoutOnBack(t *testing.T) {
	s, w := makeTestSetup(5)
	// onBack is nil — Esc should be ignored.
	screen, _ := s.Update(tea.KeyMsg{Type: tea.KeyEscape}, w)
	assert.Equal(t, s, screen, "Esc without onBack should stay on monitor")
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run TestMonitorScreen_Esc -v`
Expected: FAIL — `s.onBack undefined`

**Step 3: Add `onBack` field to struct**

In `cmd/monitor_ui.go`, add `onBack` to the `monitorScreen` struct:

```go
type monitorScreen struct {
	session       *SessionInfo
	client        *ControlClient
	onBack        func() tui.Screen
	cursor        tui.Cursor
	pollCursor    uint64
	pollInFlight  bool
	items         []logItem
	newCount      int
	firstTickSeen bool
}
```

**Step 4: Add Esc handling in Update**

In `cmd/monitor_ui.go`, inside the `tea.KeyMsg` switch in `Update`, add before the `"q", "ctrl+c"` case:

```go
		case "esc":
			if s.onBack != nil {
				s.client.Close()
				return s.onBack(), nil
			}
```

**Step 5: Add `esc` to FooterKeys**

In `cmd/monitor_ui.go`, in `FooterKeys`, add `esc` key before the cursor keys (after the allow keys block, before `keys = append(keys, s.cursor.FooterKeys()...)`):

```go
	if s.onBack != nil {
		keys = append(keys, tui.FooterKey{Key: "esc", Desc: "sessions"})
	}
```

**Step 6: Run tests to verify they pass**

Run: `go test ./cmd/ -run TestMonitorScreen_Esc -v`
Expected: PASS

**Step 7: Run all existing tests**

Run: `go test ./cmd/ -v`
Expected: All PASS (existing tests use `onBack: nil` implicitly)

**Step 8: Commit**

```
git add cmd/monitor_ui.go cmd/monitor_ui_test.go
git commit -m "Add Esc key to monitor screen for session switching"
```

---

### Task 3: Add disconnect detection to `monitorScreen`

**Files:**
- Modify: `cmd/monitor_ui.go:34-50` (struct fields)
- Modify: `cmd/monitor_ui.go:159-191` (Update — poll error + tick handling)
- Test: `cmd/monitor_ui_test.go`

**Step 1: Write the failing test for disconnect timer**

Add to `cmd/monitor_ui_test.go`:

```go
func TestMonitorScreen_DisconnectTransition(t *testing.T) {
	t.Run("poll error sets disconnectedAt", func(t *testing.T) {
		s, w := makeTestSetup(5)
		stub := &testStubScreen{}
		s.onBack = func() tui.Screen { return stub }

		s.Update(logsPollResultMsg{err: fmt.Errorf("connection refused")}, w)
		assert.False(t, s.disconnectedAt.IsZero(), "disconnectedAt should be set on error")
	})

	t.Run("transitions after 12 ticks (3s)", func(t *testing.T) {
		s, w := makeTestSetup(5)
		stub := &testStubScreen{}
		s.onBack = func() tui.Screen { return stub }

		s.Update(logsPollResultMsg{err: fmt.Errorf("connection refused")}, w)

		// Simulate 11 ticks — should stay on monitor.
		for range 11 {
			screen, _ := s.Update(tui.TickMsg{}, w)
			assert.Equal(t, s, screen, "should not transition before 3s")
		}

		// 12th tick — should transition.
		screen, _ := s.Update(tui.TickMsg{}, w)
		assert.Equal(t, stub, screen, "should transition to session selector after 3s")
	})

	t.Run("multiple errors reset timer", func(t *testing.T) {
		s, w := makeTestSetup(5)
		stub := &testStubScreen{}
		s.onBack = func() tui.Screen { return stub }

		s.Update(logsPollResultMsg{err: fmt.Errorf("error 1")}, w)

		// 6 ticks pass (1.5s).
		for range 6 {
			s.Update(tui.TickMsg{}, w)
		}

		// New error resets the timer.
		s.Update(logsPollResultMsg{err: fmt.Errorf("error 2")}, w)

		// 11 more ticks — should NOT transition (timer was reset).
		for range 11 {
			screen, _ := s.Update(tui.TickMsg{}, w)
			assert.Equal(t, s, screen)
		}

		// 12th tick after reset — should transition.
		screen, _ := s.Update(tui.TickMsg{}, w)
		assert.Equal(t, stub, screen)
	})

	t.Run("no transition without onBack", func(t *testing.T) {
		s, w := makeTestSetup(5)
		// onBack is nil.
		s.Update(logsPollResultMsg{err: fmt.Errorf("connection refused")}, w)

		for range 20 {
			screen, _ := s.Update(tui.TickMsg{}, w)
			assert.Equal(t, s, screen, "should not transition without onBack")
		}
	})

	t.Run("successful poll clears disconnect state", func(t *testing.T) {
		s, w := makeTestSetup(5)
		stub := &testStubScreen{}
		s.onBack = func() tui.Screen { return stub }

		s.Update(logsPollResultMsg{err: fmt.Errorf("connection refused")}, w)
		assert.False(t, s.disconnectedAt.IsZero())

		// Successful poll clears disconnect state.
		s.Update(logsPollResultMsg{entries: nil}, w)
		assert.True(t, s.disconnectedAt.IsZero(), "successful poll should clear disconnectedAt")

		// 20 ticks — should NOT transition.
		for range 20 {
			screen, _ := s.Update(tui.TickMsg{}, w)
			assert.Equal(t, s, screen)
		}
	})
}

func TestMonitorScreen_DisconnectFooterMessage(t *testing.T) {
	s, w := makeTestSetup(5)
	s.onBack = func() tui.Screen { return &testStubScreen{} }

	s.Update(logsPollResultMsg{err: fmt.Errorf("connection refused")}, w)
	require.Error(t, w.Err())
	assert.Contains(t, w.Err().Error(), "test123456")
	assert.Contains(t, w.Err().Error(), "disconnected")
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run TestMonitorScreen_Disconnect -v`
Expected: FAIL — `s.disconnectedAt undefined`

**Step 3: Add disconnect fields and logic**

In `cmd/monitor_ui.go`, add fields to `monitorScreen`:

```go
type monitorScreen struct {
	session        *SessionInfo
	client         *ControlClient
	onBack         func() tui.Screen
	cursor         tui.Cursor
	pollCursor     uint64
	pollInFlight   bool
	items          []logItem
	newCount       int
	firstTickSeen  bool
	disconnectedAt time.Time
	disconnectTick int
}
```

In the `logsPollResultMsg` handler, replace the current error handling:

```go
	case logsPollResultMsg:
		s.pollInFlight = false
		if msg.err != nil {
			if s.onBack != nil && s.disconnectedAt.IsZero() {
				s.disconnectedAt = time.Now()
				s.disconnectTick = 0
				w.SetError(fmt.Errorf("session %s disconnected", s.session.SessionID))
			} else if s.onBack == nil {
				w.SetError(msg.err)
			}
			break
		}
		s.disconnectedAt = time.Time{}
		w.ClearError()
```

In the `tui.TickMsg` handler, add disconnect countdown check at the top (before the polling logic):

```go
	case tui.TickMsg:
		if s.onBack != nil && !s.disconnectedAt.IsZero() {
			s.disconnectTick++
			if s.disconnectTick >= 12 { // 12 ticks * 250ms = 3s
				s.client.Close()
				return s.onBack(), nil
			}
			s.firstTickSeen = true
			return s, nil // Don't poll while disconnected.
		}
		if (w.IntervalElapsed(pollInterval) || !s.firstTickSeen) && !s.pollInFlight {
```

Note: the existing `s.firstTickSeen = true` at the end of the TickMsg case stays.

**Step 4: Run tests to verify they pass**

Run: `go test ./cmd/ -run TestMonitorScreen_Disconnect -v`
Expected: PASS

**Step 5: Run all tests**

Run: `go test ./cmd/ -v`
Expected: All PASS

**Step 6: Commit**

```
git add cmd/monitor_ui.go cmd/monitor_ui_test.go
git commit -m "Add disconnect detection with 3s transition timer"
```

---

### Task 4: Add session polling to `sessionScreen`

**Files:**
- Modify: `cmd/session_ui.go:36-59` (struct + constructor + new msg type)
- Modify: `cmd/session_ui.go:61-109` (Update)
- Modify: `cmd/session_ui.go:111-127` (View — empty state)
- Modify: `cmd/session_ui.go:148-150` (FooterStatus)
- Test: `cmd/session_ui_test.go`

**Step 1: Write the failing tests**

Add to `cmd/session_ui_test.go`:

```go
func TestSessionScreen_Polling(t *testing.T) {
	t.Run("tick triggers session poll", func(t *testing.T) {
		sessions := makeSessions(3)
		pollCalled := false
		s := newSessionScreen(sessions, nil)
		s.pollSessions = func() ([]ctr.ProxySession, error) {
			pollCalled = true
			return sessions, nil
		}
		header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "selector"}
		w := tui.NewWindow(header, s)
		w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

		// First tick should trigger poll (IntervalElapsed(1s) returns true on frame 0).
		_, cmd := s.Update(tui.TickMsg{}, w)
		require.NotNil(t, cmd, "tick should return poll command")
		assert.True(t, s.pollInFlight, "pollInFlight should be set")

		// Execute the command.
		msg := cmd()
		require.False(t, pollCalled, "poll should not be called before cmd runs")
		// Actually pollCalled is set during cmd() since that's when the closure runs.
		// Let's just check the result message type.
		result, ok := msg.(sessionPollResultMsg)
		require.True(t, ok)
		assert.Len(t, result.sessions, 3)
		assert.NoError(t, result.err)
	})

	t.Run("no double poll while in-flight", func(t *testing.T) {
		sessions := makeSessions(3)
		s := newSessionScreen(sessions, nil)
		s.pollSessions = func() ([]ctr.ProxySession, error) {
			return sessions, nil
		}
		header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "selector"}
		w := tui.NewWindow(header, s)
		w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

		s.Update(tui.TickMsg{}, w) // starts poll
		_, cmd := s.Update(tui.TickMsg{}, w)
		assert.Nil(t, cmd, "should not start second poll while in-flight")
	})

	t.Run("poll result updates session list", func(t *testing.T) {
		sessions := makeSessions(3)
		s := newSessionScreen(sessions, nil)
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
		s := newSessionScreen(sessions, nil)
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
		s := newSessionScreen(sessions, nil)
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
		s := newSessionScreen(sessions, nil)
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
		s := newSessionScreen(sessions, nil)
		header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "selector"}
		w := tui.NewWindow(header, s)
		w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

		s.pollInFlight = true
		s.Update(sessionPollResultMsg{err: fmt.Errorf("docker unavailable")}, w)
		assert.Error(t, w.Err())

		// Same error again — should not call SetError again (error stays).
		s.pollInFlight = true
		s.Update(sessionPollResultMsg{err: fmt.Errorf("docker unavailable")}, w)
		assert.Error(t, w.Err())

		// Successful poll clears error.
		s.pollInFlight = true
		s.Update(sessionPollResultMsg{sessions: makeSessions(2)}, w)
		assert.NoError(t, w.Err())
	})

	t.Run("no polling without pollSessions callback", func(t *testing.T) {
		s, w := makeSessionTestSetup(3) // pollSessions is nil
		_, cmd := s.Update(tui.TickMsg{}, w)
		assert.Nil(t, cmd, "should not poll without callback")
	})
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./cmd/ -run TestSessionScreen_Polling -v`
Expected: FAIL — `s.pollSessions undefined`, `sessionPollResultMsg undefined`

**Step 3: Add polling fields and message type**

In `cmd/session_ui.go`, add the new message type and update the struct:

```go
// sessionPollResultMsg is returned by async session list polling.
type sessionPollResultMsg struct {
	sessions []ctr.ProxySession
	err      error
}

type sessionScreen struct {
	tui.Cursor
	sessions     []ctr.ProxySession
	selected     *SessionInfo
	onSelect     func(*SessionInfo) (tui.Screen, tea.Cmd)
	pollSessions func() ([]ctr.ProxySession, error)
	pollInFlight bool
	lastPollErr  string
}
```

**Step 4: Add poll command and Update handling**

Add a poll command method:

```go
func (s *sessionScreen) pollSessionsCmd() tea.Cmd {
	return func() tea.Msg {
		sessions, err := s.pollSessions()
		return sessionPollResultMsg{sessions: sessions, err: err}
	}
}
```

In `Update`, add handling for `sessionPollResultMsg` and `tui.TickMsg`:

```go
	case sessionPollResultMsg:
		s.pollInFlight = false
		if msg.err != nil {
			errStr := msg.err.Error()
			if errStr != s.lastPollErr {
				s.lastPollErr = errStr
				w.SetError(msg.err)
			}
			break
		}
		s.lastPollErr = ""
		w.ClearError()
		s.sessions = msg.sessions
		s.ItemCount = len(s.sessions)
		if s.Pos >= s.ItemCount {
			s.Pos = max(s.ItemCount-1, 0)
		}
		s.EnsureVisible()

	case tui.TickMsg:
		if s.pollSessions != nil && w.IntervalElapsed(time.Second) && !s.pollInFlight {
			s.pollInFlight = true
			return s, s.pollSessionsCmd()
		}
```

**Step 5: Update View for empty state**

In `cmd/session_ui.go`, update the `View` method. Replace the hardcoded "Multiple sessions" note with logic that handles empty state:

```go
func (s *sessionScreen) View(w *tui.Window) string {
	var lines []string

	if len(s.sessions) == 0 {
		note := lipgloss.NewStyle().Foreground(tui.ColorField).
			Render("No sessions running. Waiting...")
		lines = append(lines, note)
	} else {
		note := lipgloss.NewStyle().Foreground(tui.ColorField).
			Render("Multiple sessions running. Select one:")
		lines = append(lines, note, "")

		end := s.Offset + s.VpHeight
		end = min(end, len(s.sessions))
		now := time.Now()
		for i := s.Offset; i < end; i++ {
			lines = append(lines, renderSessionLine(s.sessions[i], i == s.Pos, now))
		}
	}
	for len(lines) < s.VpHeight {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}
```

**Step 6: Update FooterStatus for empty state**

```go
func (s *sessionScreen) FooterStatus(w *tui.Window) string {
	if len(s.sessions) == 0 {
		return "waiting"
	}
	return fmt.Sprintf("%d sessions", len(s.sessions))
}
```

**Step 7: Update FooterKeys to hide "enter select" when empty**

```go
func (s *sessionScreen) FooterKeys(w *tui.Window) []tui.FooterKey {
	var keys []tui.FooterKey
	if len(s.sessions) > 0 {
		keys = append(keys, tui.FooterKey{Key: "enter", Desc: "select"})
	}
	keys = append(keys, s.Cursor.FooterKeys()...)
	return keys
}
```

**Step 8: Run tests to verify they pass**

Run: `go test ./cmd/ -run TestSessionScreen_Polling -v`
Expected: PASS

**Step 9: Run all tests**

Run: `go test ./cmd/ -v`
Expected: All PASS

**Step 10: Commit**

```
git add cmd/session_ui.go cmd/session_ui_test.go
git commit -m "Add live session polling to session selector"
```

---

### Task 5: Wire everything together in `monitor.go`

**Files:**
- Modify: `cmd/monitor.go:19-68`

**Step 1: Refactor `MonitorCommand` and `runMonitor`**

Replace the entire content of `cmd/monitor.go` with:

```go
package cmd

import (
	"context"
	"fmt"

	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/urfave/cli/v3"
)

func MonitorCommand() *cli.Command {
	return &cli.Command{
		Name:     "monitor",
		Usage:    "Connect to a running proxy for logs and admin",
		Category: "Utilities",
		Flags:    []cli.Flag{sessionFlag},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			client, err := ctr.NewClient()
			if err != nil {
				return fmt.Errorf("cannot create container client: %w", err)
			}
			defer client.Close()

			pollSessions := func() ([]ctr.ProxySession, error) {
				return client.ListProxySessions(ctx)
			}

			var onSelect func(*SessionInfo) (tui.Screen, tea.Cmd)
			onBack := func() tui.Screen {
				return newSessionScreen(nil, onSelect, pollSessions)
			}
			onSelect = func(info *SessionInfo) (tui.Screen, tea.Cmd) {
				cc, err := NewControlClient(info)
				if err != nil {
					return nil, func() tea.Msg { return sessionErrorMsg{err} }
				}
				return newMonitorScreen(info, cc, onBack), nil
			}

			filter := cmd.String("session")
			session, sessions, err := discoverSessionOrAll(ctx, filter)
			if err != nil {
				// No sessions found — start with empty selector if no filter.
				if filter == "" {
					s := newSessionScreen(nil, onSelect, pollSessions)
					return runTUI("vibepit", "session selector", s)
				}
				return fmt.Errorf("cannot find running proxy: %w", err)
			}

			if session != nil {
				cc, err := NewControlClient(session)
				if err != nil {
					return err
				}
				screen := newMonitorScreen(session, cc, onBack)
				return runTUI(session.ProjectDir, session.SessionID, screen)
			}

			// Multiple sessions — start with selector.
			s := newSessionScreen(sessions, onSelect, pollSessions)
			return runTUI("vibepit", "session selector", s)
		},
	}
}

func runTUI(projectDir, sessionID string, screen tui.Screen) error {
	header := &tui.HeaderInfo{ProjectDir: projectDir, SessionID: sessionID}
	w := tui.NewWindow(header, screen)
	p := tea.NewProgram(w, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("monitor UI: %w", err)
	}
	return nil
}
```

Note: `newSessionScreen` now takes a third argument `pollSessions`. The `newMonitorScreen` now takes a third argument `onBack`. The standalone `selectSession` function in `session_ui.go` and `runMonitor` function are no longer used by the monitor command, but `selectSession` is still used by `discoverSession` in `session.go`, so leave it as-is.

**Step 2: Update `newSessionScreen` signature**

In `cmd/session_ui.go`, update the constructor to accept `pollSessions`:

```go
func newSessionScreen(sessions []ctr.ProxySession, onSelect func(*SessionInfo) (tui.Screen, tea.Cmd), pollSessions func() ([]ctr.ProxySession, error)) *sessionScreen {
	return &sessionScreen{
		Cursor:       tui.Cursor{ItemCount: len(sessions)},
		sessions:     sessions,
		onSelect:     onSelect,
		pollSessions: pollSessions,
	}
}
```

Update `selectSession` (used by `session.go`) to pass `nil` for `pollSessions`:

```go
func selectSession(sessions []ctr.ProxySession) (*SessionInfo, error) {
	s := newSessionScreen(sessions, nil, nil)
```

**Step 3: Update `newMonitorScreen` signature**

In `cmd/monitor_ui.go`, update the constructor:

```go
func newMonitorScreen(session *SessionInfo, client *ControlClient, onBack func() tui.Screen) *monitorScreen {
	return &monitorScreen{
		session: session,
		client:  client,
		onBack:  onBack,
	}
}
```

**Step 4: Update `makeTestSetup` in tests**

In `cmd/monitor_ui_test.go`, update `makeTestSetup`:

```go
func makeTestSetup(n int) (*monitorScreen, *tui.Window) {
	s := newMonitorScreen(&SessionInfo{
		SessionID:  "test123456",
		ProjectDir: "/home/user/project",
	}, nil, nil)
```

Update `TestMonitorScreen_TickPollingIsAsync`:

```go
	s := newMonitorScreen(&SessionInfo{
		SessionID:  "test123456",
		ProjectDir: "/home/user/project",
	}, client, nil)
```

Update `TestMonitorScreen_AllowCmd_SourceRouting` `makeScreen`:

```go
		screen := newMonitorScreen(&SessionInfo{
			SessionID:  "test123456",
			ProjectDir: projectDir,
		}, client, nil)
```

**Step 5: Handle the "no sessions at startup" case**

In `cmd/session.go`, update `discoverSessionOrAll` to return empty list instead of error when no sessions found (so monitor can show empty selector):

Actually, looking again, `discoverSessionOrAll` returns an error when no sessions found. The `MonitorCommand` already handles this in the new code by falling through to the empty selector when `filter == ""`. No change needed to `session.go`.

**Step 6: Run all tests**

Run: `go test ./cmd/ -v`
Expected: All PASS

**Step 7: Commit**

```
git add cmd/monitor.go cmd/monitor_ui.go cmd/monitor_ui_test.go cmd/session_ui.go
git commit -m "Wire session detector: onBack, onSelect, pollSessions callbacks"
```

---

### Task 6: Update session selector header on reconnection

**Files:**
- Modify: `cmd/session_ui.go:61-109` (Update — sessionSelectResultMsg handler)

The existing `sessionSelectResultMsg` handler already sets the header via `w.SetHeader()`. When transitioning back to the session selector, we should reset the header to the generic "session selector" label.

**Step 1: Write a test**

Add to `cmd/session_ui_test.go`:

```go
func TestSessionScreen_HeaderUpdatesOnTransition(t *testing.T) {
	sessions := makeSessions(3)
	stub := &testStubScreen{}
	s := newSessionScreen(sessions, func(info *SessionInfo) (tui.Screen, tea.Cmd) {
		return stub, nil
	}, nil)
	header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "session selector"}
	w := tui.NewWindow(header, s)
	w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

	// Select session 1.
	s.Pos = 1
	_, cmd := s.Update(tea.KeyMsg{Type: tea.KeyEnter}, w)
	require.NotNil(t, cmd)
	screen, _ := s.Update(cmd(), w)
	assert.Equal(t, stub, screen)
	// Header should now show the selected session info — verified by existing tests.
}
```

Actually, the header transition is already handled by the existing `sessionSelectResultMsg` handler. The reverse direction (monitor → selector) needs the header reset. This happens in `onBack` — the `Window` receives a new screen and we need to update the header.

Let me add header reset to the `onBack` flow. The simplest approach: when `monitorScreen` transitions via `onBack` or disconnect, it should also reset the header. But `monitorScreen` doesn't have access to the `Window` to set headers — wait, it does: the `Update` method receives `w *tui.Window`.

**Step 1 (revised): Write the test**

Add to `cmd/monitor_ui_test.go`:

```go
func TestMonitorScreen_EscResetsHeader(t *testing.T) {
	s, w := makeTestSetup(5)
	stub := &testStubScreen{}
	s.onBack = func() tui.Screen { return stub }

	// Header currently shows session info.
	s.Update(tea.KeyMsg{Type: tea.KeyEscape}, w)

	// After Esc, header should be reset to selector mode.
	view := w.View()
	assert.Contains(t, view, "session selector")
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run TestMonitorScreen_EscResetsHeader -v`
Expected: FAIL — header still shows old session info

**Step 3: Add header reset on Esc and disconnect**

In `cmd/monitor_ui.go`, in the `"esc"` handler:

```go
		case "esc":
			if s.onBack != nil {
				s.client.Close()
				w.SetHeader(&tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "session selector"})
				return s.onBack(), nil
			}
```

And in the disconnect transition (inside the `tui.TickMsg` handler where `s.disconnectTick >= 12`):

```go
			if s.disconnectTick >= 12 {
				s.client.Close()
				w.SetHeader(&tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "session selector"})
				return s.onBack(), nil
			}
```

**Step 4: Run test**

Run: `go test ./cmd/ -run TestMonitorScreen_EscResetsHeader -v`
Expected: PASS

**Step 5: Run all tests**

Run: `go test ./cmd/ -v`
Expected: All PASS

**Step 6: Commit**

```
git add cmd/monitor_ui.go cmd/monitor_ui_test.go
git commit -m "Reset header to session selector on Esc and disconnect"
```

---

### Task 7: Final verification

**Step 1: Run full test suite**

Run: `make test`
Expected: All PASS

**Step 2: Run integration tests if available**

Run: `make test-integration`
Expected: PASS (or expected failures for container-dependent tests in sandbox)

**Step 3: Verify build**

Run: `go build .`
Expected: No errors

**Step 4: Commit if any remaining changes**

Only commit if there are uncommitted fixes from the verification step.
