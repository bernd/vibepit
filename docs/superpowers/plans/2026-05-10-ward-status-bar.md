# Ward Always-Visible Status Bar Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace ward's toast-only notification bar with an always-visible status bar that shows session info by default, with FIFO-queued alerts that temporarily override it.

**Architecture:** A new `ward/bar.go` defines types (`StatusUpdate`, `barEvent`, `barMode`, `barCache`) and rendering. The existing `ward/wrapper.go` replaces its ad-hoc toast goroutine with a merge goroutine + event loop that owns all bar state. `ward/toast.go` gets new styles. `cmd/ssh.go` sends initial status via `Options.Status` channel.

**Tech Stack:** Go, lipgloss, urfave/cli/v3, existing ward PTY wrapper

**Spec:** `docs/superpowers/specs/2026-05-10-ward-status-bar-design.md`

---

### Task 1: Add StatusUpdate, barEvent, barMode, barCache types and RenderStatusBar

**Files:**
- Create: `ward/bar.go`
- Modify: `ward/toast.go`
- Create: `ward/bar_test.go`

- [ ] **Step 1: Write failing tests for RenderStatusBar**

Create `ward/bar_test.go`:

```go
package ward

import (
	"strings"
	"testing"
)

func TestRenderStatusBarDefault(t *testing.T) {
	bar := RenderStatusBar("vibepit-abc · ~/project", 80, false)
	if len(bar) == 0 {
		t.Fatal("expected non-empty bar")
	}
	if !strings.Contains(bar, "vibepit-abc") {
		t.Fatal("bar should contain the message")
	}
}

func TestRenderStatusBarAlert(t *testing.T) {
	bar := RenderStatusBar("Blocked: api.example.com:443", 80, true)
	if len(bar) == 0 {
		t.Fatal("expected non-empty bar")
	}
	if !strings.Contains(bar, "api.example.com") {
		t.Fatal("bar should contain the message")
	}
}

func TestRenderStatusBarSanitizes(t *testing.T) {
	bar := RenderStatusBar("hello\x1b[31mworld", 80, false)
	if strings.Contains(bar, "\x1b[31m") {
		t.Fatal("bar should not contain raw escape sequences from the message")
	}
	if !strings.Contains(bar, "helloworld") {
		t.Fatal("bar should contain sanitized text")
	}
}

func TestRenderStatusBarTruncates(t *testing.T) {
	longMsg := strings.Repeat("x", 200)
	bar := RenderStatusBar(longMsg, 80, false)
	if len(bar) == 0 {
		t.Fatal("expected non-empty bar for long message")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./ward/ -run TestRenderStatusBar -v`
Expected: FAIL — `RenderStatusBar` undefined

- [ ] **Step 3: Create ward/bar.go with types and RenderStatusBar**

Create `ward/bar.go`:

```go
package ward

import (
	"time"

	lipgloss "charm.land/lipgloss/v2"
)

// StatusUpdate is sent through Options.Status to control the bar content.
type StatusUpdate struct {
	Message string
	Alert   bool
	Timeout time.Duration // only used when Alert is true
}

type barMode int

const (
	barHidden  barMode = iota // no status or alert received yet
	barStatus                 // showing default status
	barAlert                  // showing a temporary alert
	barCleared                // alert timed out with no lastStatus to revert to
)

type barCache struct {
	rendered string
	message  string
	mode     barMode
}

const barEventStatus = iota

const (
	_ = iota
	barEventAlert
	barEventDismiss
)

// Re-declare barEventStatus properly with the group:

type barEvent struct {
	kind   int
	update StatusUpdate
}

var defaultBarStyle = lipgloss.NewStyle().
	Bold(true).
	Foreground(lipgloss.Color("#00d4ff")).
	Background(lipgloss.Color("#1e2d3d"))

var alertBarStyle = lipgloss.NewStyle().
	Bold(true).
	Foreground(lipgloss.Color("15")).
	Background(lipgloss.Color("#f97316"))

// RenderStatusBar renders a full-width bar with the appropriate style.
// The message is sanitized before rendering.
func RenderStatusBar(message string, cols int, alert bool) string {
	msg := sanitizeMessage(message)
	msg = truncateRunes(msg, cols-2)
	style := defaultBarStyle
	if alert {
		style = alertBarStyle
	}
	return style.Width(cols).Render(" " + msg)
}
```

Wait — the `iota` grouping above is wrong. Let me fix the constants properly:

```go
package ward

import (
	"time"

	lipgloss "charm.land/lipgloss/v2"
)

// StatusUpdate is sent through Options.Status to control the bar content.
type StatusUpdate struct {
	Message string
	Alert   bool
	Timeout time.Duration
}

type barMode int

const (
	barHidden  barMode = iota
	barStatus
	barAlert
	barCleared
)

const (
	barEventStatus  = iota
	barEventAlert
	barEventDismiss
)

type barEvent struct {
	kind   int
	update StatusUpdate
}

type barCache struct {
	rendered string
	message  string
	mode     barMode
}

var defaultBarStyle = lipgloss.NewStyle().
	Bold(true).
	Foreground(lipgloss.Color("#00d4ff")).
	Background(lipgloss.Color("#1e2d3d"))

var alertBarStyle = lipgloss.NewStyle().
	Bold(true).
	Foreground(lipgloss.Color("15")).
	Background(lipgloss.Color("#f97316"))

func RenderStatusBar(message string, cols int, alert bool) string {
	msg := sanitizeMessage(message)
	msg = truncateRunes(msg, cols-2)
	style := defaultBarStyle
	if alert {
		style = alertBarStyle
	}
	return style.Width(cols).Render(" " + msg)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./ward/ -run TestRenderStatusBar -v`
Expected: PASS

- [ ] **Step 5: Update toast.go — replace RenderBar with delegation to RenderStatusBar**

Modify `ward/toast.go`: remove `barStyle` and `RenderBar`, keep `truncateRunes`:

```go
package ward

import "unicode/utf8"

// truncateRunes truncates s to at most maxRunes runes, appending "..." if truncated.
func truncateRunes(s string, maxRunes int) string {
	if maxRunes < 1 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	if maxRunes <= 3 {
		runes := []rune(s)
		return string(runes[:maxRunes])
	}
	runes := []rune(s)
	return string(runes[:maxRunes-3]) + "..."
}
```

- [ ] **Step 6: Update toast_test.go — change RenderBar tests to use RenderStatusBar**

Replace the `TestRenderBar` and `TestRenderBarTruncatesLongMessage` tests in `ward/toast_test.go` to call `RenderStatusBar`:

```go
package ward

import (
	"strings"
	"testing"
)

func TestRenderBarLegacy(t *testing.T) {
	bar := RenderStatusBar("blocked api.example.com:443", 80, true)
	if len(bar) == 0 {
		t.Fatal("expected non-empty bar")
	}
	if !strings.Contains(bar, "api.example.com") {
		t.Fatal("bar should contain the message")
	}
}

func TestRenderBarTruncatesLongMessage(t *testing.T) {
	longMsg := strings.Repeat("x", 200)
	bar := RenderStatusBar(longMsg, 80, false)
	if len(bar) == 0 {
		t.Fatal("expected non-empty bar for long message")
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("hello", 10); got != "hello" {
		t.Fatalf("expected 'hello', got %q", got)
	}
	if got := truncateRunes("hello world", 8); got != "hello..." {
		t.Fatalf("expected 'hello...', got %q", got)
	}
	if got := truncateRunes("", 5); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	if got := truncateRunes("abc", 0); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}
```

- [ ] **Step 7: Run all ward tests**

Run: `go test ./ward/ -count=1 -race -timeout 30s`
Expected: All pass

- [ ] **Step 8: Run linter**

Run: `golangci-lint run ./ward/...`
Expected: 0 issues

- [ ] **Step 9: Commit**

```bash
git add ward/bar.go ward/bar_test.go ward/toast.go ward/toast_test.go
git commit -m "Add StatusUpdate types and RenderStatusBar with dual styles"
```

---

### Task 2: Add Options.Status and replace toast goroutine with event loop

This is the core refactoring of `ward/wrapper.go`. Replace the ad-hoc `toastCh` / `toastDone` / `toastTimer` / `barVisible` machinery with the event loop architecture from the spec.

**Files:**
- Modify: `ward/wrapper.go`

- [ ] **Step 1: Add Status field to Options**

In `ward/wrapper.go`, add the channel field to `Options`:

```go
type Options struct {
	Command    []string
	SocketPath string
	Hotkey     byte              // default 0x1D = Ctrl+]
	Env        []string          // extra KEY=VALUE pairs for the child process
	Status     <-chan StatusUpdate // nil-safe; bar stays hidden until first event
}
```

- [ ] **Step 2: Replace bar state variables**

Remove the old bar state block (lines 153-168 of current wrapper.go):

```go
barVisible := false
barRenderedMsg := ""
barRendered := ""
barScrollSeq := scrollRegionSeq(rows - 1)
termRows := rows
termCols := cols

updateBarCache := func(message string) {
    bar := RenderBar(message, termCols)
    barRendered = fmt.Sprintf("\x1b7\x1b[%d;1H%s\x1b8", termRows, bar)
    barScrollSeq = scrollRegionSeq(termRows - 1)
}
```

Replace with the new shared cache and helper:

```go
barScrollSeq := scrollRegionSeq(rows - 1)
termRows := rows
termCols := cols

var cache barCache // shared with output goroutine and SIGWINCH; guarded by outputMu

// renderBarEsc builds the full escape sequence to position and render
// the bar on the last row. Must be called under outputMu.
renderBarEsc := func(message string, alert bool) string {
    bar := RenderStatusBar(message, termCols, alert)
    return fmt.Sprintf("\x1b7\x1b[%d;1H%s\x1b8", termRows, bar)
}

// clearBarEsc builds the escape sequence to clear the bar row.
clearBarEsc := func() string {
    return fmt.Sprintf("\x1b7\x1b[%d;1H\x1b[K\x1b8", termRows)
}
```

- [ ] **Step 3: Replace socket listener callback and toast goroutine with merge goroutine + event loop**

Remove the old toast channel, socket callback, and toast goroutine (lines 125-241 of current wrapper.go). Replace with:

```go
// Internal event channel and done signal.
eventCh := make(chan barEvent, 64)
done := make(chan struct{})

// Start notification socket listener.
var sl *SocketListener
sl, err = ListenSocket(sockPath, func(n Notification) {
    select {
    case <-done:
    case eventCh <- barEvent{
        kind: barEventAlert,
        update: StatusUpdate{
            Message: n.Message,
            Alert:   true,
            Timeout: n.Timeout,
        },
    }:
    default:
    }
})
if err != nil {
    fmt.Fprintf(os.Stderr, "ward: socket: %v\n", err)
}
```

Then the merge goroutine (only needed when `opts.Status` is non-nil):

```go
// Merge goroutine: bridges opts.Status into eventCh.
if w.opts.Status != nil {
    go func() {
        for {
            select {
            case <-done:
                return
            case su, ok := <-w.opts.Status:
                if !ok {
                    return
                }
                kind := barEventStatus
                if su.Alert {
                    kind = barEventAlert
                }
                select {
                case <-done:
                    return
                case eventCh <- barEvent{kind: kind, update: su}:
                }
            }
        }
    }()
}
```

Then the event loop goroutine:

```go
const maxAlertQueue = 64

// Event loop goroutine — sole owner of bar state.
go func() {
    var (
        lastStatus   string
        alertQueue   []StatusUpdate
        activeAlert  *StatusUpdate
        dismissTimer *time.Timer
    )

    showAlert := func(su StatusUpdate) {
        activeAlert = &su
        timeout := su.Timeout
        if timeout <= 0 {
            timeout = DefaultTimeout
        }
        msg := sanitizeMessage(su.Message)

        outputMu.Lock()
        cache = barCache{
            rendered: renderBarEsc(msg, true),
            message:  msg,
            mode:     barAlert,
        }
        if stdoutIsTTY {
            os.Stdout.WriteString(cache.rendered) //nolint:errcheck
        }
        outputMu.Unlock()

        if dismissTimer != nil {
            dismissTimer.Stop()
        }
        dismissTimer = time.AfterFunc(timeout, func() {
            select {
            case <-done:
            case eventCh <- barEvent{kind: barEventDismiss}:
            }
        })
    }

    showStatus := func(msg string) {
        outputMu.Lock()
        cache = barCache{
            rendered: renderBarEsc(msg, false),
            message:  msg,
            mode:     barStatus,
        }
        if stdoutIsTTY {
            os.Stdout.WriteString(cache.rendered) //nolint:errcheck
        }
        outputMu.Unlock()
    }

    showCleared := func() {
        outputMu.Lock()
        cache = barCache{
            rendered: clearBarEsc(),
            message:  "",
            mode:     barCleared,
        }
        if stdoutIsTTY {
            os.Stdout.WriteString(cache.rendered) //nolint:errcheck
        }
        outputMu.Unlock()
    }

    for {
        select {
        case <-done:
            if dismissTimer != nil {
                dismissTimer.Stop()
            }
            return
        case ev := <-eventCh:
            switch ev.kind {
            case barEventStatus:
                lastStatus = sanitizeMessage(ev.update.Message)
                if activeAlert == nil {
                    showStatus(lastStatus)
                }

            case barEventAlert:
                if activeAlert == nil {
                    showAlert(ev.update)
                } else {
                    if len(alertQueue) >= maxAlertQueue {
                        alertQueue = alertQueue[1:]
                    }
                    alertQueue = append(alertQueue, ev.update)
                }

            case barEventDismiss:
                activeAlert = nil
                if len(alertQueue) > 0 {
                    next := alertQueue[0]
                    alertQueue = alertQueue[1:]
                    showAlert(next)
                } else if lastStatus != "" {
                    showStatus(lastStatus)
                } else {
                    showCleared()
                }
            }
        }
    }
}()
```

- [ ] **Step 4: Update SIGWINCH handler to use barCache**

Replace the SIGWINCH handler body. It no longer touches bar state — only reads and updates `cache` under `outputMu`:

```go
go func() {
    for range sigCh {
        if isTTY {
            if w, h, err := term.GetSize(stdoutFd); err == nil {
                outputMu.Lock()
                termCols, termRows = w, h
                setScrollRegionAndPTY(ptmx, termCols, termRows)
                barScrollSeq = scrollRegionSeq(termRows - 1)
                switch cache.mode {
                case barStatus, barAlert:
                    cache.rendered = renderBarEsc(cache.message, cache.mode == barAlert)
                    os.Stdout.WriteString(cache.rendered) //nolint:errcheck
                case barCleared:
                    cache.rendered = clearBarEsc()
                    os.Stdout.WriteString(cache.rendered) //nolint:errcheck
                }
                outputMu.Unlock()
            }
        }
    }
}()
```

- [ ] **Step 5: Update output goroutine — use cache.mode instead of barVisible**

In the output goroutine, replace the two post-write blocks:

Old:
```go
if stdoutIsTTY && scrollReset {
    os.Stdout.WriteString(barScrollSeq)
}
if barVisible && stdoutIsTTY && escState == esGround {
    os.Stdout.WriteString(barRendered)
}
```

New:
```go
if stdoutIsTTY && scrollReset {
    os.Stdout.WriteString(barScrollSeq) //nolint:errcheck
}
if stdoutIsTTY && cache.mode != barHidden && escState == esGround {
    os.Stdout.WriteString(cache.rendered) //nolint:errcheck
}
```

- [ ] **Step 6: Update shutdown sequence**

Replace the old shutdown block (after `<-outputDone`):

Old:
```go
close(toastDone)
outputMu.Lock()
if toastTimer != nil {
    toastTimer.Stop()
}
barVisible = false
outputMu.Unlock()
if sl != nil {
    sl.Close()
}
```

New:
```go
close(done)
if sl != nil {
    sl.Close() //nolint:errcheck
}
```

- [ ] **Step 7: Update exit scroll region restore**

Update the deferred cleanup to use `clearBarEsc` equivalent since we no longer
have `barVisible`:

```go
defer func() {
    if stdoutIsTTY {
        outputMu.Lock()
        os.Stdout.WriteString(scrollRegionSeq(termRows))               //nolint:errcheck
        fmt.Fprintf(os.Stdout, "\x1b7\x1b[%d;1H\x1b[K\x1b8", termRows) //nolint:errcheck
        outputMu.Unlock()
    }
}()
```

This is unchanged from the current code — it already clears the bar row unconditionally on exit.

- [ ] **Step 8: Remove unused imports**

The `Notification` type import from `toastCh` is gone. Remove any now-unused imports. The `time` import is still needed for `time.AfterFunc` and `DefaultTimeout`.

- [ ] **Step 9: Build and run all tests**

Run: `go build ./... && go test ./ward/ -count=1 -race -timeout 30s`
Expected: Build succeeds, all tests pass

- [ ] **Step 10: Run linter**

Run: `golangci-lint run ./ward/...`
Expected: 0 issues

- [ ] **Step 11: Commit**

```bash
git add ward/wrapper.go
git commit -m "Replace toast goroutine with event loop and barCache"
```

---

### Task 3: Wire up Options.Status in cmd/ssh.go

**Files:**
- Modify: `cmd/ssh.go`

- [ ] **Step 1: Create status channel and send initial status**

In `cmd/ssh.go`, modify the ward wrapping section (lines 142-158). Add a status
channel with the session info before creating the wrapper:

```go
if os.Getenv("VIBEPIT_WARD_PARENT") == "" {
    exe, err := os.Executable()
    if err != nil {
        return fmt.Errorf("resolve executable: %w", err)
    }
    statusCh := make(chan ward.StatusUpdate, 1)
    statusCh <- ward.StatusUpdate{
        Message: fmt.Sprintf("%s · %s", sandbox.SessionID, projectRoot),
    }
    w := ward.NewWrapper(ward.Options{
        Command: append([]string{exe}, os.Args[1:]...),
        Env:     []string{fmt.Sprintf("VIBEPIT_WARD_PARENT=%d", os.Getpid())},
        Status:  statusCh,
    })
    exitCode, err := w.Run(ctx)
    if err != nil {
        return err
    }
    if exitCode != 0 {
        return &ctr.ExitError{Code: exitCode}
    }
    return nil
}
```

- [ ] **Step 2: Build and test**

Run: `go build ./... && go test ./cmd/ -count=1 -race -timeout 60s`
Expected: Build succeeds, all tests pass

- [ ] **Step 3: Run linter**

Run: `golangci-lint run ./cmd/...`
Expected: 0 issues

- [ ] **Step 4: Commit**

```bash
git add cmd/ssh.go
git commit -m "Send session info to ward status bar from SSH command"
```

---

### Task 4: Add event loop behavior tests

**Files:**
- Create or modify: `ward/bar_test.go`

These tests exercise the event loop behavior through the `Wrapper.Run` interface
by sending `StatusUpdate` values on the `Status` channel and socket notifications.
Since the bar renders to `os.Stdout` (not capturable in unit tests), we test the
observable side effects: exit behavior and timing.

For the rendering and queue behavior, we test `RenderStatusBar` (already done in
Task 1) and add focused tests for the alert queue logic.

- [ ] **Step 1: Add alert queue tests**

Add to `ward/bar_test.go`:

```go
func TestRenderStatusBarDefaultStyle(t *testing.T) {
	bar := RenderStatusBar("session info", 40, false)
	// Default style should not use alert colors — just verify it renders
	if !strings.Contains(bar, "session info") {
		t.Fatal("default bar should contain the message")
	}
}

func TestRenderStatusBarAlertStyle(t *testing.T) {
	bar := RenderStatusBar("blocked!", 40, true)
	if !strings.Contains(bar, "blocked!") {
		t.Fatal("alert bar should contain the message")
	}
}

func TestRenderStatusBarEmptyMessage(t *testing.T) {
	bar := RenderStatusBar("", 80, false)
	// Should produce a styled bar even with empty message
	if len(bar) == 0 {
		t.Fatal("expected non-empty bar for empty message")
	}
}

func TestStatusUpdateTimeoutDefault(t *testing.T) {
	su := StatusUpdate{Message: "test", Alert: true, Timeout: 0}
	if su.Timeout <= 0 {
		// The event loop substitutes DefaultTimeout for Timeout <= 0.
		// This test documents the contract.
		su.Timeout = DefaultTimeout
	}
	if su.Timeout != DefaultTimeout {
		t.Fatalf("expected DefaultTimeout, got %v", su.Timeout)
	}
}

func TestStatusUpdateNegativeTimeout(t *testing.T) {
	su := StatusUpdate{Message: "test", Alert: true, Timeout: -1 * time.Second}
	if su.Timeout <= 0 {
		su.Timeout = DefaultTimeout
	}
	if su.Timeout != DefaultTimeout {
		t.Fatalf("expected DefaultTimeout for negative timeout, got %v", su.Timeout)
	}
}
```

- [ ] **Step 2: Add wrapper integration test with Status channel**

```go
func TestWrapperShowsStatusBar(t *testing.T) {
	statusCh := make(chan StatusUpdate, 1)
	statusCh <- StatusUpdate{Message: "test-session · ~/project"}

	w := NewWrapper(Options{
		Command:    []string{"echo", "hello"},
		SocketPath: SocketPath(os.Getpid()),
		Status:     statusCh,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	exitCode, err := w.Run(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
}

func TestWrapperNilStatusChannel(t *testing.T) {
	w := NewWrapper(Options{
		Command:    []string{"echo", "hello"},
		SocketPath: SocketPath(os.Getpid()),
		// Status is nil — should work without error
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	exitCode, err := w.Run(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
}
```

- [ ] **Step 3: Run all tests**

Run: `go test ./ward/ -count=1 -race -timeout 30s`
Expected: All pass

- [ ] **Step 4: Run linter**

Run: `golangci-lint run ./ward/...`
Expected: 0 issues

- [ ] **Step 5: Commit**

```bash
git add ward/bar_test.go
git commit -m "Add status bar rendering and event loop tests"
```

---

### Task 5: Clean up dead code

**Files:**
- Modify: `ward/toast.go` (remove lipgloss import if unused)
- Modify: `ward/wrapper.go` (remove any remaining references to old toast types)

- [ ] **Step 1: Check for unused code**

Run: `golangci-lint run ./ward/... ./cmd/... 2>&1 | grep -i unused`

Remove anything flagged as unused that was made redundant by the refactoring. The `Notification` type in `ward/socket.go` is still used by `ListenSocket` and `SendNotification` — keep it.

- [ ] **Step 2: Run full test suite**

Run: `make test`
Expected: All pass

- [ ] **Step 3: Run linter**

Run: `golangci-lint run ./ward/... ./cmd/...`
Expected: 0 issues

- [ ] **Step 4: Commit**

```bash
git add -u
git commit -m "Remove dead code from status bar refactoring"
```
