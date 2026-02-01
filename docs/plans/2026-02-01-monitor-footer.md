# Monitor Footer Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a footer bar to the monitor TUI with status indicators on the left and context-sensitive keybindings on the right, replacing the existing inline status/scroll hint lines.

**Architecture:** A `renderFooter` method on `monitorModel` builds a single-line footer with left-aligned status (error/flash/new messages) and right-aligned keybindings that change based on the selected entry's type. A `newCount` field tracks unseen messages when the cursor is not following the tail. The existing status, flash, and scroll hint rendering in `View` is removed and consolidated into the footer.

**Tech Stack:** `github.com/charmbracelet/lipgloss`, `github.com/charmbracelet/x/ansi` (for `StringWidth` to calculate padding).

---

### Task 1: Add `newCount` field and tracking logic

**Files:**
- Modify: `cmd/monitor_ui.go`
- Modify: `cmd/monitor_ui_test.go`

**Step 1: Write test for newCount tracking**

Add to `cmd/monitor_ui_test.go`:

```go
func TestMonitorModel_NewCount(t *testing.T) {
	t.Run("increments when cursor not at end", func(t *testing.T) {
		m := makeModelWithItems(5)
		m.cursor = 2 // not at end (4)

		// Simulate what tickMsg does: append new items while not at end
		m.items = append(m.items, logItem{
			entry: proxy.LogEntry{ID: 100, Domain: "new.com", Action: proxy.ActionAllow, Source: proxy.SourceProxy},
		})
		m.newCount += 1 // this is what tick handler will do

		assert.Equal(t, 1, m.newCount)
		assert.Equal(t, 2, m.cursor) // cursor didn't move
	})

	t.Run("resets when cursor reaches end", func(t *testing.T) {
		m := makeModelWithItems(5)
		m.cursor = 3
		m.newCount = 10

		// Move to end
		m.cursor = len(m.items) - 1
		m.newCount = 0 // this is what navigation will do

		assert.Equal(t, 0, m.newCount)
	})
}
```

**Step 2: Run test to verify it passes**

These are just direct field manipulation tests, so they pass immediately:

```bash
go test ./cmd/ -run TestMonitorModel_NewCount -v
```

Expected: PASS.

**Step 3: Add `newCount` field to model**

In `cmd/monitor_ui.go`, add to the `monitorModel` struct (after `flashExp`):

```go
newCount int // unseen log entries when cursor is not following tail
```

**Step 4: Update tick handler to track newCount**

In the `tickMsg` handler, after the `for _, e := range entries` loop and before the `if wasAtEnd` block, add:

```go
if !wasAtEnd && len(entries) > 0 {
    m.newCount += len(entries)
}
```

And inside the `if wasAtEnd` block, add:

```go
m.newCount = 0
```

The full tick handler becomes:

```go
case tickMsg:
    if m.flash != "" && time.Now().After(m.flashExp) {
        m.flash = ""
    }
    entries, err := m.client.LogsAfter(m.pollCursor)
    if err != nil {
        m.err = err
    } else {
        m.err = nil
        wasAtEnd := len(m.items) == 0 || m.cursor == len(m.items)-1
        for _, e := range entries {
            m.items = append(m.items, logItem{entry: e})
            m.pollCursor = e.ID
        }
        if !wasAtEnd && len(entries) > 0 {
            m.newCount += len(entries)
        }
        if wasAtEnd && len(m.items) > 0 {
            m.cursor = len(m.items) - 1
            m.newCount = 0
            m.ensureCursorVisible()
        }
    }
    cmds = append(cmds, doTick())
```

**Step 5: Reset newCount in navigation handlers**

In the `tea.KeyMsg` switch, update the cases that can reach the last item:

For `"j", "down"` — after `m.cursor++`, add:
```go
if m.cursor == len(m.items)-1 {
    m.newCount = 0
}
```

For `"G", "end"` — after `m.cursor = len(m.items) - 1`, add:
```go
m.newCount = 0
```

For `"pgdown"` — after the cursor clamping, add:
```go
if m.cursor == len(m.items)-1 {
    m.newCount = 0
}
```

**Step 6: Run all tests**

```bash
go vet ./cmd/...
go test ./cmd/ -v
```

Expected: all pass.

---

### Task 2: Implement `renderFooter` method

**Files:**
- Modify: `cmd/monitor_ui.go`
- Modify: `cmd/monitor_ui_test.go`

**Step 1: Write tests for renderFooter**

Add to `cmd/monitor_ui_test.go`:

```go
func TestRenderFooter(t *testing.T) {
	t.Run("shows base keybindings", func(t *testing.T) {
		m := makeModelWithItems(5)
		footer := m.renderFooter(100)
		assert.Contains(t, footer, "navigate")
		assert.Contains(t, footer, "Home")
		assert.Contains(t, footer, "End")
		assert.Contains(t, footer, "quit")
	})

	t.Run("shows allow keys on blocked entry", func(t *testing.T) {
		m := makeModelWithItems(5)
		// makeModelWithItems creates all items as ActionBlock
		m.cursor = 2
		footer := m.renderFooter(100)
		assert.Contains(t, footer, "allow")
		assert.Contains(t, footer, "allow+save")
	})

	t.Run("hides allow keys on allowed entry", func(t *testing.T) {
		m := makeModelWithItems(5)
		m.items[2].entry.Action = proxy.ActionAllow
		m.cursor = 2
		footer := m.renderFooter(100)
		assert.NotContains(t, footer, "allow")
	})

	t.Run("shows save key on temp-allowed entry", func(t *testing.T) {
		m := makeModelWithItems(5)
		m.items[2].status = statusTemp
		m.cursor = 2
		footer := m.renderFooter(100)
		assert.Contains(t, footer, "save")
		assert.NotContains(t, footer, "allow+save")
	})

	t.Run("shows new message count", func(t *testing.T) {
		m := makeModelWithItems(5)
		m.newCount = 3
		footer := m.renderFooter(100)
		assert.Contains(t, footer, "3 new")
	})

	t.Run("shows connection error", func(t *testing.T) {
		m := makeModelWithItems(5)
		m.err = fmt.Errorf("connection refused")
		footer := m.renderFooter(100)
		assert.Contains(t, footer, "connection refused")
	})

	t.Run("shows flash message", func(t *testing.T) {
		m := makeModelWithItems(5)
		m.flash = "already allowed"
		m.flashExp = time.Now().Add(2 * time.Second)
		footer := m.renderFooter(100)
		assert.Contains(t, footer, "already allowed")
	})

	t.Run("error takes priority over flash", func(t *testing.T) {
		m := makeModelWithItems(5)
		m.err = fmt.Errorf("connection refused")
		m.flash = "already allowed"
		m.flashExp = time.Now().Add(2 * time.Second)
		footer := m.renderFooter(100)
		assert.Contains(t, footer, "connection refused")
		assert.NotContains(t, footer, "already allowed")
	})
}
```

Add `"time"` to the test file imports.

**Step 2: Run tests to verify they fail**

```bash
go test ./cmd/ -run TestRenderFooter -v
```

Expected: FAIL — `renderFooter` undefined.

**Step 3: Implement `renderFooter`**

Add to `cmd/monitor_ui.go`. Add `"github.com/charmbracelet/x/ansi"` to the imports.

```go
func (m monitorModel) renderFooter(width int) string {
	keyStyle := lipgloss.NewStyle().Foreground(colorCyan)
	descStyle := lipgloss.NewStyle().Foreground(colorField)

	// Left side: status indicators
	var left string
	if m.err != nil {
		left = lipgloss.NewStyle().Foreground(lipgloss.Color("#ef4444")).
			Render(fmt.Sprintf("connection error: %v", m.err))
	} else if m.flash != "" && time.Now().Before(m.flashExp) {
		left = lipgloss.NewStyle().Foreground(colorOrange).Render(m.flash)
	} else if m.newCount > 0 {
		left = lipgloss.NewStyle().Foreground(colorOrange).
			Render(fmt.Sprintf("↓ %d new", m.newCount))
	}

	// Right side: context-sensitive keybindings
	keys := []string{
		keyStyle.Render("↑/↓") + " " + descStyle.Render("navigate"),
		keyStyle.Render("Home/End") + " " + descStyle.Render("jump"),
		keyStyle.Render("q") + " " + descStyle.Render("quit"),
	}

	if m.cursor >= 0 && m.cursor < len(m.items) {
		item := m.items[m.cursor]
		switch {
		case item.entry.Action == proxy.ActionBlock && item.status == statusNone:
			keys = append(keys,
				keyStyle.Render("a")+" "+descStyle.Render("allow"),
				keyStyle.Render("A")+" "+descStyle.Render("allow+save"),
			)
		case item.status == statusTemp:
			keys = append(keys,
				keyStyle.Render("A")+" "+descStyle.Render("save"),
			)
		}
	}

	right := strings.Join(keys, "  ")

	// Pad between left and right
	leftWidth := ansi.StringWidth(left)
	rightWidth := ansi.StringWidth(right)
	gap := width - leftWidth - rightWidth
	if gap < 2 {
		gap = 2
	}

	return left + strings.Repeat(" ", gap) + right
}
```

**Step 4: Run tests**

```bash
go test ./cmd/ -run TestRenderFooter -v
```

Expected: PASS.

**Step 5: Run all tests**

```bash
go vet ./cmd/...
go test ./cmd/ -v
```

Expected: all pass.

---

### Task 3: Wire footer into View, remove old status/scrollHint

**Files:**
- Modify: `cmd/monitor_ui.go`
- Modify: `cmd/monitor_ui_test.go`

**Step 1: Replace the View method**

Replace the entire `View` method with:

```go
func (m monitorModel) View() string {
	if m.width == 0 {
		return "Starting..."
	}

	header := RenderHeader(m.session, m.width)

	var logLines []string
	end := m.offset + m.vpHeight
	if end > len(m.items) {
		end = len(m.items)
	}
	for i := m.offset; i < end; i++ {
		logLines = append(logLines, renderLogLine(m.items[i], i == m.cursor))
	}
	for len(logLines) < m.vpHeight {
		logLines = append(logLines, "")
	}

	footer := m.renderFooter(m.width)

	return header + "\n" + strings.Join(logLines, "\n") + "\n" + footer
}
```

This removes the `status`, `scrollHint`, and `flash` rendering that was between header and log. The footer now handles all of it.

**Step 2: Update vpHeight comment**

In the `WindowSizeMsg` handler, update the comment:

```go
vpHeight := m.height - headerH - 2 // 1 separator after header + 1 footer line
```

**Step 3: Update TestMonitorModel_FlashOnAlreadyAllowed**

The existing test checks `model.flash` field directly, which still works. No change needed.

**Step 4: Run all tests**

```bash
go vet ./cmd/...
go test ./cmd/ -v
```

Expected: all pass.
