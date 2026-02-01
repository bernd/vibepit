# Session Selector Screen Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace the `huh` select widget with a TUI session selector screen using the reusable `tui.Screen`/`tui.Window` pattern, enabling seamless screen transitions for monitor and standalone selection for other commands.

**Architecture:** New `sessionScreen` implements `tui.Screen`, embeds `tui.Cursor` for navigation. An `onSelect` callback determines behavior: `nil` means standalone mode (quit after selection), non-nil returns the next screen (seamless transition for monitor). Container start time is read from the existing `Created` field on `container.Summary` during `ListProxySessions`.

**Tech Stack:** Go, Bubbletea, Lipgloss, Docker API (`container.Summary.Created`)

**Design doc:** `docs/plans/2026-02-01-session-selector-screen-design.md`

---

### Task 1: Add StartedAt to ProxySession

**Files:**
- Modify: `container/client.go:395-434`

**Step 1: Write the failing test**

Create `container/client_test.go` (or add to existing). Since `ListProxySessions` talks to Docker, we test the `ProxySession` struct has the field and the uptime helper. Create a helper function `formatUptime` in a new file `cmd/session_ui.go` for now — but first, just add the field.

Actually, this is a struct field addition with no logic to unit-test independently. Skip directly to implementation.

**Step 1: Add StartedAt field to ProxySession**

In `container/client.go`, add `StartedAt time.Time` to the `ProxySession` struct at line 397, and populate it from `ctr.Created` (Unix timestamp) in the loop at line 418.

```go
// container/client.go:395-401 — updated struct
type ProxySession struct {
	ContainerID string
	SessionID   string
	ControlPort string
	ProjectDir  string
	StartedAt   time.Time
}
```

```go
// container/client.go:426-431 — updated append inside the loop
sessions = append(sessions, ProxySession{
	ContainerID: ctr.ID,
	SessionID:   ctr.Labels[LabelSessionID],
	ControlPort: controlPort,
	ProjectDir:  ctr.Labels[LabelProjectDir],
	StartedAt:   time.Unix(ctr.Created, 0),
})
```

Add `"time"` to the imports at the top of `container/client.go`.

**Step 2: Verify it compiles**

Run: `go build ./container/...`
Expected: success, no errors.

**Step 3: Commit**

```
git add container/client.go
git commit -m "Add StartedAt field to ProxySession from container created time"
```

---

### Task 2: Create sessionScreen with formatUptime helper

**Files:**
- Create: `cmd/session_ui.go`
- Create: `cmd/session_ui_test.go`

**Step 1: Write the failing test for formatUptime**

```go
// cmd/session_ui_test.go
package cmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFormatUptime(t *testing.T) {
	tests := []struct {
		name     string
		started  time.Time
		now      time.Time
		expected string
	}{
		{
			name:     "seconds only",
			started:  time.Date(2026, 1, 1, 12, 0, 30, 0, time.UTC),
			now:      time.Date(2026, 1, 1, 12, 0, 45, 0, time.UTC),
			expected: "< 1m",
		},
		{
			name:     "minutes only",
			started:  time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
			now:      time.Date(2026, 1, 1, 12, 7, 0, 0, time.UTC),
			expected: "7m",
		},
		{
			name:     "hours and minutes",
			started:  time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
			now:      time.Date(2026, 1, 1, 12, 13, 0, 0, time.UTC),
			expected: "2h 13m",
		},
		{
			name:     "days and hours",
			started:  time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
			now:      time.Date(2026, 1, 3, 15, 0, 0, 0, time.UTC),
			expected: "2d 5h",
		},
		{
			name:     "exactly one hour",
			started:  time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
			now:      time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC),
			expected: "1h 0m",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatUptime(tt.started, tt.now)
			assert.Equal(t, tt.expected, result)
		})
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run TestFormatUptime -v`
Expected: compile error — `formatUptime` not defined.

**Step 3: Write formatUptime and the sessionScreen struct**

```go
// cmd/session_ui.go
package cmd

import (
	"fmt"
	"strings"
	"time"

	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// sessionScreen implements tui.Screen for interactive session selection.
type sessionScreen struct {
	tui.Cursor
	sessions []ctr.ProxySession
	selected *SessionInfo
	onSelect func(*SessionInfo) (tui.Screen, tea.Cmd)
}

func newSessionScreen(sessions []ctr.ProxySession, onSelect func(*SessionInfo) (tui.Screen, tea.Cmd)) *sessionScreen {
	s := &sessionScreen{
		sessions: sessions,
		onSelect: onSelect,
	}
	s.ItemCount = len(sessions)
	return s
}

func (s *sessionScreen) Update(msg tea.Msg, w *tui.Window) (tui.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			if s.Pos >= 0 && s.Pos < len(s.sessions) {
				ps := s.sessions[s.Pos]
				s.selected = &SessionInfo{
					ControlPort: ps.ControlPort,
					SessionID:   ps.SessionID,
					ProjectDir:  ps.ProjectDir,
				}
				if s.onSelect != nil {
					return s.onSelect(s.selected)
				}
				return s, tea.Quit
			}
		case "q", "ctrl+c":
			return s, tea.Quit
		default:
			if s.HandleKey(msg) {
				return s, nil
			}
		}
	case tea.WindowSizeMsg:
		s.VpHeight = w.VpHeight()
		s.EnsureVisible()
	}
	return s, nil
}

func (s *sessionScreen) View(w *tui.Window) string {
	now := time.Now()
	var lines []string
	end := s.Offset + s.VpHeight
	if end > len(s.sessions) {
		end = len(s.sessions)
	}
	for i := s.Offset; i < end; i++ {
		lines = append(lines, renderSessionLine(s.sessions[i], i == s.Pos, now))
	}
	for len(lines) < s.VpHeight {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func (s *sessionScreen) FooterKeys(w *tui.Window) []tui.FooterKey {
	keys := []tui.FooterKey{
		{Key: "enter", Desc: "select"},
	}
	keys = append(keys, s.Cursor.FooterKeys()...)
	return keys
}

func (s *sessionScreen) FooterStatus(w *tui.Window) string {
	return lipgloss.NewStyle().Foreground(tui.ColorField).
		Render(fmt.Sprintf("%d sessions", len(s.sessions)))
}

func renderSessionLine(ps ctr.ProxySession, highlighted bool, now time.Time) string {
	base := lipgloss.NewStyle()
	marker := "  "
	if highlighted {
		base = base.Background(tui.ColorHighlight)
		marker = lipgloss.NewStyle().Foreground(tui.ColorCyan).Background(tui.ColorHighlight).Render("➔") + base.Render(" ")
	}

	dir := base.Foreground(tui.ColorCyan).Render(ps.ProjectDir)

	shortID := ps.SessionID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	id := base.Foreground(tui.ColorField).Render(shortID)

	uptime := base.Foreground(tui.ColorOrange).Render(formatUptime(ps.StartedAt, now))

	sp := base.Render(" ")
	return marker + dir + sp + id + sp + uptime
}

func formatUptime(started, now time.Time) string {
	d := now.Sub(started)
	if d < time.Minute {
		return "< 1m"
	}

	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./cmd/ -run TestFormatUptime -v`
Expected: PASS

**Step 5: Commit**

```
git add cmd/session_ui.go cmd/session_ui_test.go
git commit -m "Add sessionScreen with formatUptime helper"
```

---

### Task 3: Test sessionScreen navigation and selection

**Files:**
- Modify: `cmd/session_ui_test.go`

**Step 1: Write the failing tests**

Add to `cmd/session_ui_test.go`:

```go
func makeSessions(n int) []ctr.ProxySession {
	var sessions []ctr.ProxySession
	for i := 0; i < n; i++ {
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

func TestSessionScreen_Navigation(t *testing.T) {
	t.Run("j moves cursor down", func(t *testing.T) {
		s, w := makeSessionTestSetup(5)
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}, w)
		assert.Equal(t, 1, s.Pos)
	})

	t.Run("k moves cursor up from position 1", func(t *testing.T) {
		s, w := makeSessionTestSetup(5)
		s.Pos = 1
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}, w)
		assert.Equal(t, 0, s.Pos)
	})

	t.Run("G jumps to end", func(t *testing.T) {
		s, w := makeSessionTestSetup(5)
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
		// cmd should be tea.Quit
		assert.NotNil(t, cmd)
	})

	t.Run("enter calls onSelect callback when set", func(t *testing.T) {
		sessions := makeSessions(3)
		var called bool
		var receivedSession *SessionInfo
		stub := &tui.StubScreen{}
		onSelect := func(info *SessionInfo) (tui.Screen, tea.Cmd) {
			called = true
			receivedSession = info
			return stub, nil
		}
		s := newSessionScreen(sessions, onSelect)
		header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "selector"}
		w := tui.NewWindow(header, s)
		w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

		s.Pos = 2
		newScreen, _ := s.Update(tea.KeyMsg{Type: tea.KeyEnter}, w)
		require.True(t, called)
		assert.Equal(t, "/home/user/project2", receivedSession.ProjectDir)
		assert.Equal(t, stub, newScreen)
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
	s, w := makeSessionTestSetup(3)
	view := s.View(w)
	assert.Contains(t, view, "/home/user/project0")
	assert.Contains(t, view, "/home/user/project1")
	assert.Contains(t, view, "/home/user/project2")
}
```

Note: The test for the `onSelect` callback uses a `tui.StubScreen`. We need to export the `stubScreen` from `tui/screen_test.go` — but test types aren't exported. Instead, define a minimal stub inline or add a small exported `StubScreen` to the `tui` package for testing. The simplest approach: just define a local `testStubScreen` in the test file.

Replace the `stub := &tui.StubScreen{}` line with an inline stub:

```go
type testStubScreen struct{}
func (s *testStubScreen) Update(msg tea.Msg, w *tui.Window) (tui.Screen, tea.Cmd) { return s, nil }
func (s *testStubScreen) View(w *tui.Window) string           { return "" }
func (s *testStubScreen) FooterKeys(w *tui.Window) []tui.FooterKey { return nil }
func (s *testStubScreen) FooterStatus(w *tui.Window) string    { return "" }
```

And use `stub := &testStubScreen{}`.

Add these imports to the test file:

```go
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
```

**Step 2: Run tests to verify they fail**

Run: `go test ./cmd/ -run TestSessionScreen -v`
Expected: PASS (the implementation was written in Task 2). If any fail, fix.

**Step 3: Run tests to verify they pass**

Run: `go test ./cmd/ -run TestSessionScreen -v`
Expected: all PASS.

**Step 4: Commit**

```
git add cmd/session_ui_test.go
git commit -m "Add tests for sessionScreen navigation and selection"
```

---

### Task 4: Add selectSession helper and wire into discoverSession

**Files:**
- Modify: `cmd/session_ui.go`
- Modify: `cmd/session.go:1-92`

**Step 1: Add selectSession to session_ui.go**

Append to `cmd/session_ui.go`:

```go
// selectSession runs a temporary TUI for the user to pick a session.
// Returns nil if the user quit without selecting.
func selectSession(sessions []ctr.ProxySession) (*SessionInfo, error) {
	s := newSessionScreen(sessions, nil)
	header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "session selector"}
	w := tui.NewWindow(header, s)
	p := tea.NewProgram(w, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return nil, fmt.Errorf("session selector: %w", err)
	}
	if s.selected == nil {
		return nil, fmt.Errorf("no session selected")
	}
	return s.selected, nil
}
```

**Step 2: Update discoverSession in session.go**

Replace lines 71-91 in `cmd/session.go` (the `huh` selection block) with a call to `selectSession`:

```go
	// Multiple sessions — interactive selection.
	return selectSession(sessions)
```

Remove the `"github.com/charmbracelet/huh"` import from `cmd/session.go`. The import block should become:

```go
import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"

	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/proxy"
)
```

**Step 3: Verify it compiles**

Run: `go build ./cmd/...`
Expected: success.

**Step 4: Run all tests**

Run: `go test ./cmd/ -v`
Expected: all PASS.

**Step 5: Commit**

```
git add cmd/session_ui.go cmd/session.go
git commit -m "Wire selectSession into discoverSession, replace huh widget"
```

---

### Task 5: Wire sessionScreen into MonitorCommand for seamless transition

**Files:**
- Modify: `cmd/monitor.go:19-45`

**Step 1: Update MonitorCommand**

Replace the `Action` function body in `cmd/monitor.go` (lines 25-42). The new flow:
1. List sessions.
2. If one session (or filter matches): go straight to monitor screen.
3. If multiple: start with sessionScreen, transition to monitorScreen on select.

```go
func MonitorCommand() *cli.Command {
	return &cli.Command{
		Name:     "monitor",
		Usage:    "Connect to a running proxy for logs and admin",
		Category: "Utilities",
		Flags:    []cli.Flag{sessionFlag},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			session, sessions, err := discoverSessionOrAll(ctx, cmd.String("session"))
			if err != nil {
				return fmt.Errorf("cannot find running proxy: %w", err)
			}

			if session != nil {
				// Single session — go straight to monitor.
				return runMonitor(session)
			}

			// Multiple sessions — start with selector screen.
			onSelect := func(info *SessionInfo) (tui.Screen, tea.Cmd) {
				client, err := NewControlClient(info)
				if err != nil {
					// Can't connect; show error and stay on selector.
					return nil, func() tea.Msg { return sessionErrorMsg{err} }
				}
				return newMonitorScreen(info, client), nil
			}
			s := newSessionScreen(sessions, onSelect)
			header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "session selector"}
			w := tui.NewWindow(header, s)
			p := tea.NewProgram(w, tea.WithAltScreen())
			if _, err := p.Run(); err != nil {
				return fmt.Errorf("monitor UI: %w", err)
			}
			return nil
		},
	}
}

func runMonitor(session *SessionInfo) error {
	client, err := NewControlClient(session)
	if err != nil {
		return err
	}
	screen := newMonitorScreen(session, client)
	header := &tui.HeaderInfo{ProjectDir: session.ProjectDir, SessionID: session.SessionID}
	w := tui.NewWindow(header, screen)
	p := tea.NewProgram(w, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("monitor UI: %w", err)
	}
	return nil
}
```

This requires a new helper `discoverSessionOrAll` that returns either a single session or the full list. Add it to `cmd/session.go`:

```go
// discoverSessionOrAll returns a single SessionInfo if a filter matches or only
// one session exists, or the full list of ProxySessions for interactive selection.
func discoverSessionOrAll(ctx context.Context, filter string) (*SessionInfo, []ctr.ProxySession, error) {
	client, err := ctr.NewClient()
	if err != nil {
		return nil, nil, err
	}
	defer client.Close()

	sessions, err := client.ListProxySessions(ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(sessions) == 0 {
		return nil, nil, fmt.Errorf("no running vibepit sessions found")
	}

	if filter != "" {
		for _, s := range sessions {
			if s.SessionID == filter || s.ProjectDir == filter {
				return &SessionInfo{
					ControlPort: s.ControlPort,
					SessionID:   s.SessionID,
					ProjectDir:  s.ProjectDir,
				}, nil, nil
			}
		}
		return nil, nil, fmt.Errorf("no session matching %q found", filter)
	}

	if len(sessions) == 1 {
		return &SessionInfo{
			ControlPort: sessions[0].ControlPort,
			SessionID:   sessions[0].SessionID,
			ProjectDir:  sessions[0].ProjectDir,
		}, nil, nil
	}

	return nil, sessions, nil
}
```

We also need a `sessionErrorMsg` type for the error case in `onSelect`. Add to `cmd/session_ui.go`:

```go
// sessionErrorMsg is sent when the onSelect callback fails.
type sessionErrorMsg struct{ err error }
```

And handle it in `sessionScreen.Update`:

```go
case sessionErrorMsg:
	w.SetError(msg.err)
```

When `onSelect` returns `nil` screen (error case), the sessionScreen should stay active. Update the enter handling:

```go
case "enter":
	if s.Pos >= 0 && s.Pos < len(s.sessions) {
		ps := s.sessions[s.Pos]
		s.selected = &SessionInfo{
			ControlPort: ps.ControlPort,
			SessionID:   ps.SessionID,
			ProjectDir:  ps.ProjectDir,
		}
		if s.onSelect != nil {
			screen, cmd := s.onSelect(s.selected)
			if screen != nil {
				return screen, cmd
			}
			// onSelect returned nil screen — stay on selector (error will come via msg).
			return s, cmd
		}
		return s, tea.Quit
	}
```

Also update the Window's header when transitioning. Add a `SetHeader` method to `tui.Window`:

```go
// tui/window.go — add method
func (w *Window) SetHeader(info *HeaderInfo) {
	w.header = info
}
```

Then in the `onSelect` callback, before returning the monitor screen, update the header:

Actually, the `onSelect` callback doesn't have access to the Window. The cleaner approach: have the monitor screen set the header in its first Update (on `TickMsg` or `WindowSizeMsg`). But that couples the screen to the window's header.

Simpler: handle the header update in `sessionScreen.Update` after `onSelect` returns a non-nil screen:

```go
if screen != nil {
	w.SetHeader(&tui.HeaderInfo{
		ProjectDir: s.selected.ProjectDir,
		SessionID:  s.selected.SessionID,
	})
	return screen, cmd
}
```

**Step 2: Verify it compiles**

Run: `go build ./...`
Expected: success.

**Step 3: Run all tests**

Run: `go test ./... -v`
Expected: all PASS.

**Step 4: Commit**

```
git add cmd/monitor.go cmd/session.go cmd/session_ui.go tui/window.go
git commit -m "Wire sessionScreen into monitor for seamless screen transition"
```

---

### Task 6: Update header rendering to handle nil/minimal header

**Files:**
- Modify: `tui/header.go:117-156`
- Modify: `tui/header_test.go` (if exists, or create)

**Step 1: Check current behavior with nil HeaderInfo**

The current `RenderHeader` accesses `info.ProjectDir` and `info.SessionID` (line 127-128) without nil checks. When the session selector runs standalone, we pass a minimal `HeaderInfo` with placeholder strings, so this is already safe. But we should render the session info line differently when the selector is active.

Actually, looking at the code again — in standalone mode we pass `HeaderInfo{ProjectDir: "vibepit", SessionID: "session selector"}` which works fine. In monitor mode, we update the header via `SetHeader` before transitioning. No changes needed to header rendering.

**This task is a no-op. Skip it.**

---

### Task 6 (renumbered): Final integration test and cleanup

**Files:**
- Modify: `cmd/session_ui_test.go`

**Step 1: Add test for sessionErrorMsg handling**

```go
func TestSessionScreen_ErrorMsg(t *testing.T) {
	s, w := makeSessionTestSetup(3)
	s.Update(sessionErrorMsg{err: fmt.Errorf("connection refused")}, w)
	require.Error(t, w.Err())
	assert.Contains(t, w.Err().Error(), "connection refused")
}
```

**Step 2: Add test for onSelect returning nil (error path)**

```go
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
```

**Step 3: Run all tests**

Run: `go test ./... -v`
Expected: all PASS.

**Step 4: Verify the full build**

Run: `go build ./...`
Expected: success.

**Step 5: Commit**

```
git add cmd/session_ui_test.go
git commit -m "Add integration tests for session selector error handling"
```

---

## Summary of tasks

| Task | Description | Files |
|------|-------------|-------|
| 1 | Add `StartedAt` to `ProxySession` | `container/client.go` |
| 2 | Create `sessionScreen` + `formatUptime` | `cmd/session_ui.go`, `cmd/session_ui_test.go` |
| 3 | Test navigation and selection | `cmd/session_ui_test.go` |
| 4 | Add `selectSession`, wire into `discoverSession` | `cmd/session_ui.go`, `cmd/session.go` |
| 5 | Wire into `MonitorCommand` with seamless transition | `cmd/monitor.go`, `cmd/session.go`, `cmd/session_ui.go`, `tui/window.go` |
| 6 | Integration tests and cleanup | `cmd/session_ui_test.go` |
