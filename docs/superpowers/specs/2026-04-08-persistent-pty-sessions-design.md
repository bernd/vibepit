# Persistent PTY Sessions Design

## Problem

SSH sessions spawned by `vibed` tie the shell process lifetime to the SSH
connection. When a developer's machine suspends (common on notebooks and
workstations), the TCP connection goes stale, the SSH channel closes, and the
shell — along with any running AI agent (Claude Code, Codex, etc.) — is killed.

## Goals

1. **Survive disconnects** — shell processes and their children keep running
   when the SSH connection drops (suspend, network change, client crash).
2. **Seamless reconnect** — `vibepit ssh` reattaches to a running session with
   the current screen state restored. For regular shell output, scrollback
   history is preserved. For TUI applications (alternate screen mode), the
   current screen is restored but intermediate redraws during disconnect are
   not captured (same behavior as tmux).
3. **Invisible to the user** — no tmux prefix keys, status bars, or extra tools.
   Sessions are managed transparently by `vibed`.

## Non-Goals

- Persistent sessions for command mode (`vibepit ssh -- cmd args`). Command mode
  remains fire-and-forget.
- Session persistence across `vibepit down` / container restarts.
- Remote session sharing between different users.
- Multi-user session ownership. `vibed` serves a single SSH identity (one
  authorized key per sandbox). All sessions are implicitly owned by that user.
  If multi-user support is added later, sessions should be tagged with the
  authenticated SSH principal and filtered accordingly.

## Design

### Session Package (`session/`)

A new `session` package manages persistent PTY sessions. It knows nothing about
SSH — it exposes a pure I/O interface.

#### Core Types

```go
type Manager struct { ... }  // owns all sessions, provides create/list/get
type Session struct { ... }  // one PTY + shell process + VTE + scrollback + attached clients
type Client struct { ... }   // one attached reader/writer (wired to SSH channel by caller)
```

#### Manager

- `Create(cols, rows uint16) *Session` — spawns `/bin/bash --login` with a PTY
  via `creack/pty`, starts the I/O pump goroutine, returns the session.
- `Get(id string) *Session` — lookup by ID.
- `List() []SessionInfo` — returns ID, command name, attached/detached status,
  and client count for each session.
- Sessions get auto-incrementing IDs: `session-1`, `session-2`, etc.
- Hard limit of 50 concurrent sessions (default). `Create` returns an error
  when the limit is reached. `vibepit status` shows the count for user
  visibility.

#### Session

Owns the PTY master fd, the `exec.Cmd`, a virtual terminal emulator, a
scrollback ring buffer, and a list of attached clients.

**I/O pump goroutine:** Reads PTY output in a loop. For each chunk:
1. Feeds it to the VTE (updates screen state).
2. Appends to the scrollback buffer (primary screen lines only).
3. Fans out to all attached clients.

**Methods:**
- `Attach(cols, rows uint16) *Client` — creates a client and replays the
  session's scrollback and VTE screen state (including terminal modes —
  alternate screen, cursor visibility, mouse tracking, bracketed paste).
  Multiple clients can be attached simultaneously. The first attached client
  (or the one promoted after a writer disconnects) is the **writer** — only the
  writer can send input and trigger PTY resize. Other clients are read-only
  viewers.
  **Replay handoff (snapshot-plus-queue):** The attach operation holds the
  session's output lock while snapshotting VTE/scrollback and creating a
  per-client output queue, then registers the client for live delivery and
  releases the lock. The pump resumes immediately — new output goes to the
  client's queue. Replay is sent to the client without holding the lock (safe
  for slow connections). After replay completes, the queue is drained and the
  client switches to direct fan-out. This ensures no output is lost or
  duplicated without stalling the PTY pump.
- `TakeOver(client *Client)` — promotes a read-only client to writer,
  force-demoting the current writer to read-only. Used when reconnecting after
  suspend and the stale writer hasn't timed out yet (see Session Selector).
- `Detach(client *Client)` — removes the client from the fan-out list. If the
  detached client was the writer, the next most recent client is promoted to
  writer.
- `Resize(cols, rows uint16)` — resizes the PTY and VTE. Only the writer
  client can trigger this; resize requests from read-only viewers are ignored.
- `Info() SessionInfo` — returns session metadata (ID, command, status, client
  count).

**Shell exit handling:** When the shell process exits, the pump goroutine
detects PTY EOF. All attached clients receive EOF and their SSH channels close.
If no clients are attached, the session becomes a tombstone: it stays in the
manager with its exit code and exit time, but its PTY and VTE resources are
released. Tombstones are shown in the session selector so the user can see
that a session exited (and why). Tombstones are auto-removed after 1 hour or
when the user has seen them in the selector.

#### Client

Implements `io.ReadWriteCloser`.

- `Read(p []byte) (int, error)` — returns output from the replay buffer
  (scrollback + VTE screen) followed by live output from the pump.
- `Write(p []byte) (int, error)` — writes input to the PTY master fd.
- `Close()` — triggers detach.

### Virtual Terminal Emulator

Uses `charmbracelet/x/vt.SafeEmulator` (already a dependency via the `ward`
package).

- Initialized with the session's PTY size (cols × rows).
- Fed all PTY output — tracks cursor position, colors, attributes, alternate
  screen mode, scroll regions.
- On reconnect: the current screen state is rendered and sent to the client.
- On resize: the VTE is resized to match the new PTY dimensions.

### Scrollback Buffer

A ring buffer separate from the VTE that captures lines scrolling off the top
of the primary screen.

- Fixed capacity: 10,000 lines.
- Only captures primary screen output. When the VTE is in alternate screen
  mode (as reported by `vt.SafeEmulator`), scrollback capture is paused. TUI
  applications use alternate screen and don't produce meaningful scrollback.
- Stores raw bytes including ANSI escape sequences so replay preserves
  colors and formatting.

### Reconnect Flow

When an SSH client attaches to an existing session:

1. Send a terminal reset sequence.
2. Dump scrollback buffer lines (content that scrolled off the top).
3. Render the full VTE state: screen contents, terminal modes (alternate
   screen, cursor visibility, mouse tracking, bracketed paste, keypad mode),
   and cursor position. The VTE emulator emits the necessary escape sequences
   to reconstruct the complete terminal state.
4. Switch to live output from the pump goroutine.

The client sees the terminal restored to where it was — both visually and in
terms of terminal mode — then continues receiving live output. If a TUI
application does not render correctly after reconnect, the user can trigger a
redraw (e.g., Ctrl-L).

### SSH Integration

#### Interactive Mode (PTY sessions)

`handleSession` in `sshd/server.go` changes to:

1. SSH client connects, PTY is allocated.
2. Query the session manager for all sessions (running and tombstoned).
3. **Zero sessions:** create a new session, attach.
4. **One or more sessions:** show a Bubble Tea selector (see below) listing
   all sessions plus a "new session" option. This prevents accidental
   takeover of a healthy attached session — the user always explicitly
   chooses.
6. On SSH disconnect (broken pipe, timeout): call `session.Detach(client)`.
   The shell keeps running.
7. On clean shell exit: session removes itself, SSH channel closes with the
   shell's exit code.

**SSH keepalives:** The SSH server is configured with aggressive keepalive
settings (interval: 2 seconds, max missed: 2) so stale connections are
detected within ~4 seconds. Combined with the explicit takeover prompt in the
selector, the user can always recover write control immediately after
suspend/network-loss.

#### Command Mode

Unchanged — `handleExecSession` runs the command directly with `exec.Command`,
no session manager involvement. Fire-and-forget.

### Session Selector

A Bubble Tea model shown over the SSH PTY when multiple sessions exist.
`charmbracelet/ssh` has built-in support for running Bubble Tea programs over
SSH sessions.

```
Sessions:
  [1] session-1 (bash) — detached 3m ago
  [2] session-2 (claude) — 1 client attached
  [3] session-3 (bash) — exited (1) 5m ago
  [n] new session

Select [1-n]: _
```

The selector shows all sessions — running (attached/detached) and tombstoned
(exited) — with session ID, command name, and status. Tombstoned sessions
show the exit code and are not attachable; they are displayed for visibility
and dismissed after being seen.

Selecting a **detached** session attaches as writer immediately.

Selecting an **attached** session prompts: "Take over as writer? [y/n]". If
yes, the new client becomes writer via `TakeOver()` and the old writer is
demoted to read-only (or disconnected if stale). If no, the client attaches
as a read-only viewer. This gives the user instant recovery after
suspend/network-loss without waiting for keepalive timeout.

Cursor navigation with enter to select, `n` for new session.

After selection, the Bubble Tea program exits and the handler switches to the
persistent session's I/O pump (bidirectional copy between SSH channel and
session client).

### Session Lifecycle

**Session creation:**
- Triggered by first SSH connection when no detached sessions exist, or when
  user selects "new session" from the selector.
- `Manager.Create()` spawns `/bin/bash --login`, starts I/O pump.
- The process inherits the container environment via `mergeEnv()`.

**SSH disconnect (network drop, suspend, client killed):**
- `io.Copy` on the SSH channel returns an error.
- Session handler calls `session.Detach(client)`.
- Shell and all child processes keep running.
- Session becomes detached (no clients).

**Shell exits (user types `exit`, process crashes, signal):**
- Shell process exits.
- Pump goroutine detects PTY EOF.
- All attached clients get EOF, SSH channels close with exit code.
- Session becomes a tombstone: PTY and VTE resources are released, exit code
  and exit time are recorded. The tombstone stays in the manager and is shown
  in the session selector. Tombstones are auto-removed after 1 hour or after
  being seen in the selector.

**`vibepit down`:**
- Stops the sandbox container.
- `vibed` gets SIGTERM via docker-init.
- All shell processes get SIGHUP and terminate.
- No special cleanup needed — container removal handles everything.

### Changes to Existing Code

**New package:**
- `session/` — `Manager`, `Session`, `Client`, scrollback ring buffer, VTE
  integration.

**Modified:**
- `sshd/server.go` — `NewServer` accepts a `*session.Manager`. PTY handler
  replaced by session attach/detach logic with Bubble Tea selector. Exec
  handler unchanged.
- `cmd/vibed.go` — creates a `session.Manager` at startup, passes to
  `sshd.NewServer`.
- `cmd/status.go` — show active session count by reading the session state
  file (see Session State File below) via SSH command mode.

**Unchanged:**
- `cmd/ssh.go` — client doesn't know about persistent sessions.
- `cmd/up.go`, `cmd/down.go` — no changes.
- `proxy/` — no changes.

### Session State File

The session manager writes a JSON state file on every state change (session
created, attached, detached, exited). This allows external tools to observe
session state without IPC or a control endpoint.

- Path: `/tmp/vibed-sessions.json` (inside the sandbox container).
- Written atomically (write to temp file, rename) to prevent partial reads.
- Contains an array of session info objects (ID, command name,
  attached/detached status, client count, created time).
- `vibepit status` reads this file via SSH command mode
  (e.g., `vibepit ssh -- cat /tmp/vibed-sessions.json`).
- Data may be slightly stale (milliseconds), which is acceptable for status
  display.
- The state file is informational, not a security boundary. The sandbox is a
  single-user environment — processes running inside the session could
  overwrite this file, but they could also run `ps` directly. The file is a
  convenience for external observability, not an authoritative trust anchor.

### Dependencies

- `charmbracelet/x/vt` — virtual terminal emulator (already in project via
  `ward`).
- `charmbracelet/bubbletea` — for the session selector (already in project for
  the monitor TUI).
- `creack/pty` — PTY allocation (already used by `sshd/`).
