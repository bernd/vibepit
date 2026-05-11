# Ward Command Mode: Interactive Key Handling

## Overview

Add a command mode to ward's PTY wrapper so users can take actions (e.g.,
allow blocked network connections) by pressing Ctrl+] followed by an action
key. The bar shows available actions and the current alert target while in
command mode.

## State Machine

The stdin goroutine gains a two-state machine:

```
Normal ──[Ctrl+]]──▶ Command ──[action key]──▶ OnKey callback ──▶ Normal
           │             │
           │             ├── Esc ──▶ Normal (cancel)
           │             ├── 5s timeout ──▶ Normal (cancel)
           │             └── Ctrl+] ──▶ forward literal 0x1D to child ──▶ Normal
           │
           └──[Ctrl+] Ctrl+] in same chunk]──▶ forward literal 0x1D ──▶ Normal
```

- **Normal mode**: All input forwarded to child PTY. Ctrl+] (0x1D) transitions
  to Command mode. If `OnKey` is nil, Ctrl+] is passed through (backward
  compatible).
- **Command mode**: Input intercepted by ward. The bar shows action hints and
  the current alert target. Exits on: action key (calls `OnKey`), Esc (cancel),
  event loop timeout (cancel — see below), or a second Ctrl+] (forwards one
  literal 0x1D to the child). Keys that don't match a visible `KeyHint` are
  ignored (the user stays in command mode).
- **TTY guard**: Command mode is only enabled when both stdin and stdout are
  TTYs (the existing `isTTY` check). When not a TTY, 0x1D is forwarded
  transparently even if `OnKey` is set — byte-stream passthrough is preserved.

## Input Parsing

`os.Stdin.Read` returns arbitrary byte chunks, not single keystrokes. The
stdin goroutine scans each chunk byte-by-byte:

- **Normal mode**: scan for 0x1D. Everything before it is written to the PTY.
  On 0x1D, transition to command mode (send `barEventEnterCommand`, wait for
  target response). Remaining bytes in the chunk are processed in command mode.
- **Command mode**: before processing each byte, check `commandCtx.Done()`
  (see timeout section below). If cancelled, return to normal mode and forward
  remaining bytes to PTY. Otherwise, each byte is checked against Esc (0x1B),
  the hotkey (0x1D for literal forwarding), and visible `KeyHint` keys. A
  matched action key triggers `OnKey` and `barEventAction`. Unmatched bytes
  are discarded. Any remaining bytes after an exit (action, Esc, Ctrl+]) are
  forwarded to the PTY in normal mode.

## Command Mode Lifecycle

The stdin goroutine does **not** read alert state directly. Instead, it
communicates with the event loop via events.

### Entering

The stdin goroutine sends `barEventEnterCommand`. The event loop:

1. Increments a `commandGen` counter (uint64). All command events carry this
   generation — the event loop ignores events whose generation does not match
   the current `commandGen`. This guards against stale cancel/action events
   from a previous command session.
2. Sets a `commandMode` flag. While this flag is set, incoming
   `barEventDismiss` events are ignored — this guards against dismiss timers
   that already fired or had callbacks blocked before the timer was stopped.
3. Stops the active alert's dismiss timer (if any) and records the remaining
   duration so it can be restored on cancel.
4. Starts a 5-second idle timer. When it fires, its callback sends
   `barEventCancelCommand` (with the current `commandGen`) to the event loop.
5. Creates a `commandCtx` (a cancellable context, no deadline) and sends it
   along with the current alert target, visible `KeyHint` keys, and
   `commandGen` back to the stdin goroutine via a response channel. The stdin
   goroutine checks `commandCtx.Done()` before processing each byte — if
   cancelled, it returns to normal mode. This handles the case where
   `os.Stdin.Read` is blocked with no input.
6. Renders the command bar with key hints.

### Exiting — three paths

All exit paths cancel `commandCtx` (so the stdin goroutine returns to
normal mode) and stop the idle timer.

**Action** (user pressed a matched key): the stdin goroutine sends
`barEventBeginAction` (with `commandGen` and an ack channel) *before*
calling `OnKey`. The event loop receives it, verifies the generation
matches, stops the idle timer, transitions from `commandMode` to
`pendingAction`, and sends `true` on the ack channel. If the generation is
stale (command mode was already cancelled by timeout), it sends `false`.
The stdin goroutine waits for the ack — if `false` or `commandCtx` is
cancelled, it skips `OnKey` and returns to normal mode. If `true`, it calls
`OnKey` with a fresh 5-second context, then sends `barEventAction` (with
`commandGen`, returned message, and error). The event loop accepts
`barEventAction` only in `pendingAction` state with a matching generation,
clears the state, cancels `commandCtx`, and then:

- *With active alert*: dismisses the alert (resolved), flashes the result
  (success message as status style, error as alert style), then processes
  the next queued alert or falls back to status after the flash timeout.
  The paused timer is discarded.
- *Without active alert*: flashes the result, then processes the next queued
  alert (alerts may have arrived during command mode) or reverts to last
  status or cleared.

**Cancel** (Esc or second Ctrl+]): stdin sends `barEventCancelCommand`
(with `commandGen`). The event loop verifies the generation, clears
`commandMode`, cancels `commandCtx`, stops the idle timer, and then:

- *With active alert*: restores the alert's bar rendering and restarts its
  dismiss timer with the remaining duration that was saved on enter.
- *Without active alert*: processes the next queued alert (alerts may have
  arrived during command mode) or reverts to last status or cleared.

**Timeout** (5-second idle timer fires): the timer callback sends
`barEventCancelCommand` (with `commandGen`). The event loop handles it
identically to an Esc cancel — verifies generation, clears `commandMode`,
cancels `commandCtx`. The stdin goroutine detects the cancelled context on
its next byte or next `Read` return and switches back to normal mode.

### Concurrent events during command mode

While `commandMode` is set, the event loop still processes incoming events:

- `barEventDismiss`: ignored (stale timer guard).
- `barEventAlert`: queued as normal, but does not repaint — the command bar
  stays visible.
- `barEventStatus`: updates `lastStatus` silently, no repaint.

The command bar is never overwritten by background events. It is only replaced
when command mode exits.

This keeps `barCache` ownership entirely in the event loop — no shared reads
from the stdin goroutine and no data races.

## API Changes

### New types in `ward/bar.go`

```go
type KeyHint struct {
    Key          byte
    Desc         string
    RequireAlert bool  // only show when an alert with a target is active
}
```

### Options changes in `ward/wrapper.go`

```go
type Options struct {
    // ... existing fields ...
    OnKey    func(ctx context.Context, key byte, target string) (string, error)
    KeyHints []KeyHint
}
```

- `OnKey` receives a fresh context with a 5-second deadline (independent of
  the command mode idle timer), the raw key byte, and the `Target` from the
  current alert (empty string if no alert is active). Returns a message to
  flash on the bar (e.g., "github.com:443 allowed") and an error. On error
  (including context deadline exceeded), the bar flashes the error message.
  Called synchronously in the stdin goroutine — the child's input is blocked
  while the callback runs, which is acceptable since the user just pressed a
  key and is waiting for feedback. **Caller contract**: the callback must
  respect context cancellation. If it blocks without checking `ctx.Done()`,
  the stdin goroutine remains blocked until the callback returns.
- `KeyHints` defines what to render on the bar and which keys are accepted.
  Ward uses the `Key` field to gate which keypresses trigger `OnKey` (unmatched
  keys are ignored), but does not interpret what the keys mean — that is the
  caller's responsibility via the callback.

### StatusUpdate changes in `ward/bar.go`

```go
type StatusUpdate struct {
    Message string
    Alert   bool
    Timeout time.Duration
    Target  string  // action target, e.g., "github.com:443"
}
```

`Target` flows through `barEvent` into the event loop. The event loop is the
sole owner of alert target state.

## Bar Rendering

### Command mode with active alert

```
╱╱╱ VIBEPIT  github.com:443  [a] allow  [A] allow+save  [esc] cancel
```

Target is rendered before key hints. Uses alert style (orange background).
Truncates with `…` if the bar is too narrow.

### Command mode without alert

```
╱╱╱ VIBEPIT  [esc] cancel
```

Only hints where `RequireAlert == false` are shown (plus Esc).

### After action

1. Exit command mode.
2. If `OnKey` returned a non-empty message, flash it as a non-alert bar
   message for ~2 seconds. If `OnKey` returned an error, flash the error
   message using alert style. If both message and error are empty/nil, skip
   the flash.
3. Process next queued alert if any, otherwise revert to last status or cleared.

## Caller Integration

The caller (`cmd/ssh.go`) provides `OnKey` and `KeyHints`:

```go
OnKey: func(ctx context.Context, key byte, target string) (string, error) {
    // No-op stub. Will be wired to control API later.
    return "", nil
},
KeyHints: []ward.KeyHint{
    {Key: 'a', Desc: "allow", RequireAlert: true},
    {Key: 'A', Desc: "allow+save", RequireAlert: true},
},
```

The actual proxy actions (allow, allow+save) are no-ops in the initial
implementation. The control API wiring will be added later.

## Testing

- **Command mode timeout**: Enter command mode, wait 5 seconds without input,
  verify ward returns to normal mode and the paused alert timer resumes.
- **Unknown key in command mode**: Press a key not in `KeyHints`, verify it is
  ignored and command mode stays active.
- **Callback error**: `OnKey` returns an error, verify the error message is
  flashed on the bar using alert style.
- **Callback success**: `OnKey` returns a message, verify it is flashed as a
  non-alert bar message.
- **No alert active**: Enter command mode without an active alert, verify
  `RequireAlert` hints are hidden and only Esc exits command mode (no keys
  match visible hints).
- **Cancel restores alert timer**: Enter command mode with an active alert,
  press Esc, verify the alert reappears and its dismiss timer resumes with
  the remaining duration (not the full timeout).
- **Ctrl+] Ctrl+] forwards literal**: Press Ctrl+] twice, verify a single
  0x1D byte is forwarded to the child PTY and ward returns to normal mode.
- **Hotkey mid-chunk**: Send a chunk containing bytes before and after 0x1D,
  verify pre-hotkey bytes go to PTY, command mode activates, and post-hotkey
  bytes are processed in command mode.
- **Action key in same chunk as hotkey**: Send 0x1D followed by an action key
  in one read, verify `OnKey` fires and remaining bytes go to PTY.
- **OnKey timeout**: Provide a callback that blocks longer than 5 seconds,
  verify ward flashes a context deadline error.
- **Events during command mode**: Send status updates and new alerts while in
  command mode, verify the command bar is not repainted and events are queued
  or stored silently.
- **Stale generation events ignored**: Enter command mode, cancel, enter
  again. Deliver a cancel event with the old generation — verify it is
  ignored and the current command session is unaffected.
- **Action near idle timeout**: Press an action key at 4.9s into command
  mode. Verify the callback gets a fresh 5-second context (not 100ms), and
  a stale timeout cancel arriving during OnKey is ignored via generation.

## Files Changed

| File | Change |
|---|---|
| `ward/bar.go` | `KeyHint` type, `RenderCommandBar` function, `Target` on `StatusUpdate` |
| `ward/wrapper.go` | Command mode state machine in stdin goroutine, `OnKey`/`KeyHints` on `Options`, idle timer + `commandGen` generation counter, `barEventEnterCommand`/`barEventBeginAction`/`barEventAction`/`barEventCancelCommand` events with generation, `commandMode`/`pendingAction` states, `commandCtx` for stdin signalling, response channel for target |
| `cmd/ssh.go` | Provide no-op `OnKey` callback and `KeyHints` when constructing `ward.Options` |

No changes to the control API, monitor TUI, or allow commands.

## Out of Scope

- **Control API calls from `OnKey`**: The callback is a no-op in this
  iteration. Wiring `allow-http`/`allow-dns` via the control client will be
  a follow-up.
