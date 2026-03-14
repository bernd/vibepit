# Session Selector Screen Design

## Goal

Replace the `charmbracelet/huh` select widget in `discoverSession()` with a
full TUI screen using the reusable `tui.Screen`/`tui.Window` pattern. This
gives visual consistency with the monitor, shows richer session info (project
dir, session ID, uptime), and enables seamless screen transitions.

## Behavior

When multiple sessions are discovered:

- **Monitor command**: the session selector is the initial screen inside the
  same `tea.Program`. On selection, `Update` returns a `monitorScreen` and the
  Window swaps seamlessly -- no flicker, one continuous TUI.
- **Other commands** (allow, future): a temporary `tea.Program` runs the
  selector. On selection the program exits and the command proceeds with the
  chosen `*SessionInfo`.

When only one session matches (or a `--session` filter narrows to one), the
selector is skipped entirely -- same as today.

## Session Row

Each row displays:

- **Project directory**
- **Session ID** (short form)
- **Uptime** (e.g. "2h 13m", "< 1m")

## Architecture

### sessionScreen (cmd/session_ui.go)

Implements `tui.Screen`. Embeds `tui.Cursor` for j/k/arrow/pgup/pgdn
navigation.

```go
type sessionScreen struct {
    tui.Cursor
    sessions []container.ProxySession
    selected *SessionInfo          // set on Enter
    onSelect func(*SessionInfo) (tui.Screen, tea.Cmd)  // nil = standalone (quit)
}
```

- `Update`: on Enter, populates `selected`. If `onSelect` is set, calls it and
  returns the new screen. Otherwise returns `tea.Quit`.
- `View`: renders a table-style list with project dir, session ID, uptime.
- `FooterKeys`: Enter to select, q to quit.
- `FooterStatus`: shows count of sessions.

### selectSession helper

```go
func selectSession(sessions []container.ProxySession) (*SessionInfo, error)
```

Creates a `sessionScreen` with `onSelect = nil`, wraps in `tui.NewWindow(nil,
screen)`, runs `tea.NewProgram`, returns `screen.selected` after exit.

### Monitor integration

`MonitorCommand` creates a `sessionScreen` with an `onSelect` callback that
builds and returns a `monitorScreen`. The Window starts with the selector as
its initial screen.

```go
onSelect := func(info *SessionInfo) (tui.Screen, tea.Cmd) {
    // build ControlClient, create monitorScreen, return it
}
screen := newSessionScreen(sessions, onSelect)
w := tui.NewWindow(header, screen)
```

### Header

- Standalone mode: `nil` or minimal `HeaderInfo` (wordmark only, no session
  info).
- Monitor mode: full `HeaderInfo`. Updated when transitioning to monitor
  screen.

## Data Changes

### container.ProxySession

Add `StartedAt time.Time` field. Populated from Docker/Podman container inspect
data during `ListProxySessions()`.

## Files

| File | Change |
|------|--------|
| `cmd/session_ui.go` | New -- `sessionScreen`, `selectSession()` |
| `cmd/session_ui_test.go` | New -- table-driven tests |
| `cmd/session.go` | Modify -- `discoverSession()` calls `selectSession()`, remove `huh` usage |
| `cmd/monitor.go` | Modify -- use `sessionScreen` as initial screen with `onSelect` callback |
| `container/client.go` | Modify -- add `StartedAt` to `ProxySession`, populate from inspect |
| `go.mod` | Modify -- remove `charmbracelet/huh` if unused elsewhere |

## Testing

Table-driven tests with subtests:

- **Navigation**: send j/k/Enter key messages, assert cursor position and selection
- **Standalone quit**: Enter selects and triggers `tea.Quit`
- **Monitor transition**: Enter selects and callback returns new screen
- **Single session bypass**: `discoverSession` returns directly, no TUI
- **Uptime rendering**: assert formatting ("2h 13m", "< 1m", etc.)
- **Empty / error states**: no sessions found, context cancelled

Uses `makeTestSetup()` helper pattern from `monitor_ui_test.go`.
