# Vibepit Ward Design

## Overview

A host-side PTY wrapper for Vibepit sandbox sessions. It sits between the user's terminal and the sandbox byte stream, reserving the bottom terminal row for a notification bar that shows blocked network requests from the proxy.

## Goals

1. Wrap sandbox terminal sessions with a PTY layer on the host side
2. Display a bottom notification bar when the proxy blocks network requests
3. Provide a modal popup (Ctrl+]) for interactively allowing/denying blocked requests (not yet implemented)
4. Keep the transport layer pluggable (Docker attach today, SSH later)

## Architecture

```
User's Terminal
  |
  ward.Wrapper (host process, vibepit run)
  |  - vt Emulator (screen state tracking)
  |  - Notification bar (bottom row, scroll region reserved)
  |  - stdin: user input (Ctrl+] interception planned)
  |  - stdout: real terminal output
  |
  io.ReadWriteCloser (transport)
  |
  Docker attach / SSH (future)
  |
  mTLS /logs polling (planned)
  |
  Proxy Container
```

The ward runs in the `vibepit run` host process. It accepts any `io.ReadWriteCloser` as the backend transport, making it agnostic to whether the sandbox session uses Docker attach or SSH.

## Current Implementation (ward/)

### Package Layout

```
ward/
  wrapper.go         # Wrapper struct, Run(), raw mode, goroutine lifecycle
  screen.go          # vt.SafeEmulator wrapper, snapshot, resize
  toast.go           # Bottom bar rendering with lipgloss
  socket.go          # Unix socket notification protocol
  osc133.go          # OSC 133 streaming parser (built, not yet wired in)
  *_test.go          # Tests for all components
ward/cmd/
  main.go            # Standalone CLI for development/testing
```

### Key Dependencies

| Need | Library |
|------|---------|
| Terminal emulation / screen state | `charmbracelet/x/vt` |
| Styled bar rendering | `charm.land/lipgloss/v2` |
| Raw mode, terminal size | `charmbracelet/x/term` |
| PTY management | `creack/pty` |

### API

```go
w := ward.NewWrapper(ward.Options{
    Command:    []string{"bash"},
    SocketPath: "/tmp/ward-1234.sock",
    Hotkey:     0x1D, // Ctrl+]
})
exitCode, err := w.Run(ctx)
```

`Run` enters raw mode, reserves the bottom row via scroll region, starts goroutines, and blocks until the child process exits. It restores the terminal on return.

### Notification Bar

The bar uses **scroll region reservation** rather than overlays:

1. On startup, the real terminal's scroll region is set to `1..(rows-1)` and the PTY is sized to `rows-1`. The child process never sees the last row.
2. When a notification arrives (via Unix socket), the bar content is rendered at the last row using lipgloss and ANSI cursor positioning.
3. After a configurable timeout (default 3 seconds), the bar is cleared.
4. On SIGWINCH, the scroll region and PTY are resized, and the bar is re-rendered if visible.
5. An escape sequence state tracker (CSI, OSC, DCS, APC, PM) prevents scroll region re-apply from corrupting ANSI sequences that span multiple PTY reads.

This approach avoids all the problems of overlay rendering:
- No artifacts with pagers (less, git diff), editors (vim), or TUI apps (Claude Code, Codex)
- No scroll-region conflicts with full-screen programs
- No screen restore needed on dismiss (just clear the row)

### Socket Protocol

Notifications are sent via a Unix socket (path exposed as `WARD_SOCKET` env var). Wire format: `timeout_seconds;message\n`. Messages are sanitized (C0 control characters and DEL stripped) to prevent terminal escape injection.

Socket is `chmod 0600` after bind.

### Goroutines

1. **PTY -> stdout** — Reads child output, feeds vt emulator, writes to real terminal. When bar is visible and stream is in ground state, re-applies scroll region and bar.
2. **stdin -> PTY** — Forwards user input to child. (Hotkey interception planned here.)
3. **Toast receiver** — Reads from notification channel, renders bar, schedules dismiss timer.
4. **SIGWINCH handler** — Resizes PTY and scroll region, re-renders bar if visible.
5. **Socket listener** — Accepts connections, parses messages, sends to notification channel.

### Concurrency

All stdout writes, screen access, and bar state are protected by a single `outputMu sync.Mutex`. The escape state scanner and bar re-render only run when the bar is visible, keeping the normal passthrough path lock-free except for the mutex acquisition.

Shutdown uses a `toastDone` channel (not `close(toastCh)`) to avoid send-on-closed-channel panics from in-flight socket connections.

### OSC 133 Parser (Built, Not Yet Wired)

A streaming parser for FinalTerm semantic prompt zones (Prompt, Input, Output, Unknown) is implemented with 38 tests ported from the Rust source. It is not yet connected to the PTY output path.

**Integration path:** Feed the same PTY bytes through the parser alongside the vt emulator in the PTY->stdout goroutine.

**Features it would unlock:**
- Suppress notifications while command output is streaming
- Associate blocked requests with the command that triggered them
- Context-aware notification placement

## Vibepit Integration (Planned)

### Changes to Existing Code

- `cmd/run.go` — After container setup, pass the Docker hijacked connection and mTLS config to `ward.NewWrapper()` instead of bridging directly to stdin/stdout. The wrapper needs the mTLS credentials to poll the proxy's `/logs` API.
- `container/terminal.go` — Refactor to expose the hijacked connection as an `io.ReadWriteCloser` rather than managing raw mode and I/O bridging itself. Raw mode and SIGWINCH handling move into the ward.
- `cmd/control.go` — Extract the allow/save-and-allow API call logic into a shared internal package so the modal popup can reuse it.

### New Code Needed

- **Poller** (`ward/poller.go`) — Polls the proxy's `/logs` mTLS API every second for blocked requests. Sends `Notification` values to the wrapper's toast channel. Exponential backoff on failure (1s, 2s, 4s, capped at 30s). After 3 consecutive failures, shows "proxy connection lost" notification. Poller errors never terminate the session.
- **Modal popup** (`ward/popup.go`) — Ctrl+] opens a centered overlay showing recent blocked requests. Suspends stdin forwarding while active. Uses lipgloss for rendering. Keyboard: j/k navigate, `a` allows for session, `s` allows and saves to config, ESC closes.
- **Hotkey interception** (`ward/input.go`) — stdin reader that intercepts Ctrl+] and routes input to either the PTY or the popup.

### No Changes To

- Proxy container / proxy code — ward polls the existing `/logs` API and calls existing `/allow-http`, `/allow-dns` endpoints.
- Monitor TUI — remains independent, can still be used alongside ward.
- Config system — `.vibepit/network.yaml` handling stays as-is.

### SSH Migration Path

When SSH replaces Docker attach, the only change is what `io.ReadWriteCloser` gets passed to `ward.NewWrapper()`. The ward, notification system, and popup are completely transport-agnostic.

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Wrapper location | Host-side, wrapping Docker attach stream | Simplest, clean security boundary, direct access to proxy API |
| Notification approach | Bottom bar via scroll region reservation | Avoids overlay artifacts with pagers, editors, and TUI apps |
| Notification model | Non-modal bar + modal hotkey popup (planned) | Bar informs without interrupting; hotkey gives user control |
| Proxy communication | Poll `/logs` API over mTLS | Reuses existing infrastructure, 1s latency acceptable for notifications |
| Transport integration | Wrap around existing Docker attach | Transport-agnostic via `io.ReadWriteCloser`, eases SSH migration |
| Code location | `ward/` package in Vibepit | Faster iteration, extract later if warranted |
| Popup rendering | Custom overlay with lipgloss (planned) | Hex wrapper owns raw mode, bubbletea event loop would conflict |
| Hotkey | Ctrl+] (fixed) | Rarely used, configurable later if needed |
| Terminal emulation | charmbracelet/x/vt | Full-featured, well-maintained, Go-native, SafeEmulator for thread safety |
| Escape safety | State tracker for CSI/OSC/DCS/APC/PM | Prevents ANSI corruption when injecting scroll region between PTY reads |
