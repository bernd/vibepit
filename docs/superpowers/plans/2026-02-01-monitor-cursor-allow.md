# Monitor Cursor & Allow Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a navigable cursor to the monitor log view with `a`/`A` keybindings to allow blocked domains from within the TUI.

**Architecture:** Replace the `bubbles/viewport` with a custom list renderer that maintains a cursor index over a `[]logItem` slice. Each `logItem` wraps a `proxy.LogEntry` plus an allow status. The `a` key calls `client.Allow()` asynchronously via a `tea.Cmd`; `A` also persists via `config.AppendAllow()`. The cursor line is highlighted with a background color. Scroll offset is managed manually based on cursor position and viewport height.

**Tech Stack:** `github.com/charmbracelet/bubbletea`, `github.com/charmbracelet/lipgloss`, existing `ControlClient.Allow()` and `config.AppendAllow()`.

---

### Task 1: Introduce `logItem` type and replace `[]string` with `[]logItem`

**Files:**
- Modify: `cmd/monitor_ui.go`
- Modify: `cmd/monitor_ui_test.go`

**Step 1: Define `logItem` and `allowStatus`**

Add to `cmd/monitor_ui.go`, replacing the `logs []string` field:

```go
type allowStatus int

const (
	statusNone  allowStatus = iota
	statusTemp              // allowed via 'a' (runtime only)
	statusSaved             // allowed via 'A' (persisted to config)
)

type logItem struct {
	entry  proxy.LogEntry
	status allowStatus
}
```

**Step 2: Update the model struct**

Replace:
```go
cursor   uint64
logs     []string
```

With:
```go
pollCursor uint64 // cursor for API polling (last seen log entry ID)
items      []logItem
cursor     int // index of highlighted line
offset     int // scroll offset (first visible line index)
vpHeight   int // number of visible log lines
```

Remove the `viewport viewport.Model` field entirely — we no longer use bubbles/viewport.

Remove the `"github.com/charmbracelet/bubbles/viewport"` import.

**Step 3: Update `tickMsg` handler**

Replace the current tick handler with:

```go
case tickMsg:
    entries, err := m.client.LogsAfter(m.pollCursor)
    if err != nil {
        m.err = err
    } else {
        m.err = nil
        wasAtEnd := m.cursor >= len(m.items)-1 || len(m.items) == 0
        for _, e := range entries {
            m.items = append(m.items, logItem{entry: e})
            m.pollCursor = e.ID
        }
        if wasAtEnd && len(m.items) > 0 {
            m.cursor = len(m.items) - 1
            m.ensureCursorVisible()
        }
    }
    cmds = append(cmds, doTick())
```

**Step 4: Add `ensureCursorVisible` helper**

```go
func (m *monitorModel) ensureCursorVisible() {
    if m.cursor < m.offset {
        m.offset = m.cursor
    }
    if m.cursor >= m.offset+m.vpHeight {
        m.offset = m.cursor - m.vpHeight + 1
    }
}
```

**Step 5: Update `WindowSizeMsg` handler**

Replace the current handler with:

```go
case tea.WindowSizeMsg:
    m.width = msg.Width
    m.height = msg.Height
    headerH := m.headerHeight()
    m.vpHeight = m.height - headerH - 2
    if m.vpHeight < 1 {
        m.vpHeight = 1
    }
    m.ensureCursorVisible()
```

**Step 6: Remove viewport.Update forwarding**

Remove these lines from the end of `Update`:

```go
var cmd tea.Cmd
m.viewport, cmd = m.viewport.Update(msg)
cmds = append(cmds, cmd)
```

**Step 7: Update `formatLogEntry` to accept `logItem`**

Rename to `renderLogLine` and change signature:

```go
func renderLogLine(item logItem, highlighted bool) string {
    e := item.entry

    var symbol string
    switch {
    case item.status == statusTemp:
        symbol = lipgloss.NewStyle().Foreground(colorOrange).Render("a")
    case item.status == statusSaved:
        symbol = lipgloss.NewStyle().Foreground(colorOrange).Bold(true).Render("A")
    case e.Action == proxy.ActionBlock:
        symbol = lipgloss.NewStyle().Foreground(lipgloss.Color("#ef4444")).Render("x")
    default:
        symbol = lipgloss.NewStyle().Foreground(colorCyan).Render("+")
    }

    host := e.Domain
    if e.Port != "" {
        host = e.Domain + ":" + e.Port
    }
    ts := lipgloss.NewStyle().Foreground(colorField).Render(e.Time.Format("15:04:05"))
    src := lipgloss.NewStyle().Foreground(colorField).Render(fmt.Sprintf("%-5s", string(e.Source)))
    line := fmt.Sprintf("[%s] %s %s %s %s", ts, symbol, src, host, e.Reason)

    if highlighted {
        line = lipgloss.NewStyle().Background(colorField).Render(line)
    }
    return line
}
```

**Step 8: Update `View` to render the custom list**

Replace the viewport rendering in `View`:

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

    var scrollHint string
    if len(m.items) > 0 && m.cursor < len(m.items)-1 {
        pct := 0
        if len(m.items) > 1 {
            pct = m.cursor * 100 / (len(m.items) - 1)
        }
        scrollHint = lipgloss.NewStyle().Foreground(colorField).
            Render(fmt.Sprintf("  ↑ %d%% — press End to jump to latest", pct))
    }

    var logLines []string
    end := m.offset + m.vpHeight
    if end > len(m.items) {
        end = len(m.items)
    }
    for i := m.offset; i < end; i++ {
        logLines = append(logLines, renderLogLine(m.items[i], i == m.cursor))
    }
    // Pad with empty lines if viewport is not full
    for len(logLines) < m.vpHeight {
        logLines = append(logLines, "")
    }

    return header + status + scrollHint + "\n" + strings.Join(logLines, "\n")
}
```

**Step 9: Update tests**

Update `cmd/monitor_ui_test.go`:

- `TestMonitorModel_WindowSizeMsg`: replace `model.viewport.Height` assertion with `model.vpHeight`:
  ```go
  assert.Greater(t, model.vpHeight, 0)
  ```

- `TestFormatLogEntry` and `TestFormatLogEntry_Block`: update to use `renderLogLine`:
  ```go
  func TestRenderLogLine(t *testing.T) {
      item := logItem{
          entry: proxy.LogEntry{
              Domain: "example.com",
              Port:   "443",
              Action: proxy.ActionAllow,
              Source: proxy.SourceProxy,
          },
      }
      line := renderLogLine(item, false)
      require.Contains(t, line, "example.com:443")
      require.Contains(t, line, "+")
  }

  func TestRenderLogLine_Block(t *testing.T) {
      item := logItem{
          entry: proxy.LogEntry{
              Domain: "evil.com",
              Action: proxy.ActionBlock,
              Source: proxy.SourceDNS,
              Reason: "not allowed",
          },
      }
      line := renderLogLine(item, false)
      require.Contains(t, line, "evil.com")
      require.Contains(t, line, "x")
      require.Contains(t, line, "not allowed")
  }

  func TestRenderLogLine_AllowStatuses(t *testing.T) {
      base := proxy.LogEntry{
          Domain: "example.com",
          Action: proxy.ActionBlock,
          Source: proxy.SourceProxy,
      }

      t.Run("temp allowed shows a", func(t *testing.T) {
          line := renderLogLine(logItem{entry: base, status: statusTemp}, false)
          assert.Contains(t, line, "a")
          assert.NotContains(t, line, "x")
      })

      t.Run("saved allowed shows A", func(t *testing.T) {
          line := renderLogLine(logItem{entry: base, status: statusSaved}, false)
          assert.Contains(t, line, "A")
          assert.NotContains(t, line, "x")
      })
  }

  func TestRenderLogLine_Highlighted(t *testing.T) {
      item := logItem{
          entry: proxy.LogEntry{
              Domain: "example.com",
              Action: proxy.ActionAllow,
              Source: proxy.SourceProxy,
          },
      }
      normal := renderLogLine(item, false)
      highlighted := renderLogLine(item, true)
      assert.NotEqual(t, normal, highlighted, "highlighted line should have different styling")
  }
  ```

**Step 10: Run tests**

```bash
go vet ./cmd/...
go test ./cmd/ -v
```

Expected: all pass.

---

### Task 2: Add cursor navigation keybindings

**Files:**
- Modify: `cmd/monitor_ui.go`
- Modify: `cmd/monitor_ui_test.go`

**Step 1: Write tests for cursor movement**

Add to `cmd/monitor_ui_test.go`:

```go
func makeModelWithItems(n int) monitorModel {
    m := newMonitorModel(&SessionInfo{
        SessionID:  "test123456",
        ProjectDir: "/home/user/project",
    }, nil)
    m.vpHeight = 10
    m.width = 100
    m.height = 20
    for i := 0; i < n; i++ {
        m.items = append(m.items, logItem{
            entry: proxy.LogEntry{
                ID:     uint64(i + 1),
                Domain: fmt.Sprintf("domain%d.com", i),
                Action: proxy.ActionBlock,
                Source: proxy.SourceProxy,
            },
        })
    }
    m.cursor = len(m.items) - 1
    return m
}

func TestMonitorModel_CursorNavigation(t *testing.T) {
    t.Run("j moves cursor down", func(t *testing.T) {
        m := makeModelWithItems(20)
        m.cursor = 5
        updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
        model := updated.(monitorModel)
        assert.Equal(t, 6, model.cursor)
    })

    t.Run("k moves cursor up", func(t *testing.T) {
        m := makeModelWithItems(20)
        m.cursor = 5
        updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
        model := updated.(monitorModel)
        assert.Equal(t, 4, model.cursor)
    })

    t.Run("j at end stays at end", func(t *testing.T) {
        m := makeModelWithItems(5)
        m.cursor = 4
        updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
        model := updated.(monitorModel)
        assert.Equal(t, 4, model.cursor)
    })

    t.Run("k at start stays at start", func(t *testing.T) {
        m := makeModelWithItems(5)
        m.cursor = 0
        updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
        model := updated.(monitorModel)
        assert.Equal(t, 0, model.cursor)
    })

    t.Run("G jumps to end", func(t *testing.T) {
        m := makeModelWithItems(20)
        m.cursor = 0
        updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
        model := updated.(monitorModel)
        assert.Equal(t, 19, model.cursor)
    })

    t.Run("g jumps to start", func(t *testing.T) {
        m := makeModelWithItems(20)
        m.cursor = 15
        updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
        model := updated.(monitorModel)
        assert.Equal(t, 0, model.cursor)
    })
}
```

**Step 2: Run tests to verify they fail**

```bash
go test ./cmd/ -run TestMonitorModel_CursorNavigation -v
```

Expected: FAIL — key messages not handled yet.

**Step 3: Add navigation key handling**

In the `tea.KeyMsg` switch in `Update`, replace the existing `"end"` case with:

```go
case "j", "down":
    if m.cursor < len(m.items)-1 {
        m.cursor++
        m.ensureCursorVisible()
    }
case "k", "up":
    if m.cursor > 0 {
        m.cursor--
        m.ensureCursorVisible()
    }
case "G", "end":
    if len(m.items) > 0 {
        m.cursor = len(m.items) - 1
        m.ensureCursorVisible()
    }
case "g", "home":
    m.cursor = 0
    m.ensureCursorVisible()
case "pgdown":
    m.cursor += m.vpHeight
    if m.cursor >= len(m.items) {
        m.cursor = len(m.items) - 1
    }
    if m.cursor < 0 {
        m.cursor = 0
    }
    m.ensureCursorVisible()
case "pgup":
    m.cursor -= m.vpHeight
    if m.cursor < 0 {
        m.cursor = 0
    }
    m.ensureCursorVisible()
```

**Step 4: Run tests**

```bash
go test ./cmd/ -run TestMonitorModel_CursorNavigation -v
```

Expected: PASS.

---

### Task 3: Add allow actions (`a` and `A` keybindings)

**Files:**
- Modify: `cmd/monitor_ui.go`
- Modify: `cmd/monitor_ui_test.go`

**Step 1: Define result messages**

Add to `cmd/monitor_ui.go`:

```go
// allowResultMsg is returned by the async allow command.
type allowResultMsg struct {
    index int         // index in m.items that was allowed
    status allowStatus // statusTemp or statusSaved
    err   error
}
```

**Step 2: Write the async allow command**

```go
func (m monitorModel) allowCmd(index int, save bool) tea.Cmd {
    return func() tea.Msg {
        item := m.items[index]
        _, err := m.client.Allow([]string{item.entry.Domain})
        if err != nil {
            return allowResultMsg{index: index, err: err}
        }

        status := statusTemp
        if save {
            status = statusSaved
            projectPath := config.DefaultProjectPath(m.session.ProjectDir)
            if err := config.AppendAllow(projectPath, []string{item.entry.Domain}); err != nil {
                return allowResultMsg{index: index, err: err}
            }
        }
        return allowResultMsg{index: index, status: status}
    }
}
```

Add import for `"github.com/bernd/vibepit/config"`.

**Step 3: Add key handlers for `a` and `A`**

In the `tea.KeyMsg` switch:

```go
case "a":
    if m.cursor >= 0 && m.cursor < len(m.items) {
        item := m.items[m.cursor]
        if item.entry.Action == proxy.ActionBlock && item.status == statusNone {
            return m, m.allowCmd(m.cursor, false)
        }
    }
case "A":
    if m.cursor >= 0 && m.cursor < len(m.items) {
        item := m.items[m.cursor]
        if item.entry.Action == proxy.ActionBlock && item.status == statusNone {
            return m, m.allowCmd(m.cursor, true)
        }
    }
```

**Step 4: Handle the result message**

Add a new case in the `Update` switch:

```go
case allowResultMsg:
    if msg.err != nil {
        m.err = msg.err
    } else if msg.index >= 0 && msg.index < len(m.items) {
        m.items[msg.index].status = msg.status
    }
```

**Step 5: Write tests for allow status rendering**

Add to `cmd/monitor_ui_test.go`:

```go
func TestMonitorModel_AllowAction(t *testing.T) {
    t.Run("allowResultMsg updates item status", func(t *testing.T) {
        m := makeModelWithItems(5)
        m.cursor = 2

        updated, _ := m.Update(allowResultMsg{index: 2, status: statusTemp})
        model := updated.(monitorModel)

        assert.Equal(t, statusTemp, model.items[2].status)
    })

    t.Run("allowResultMsg with error sets model error", func(t *testing.T) {
        m := makeModelWithItems(5)

        updated, _ := m.Update(allowResultMsg{index: 0, err: fmt.Errorf("connection failed")})
        model := updated.(monitorModel)

        assert.Error(t, model.err)
        assert.Contains(t, model.err.Error(), "connection failed")
    })

    t.Run("allowResultMsg saved status", func(t *testing.T) {
        m := makeModelWithItems(5)

        updated, _ := m.Update(allowResultMsg{index: 3, status: statusSaved})
        model := updated.(monitorModel)

        assert.Equal(t, statusSaved, model.items[3].status)
    })
}
```

**Step 6: Run all tests**

```bash
go vet ./cmd/...
go test ./cmd/ -v
```

Expected: all pass.

---

### Task 4: Add flash message for already-allowed entries

**Files:**
- Modify: `cmd/monitor_ui.go`
- Modify: `cmd/monitor_ui_test.go`

**Step 1: Add flash message field to model**

```go
type monitorModel struct {
    // ... existing fields ...
    flash    string
    flashExp time.Time
}
```

**Step 2: Update `a`/`A` handlers to show flash on no-op**

Change the `a` and `A` key handlers. After the `if item.entry.Action == proxy.ActionBlock && item.status == statusNone` block, add an else:

```go
case "a":
    if m.cursor >= 0 && m.cursor < len(m.items) {
        item := m.items[m.cursor]
        if item.entry.Action == proxy.ActionBlock && item.status == statusNone {
            return m, m.allowCmd(m.cursor, false)
        }
        m.flash = "already allowed"
        m.flashExp = time.Now().Add(2 * time.Second)
    }
case "A":
    if m.cursor >= 0 && m.cursor < len(m.items) {
        item := m.items[m.cursor]
        if item.entry.Action == proxy.ActionBlock && item.status == statusNone {
            return m, m.allowCmd(m.cursor, true)
        }
        m.flash = "already allowed"
        m.flashExp = time.Now().Add(2 * time.Second)
    }
```

**Step 3: Show flash in View**

In the `View` method, after the error status line, add:

```go
if m.flash != "" && time.Now().Before(m.flashExp) {
    status = lipgloss.NewStyle().Foreground(colorOrange).
        Render("  " + m.flash)
}
```

The flash naturally disappears on the next tick re-render (after 2 seconds) because the time check fails. Clear it explicitly in the `tickMsg` handler:

```go
// At the start of the tickMsg handler:
if m.flash != "" && time.Now().After(m.flashExp) {
    m.flash = ""
}
```

**Step 4: Run all tests**

```bash
go vet ./cmd/...
go test ./cmd/ -v
```

Expected: all pass.
