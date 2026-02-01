# Bubbletea Monitor TUI Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Rewrite the monitor command as a bubbletea TUI with a pinned header that re-renders on terminal resize and a scrollable log viewport below it.

**Architecture:** A new `cmd/monitor_ui.go` file defines a bubbletea `Model` with two regions: a styled header (reusing `RenderHeader` from `cmd/logo.go`) pinned to the top, and a `bubbles/viewport` for scrolling log entries. A `tea.Tick` polls the control API every second and appends new log lines to the viewport. `WindowSizeMsg` recalculates the header and viewport dimensions. The existing `cmd/monitor.go` action is replaced with a `tea.NewProgram(...).Run()` call.

**Tech Stack:** `github.com/charmbracelet/bubbletea` (v1, already indirect dep), `github.com/charmbracelet/bubbles/viewport` (already indirect dep), `github.com/charmbracelet/lipgloss` (already indirect dep). Existing: `cmd/logo.go` (RenderHeader), `cmd/control.go` (ControlClient).

---

### Task 1: Define the bubbletea model

**Files:**
- Create: `cmd/monitor_ui.go`

**Step 1: Create the model struct and constructor**

```go
package cmd

import (
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type monitorModel struct {
	session  *SessionInfo
	client   *ControlClient
	viewport viewport.Model
	width    int
	height   int
	cursor   uint64
	logs     []string // all formatted log lines
	err      error
}

func newMonitorModel(session *SessionInfo, client *ControlClient) monitorModel {
	return monitorModel{
		session: session,
		client:  client,
	}
}

func (m monitorModel) headerHeight() int {
	h := RenderHeader(m.session, m.width)
	return ansi.StringHeight(h)
}
```

**Step 2: Run `go vet ./cmd/...`**

Expected: passes.

---

### Task 2: Implement Init and the tick message

**Files:**
- Modify: `cmd/monitor_ui.go`

**Step 1: Write the Init method and tick command**

```go
import "time"

type tickMsg struct{}

func doTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		return tickMsg{}
	})
}

func (m monitorModel) Init() tea.Cmd {
	return tea.Batch(doTick(), tea.WindowSize())
}
```

`Init` starts the polling tick and requests the initial window size.

**Step 2: Run `go vet ./cmd/...`**

Expected: passes.

---

### Task 3: Implement Update

**Files:**
- Modify: `cmd/monitor_ui.go`

**Step 1: Write the Update method**

Handle `WindowSizeMsg` (resize header + viewport), `tickMsg` (poll logs), `KeyMsg` (quit on q/ctrl+c), and pass remaining messages to the viewport.

```go
import (
	"fmt"
	"strings"

	"github.com/bernd/vibepit/proxy"
	"github.com/charmbracelet/lipgloss"
)

func (m monitorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		headerH := m.headerHeight()
		vpHeight := m.height - headerH - 1 // 1 for blank line between header and logs
		if vpHeight < 1 {
			vpHeight = 1
		}
		m.viewport = viewport.New(m.width, vpHeight)
		m.viewport.SetContent(strings.Join(m.logs, "\n"))
		m.viewport.GotoBottom()

	case tickMsg:
		entries, err := m.client.LogsAfter(m.cursor)
		if err != nil {
			m.err = err
		} else {
			m.err = nil
			for _, e := range entries {
				m.logs = append(m.logs, formatLogEntry(e))
				m.cursor = e.ID
			}
			m.viewport.SetContent(strings.Join(m.logs, "\n"))
			m.viewport.GotoBottom()
		}
		cmds = append(cmds, doTick())
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

func formatLogEntry(e proxy.LogEntry) string {
	symbol := lipgloss.NewStyle().Foreground(colorCyan).Render("+")
	if e.Action == proxy.ActionBlock {
		symbol = lipgloss.NewStyle().Foreground(lipgloss.Color("#ef4444")).Render("x")
	}
	host := e.Domain
	if e.Port != "" {
		host = e.Domain + ":" + e.Port
	}
	ts := lipgloss.NewStyle().Foreground(colorField).Render(e.Time.Format("15:04:05"))
	src := lipgloss.NewStyle().Foreground(colorField).Render(fmt.Sprintf("%-5s", e.Source))
	return fmt.Sprintf("[%s] %s %s %s %s", ts, symbol, src, host, e.Reason)
}
```

**Step 2: Run `go vet ./cmd/...`**

Expected: passes.

---

### Task 4: Implement View

**Files:**
- Modify: `cmd/monitor_ui.go`

**Step 1: Write the View method**

Render the header pinned at the top, followed by the viewport.

```go
func (m monitorModel) View() string {
	if m.width == 0 {
		return "Starting..."
	}

	header := RenderHeader(m.session, m.width)

	var status string
	if m.err != nil {
		status = lipgloss.NewStyle().Foreground(lipgloss.Color("#ef4444")).
			Render(fmt.Sprintf("  connection error: %v", m.err))
	}

	return header + status + "\n" + m.viewport.View()
}
```

**Step 2: Run `go vet ./cmd/...`**

Expected: passes.

---

### Task 5: Write tests for the TUI model

**Files:**
- Create: `cmd/monitor_ui_test.go`

**Step 1: Write test for model initialization and tick polling**

Test that the model correctly processes `WindowSizeMsg` and `tickMsg` with a fake client.

```go
package cmd

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMonitorModel_WindowSizeMsg(t *testing.T) {
	m := newMonitorModel(&SessionInfo{
		SessionID:  "test123456",
		ProjectDir: "/home/user/project",
	}, nil)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	model := updated.(monitorModel)

	assert.Equal(t, 100, model.width)
	assert.Equal(t, 40, model.height)
	assert.Greater(t, model.viewport.Height, 0)
}

func TestMonitorModel_ViewContainsHeader(t *testing.T) {
	m := newMonitorModel(&SessionInfo{
		SessionID:  "test123456",
		ProjectDir: "/home/user/project",
	}, nil)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	model := updated.(monitorModel)

	view := model.View()
	assert.Contains(t, view, "I PITY THE VIBES")
}

func TestFormatLogEntry(t *testing.T) {
	entry := proxy.LogEntry{
		Domain: "example.com",
		Port:   "443",
		Action: proxy.ActionAllow,
		Source: proxy.SourceProxy,
	}
	line := formatLogEntry(entry)
	require.Contains(t, line, "example.com:443")
	require.Contains(t, line, "+")
}

func TestFormatLogEntry_Block(t *testing.T) {
	entry := proxy.LogEntry{
		Domain: "evil.com",
		Action: proxy.ActionBlock,
		Source: proxy.SourceDNS,
		Reason: "not allowed",
	}
	line := formatLogEntry(entry)
	require.Contains(t, line, "evil.com")
	require.Contains(t, line, "x")
	require.Contains(t, line, "not allowed")
}
```

**Step 2: Run tests**

```bash
go test ./cmd/ -run "TestMonitorModel|TestFormatLogEntry" -v
```

Expected: PASS.

---

### Task 6: Wire the TUI into the monitor command

**Files:**
- Modify: `cmd/monitor.go`

**Step 1: Replace the polling loop with a bubbletea program**

Replace the entire action body after `client` creation with:

```go
m := newMonitorModel(session, client)
p := tea.NewProgram(m, tea.WithAltScreen())
if _, err := p.Run(); err != nil {
	return fmt.Errorf("monitor UI: %w", err)
}
return nil
```

Remove the unused imports (`"os"`, `"time"`, `"golang.org/x/term"`, `"github.com/bernd/vibepit/proxy"`). Add `tea "github.com/charmbracelet/bubbletea"`.

The full file should look like:

```go
package cmd

import (
	"context"
	"fmt"

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
			session, err := discoverSession(ctx, cmd.String("session"))
			if err != nil {
				return fmt.Errorf("cannot find running proxy: %w", err)
			}
			client, err := NewControlClient(session)
			if err != nil {
				return err
			}

			m := newMonitorModel(session, client)
			p := tea.NewProgram(m, tea.WithAltScreen())
			if _, err := p.Run(); err != nil {
				return fmt.Errorf("monitor UI: %w", err)
			}
			return nil
		},
	}
}
```

**Step 2: Build and verify**

```bash
go vet ./cmd/...
go build ./...
```

Expected: compiles without errors.

**Step 3: Run all tests**

```bash
go test ./cmd/ -v
```

Expected: all pass (existing + new).

---

### Task 7: Auto-scroll behavior — only auto-scroll when at bottom

**Files:**
- Modify: `cmd/monitor_ui.go`

**Step 1: Add auto-scroll logic**

When new log entries arrive, only auto-scroll to the bottom if the user was already at the bottom. This lets users scroll up to read history without being yanked back down.

In the `tickMsg` handler, change the `GotoBottom` call:

```go
// Replace:
m.viewport.GotoBottom()

// With:
wasAtBottom := m.viewport.AtBottom()
m.viewport.SetContent(strings.Join(m.logs, "\n"))
if wasAtBottom {
	m.viewport.GotoBottom()
}
```

Apply the same pattern in the `WindowSizeMsg` handler.

**Step 2: Run tests**

```bash
go test ./cmd/ -v
```

Expected: all pass.

---

### Task 8: Add a scroll indicator in the header area

**Files:**
- Modify: `cmd/monitor_ui.go`

**Step 1: Show a scroll position hint when not at bottom**

In the `View` method, after the error status line and before the viewport, add a subtle indicator when the user has scrolled up:

```go
var scrollHint string
if !m.viewport.AtBottom() && m.viewport.TotalLineCount() > 0 {
	pct := int(m.viewport.ScrollPercent() * 100)
	scrollHint = lipgloss.NewStyle().Foreground(colorField).
		Render(fmt.Sprintf("  ↑ %d%% — press End to jump to latest", pct))
}
```

Include `scrollHint` in the view output between header and viewport.

**Step 2: Add End key binding to jump to bottom**

In the `KeyMsg` handler:

```go
case "end":
	m.viewport.GotoBottom()
```

**Step 3: Run tests**

```bash
go test ./cmd/ -v
```

Expected: all pass.
