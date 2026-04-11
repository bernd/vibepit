# SSH Session Selector Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Only show the SSH session selector when detached sessions exist, and only list detached sessions in it.

**Architecture:** Filter `mgr.List()` in `handlePTYSession` to detached sessions only. If none exist, auto-create. If any exist, show a simplified selector that only contains detached sessions. Remove the take-over confirmation and exited-session handling from the selector model since it will never see those states.

**Tech Stack:** Go, Bubble Tea v2, testify

---

### Task 1: Update selector tests for new behavior

**Files:**
- Modify: `sshd/selector_test.go`

- [ ] **Step 1: Replace `testSessions` helper with detached-only fixture**

The selector will only ever receive detached sessions. Replace the mixed-status
fixture and remove tests for exited and attached session handling.

```go
func testSessions() []session.SessionInfo {
	now := time.Now()
	return []session.SessionInfo{
		{ID: "session-1", Command: "/bin/bash", Status: "detached", ClientCount: 0, CreatedAt: now.Add(-10 * time.Minute)},
		{ID: "session-2", Command: "/bin/bash", Status: "detached", ClientCount: 0, CreatedAt: now.Add(-5 * time.Minute)},
	}
}
```

- [ ] **Step 2: Remove tests for exited and attached session behavior**

Delete these test functions entirely:
- `TestSelectorExitedSessionNotSelectable`
- `TestSelectorAttachedSessionPromptsTakeOver`
- `TestSelectorTakeOverDeclined`

- [ ] **Step 3: Update `TestSelectorNavigateAndSelectDetached`**

Session-2 is now at index 1 (second of two detached sessions). The test still
navigates down once and selects — but the result no longer has a `takeOver`
field.

```go
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
```

- [ ] **Step 4: Update `TestSelectorNewSessionOption` for new item count**

With 2 sessions, the "new session" option is at index 2.

```go
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
```

- [ ] **Step 5: Update `TestSelectorCursorBounds`**

Max cursor is now 2 (2 sessions + 1 new session option).

```go
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
```

- [ ] **Step 6: Update `TestSelectorViewContainsSessionInfo`**

Remove assertions for attached/exited/not-selectable content. The view only
shows detached sessions now.

```go
func TestSelectorViewContainsSessionInfo(t *testing.T) {
	m := newSelectorModel(testSessions())
	view := m.View().Content
	assert.Contains(t, view, "session-1")
	assert.Contains(t, view, "session-2")
	assert.Contains(t, view, "[new session]")
	assert.Contains(t, view, "detached")
	assert.NotContains(t, view, "not selectable")
}
```

- [ ] **Step 7: Update `TestFormatStatus`**

Remove the "attached", "exited", and "unknown" test cases. Only "detached"
remains.

```go
func TestFormatStatus(t *testing.T) {
	now := time.Now()
	info := session.SessionInfo{Status: "detached", CreatedAt: now.Add(-5 * time.Minute)}
	result := formatStatus(info)
	assert.Contains(t, result, "detached")
}
```

- [ ] **Step 8: Run tests to verify they fail**

Run: `cd /home/bernd/Code/vibepit && go test ./sshd/ -run 'TestSelector|TestFormatStatus' -v`
Expected: FAIL — tests reference `result.takeOver` which still exists, and
removed tests no longer compile. This confirms tests are ahead of
implementation.

### Task 2: Simplify selector model

**Files:**
- Modify: `sshd/selector.go`

- [ ] **Step 1: Remove `takeOver` from `selectorResult` and `confirmTakeOver` from `selectorModel`**

```go
// selectorResult holds the outcome of the session selector.
type selectorResult struct {
	sessionID string // empty means "new session"
}

// selectorModel is a Bubble Tea model for choosing a session to attach to.
type selectorModel struct {
	sessions []session.SessionInfo
	cursor   int
	result   *selectorResult
}
```

- [ ] **Step 2: Remove `handleTakeOverPrompt` method and simplify `Update`**

```go
func (m selectorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.handleNormalKey(msg)
	}
	return m, nil
}
```

Delete the `handleTakeOverPrompt` method entirely.

- [ ] **Step 3: Simplify `handleNormalKey` enter handler**

Remove the exited-session guard and attached-session take-over prompt. Every
session in the list is detached and directly selectable.

```go
	case "enter":
		// "New session" is the last item.
		if m.cursor == len(m.sessions) {
			m.result = &selectorResult{}
			return m, tea.Quit
		}
		m.result = &selectorResult{sessionID: m.sessions[m.cursor].ID}
		return m, tea.Quit
```

- [ ] **Step 4: Simplify `View` — remove exited label and take-over prompt**

```go
func (m selectorModel) View() tea.View {
	var b strings.Builder

	b.WriteString("Sessions:\n\n")

	for i, info := range m.sessions {
		cursor := "  "
		if i == m.cursor {
			cursor = "> "
		}
		status := formatStatus(info)
		line := fmt.Sprintf("%s%-14s %-12s %s", cursor, info.ID, info.Command, status)
		b.WriteString(line)
		b.WriteString("\n")
	}

	// "New session" option.
	cursor := "  "
	if m.cursor == len(m.sessions) {
		cursor = "> "
	}
	fmt.Fprintf(&b, "%s[new session]\n", cursor)

	b.WriteString("\nj/k or arrows to move, enter to select, n for new, q to quit")

	return tea.NewView(b.String())
}
```

- [ ] **Step 5: Simplify `formatStatus` to detached-only**

```go
func formatStatus(info session.SessionInfo) string {
	ago := time.Since(info.CreatedAt).Truncate(time.Second)
	return fmt.Sprintf("detached %s ago", ago)
}
```

- [ ] **Step 6: Remove unused `"time"` import if `formatStatus` no longer needs `time.Since` — actually it still does, so just verify the import list**

The `"time"` import is still needed for `time.Since`. Remove only the `"fmt"`
import if it's no longer needed — but it is (used in `View`). Verify the imports
are: `"fmt"`, `"strings"`, `"time"`, `tea`, `session`.

- [ ] **Step 7: Run tests to verify they pass**

Run: `cd /home/bernd/Code/vibepit && go test ./sshd/ -run 'TestSelector|TestFormatStatus' -v`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add sshd/selector.go sshd/selector_test.go
git commit -m "Simplify SSH session selector to only handle detached sessions"
```

### Task 3: Filter sessions in `handlePTYSession` and remove take-over logic

**Files:**
- Modify: `sshd/server.go`

- [ ] **Step 1: Filter `mgr.List()` to detached sessions and change the decision logic**

Replace lines 90–142 of `handlePTYSession`:

```go
	allSessions := mgr.List()

	// Only detached sessions are relevant for the selector.
	var detached []session.SessionInfo
	for _, info := range allSessions {
		if info.Status == "detached" {
			detached = append(detached, info)
		}
	}

	var target *session.Session

	if len(detached) == 0 {
		// No detached sessions — create one directly.
		var err error
		target, err = mgr.Create(cols, rows, sshEnv)
		if err != nil {
			fmt.Fprintf(sess.Stderr(), "create session: %s\n", err) //nolint:errcheck
			sess.Exit(1)                                            //nolint:errcheck
			return
		}
	} else {
		// Show selector with detached sessions only.
		model := newSelectorModel(detached)
		p := tea.NewProgram(model,
			tea.WithInput(sess),
			tea.WithOutput(sess),
			tea.WithWindowSize(int(cols), int(rows)),
			tea.WithoutSignalHandler(),
		)
		finalModel, err := p.Run()
		if err != nil {
			fmt.Fprintf(sess.Stderr(), "selector: %s\n", err) //nolint:errcheck
			sess.Exit(1)                                      //nolint:errcheck
			return
		}
		result := finalModel.(selectorModel).result
		if result == nil {
			// User quit without selecting.
			sess.Exit(0) //nolint:errcheck
			return
		}
		if result.sessionID == "" {
			// New session.
			var err error
			target, err = mgr.Create(cols, rows, sshEnv)
			if err != nil {
				fmt.Fprintf(sess.Stderr(), "create session: %s\n", err) //nolint:errcheck
				sess.Exit(1)                                            //nolint:errcheck
				return
			}
		} else {
			target = mgr.Get(result.sessionID)
			if target == nil {
				fmt.Fprintf(sess.Stderr(), "session %s not found\n", result.sessionID) //nolint:errcheck
				sess.Exit(1)                                                           //nolint:errcheck
				return
			}
		}
	}
```

- [ ] **Step 2: Remove `takeOver` variable and `TakeOver` call**

Delete the `var takeOver bool` declaration (was alongside `var target`) and the
take-over block after `Attach`:

```go
	// Remove these lines:
	// takeOver = result.takeOver
	// ...
	// if takeOver {
	//     target.TakeOver(client, cols, rows)
	// }
```

The code after the session selection block becomes simply:

```go
	client := target.Attach(cols, rows)
	defer client.Close() //nolint:errcheck

	// Forward window resize (writer only).
	go func() {
```

- [ ] **Step 3: Run all sshd tests**

Run: `cd /home/bernd/Code/vibepit && go test ./sshd/ -v`
Expected: PASS

- [ ] **Step 4: Run full test suite**

Run: `cd /home/bernd/Code/vibepit && make test`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add sshd/server.go
git commit -m "Filter SSH session selector to detached sessions only

Skip the selector entirely when no detached sessions exist,
auto-creating a new session instead. This fixes the UX issue where
exited or attached sessions would force an unnecessary selector
interaction."
```
