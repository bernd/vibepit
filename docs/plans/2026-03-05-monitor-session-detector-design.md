# Monitor Session Detector

## Problem

When a monitored session disappears (container stops, network error), the
monitor shows a connection error in the footer and stops polling. The user has
no recovery path other than quitting and restarting.

## Design

### Session Loss Flow

1. Log polling returns a connection error.
2. Monitor shows "Session `<id>` disconnected" in the footer for 3 seconds.
3. After 3 seconds, monitor transitions to the session selector screen.

### Session Selector with Live Refresh

The session selector polls `ListProxySessions` every 1 second, always — whether
the list is empty or populated.

- Empty list: shows an empty state message, continues refreshing.
- Sessions appear/disappear: list updates live, cursor clamped to bounds.
- User can quit with `q` at any time.

### Manual Session Switching

`Esc` from the monitor screen transitions immediately to the session selector
(no "disconnected" message — this is user-initiated).

`Esc` is NOT shown in the session selector footer since there's nothing to go
back to (neither on initial launch nor after disconnect).

### Reconnection

Selecting a session creates a fresh monitor screen: new `ControlClient`, empty
log history. Same flow as the existing `onSelect` callback. The old
`ControlClient` is closed before transitioning (via `Close()` which calls
`http.Client.CloseIdleConnections()`).

### Session Polling

`sessionScreen` receives a `pollSessions func() ([]ctr.ProxySession, error)`
callback, created in `monitor.go` by closing over the existing `container.Client`.
The screen polls on `tui.TickMsg` using `w.IntervalElapsed(time.Second)` with a
`pollInFlight` guard, identical to the monitor screen's log polling pattern.
Results arrive as a new `sessionPollResultMsg` carrying `[]ctr.ProxySession` and
`error`.

### Monitor Back-Reference

`monitorScreen` gains an `onBack func() tui.Screen` field to construct the
session selector on disconnect or Esc. This callback is created in `monitor.go`
alongside `onSelect`, closing over `pollSessions` and `onSelect` itself.

## Files Changed

| File | Change |
|---|---|
| `cmd/monitor_ui.go` | Add `onBack` field, disconnection timer (3s), `Esc` key handler, transition to session selector |
| `cmd/session_ui.go` | Add `pollSessions` callback, periodic session list refresh (1s), handle list changes with cursor clamping |
| `cmd/monitor.go` | Extract `onSelect` callback for reuse in both startup and reconnection paths |
| `cmd/control.go` | Add `Close()` method to `ControlClient` |

## Test Plan

Each behavior is implemented test-first using the existing `Update`-level
testing pattern.

### monitor_ui_test.go
- Connection error in `logsPollResultMsg` sets `disconnectedAt` timestamp.
- After 12 ticks (3s at 250ms), `Update` returns a `sessionScreen`.
- `Esc` key returns a `sessionScreen` immediately (no timer).
- Multiple errors reset the timer (use latest error timestamp).

### session_ui_test.go
- `TickMsg` after 1s interval triggers session poll command.
- `sessionPollResultMsg` updates session list and clamps cursor.
- Cursor at position 3, list shrinks to 2 items: cursor clamps to 1.
- Empty list: shows empty state, cursor at 0.
- Poll error: shows error in footer, continues polling.

### monitor_test.go
- Extracted `onSelect` produces a `monitorScreen` with fresh `ControlClient`.

## Edge Cases

- **Session reappears during 3s grace period**: don't cancel the transition.
- **Cursor on refresh**: clamp to list bounds if selected item disappears.
- **Multiple rapid disconnects**: latest error resets the 3s timer.
- **Repeated poll errors**: show error once, suppress duplicates until a successful poll clears it.
