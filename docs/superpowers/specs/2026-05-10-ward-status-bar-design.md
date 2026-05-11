# Ward Always-Visible Status Bar

## Goal

Replace ward's toast-only notification bar with an always-visible status bar
that shows session info by default and temporarily displays alerts (blocked
requests, etc.) when they arrive.

## Architecture

A single **event loop goroutine** inside `Run` owns all bar state:
`lastStatus`, the active alert, the alert queue, and the dismiss timer. No
other goroutine reads or writes this state.

The event loop communicates with other goroutines through two mechanisms:

- **Internal event channel** (`chan barEvent`): all sources feed typed events
  into this channel. The event loop is the sole consumer.
- **Cached render state** (`barCache`): a small struct written by the event
  loop under `outputMu` after every state change. This is the only bar state
  shared with other goroutines:

  ```go
  type barCache struct {
      rendered string  // pre-rendered escape sequence to emit on the bar row
      message  string  // sanitized display text (for re-rendering at new width)
      mode     barMode // hidden, status, alert, or cleared
  }

  type barMode int
  const (
      barHidden  barMode = iota // no status or alert received yet
      barStatus                 // showing default status
      barAlert                  // showing a temporary alert
      barCleared                // alert timed out with no lastStatus to revert to
  )
  ```

  The `mode` field drives all rendering decisions:

  - `barHidden`: bar row is not rendered. Output goroutine and SIGWINCH
    handler skip re-rendering.
  - `barStatus`: render `message` with default style.
  - `barAlert`: render `message` with alert style.
  - `barCleared`: render a clear-row escape sequence. `message` is empty.

  On SIGWINCH, the handler checks `mode` under `outputMu`:
  - `barHidden`: skip.
  - `barStatus` / `barAlert`: re-render from `message` with the appropriate
    style at the new width, update `rendered`.
  - `barCleared`: regenerate the clear-row escape sequence for the new row
    position, update `rendered`.

  The output goroutine (scroll-reset) re-emits `rendered` under `outputMu`
  for any mode except `barHidden`.

  **Cache update rule**: the event loop updates `barCache` to reflect the
  *currently visible* bar content, not necessarily the state field that just
  changed. For example, when a new status arrives while an alert is active,
  `lastStatus` changes but `barCache` continues to represent the active alert.
  The cache is only updated to show the new status when the alert is resolved.

  Neither the output goroutine nor the SIGWINCH handler touches event loop
  state (`lastStatus`, `alertQueue`, etc.).

### Internal Event Type

```go
type barEvent struct {
    kind   int          // barEventStatus, barEventAlert, barEventDismiss
    update StatusUpdate // only used for status/alert kinds
}
```

- `barEventStatus` / `barEventAlert`: carry a `StatusUpdate` from any source.
- `barEventDismiss`: timer sentinel, no payload. Tells the event loop to
  advance the alert queue or revert to status.

### Sources

All sources post `barEvent` values to the internal event channel (`eventCh`).
The merge goroutine handles sources 1 and 2; source 3 posts directly.

1. **`Options.Status`** (`<-chan StatusUpdate`): a buffered channel owned by
   the caller (`cmd/ssh.go`). The caller must use a buffered channel (capacity
   >= 1) or send from a goroutine to avoid deadlocking before `Run` starts
   reading. When nil, ward operates without a status channel — the bar stays
   hidden until the first socket notification arrives, then reverts to a blank
   (cleared) row after the alert times out. This preserves backward
   compatibility for standalone `ward` usage outside `cmd/ssh.go`.
   The merge goroutine maps `StatusUpdate.Alert` to the event kind:
   `Alert == false` → `barEventStatus`, `Alert == true` → `barEventAlert`.
2. **Socket listener** (`WARD_SOCKET`): receives external notifications.
   The merge goroutine wraps them as
   `barEvent{kind: barEventAlert, update: StatusUpdate{...}}`.
3. **Timer callbacks**: when an alert dismiss timer fires, it posts
   `barEvent{kind: barEventDismiss}` directly to `eventCh` (not through the
   merge goroutine, since the timer is owned by the event loop).

## StatusUpdate Type

```go
type StatusUpdate struct {
    Message string
    Alert   bool          // false = default status, true = temporary alert
    Timeout time.Duration // only used when Alert is true
}
```

- **Status update** (`Alert: false`): replaces the current default bar content
  immediately. Persists until the next status update. The last received status
  is stored as `lastStatus` so alerts can revert to it.
- **Alert update** (`Alert: true`): queued. See "Alert Queue Behavior" below.
- **Timeout semantics**: when `Alert` is true and `Timeout <= 0`, ward
  substitutes `DefaultTimeout` (3 s). This prevents accidental zero-duration
  alerts from flashing invisibly or blocking the queue.

## Message Sanitization

All `StatusUpdate.Message` values are sanitized before rendering, regardless
of source. The existing `sanitizeMessage` function (strips C0 control
characters and DEL) is applied to every message — status and alert — inside
ward before it reaches the rendering path. Callers are not required to
pre-sanitize.

## Styling

Uses the Vibepit color scheme defined in `tui/header.go`:

- **Default status bar**: cyan (`#00d4ff`) text on dark background
  (`#1e2d3d`). Unobtrusive, always visible.
- **Alert bar**: white text on orange (`#f97316`) background. Stands out
  without the alarm-level intensity of red.

Both styles render full-width on the reserved last row using lipgloss.

## Alert Queue Behavior

Alerts are FIFO-queued with a maximum capacity of 64 entries. When the queue
is full, the oldest unshown alert is dropped to make room for the new one.
Only one alert is visible at a time. The current alert stays until it is
resolved, then the next queued alert is shown. When the queue empties, the bar
reverts to `lastStatus`.

Resolution triggers (current implementation: timeout only):

- **Timeout fires**: current alert is resolved automatically.
- **Future — ack/nack key press**: resolves the current alert immediately.

Update rules while an alert is active:

- **New alert arrives**: queued behind the current alert. The current alert
  is not interrupted.
- **Current alert resolved**: next alert from the queue is shown with its own
  timeout. If the queue is empty, the bar reverts to `lastStatus`.
- **New status arrives**: updates `lastStatus` silently. No visual change
  while an alert is active. When all alerts are resolved, the bar shows the
  updated status.

## Data Flow

1. `cmd/ssh.go` discovers session ID and project directory.
2. Creates a buffered `chan ward.StatusUpdate` (capacity 1) and sends the
   initial status: `StatusUpdate{Message: "session-id · ~/project"}`.
3. Passes the channel via `ward.Options{Status: ch}`.
4. `ward.Run` starts a merge goroutine that selects on `opts.Status` (if
   non-nil) and socket listener callbacks, mapping each to a `barEvent` and
   posting to the internal event channel. Timer callbacks post directly.
5. The event loop goroutine reads from the internal channel, owns all bar
   state, and renders under `outputMu`. First status update renders the bar
   immediately. Alerts are enqueued per the queue behavior above.
6. When `opts.Status` is nil (standalone ward), the bar stays hidden until
   the first socket notification. After the alert times out, the row is
   cleared.

## Ward Changes

### `ward/bar.go` (new)

- `StatusUpdate` struct as defined above.
- `defaultBarStyle` — cyan on dark, full-width.
- `alertBarStyle` — white on orange, full-width.
- `RenderStatusBar(message string, cols int, alert bool) string` — renders
  with the appropriate style. Sanitizes the message before rendering.

### `ward/wrapper.go`

- `Options` gains `Status <-chan StatusUpdate` (nil-safe — ward works without
  it for standalone usage).
- **Event loop goroutine** owns all bar state (private, never shared):
  - `lastStatus string` — most recent non-alert message for revert.
  - `alertQueue []StatusUpdate` — FIFO, max 64 entries, drop oldest on overflow.
  - `activeAlert *StatusUpdate` — currently displayed alert (nil when showing status).
  - `dismissTimer *time.Timer` — fires `barEventDismiss` into the event channel.
- **Cached render state** (`barCache`): written by the event loop under
  `outputMu` after every state change. See Architecture section for the
  struct definition and access contract.
- **Merge goroutine**: selects on `opts.Status` (if non-nil) and socket
  channel. Maps `StatusUpdate.Alert` to `barEventStatus` / `barEventAlert`
  and writes to `eventCh`.
- **Event loop**: reads `barEvent` from `eventCh`. On status: set
  `lastStatus`, render (unless alert active). On alert: enqueue; if no alert
  active, show and start timer. On dismiss: show next queued alert or revert
  to `lastStatus`. After every state change, updates `barCache` under
  `outputMu`.
- **Output goroutine** (scroll-reset detection): re-emits `barScrollSeq` +
  `barCache.rendered` under `outputMu`. No bar state access.
- **SIGWINCH handler**: re-renders from `barCache.message` and
  `barCache.mode` at new width, updates `barCache.rendered` under
  `outputMu`. No bar state access.

### `ward/toast.go`

- Replace `barStyle` with `defaultBarStyle` and `alertBarStyle`.
- Replace `RenderBar` with `RenderStatusBar` that takes an `alert bool`
  parameter to select the style.

### `cmd/ssh.go`

- Create buffered status channel (capacity 1) before re-exec into ward.
- Send initial `StatusUpdate{Message: "session-id · ~/project/dir"}`.
- Pass channel via `ward.Options{Status: statusCh}`.

## Testing

- **Status rendering**: `RenderStatusBar` produces correct output for both
  default and alert styles, truncates long messages, sanitizes control bytes.
- **Alert-over-status revert**: send status, then alert, wait for timeout,
  verify bar shows original status.
- **Status update while alert active**: send status, alert, new status. After
  alert timeout, verify bar shows the new (not original) status.
- **Queued alerts**: send status, alert A, alert B. Verify A is shown first.
  After A times out, verify B is shown. After B times out, verify revert to
  status.
- **Resize preserves content**: send status or alert, trigger SIGWINCH, verify
  bar re-renders at new width with correct style.
- **Socket fan-in**: send a socket notification while a status is displayed,
  verify it appears as an alert and reverts.
- **Timeout edge cases**: alert with `Timeout <= 0` uses `DefaultTimeout`.
  Alert with valid timeout resolves after that duration.

## Shutdown

When the child process exits and the PTY output drains:

1. Close a `done` channel to signal all producers to stop.
2. The merge goroutine checks `done` in its select and exits.
3. Socket listener callbacks check `done` before sending to `eventCh`
   (select with default, same pattern as current `toastDone`).
4. Stop the active dismiss timer (if any) to prevent post-shutdown sends.
   Timer callbacks also check `done` before sending to `eventCh`.
5. The event loop exits without draining `eventCh`. Remaining events are
   abandoned — producers are already stopped and the channel is garbage
   collected.
6. `eventCh` is never closed by producers — the event loop simply stops
   reading after `done` is signaled.
7. Close the socket listener, restore the terminal scroll region.

## What Stays the Same

- Scroll region management (ESC c / CSI r detection + re-apply).
- PTY wrapper architecture (child process in rows-1 PTY).
- `WARD_SOCKET` for external notification sources.
- `VIBEPIT_WARD_PARENT` env var for re-exec detection.
- Notification wire format (`timeout;message`).

## Future Extensions

- Proxy log polling sends `StatusUpdate{Alert: true}` for blocked requests.
- OSC 133 zone tracking adjusts notification behavior during command output.
- Live session stats (uptime, blocked count) via periodic status updates.
- Ack/nack key press resolves the current alert immediately.
