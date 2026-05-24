# Automatic SSH Port Forwarding (PoC)

## Goal

When a process inside the sandbox starts listening on a TCP port, automatically
publish that port on the host's `127.0.0.1` so the developer can reach it from
a browser or local tool without any manual configuration. The natural use case
is dev servers (`npm run dev`, `python -m http.server`, etc.) — the user runs
the command in the sandbox and the URL just works on the host.

This is a proof-of-concept. A follow-up project will add a confirmation UI in
the `ward` status bar and the `monitor` TUI, and will replace the periodic
`/proc/net/tcp` scan with an event-driven source. Both are out of scope here.

## Non-goals

- IPv6 forwarding (only `/proc/net/tcp`, not `/proc/net/tcp6`).
- UDP forwarding.
- Reverse forwarding (sandbox → host).
- A user-visible `vibepit forward` command. The PoC has no manual control
  surface; forwards appear and disappear with sandbox listeners.
- Bind addresses other than `127.0.0.1` on the host.
- ward / monitor UI integration.

## Architecture overview

Two components, one new wire format:

- **Sandbox side**: `sshd/server.go` exposes a new SSH channel type,
  `port-events@vibepit`. When the channel is open, vibed periodically scans
  `/proc/net/tcp` and pushes diff events as newline-delimited JSON.
- **Host side**: a new detached process, `vibepit forward-watcher`, spawned
  by `vibepit up`. It opens the `port-events@vibepit` channel, maintains a
  table of `sandbox-port → host-listener`, and forwards new TCP connections
  through the SSH client using `direct-tcpip` channels.
- **Control plane**: the watcher listens on a Unix domain socket in
  `os.TempDir()`. `vibepit down` dials the socket to signal graceful
  shutdown. `vibepit status` dials it to read the current forwards table.

There is no PID file. The control socket is the only on-disk artifact, and
its existence (and reachability) is the watcher's proof-of-life.

### Why not a separate container?

A forward-daemon container with `--network=host` would have given us Docker
lifecycle management for free, but `--network=host` does not surface bound
ports on the host's loopback under Docker Desktop or Podman Machine on macOS.
A single cross-platform implementation is more important than the lifecycle
savings, so we use a detached host-side process.

### Why server-push instead of client-poll?

The watcher does no per-tick work. The only periodic activity is one
`/proc/net/tcp` read per second inside vibed (local file I/O). The eventual
non-polling refactor (planned in a separate session) replaces the producer
side only — the channel and wire format stay the same, so the watcher and
control plane don't change.

## Sandbox-side: `sshd/server.go`

Three additions to `NewServer`:

### 1. Enable local port forwarding

```go
srv.LocalPortForwardingCallback = func(_ charmssh.Context, _ string, _ uint32) bool {
    return true
}
```

Authentication is already gated by the session's client key. The sandbox is
the only thing reachable through this SSH server, so no per-destination
filtering is needed.

### 2. Register channel handlers explicitly

Adding `ChannelHandlers` requires the full set — the implicit `session`
handler is no longer applied once the map is set:

```go
srv.ChannelHandlers = map[string]charmssh.ChannelHandler{
    "session":             charmssh.DefaultSessionHandler,
    "direct-tcpip":        charmssh.DirectTCPIPHandler,
    "port-events@vibepit": s.handlePortEvents,
}
```

### 3. `handlePortEvents`

When the watcher opens the channel:

1. Read `/proc/net/tcp` once. Parse lines with state `0A` (TCP_LISTEN),
   take `local_port`, filter to ports `>= 1024`. Build the initial set.
2. Encode and send `{"snapshot":[...]}` on the channel.
3. Start a 1-second ticker. On each tick, re-read `/proc/net/tcp`, diff
   against the previous set, and if anything changed, send
   `{"added":[...],"removed":[...]}`.
4. On channel close or session context cancel, stop the ticker and return.

`/proc/net/tcp6` is intentionally not read.

The parser (`readListeningPorts`) is a small, pure function: split lines on
whitespace, take field 2 (`local_address`), split on `:`, parse the
hex-encoded port, keep entries where field 4 (`state`) is `0A` and port
≥ 1024. The diff (`diffPorts`) is a trivial set diff returning two sorted
slices.

## Host-side: `vibepit forward-watcher`

A new hidden internal subcommand, similar to `vibed` and `proxy`. Takes a
single `--session <sid>` argument. Spawned by `vibepit up`, never invoked
by the user directly.

### Connection setup

Standard `gossh.Dial` is sufficient — events flow over the channel itself, not
as SSH global requests, so the default `DiscardRequests` behavior on the
client's global-request handler is fine. Each SSH connection opens
`port-events@vibepit` once:

```go
client, err := gossh.Dial("tcp", addr, cfg)
ch, reqs, err := client.OpenChannel("port-events@vibepit", nil)
go gossh.DiscardRequests(reqs) // per-channel requests — discard
dec := json.NewDecoder(ch)
// read events in a loop
```

### Forwards table and accept loops

State held in memory:

```go
type forward struct {
    sandboxPort uint16
    hostPort    uint16
    listener    net.Listener
    cancel      context.CancelFunc
}
forwards map[uint16]*forward // keyed by sandbox port
```

On `snapshot`: reconcile — open listeners for any sandbox port not already
in the table, close listeners for any held port not in the snapshot.

On `added[N]`: try `net.Listen("tcp", "127.0.0.1:N")`. If that fails with
`EADDRINUSE`, retry with `net.Listen("tcp", "127.0.0.1:0")` and record the
OS-assigned port. Start the accept loop.

On `removed[N]`: close the listener, cancel its accept loop, drop from the
table.

Each accept loop:

```go
for {
    c, err := lst.Accept()
    if err != nil { return }
    go pipeThroughSSH(c, sandboxPort)
}

func pipeThroughSSH(client net.Conn, port uint16) {
    defer client.Close()
    remote, err := currentClient.Load().Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
    if err != nil { return }
    defer remote.Close()
    // bidirectional io.Copy with half-close on EOF
}
```

`currentClient` is an `atomic.Pointer[ssh.Client]` swapped on reconnect so
new dials route through the live client without disrupting host listeners.

### SSH reconnect

On any SSH read error or channel close:

1. Mark the current client invalid (in-flight dials fail).
2. Sleep `1s, 2s, 4s, 8s` (capped at 8s) between reconnect attempts.
3. On success, swap `currentClient`, reopen the channel. The next
   `snapshot` is the reconcile point.

Host listeners and the forwards table are preserved across reconnects.
Only in-flight `direct-tcpip` streams die.

### Self-monitoring

Every 5 seconds, the watcher queries the container runtime for the
session's sandbox container status (via the existing
`container.Client.ContainerStatus` helper). If the container is no longer
`running`, the watcher exits cleanly (close listeners, close SSH, unlink
socket). This handles cases where the sandbox dies outside the normal
`vibepit down` path (Docker restart, manual `docker rm`, etc.).

### Control socket

Path: `filepath.Join(os.TempDir(), "vibepit-watcher-"+sid+".sock")`. Mode
`0600`. On start, the watcher removes any stale socket file before binding.

Wire format: newline-delimited text commands.

- `shutdown\n` → close listeners, close SSH, unlink the socket file, exit.
- `list\n` → reply with one JSON line:
  ```json
  {"forwards":[{"sandbox":3000,"host":3000},{"sandbox":5173,"host":54321}]}
  ```

## Lifecycle integration

### Spawn (from `vibepit up`)

After the existing "SSH daemon is ready" check in `cmd/up.go`:

```go
exe, _ := os.Executable()
logPath := filepath.Join(infra.SessionDir, "forwards.log")
logFile, _ := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
cmd := exec.Cmd{
    Path: exe,
    Args: []string{"vibepit-forward-watcher", "forward-watcher", "--session", sid},
    SysProcAttr: &syscall.SysProcAttr{Setsid: true},
    Stdin:  nil,
    Stdout: logFile,
    Stderr: logFile,
}
_ = cmd.Start() // do not Wait
```

`Setsid: true` detaches the watcher from any controlling terminal and from
`up`'s process group. `up` returns immediately.

If `cmd.Start()` fails, `up` logs a warning and continues — the session is
still usable for `connect`/`exec` without auto-forwards.

### Shutdown (from `vibepit down`)

Before tearing down containers:

```go
socketPath := filepath.Join(os.TempDir(), "vibepit-watcher-"+sid+".sock")
if conn, err := net.DialTimeout("unix", socketPath, 500*time.Millisecond); err == nil {
    _, _ = conn.Write([]byte("shutdown\n"))
    _ = conn.Close()
}
```

Then proceed with the existing container teardown. Failure modes:

- **Socket missing** → watcher never started or already exited. Skip.
- **`ECONNREFUSED`** → stale socket from a crashed watcher. Unlink it
  and continue.
- **Dial succeeds but watcher hangs** → 500ms timeout caps it. Containers
  go down, watcher's sandbox-health probe fires within 5s and it exits.

### Edge cases

- **`up` called while a previous watcher still runs**: the existing orphan-
  container check in `up.go` already refuses with "run `vibepit down`
  first". That `down` signals the watcher. No new logic needed.
- **Watcher killed manually (`kill -9`)**: forwards disappear, stale socket
  remains. Next `down` finds and unlinks it. Next `up` starts cleanly.
- **`vibepit down` crashes mid-shutdown**: stale socket; watcher's
  sandbox-health probe exits it within 5s anyway.

## Status integration (`cmd/status.go`)

Extend `printSessionStatus` to query the watcher and display its forwards
table below the existing per-container status.

```
Forwards     3000  →  http://127.0.0.1:3000
             5173  →  http://127.0.0.1:54321  (host :5173 was busy)
```

Plain mappings get the trivial form; remapped ports get the parenthetical
hint so the user can find the right URL.

If the forwards table is empty, omit the section.

Graceful degradation: if the socket doesn't exist, dial times out, or the
response is malformed, skip the section silently in non-verbose mode. In
`--verbose` mode, print:

```
Forwards     (watcher unavailable — see <session-dir>/forwards.log)
```

`--verbose` also surfaces the watcher log path and the socket path for
debugging. No new flags, no new commands.

## Path lengths and the macOS sockaddr_un cap

macOS `sockaddr_un.sun_path` is 104 bytes (Linux is 108). Putting the
socket in `os.TempDir()` instead of the session dir keeps the path well
under the cap on both platforms:

- Linux: `/tmp/vibepit-watcher-<20-char-sid>.sock` ≈ 46 chars.
- macOS: `/var/folders/.../T/vibepit-watcher-<20-char-sid>.sock` ≈ 88 chars.
  macOS `$TMPDIR` is hash-derived, not username-derived, so path length is
  stable regardless of username.

As belt-and-suspenders, the watcher computes the path at startup and
refuses to start with a clear error if it exceeds 104 bytes. This should
never trigger in practice.

## Testing strategy

### Unit tests

- `readListeningPorts`: fixtures covering single listener, multiple
  listeners, mixed `LISTEN`/`ESTABLISHED` states, privileged ports
  filtered out, malformed lines skipped, empty file.
- `diffPorts`: table-driven — empty/empty, add-only, remove-only, mixed,
  identical.
- Watcher event decoder: feed `snapshot` then a sequence of
  `added`/`removed` events, assert the in-memory forwards table evolves
  correctly.
- Port-clash fallback: mock `net.Listen` to fail with `EADDRINUSE` on
  `:N` and succeed on `:0`; assert the table records the OS-assigned port.

### Integration tests

Gated like the existing `make test-integration`. Wire a real `sshd.Server`
and a real watcher in-process (no Docker required):

1. Start `sshd.Server` on a localhost port with the port-events handler.
2. Start a fake "sandbox" process listening on `127.0.0.1:0` to get an
   ephemeral port.
3. Connect the watcher's SSH client; open the channel.
4. Assert the watcher's forwards table eventually contains the ephemeral
   port.
5. Stop the fake listener; assert the table eventually drops it.
6. Open a TCP connection to the watcher's host listener; assert bytes
   round-trip to the fake sandbox listener.

### Manual verification

End-to-end on a real host (nested-sandbox limits container ops):

- `vibepit up` → `python3 -m http.server 3000` inside sandbox →
  `curl 127.0.0.1:3000` from host.
- Bind something on host `:3000` first → repeat → confirm the remap shows
  in `vibepit status`.
- `vibepit down` → confirm watcher exits and socket is unlinked.
- `kill -9` the watcher mid-session → run `vibepit down` → confirm stale
  socket cleanup.

## Open decisions made during design

- **SSH reconnect backoff cap**: 8 seconds.
- **Sandbox health probe interval**: 5 seconds.
- **Poll interval inside vibed**: 1 second.
- **Port filter**: TCP, port ≥ 1024, including loopback listeners.
- **Host port clash**: fall back to OS-assigned port and surface the
  remap via `vibepit status`.

## Future work (out of scope)

- Replace the `/proc/net/tcp` poll inside vibed with an event-driven
  source (netlink, inotify on `/proc`, or similar). The wire protocol
  stays the same so the watcher does not change.
- ward status-bar prompt: detect a new port → show
  `Port N detected — [F]orward / [I]gnore` → user accepts → forward
  opens.
- monitor TUI "ports" view: list active forwards with manual close /
  reopen / remap actions.
- Refactored host↔proxy control plane to remove polling end-to-end.
