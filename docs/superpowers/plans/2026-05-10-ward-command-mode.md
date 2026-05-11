# Ward Command Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add interactive key handling to ward so users can press Ctrl+] to enter a command mode with action hints on the bar, then press a key to trigger a callback (e.g., allow a blocked network connection).

**Architecture:** The stdin goroutine gains a byte-by-byte scanner with a two-state machine (normal/command). The event loop gains four new event types and two new states (commandMode, pendingAction) with a generation counter to reject stale events. A `commandCtx` context signals the stdin goroutine when command mode is cancelled externally (timeout). Bar rendering gets a new `RenderCommandBar` function for key hints.

**Tech Stack:** Go, `github.com/charmbracelet/x/ansi` for string width, `charm.land/lipgloss/v2` for styling, `github.com/stretchr/testify` for tests.

**Spec:** `docs/superpowers/specs/2026-05-10-ward-command-mode-design.md`

---

## File Structure

| File | Responsibility |
|---|---|
| `ward/bar.go` | `KeyHint` type, `RenderCommandBar` function, `Target` field on `StatusUpdate` |
| `ward/bar_test.go` | Tests for `RenderCommandBar` |
| `ward/command.go` | Command mode types: `commandResponse`, `barEventEnterCommand`/`barEventBeginAction`/`barEventAction`/`barEventCancelCommand` event kinds, `commandState` enum |
| `ward/command_test.go` | Tests for input parsing (`processInput`) and event loop command mode logic |
| `ward/wrapper.go` | `OnKey`/`KeyHints` on `Options`, input parsing in stdin goroutine, command mode handling in event loop |
| `cmd/ssh.go` | No-op `OnKey` callback and `KeyHints` in ward.Options |

---

### Task 1: Add `KeyHint` type and `Target` to `StatusUpdate`

**Files:**
- Modify: `ward/bar.go:15-20` (StatusUpdate struct), `ward/bar.go:40-43` (barEvent/barCache)

- [ ] **Step 1: Add `Target` field to `StatusUpdate` and `KeyHint` type**

In `ward/bar.go`, add the `KeyHint` type after `DefaultTimeout` and add `Target` to `StatusUpdate`:

```go
// KeyHint defines an action key shown on the bar during command mode.
type KeyHint struct {
	Key          byte
	Desc         string
	RequireAlert bool
}

// StatusUpdate carries a message, alert flag, and display timeout for the status bar.
type StatusUpdate struct {
	Message string
	Alert   bool
	Timeout time.Duration
	Target  string
}
```

Add `target` to `barCache`:

```go
type barCache struct {
	rendered string
	message  string
	mode     barMode
	target   string
}
```

- [ ] **Step 2: Run tests to verify nothing breaks**

Run: `go test ./ward/...`
Expected: all existing tests pass (no code uses `Target` yet)

- [ ] **Step 3: Commit**

```
git add ward/bar.go
git commit -m "Add KeyHint type and Target field to StatusUpdate"
```

---

### Task 2: Implement `RenderCommandBar`

**Files:**
- Modify: `ward/bar.go`
- Modify: `ward/bar_test.go`

- [ ] **Step 1: Write failing tests for `RenderCommandBar`**

Add to `ward/bar_test.go`:

```go
func TestRenderCommandBarWithAlert(t *testing.T) {
	hints := []KeyHint{
		{Key: 'a', Desc: "allow", RequireAlert: true},
		{Key: 'A', Desc: "allow+save", RequireAlert: true},
	}
	bar := RenderCommandBar("github.com:443", hints, 120, true)
	require.Contains(t, bar, "github.com:443")
	require.Contains(t, bar, "[a]")
	require.Contains(t, bar, "allow")
	require.Contains(t, bar, "[A]")
	require.Contains(t, bar, "allow+save")
	require.Contains(t, bar, "[esc]")
	require.Contains(t, bar, "cancel")
}

func TestRenderCommandBarWithoutAlert(t *testing.T) {
	hints := []KeyHint{
		{Key: 'a', Desc: "allow", RequireAlert: true},
	}
	bar := RenderCommandBar("", hints, 80, false)
	assert.NotContains(t, bar, "[a]")
	require.Contains(t, bar, "[esc]")
	require.Contains(t, bar, "cancel")
}

func TestRenderCommandBarTruncates(t *testing.T) {
	hints := []KeyHint{
		{Key: 'a', Desc: "allow", RequireAlert: true},
	}
	bar := RenderCommandBar("very-long-domain.example.com:443", hints, 30, true)
	require.NotEmpty(t, bar)
}

func TestRenderCommandBarNoHints(t *testing.T) {
	bar := RenderCommandBar("", nil, 80, false)
	require.Contains(t, bar, "[esc]")
	require.Contains(t, bar, "cancel")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./ward/... -run TestRenderCommandBar -v`
Expected: FAIL — `RenderCommandBar` undefined

- [ ] **Step 3: Implement `RenderCommandBar`**

Add to `ward/bar.go`:

```go
// RenderCommandBar renders the command mode bar with key hints and optional target.
// hasAlert controls whether RequireAlert hints are shown.
func RenderCommandBar(target string, hints []KeyHint, cols int, hasAlert bool) string {
	var parts []string
	if target != "" {
		parts = append(parts, sanitizeMessage(target))
	}
	for _, h := range hints {
		if h.RequireAlert && !hasAlert {
			continue
		}
		parts = append(parts, fmt.Sprintf("[%s] %s", string(h.Key), h.Desc))
	}
	parts = append(parts, "[esc] cancel")
	msg := " " + strings.Join(parts, "  ") + " "

	style := alertBarStyle
	msgWidth := ansi.StringWidth(msg)
	if (cols - prefixLen) < msgWidth {
		return prefix + style.Render(ansi.Truncate(msg, max(cols-prefixLen, 0), "…"))
	}
	fill := max(cols-prefixLen-msgWidth, 0) + 1
	return prefix + style.Render(msg) + style.Render(strings.Repeat("╱", fill))
}
```

Add `"fmt"` to the import block in `bar.go`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./ward/... -run TestRenderCommandBar -v`
Expected: all PASS

- [ ] **Step 5: Commit**

```
git add ward/bar.go ward/bar_test.go
git commit -m "Add RenderCommandBar for command mode key hints"
```

---

### Task 3: Add command mode types and event kinds

**Files:**
- Create: `ward/command.go`

- [ ] **Step 1: Create `ward/command.go` with types**

```go
package ward

import "context"

// commandState tracks the event loop's command mode state.
type commandState int

const (
	commandNone    commandState = iota
	commandActive               // command bar shown, idle timer running
	commandPending              // action key pressed, waiting for OnKey result
)

// commandResponse is sent from the event loop to the stdin goroutine
// when entering command mode.
type commandResponse struct {
	Target     string
	VisibleKeys []byte
	Gen        uint64
	Ctx        context.Context
}

// actionResult is sent from the stdin goroutine to the event loop
// after OnKey completes.
type actionResult struct {
	Message string
	Err     error
}
```

Extend the `barEventKind`, `barMode`, and `barEvent` in `ward/bar.go`. Add the new event kinds after the existing ones:

In `ward/bar.go`, change the `barEventKind` const block:

```go
const (
	barEventStatus barEventKind = iota
	barEventAlert
	barEventDismiss
	barEventEnterCommand
	barEventBeginAction
	barEventAction
	barEventCancelCommand
)
```

Add `barCommand` to the `barMode` enum:

```go
const (
	barHidden barMode = iota
	barStatus
	barAlert
	barCleared
	barCommand
)
```

Extend `barEvent` to carry command mode fields:

```go
type barEvent struct {
	kind   barEventKind
	update StatusUpdate

	// Command mode fields
	gen      uint64
	respCh   chan<- commandResponse // barEventEnterCommand
	ackCh    chan<- bool            // barEventBeginAction
	result   actionResult           // barEventAction
}
```

- [ ] **Step 2: Run tests to verify compilation**

Run: `go test ./ward/... -v`
Expected: all existing tests pass

- [ ] **Step 3: Commit**

```
git add ward/command.go ward/bar.go
git commit -m "Add command mode types and event kinds"
```

---

### Task 4: Add `OnKey` and `KeyHints` to `Options`

**Files:**
- Modify: `ward/wrapper.go:18-24`

- [ ] **Step 1: Add fields to `Options`**

In `ward/wrapper.go`, update the `Options` struct:

```go
// Options configures the PTY wrapper.
type Options struct {
	Command  []string
	Hotkey   byte                // default 0x1D = Ctrl+]
	Env      []string            // extra KEY=VALUE pairs for the child process
	Status   <-chan StatusUpdate // nil-safe; bar stays hidden until first event
	OnKey    func(ctx context.Context, key byte, target string) (string, error)
	KeyHints []KeyHint
}
```

- [ ] **Step 2: Run tests to verify nothing breaks**

Run: `go test ./ward/... -v`
Expected: all existing tests pass

- [ ] **Step 3: Commit**

```
git add ward/wrapper.go
git commit -m "Add OnKey and KeyHints to ward Options"
```

---

### Task 5: Implement input parsing with byte-by-byte scanning

**Files:**
- Create: `ward/command_test.go`
- Modify: `ward/wrapper.go:430-450`

This task replaces the existing stdin→PTY goroutine with a byte-by-byte scanner that detects the hotkey and dispatches command mode events. The `processInput` function is extracted so it can be tested without a real PTY.

- [ ] **Step 1: Write failing tests for `processInput`**

Create `ward/command_test.go`:

```go
package ward

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockPTY collects bytes written to the "PTY" for inspection.
type mockPTY struct {
	written []byte
}

func (m *mockPTY) Write(p []byte) (int, error) {
	m.written = append(m.written, p...)
	return len(p), nil
}

func TestProcessInputNormalPassthrough(t *testing.T) {
	pty := &mockPTY{}
	input := []byte("hello world")
	inCommand := processInput(input, pty, 0x1D, false, nil, nil)
	assert.False(t, inCommand)
	assert.Equal(t, input, pty.written)
}

func TestProcessInputHotkeyNilOnKey(t *testing.T) {
	pty := &mockPTY{}
	input := []byte{0x1D, 'x'}
	// OnKey is nil via inputHandler — hotkey is passed through
	inCommand := processInput(input, pty, 0x1D, false, nil, nil)
	assert.False(t, inCommand)
	assert.Equal(t, input, pty.written)
}

func TestProcessInputHotkeyMidChunk(t *testing.T) {
	pty := &mockPTY{}
	enterCh := make(chan barEvent, 1)
	respCh := make(chan commandResponse, 1)
	respCh <- commandResponse{
		Target:      "example.com:443",
		VisibleKeys: []byte{'a'},
		Gen:         1,
		Ctx:         context.Background(),
	}

	handler := &inputHandler{
		hotkey:   0x1D,
		eventCh:  enterCh,
		onKey:    func(ctx context.Context, key byte, target string) (string, error) { return "", nil },
		keyHints: []KeyHint{{Key: 'a', Desc: "allow", RequireAlert: true}},
	}

	input := []byte{'A', 'B', 0x1D, 'x', 'y'}
	inCommand := processInput(input, pty, 0x1D, false, handler, nil)
	// "AB" should be written to PTY, then hotkey triggers command mode
	// 'x' and 'y' are processed in command mode: 'x' is not a visible key so discarded,
	// 'y' is also discarded, we stay in command mode
	assert.True(t, inCommand)
	assert.Equal(t, []byte("AB"), pty.written)
	// An enter event was sent
	require.Len(t, enterCh, 1)
}

func TestProcessInputDoubleHotkeyForwardsLiteral(t *testing.T) {
	pty := &mockPTY{}
	cancelCh := make(chan barEvent, 1)
	handler := &inputHandler{
		hotkey:   0x1D,
		eventCh:  cancelCh,
		onKey:    func(ctx context.Context, key byte, target string) (string, error) { return "", nil },
		keyHints: []KeyHint{{Key: 'a', Desc: "allow", RequireAlert: true}},
	}

	input := []byte{0x1D}
	// commandCtx from a previous enter
	cmdCtx, cmdCancel := context.WithCancel(context.Background())
	defer cmdCancel()

	inCommand := processInput(input, pty, 0x1D, true, handler, cmdCtx)
	assert.False(t, inCommand)
	assert.Equal(t, []byte{0x1D}, pty.written)
	// A cancel event was sent
	require.Len(t, cancelCh, 1)
}

func TestProcessInputEscCancels(t *testing.T) {
	pty := &mockPTY{}
	cancelCh := make(chan barEvent, 1)
	handler := &inputHandler{
		hotkey:  0x1D,
		eventCh: cancelCh,
		onKey:   func(ctx context.Context, key byte, target string) (string, error) { return "", nil },
	}

	input := []byte{0x1B, 'z'}
	cmdCtx, cmdCancel := context.WithCancel(context.Background())
	defer cmdCancel()

	inCommand := processInput(input, pty, 0x1D, true, handler, cmdCtx)
	assert.False(t, inCommand)
	// 'z' after Esc goes to PTY
	assert.Equal(t, []byte("z"), pty.written)
	require.Len(t, cancelCh, 1)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./ward/... -run TestProcessInput -v`
Expected: FAIL — `processInput`, `inputHandler` undefined

- [ ] **Step 3: Implement `inputHandler` and `processInput`**

Add to `ward/command.go`:

```go
import (
	"context"
	"io"
	"time"
)

// inputHandler holds the state and callbacks for the stdin goroutine's
// command mode logic. nil means command mode is disabled.
type inputHandler struct {
	hotkey   byte
	eventCh  chan<- barEvent
	onKey    func(ctx context.Context, key byte, target string) (string, error)
	keyHints []KeyHint

	// Set by enterCommandMode, cleared on exit
	target      string
	visibleKeys []byte
	gen         uint64
	cmdCtx      context.Context
}

const onKeyTimeout = 5 * time.Second

// processInput scans a byte chunk and dispatches to the PTY or command mode.
// Returns true if still in command mode after processing the chunk.
func processInput(data []byte, pty io.Writer, hotkey byte, inCommand bool, handler *inputHandler, cmdCtx context.Context) bool {
	i := 0

	for i < len(data) {
		if !inCommand {
			// Normal mode: scan for hotkey
			if handler == nil || handler.onKey == nil {
				// No command mode — pass everything through
				pty.Write(data[i:]) //nolint:errcheck
				return false
			}

			// Find next hotkey byte
			start := i
			for i < len(data) && data[i] != hotkey {
				i++
			}
			if start < i {
				pty.Write(data[start:i]) //nolint:errcheck
			}
			if i >= len(data) {
				return false
			}

			// Found hotkey — enter command mode
			i++ // consume the hotkey byte

			respCh := make(chan commandResponse, 1)
			handler.eventCh <- barEvent{
				kind:   barEventEnterCommand,
				respCh: respCh,
			}

			resp := <-respCh
			handler.target = resp.Target
			handler.visibleKeys = resp.VisibleKeys
			handler.gen = resp.Gen
			handler.cmdCtx = resp.Ctx
			cmdCtx = resp.Ctx
			inCommand = true
			continue
		}

		// Command mode: check context first
		select {
		case <-cmdCtx.Done():
			// Timeout cancelled command mode — forward remaining to PTY
			if i < len(data) {
				pty.Write(data[i:]) //nolint:errcheck
			}
			return false
		default:
		}

		b := data[i]
		i++

		switch {
		case b == 0x1B: // Esc — cancel
			handler.eventCh <- barEvent{
				kind: barEventCancelCommand,
				gen:  handler.gen,
			}
			inCommand = false
			// Forward remaining bytes to PTY
			if i < len(data) {
				pty.Write(data[i:]) //nolint:errcheck
			}
			return false

		case b == hotkey: // Second hotkey — forward literal
			pty.Write([]byte{hotkey}) //nolint:errcheck
			handler.eventCh <- barEvent{
				kind: barEventCancelCommand,
				gen:  handler.gen,
			}
			inCommand = false
			// Forward remaining bytes to PTY
			if i < len(data) {
				pty.Write(data[i:]) //nolint:errcheck
			}
			return false

		default:
			// Check if this is a visible key hint
			matched := false
			for _, k := range handler.visibleKeys {
				if b == k {
					matched = true
					break
				}
			}
			if !matched {
				continue // Ignore unmatched keys, stay in command mode
			}

			// Action key — begin action handshake
			ackCh := make(chan bool, 1)
			handler.eventCh <- barEvent{
				kind:  barEventBeginAction,
				gen:   handler.gen,
				ackCh: ackCh,
			}

			ack := <-ackCh
			if !ack {
				// Command mode was cancelled (e.g., timeout race)
				inCommand = false
				if i < len(data) {
					pty.Write(data[i:]) //nolint:errcheck
				}
				return false
			}

			// Call OnKey synchronously with a fresh context
			onKeyCtx, onKeyCancel := context.WithTimeout(context.Background(), onKeyTimeout)
			msg, err := handler.onKey(onKeyCtx, b, handler.target)
			onKeyCancel()

			handler.eventCh <- barEvent{
				kind:   barEventAction,
				gen:    handler.gen,
				result: actionResult{Message: msg, Err: err},
			}
			inCommand = false
			// Forward remaining bytes to PTY
			if i < len(data) {
				pty.Write(data[i:]) //nolint:errcheck
			}
			return false
		}
	}

	return inCommand
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./ward/... -run TestProcessInput -v`
Expected: all PASS

- [ ] **Step 5: Commit**

```
git add ward/command.go ward/command_test.go
git commit -m "Implement command mode input parsing with byte-by-byte scanning"
```

---

### Task 6: Implement command mode in the event loop

**Files:**
- Modify: `ward/wrapper.go:250-357` (event loop)
- Modify: `ward/command_test.go`

- [ ] **Step 1: Write failing tests for event loop command mode**

Add to `ward/command_test.go`:

```go
func TestEventLoopEnterCommandWithAlert(t *testing.T) {
	eventCh := make(chan barEvent, 64)
	done := make(chan struct{})

	// Simulate an active alert by sending it first
	eventCh <- barEvent{
		kind:   barEventAlert,
		update: StatusUpdate{Message: "blocked", Alert: true, Timeout: time.Hour, Target: "example.com:443"},
	}

	// Give event loop a tick to process the alert, then enter command mode
	respCh := make(chan commandResponse, 1)
	eventCh <- barEvent{
		kind:   barEventEnterCommand,
		respCh: respCh,
	}

	// Start a minimal event loop
	loopDone := make(chan struct{})
	go testEventLoop(eventCh, done, loopDone)

	resp := <-respCh
	assert.Equal(t, "example.com:443", resp.Target)
	assert.Equal(t, uint64(1), resp.Gen)
	require.NotNil(t, resp.Ctx)

	close(done)
	<-loopDone
}

func TestEventLoopCancelRestoresAlert(t *testing.T) {
	eventCh := make(chan barEvent, 64)
	done := make(chan struct{})

	eventCh <- barEvent{
		kind:   barEventAlert,
		update: StatusUpdate{Message: "blocked", Alert: true, Timeout: time.Hour, Target: "example.com:443"},
	}

	respCh := make(chan commandResponse, 1)
	eventCh <- barEvent{
		kind:   barEventEnterCommand,
		respCh: respCh,
	}

	loopDone := make(chan struct{})
	var states []commandState
	go testEventLoopWithStateLog(eventCh, done, loopDone, &states)

	resp := <-respCh

	// Cancel command mode
	eventCh <- barEvent{
		kind: barEventCancelCommand,
		gen:  resp.Gen,
	}

	// Allow event loop to process
	time.Sleep(10 * time.Millisecond)

	close(done)
	<-loopDone

	// Should have gone: none -> active -> none
	require.Contains(t, states, commandActive)
}

func TestEventLoopStaleGenIgnored(t *testing.T) {
	eventCh := make(chan barEvent, 64)
	done := make(chan struct{})

	// First command session
	respCh1 := make(chan commandResponse, 1)
	eventCh <- barEvent{kind: barEventEnterCommand, respCh: respCh1}

	loopDone := make(chan struct{})
	go testEventLoop(eventCh, done, loopDone)

	resp1 := <-respCh1

	// Cancel first session
	eventCh <- barEvent{kind: barEventCancelCommand, gen: resp1.Gen}
	time.Sleep(10 * time.Millisecond)

	// Second command session
	respCh2 := make(chan commandResponse, 1)
	eventCh <- barEvent{kind: barEventEnterCommand, respCh: respCh2}
	resp2 := <-respCh2
	require.Equal(t, uint64(2), resp2.Gen)

	// Stale cancel from first session — should be ignored
	eventCh <- barEvent{kind: barEventCancelCommand, gen: resp1.Gen}
	time.Sleep(10 * time.Millisecond)

	// Verify second session is still active by successfully beginning an action
	ackCh := make(chan bool, 1)
	eventCh <- barEvent{kind: barEventBeginAction, gen: resp2.Gen, ackCh: ackCh}
	ack := <-ackCh
	assert.True(t, ack)

	close(done)
	<-loopDone
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./ward/... -run TestEventLoop -v`
Expected: FAIL — `testEventLoop` undefined

- [ ] **Step 3: Implement command mode in the event loop**

This is the main event loop modification in `ward/wrapper.go`. In the event loop goroutine (starting at line ~254), add the command mode state variables alongside the existing ones:

```go
var (
	lastStatus   string
	alertQueue   []StatusUpdate
	activeAlert  *StatusUpdate
	dismissTimer *time.Timer

	// Command mode state
	cmdState         commandState
	commandGen       uint64
	cmdIdleTimer     *time.Timer
	cmdCancel        context.CancelFunc
	savedAlertRemain time.Duration
	savedAlertStart  time.Time
)
```

Track when alerts start for remaining-time calculation. In `showAlert`, record the start time:

Add `alertStartedAt time.Time` to the vars, and set it in `showAlert`:

```go
showAlert := func(su StatusUpdate) {
	activeAlert = &su
	timeout := su.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	alertStartedAt = time.Now()
	// ... existing rendering code ...
}
```

Add a helper to compute visible keys from hints and alert state:

```go
visibleKeys := func() []byte {
	hasAlert := activeAlert != nil && activeAlert.Target != ""
	var keys []byte
	for _, h := range w.opts.KeyHints {
		if h.RequireAlert && !hasAlert {
			continue
		}
		keys = append(keys, h.Key)
	}
	return keys
}
```

Add a helper to exit command mode cleanly:

```go
exitCommandMode := func() {
	if cmdIdleTimer != nil {
		cmdIdleTimer.Stop()
		cmdIdleTimer = nil
	}
	if cmdCancel != nil {
		cmdCancel()
		cmdCancel = nil
	}
	cmdState = commandNone
}
```

Add a helper to restore alert or fall back:

```go
restoreAfterCommand := func() {
	if activeAlert != nil {
		showAlert(*activeAlert)
		if savedAlertRemain > 0 {
			if dismissTimer != nil {
				dismissTimer.Stop()
			}
			dismissTimer = time.AfterFunc(savedAlertRemain, func() {
				select {
				case <-done:
				case eventCh <- barEvent{kind: barEventDismiss}:
				}
			})
		}
	} else if len(alertQueue) > 0 {
		next := alertQueue[0]
		alertQueue = alertQueue[1:]
		showAlert(next)
	} else if lastStatus != "" {
		showStatus(lastStatus)
	} else {
		showCleared()
	}
}

dismissAfterAction := func() {
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
```

Handle the new event kinds in the event loop switch. Insert these cases after the existing `barEventDismiss` case:

```go
case barEventDismiss:
	if cmdState != commandNone {
		break // Ignore dismiss events during command mode
	}
	activeAlert = nil
	// ... existing dismiss logic ...

case barEventEnterCommand:
	commandGen++
	cmdState = commandActive

	// Pause active alert timer
	if dismissTimer != nil {
		dismissTimer.Stop()
		elapsed := time.Since(alertStartedAt)
		timeout := DefaultTimeout
		if activeAlert != nil && activeAlert.Timeout > 0 {
			timeout = activeAlert.Timeout
		}
		savedAlertRemain = timeout - elapsed
		if savedAlertRemain < 0 {
			savedAlertRemain = 0
		}
	}

	// Start idle timer
	idleGen := commandGen
	cmdIdleTimer = time.AfterFunc(5*time.Second, func() {
		select {
		case <-done:
		case eventCh <- barEvent{kind: barEventCancelCommand, gen: idleGen}:
		}
	})

	// Create commandCtx
	ctx, cancel := context.WithCancel(context.Background())
	cmdCancel = cancel

	target := ""
	if activeAlert != nil {
		target = activeAlert.Target
	}

	ev.respCh <- commandResponse{
		Target:      target,
		VisibleKeys: visibleKeys(),
		Gen:         commandGen,
		Ctx:         ctx,
	}

	// Render command bar
	hasAlert := activeAlert != nil && activeAlert.Target != ""
	outputMu.Lock()
	barWidth := max(termCols-1, 1)
	bar := RenderCommandBar(target, w.opts.KeyHints, barWidth, hasAlert)
	cache = barCache{
		rendered: fmt.Sprintf("\x1b7\x1b[%d;1H\x1b[K%s\x1b8", termRows, bar),
		message:  target,
		mode:     barCommand,
		target:   target,
	}
	if stdoutIsTTY {
		os.Stdout.WriteString(cache.rendered) //nolint:errcheck
	}
	outputMu.Unlock()

case barEventBeginAction:
	if cmdState != commandActive || ev.gen != commandGen {
		ev.ackCh <- false
		break
	}
	if cmdIdleTimer != nil {
		cmdIdleTimer.Stop()
		cmdIdleTimer = nil
	}
	cmdState = commandPending
	ev.ackCh <- true

case barEventAction:
	if cmdState != commandPending || ev.gen != commandGen {
		break
	}
	exitCommandMode()

	if ev.result.Err != nil {
		// Flash error using alert style
		msg := ev.result.Err.Error()
		showAlert(StatusUpdate{Message: msg, Alert: true, Timeout: 2 * time.Second})
		// After flash, dismiss the acted-on alert
		activeAlert = nil
	} else if ev.result.Message != "" {
		// Flash success using status style, then dismiss
		activeAlert = nil
		showStatus(ev.result.Message)
		if dismissTimer != nil {
			dismissTimer.Stop()
		}
		dismissTimer = time.AfterFunc(2*time.Second, func() {
			select {
			case <-done:
			case eventCh <- barEvent{kind: barEventDismiss}:
			}
		})
	} else {
		// No flash — just dismiss and move on
		dismissAfterAction()
	}

case barEventCancelCommand:
	if cmdState != commandActive || ev.gen != commandGen {
		break
	}
	exitCommandMode()
	restoreAfterCommand()
```

Also update the SIGWINCH handler's repaint switch to handle `barCommand`:

```go
switch cache.mode {
case barStatus, barAlert, barCommand:
	cache.rendered = renderBarEsc(cache.message, cache.mode == barAlert || cache.mode == barCommand)
	os.Stdout.WriteString(cache.rendered) //nolint:errcheck
case barCleared:
	cache.rendered = clearBarEsc()
	os.Stdout.WriteString(cache.rendered) //nolint:errcheck
}
```

Also modify the existing `barEventStatus` and `barEventAlert` cases to suppress repainting during command mode:

```go
case barEventStatus:
	lastStatus = ev.update.Message
	if activeAlert == nil && cmdState == commandNone {
		showStatus(lastStatus)
	}

case barEventAlert:
	if activeAlert == nil && cmdState == commandNone {
		showAlert(ev.update)
	} else {
		if len(alertQueue) >= maxAlertQueue {
			alertQueue = alertQueue[1:]
		}
		alertQueue = append(alertQueue, ev.update)
	}
```

Also add the `testEventLoop` and `testEventLoopWithStateLog` helpers at the bottom of `ward/command_test.go`. These are minimal event loops that exercise the command mode logic without terminal I/O. They should replicate the relevant parts of the event loop from wrapper.go but write to a no-op output instead of stdout. Keep them focused — only the event dispatch logic, not rendering.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./ward/... -v`
Expected: all PASS

- [ ] **Step 5: Commit**

```
git add ward/wrapper.go ward/command.go ward/command_test.go
git commit -m "Implement command mode state machine in event loop"
```

---

### Task 7: Wire input parsing into the stdin goroutine

**Files:**
- Modify: `ward/wrapper.go:430-450` (stdin goroutine)

- [ ] **Step 1: Replace the stdin goroutine**

Replace the existing stdin→PTY goroutine in `ward/wrapper.go` (the `go func()` block with the `TODO: hotkey interception` comment):

```go
// stdin -> PTY goroutine with command mode input parsing.
go func() {
	buf := make([]byte, 32*1024)
	inCommand := false
	var handler *inputHandler
	var cmdCtx context.Context

	if w.opts.OnKey != nil && isTTY {
		handler = &inputHandler{
			hotkey:   w.opts.Hotkey,
			eventCh:  eventCh,
			onKey:    w.opts.OnKey,
			keyHints: w.opts.KeyHints,
		}
	}

	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			if handler == nil {
				if _, werr := ptmx.Write(buf[:n]); werr != nil {
					break
				}
			} else {
				// Check if command mode was cancelled while we were blocked in Read
				if inCommand && cmdCtx != nil {
					select {
					case <-cmdCtx.Done():
						inCommand = false
					default:
					}
				}
				inCommand = processInput(buf[:n], ptmx, w.opts.Hotkey, inCommand, handler, cmdCtx)
				if inCommand {
					cmdCtx = handler.cmdCtx
				} else {
					cmdCtx = nil
				}
			}
		}
		if err != nil {
			break
		}
	}
}()
```

- [ ] **Step 2: Run all tests**

Run: `go test ./ward/... -v`
Expected: all PASS

- [ ] **Step 3: Run full test suite**

Run: `make test`
Expected: all PASS

- [ ] **Step 4: Commit**

```
git add ward/wrapper.go
git commit -m "Wire command mode input parsing into stdin goroutine"
```

---

### Task 8: Wire no-op callback in `cmd/ssh.go`

**Files:**
- Modify: `cmd/ssh.go:153-157`

- [ ] **Step 1: Add `OnKey` and `KeyHints` to ward.Options in ssh.go**

In `cmd/ssh.go`, update the `ward.Options` construction:

```go
w := ward.NewWrapper(ward.Options{
	Command: append([]string{exe}, os.Args[1:]...),
	Env:     []string{fmt.Sprintf("VIBEPIT_WARD_PARENT=%d", os.Getpid())},
	Status:  statusCh,
	OnKey: func(ctx context.Context, key byte, target string) (string, error) {
		return "", nil
	},
	KeyHints: []ward.KeyHint{
		{Key: 'a', Desc: "allow", RequireAlert: true},
		{Key: 'A', Desc: "allow+save", RequireAlert: true},
	},
})
```

Add `"context"` to the import block if not already present.

- [ ] **Step 2: Run full test suite**

Run: `make test`
Expected: all PASS

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: no errors

- [ ] **Step 4: Commit**

```
git add cmd/ssh.go
git commit -m "Wire no-op OnKey callback and KeyHints in ssh command"
```

---

### Task 9: Update doc.go with command mode architecture

**Files:**
- Modify: `ward/doc.go`

- [ ] **Step 1: Update the doc.go architecture diagrams**

Update the input diagram to show command mode branching, and add a command mode lifecycle section. In the input section, update the description to mention the state machine:

Replace the stdin diagram description text at the top:

```go
Two data paths (input and output) plus a status channel feed into a single
event loop that owns the bar. The stdin path includes a two-state machine
(normal/command) that intercepts the hotkey (Ctrl+], 0x1D) when OnKey is
configured and stdin is a TTY.
```

Add after the notifications diagram:

```
Command mode lifecycle (stdin goroutine ↔ event loop):

	stdin goroutine                    event loop
	───────────────                    ──────────
	detect 0x1D
	  → barEventEnterCommand ────────► commandGen++
	  ← commandResponse ◄──────────── stop dismiss timer
	                                   start idle timer
	                                   render command bar

	(waiting for key)

	matched key pressed
	  → barEventBeginAction ─────────► stop idle timer
	  ← ack (true) ◄────────────────── enter pendingAction

	call OnKey(ctx, key, target)

	  → barEventAction ──────────────► flash result
	                                   dismiss alert
	                                   process queue
```

- [ ] **Step 2: Run tests to verify nothing breaks**

Run: `go test ./ward/... -v`
Expected: all PASS

- [ ] **Step 3: Commit**

```
git add ward/doc.go
git commit -m "Update ward doc.go with command mode architecture"
```

---

### Task 10: Add integration-level command mode tests

**Files:**
- Modify: `ward/command_test.go`

- [ ] **Step 1: Add timeout, error, and edge case tests**

Add to `ward/command_test.go`:

```go
func TestProcessInputActionKeyTriggersOnKey(t *testing.T) {
	pty := &mockPTY{}
	eventCh := make(chan barEvent, 64)
	var calledKey byte
	var calledTarget string

	handler := &inputHandler{
		hotkey:  0x1D,
		eventCh: eventCh,
		onKey: func(ctx context.Context, key byte, target string) (string, error) {
			calledKey = key
			calledTarget = target
			return "allowed", nil
		},
		keyHints:    []KeyHint{{Key: 'a', Desc: "allow", RequireAlert: true}},
		target:      "example.com:443",
		visibleKeys: []byte{'a'},
		gen:         1,
	}

	cmdCtx, cmdCancel := context.WithCancel(context.Background())
	defer cmdCancel()
	handler.cmdCtx = cmdCtx

	// Pre-fill ack channel with success
	go func() {
		ev := <-eventCh // barEventBeginAction
		require.Equal(t, barEventBeginAction, ev.kind)
		ev.ackCh <- true
		// Consume barEventAction
		<-eventCh
	}()

	input := []byte{'a', 'z'}
	inCommand := processInput(input, pty, 0x1D, true, handler, cmdCtx)
	assert.False(t, inCommand)
	assert.Equal(t, byte('a'), calledKey)
	assert.Equal(t, "example.com:443", calledTarget)
	// 'z' after action should go to PTY
	assert.Equal(t, []byte("z"), pty.written)
}

func TestProcessInputActionKeyRejectedByAck(t *testing.T) {
	pty := &mockPTY{}
	eventCh := make(chan barEvent, 64)
	onKeyCalled := false

	handler := &inputHandler{
		hotkey:  0x1D,
		eventCh: eventCh,
		onKey: func(ctx context.Context, key byte, target string) (string, error) {
			onKeyCalled = true
			return "", nil
		},
		keyHints:    []KeyHint{{Key: 'a', Desc: "allow", RequireAlert: true}},
		target:      "example.com:443",
		visibleKeys: []byte{'a'},
		gen:         1,
	}

	cmdCtx, cmdCancel := context.WithCancel(context.Background())
	defer cmdCancel()
	handler.cmdCtx = cmdCtx

	// Reject the ack
	go func() {
		ev := <-eventCh
		require.Equal(t, barEventBeginAction, ev.kind)
		ev.ackCh <- false
	}()

	input := []byte{'a', 'z'}
	inCommand := processInput(input, pty, 0x1D, true, handler, cmdCtx)
	assert.False(t, inCommand)
	assert.False(t, onKeyCalled)
	// 'z' after rejected action should go to PTY
	assert.Equal(t, []byte("z"), pty.written)
}

func TestProcessInputContextCancelledDuringCommandMode(t *testing.T) {
	pty := &mockPTY{}
	handler := &inputHandler{
		hotkey:  0x1D,
		eventCh: make(chan barEvent, 64),
		onKey:   func(ctx context.Context, key byte, target string) (string, error) { return "", nil },
	}

	cmdCtx, cmdCancel := context.WithCancel(context.Background())
	cmdCancel() // Cancel immediately

	input := []byte{'a', 'b', 'c'}
	inCommand := processInput(input, pty, 0x1D, true, handler, cmdCtx)
	assert.False(t, inCommand)
	// All bytes forwarded to PTY since context is cancelled
	assert.Equal(t, []byte("abc"), pty.written)
}

func TestProcessInputHotkeyFollowedByActionInSameChunk(t *testing.T) {
	pty := &mockPTY{}
	eventCh := make(chan barEvent, 64)
	var calledKey byte

	handler := &inputHandler{
		hotkey:  0x1D,
		eventCh: eventCh,
		onKey: func(ctx context.Context, key byte, target string) (string, error) {
			calledKey = key
			return "", nil
		},
		keyHints: []KeyHint{{Key: 'a', Desc: "allow", RequireAlert: true}},
	}

	// Handle events in background
	go func() {
		// barEventEnterCommand
		ev := <-eventCh
		require.Equal(t, barEventEnterCommand, ev.kind)
		ev.respCh <- commandResponse{
			Target:      "test.com:80",
			VisibleKeys: []byte{'a'},
			Gen:         1,
			Ctx:         context.Background(),
		}
		// barEventBeginAction
		ev = <-eventCh
		require.Equal(t, barEventBeginAction, ev.kind)
		ev.ackCh <- true
		// barEventAction
		<-eventCh
	}()

	input := []byte("pre\x1daz")
	inCommand := processInput(input, pty, 0x1D, false, handler, nil)
	assert.False(t, inCommand)
	assert.Equal(t, byte('a'), calledKey)
	// "pre" before hotkey + "z" after action
	assert.Equal(t, []byte("prez"), pty.written)
}
```

- [ ] **Step 2: Run all tests**

Run: `go test ./ward/... -v`
Expected: all PASS

- [ ] **Step 3: Run full test suite**

Run: `make test`
Expected: all PASS

- [ ] **Step 4: Commit**

```
git add ward/command_test.go
git commit -m "Add integration-level command mode tests"
```
