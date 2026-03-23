# SSH Session Architecture Design

## Problem

The current `vibepit run` command couples the container lifecycle to the CLI
process. The first `run` starts the sandbox and proxy containers; when it exits,
both containers are stopped and all attached sessions are closed. Re-attaching
uses `docker exec` which lacks features like proper signal forwarding, port
forwarding, and detach support.

## Goals

1. **Lifecycle decoupling** -- containers run independently of any CLI process.
2. **Multi-session support** -- multiple independent shell sessions via SSH
   instead of `docker exec`.
3. **Richer shell features** -- SSH provides proper PTY handling, window resize,
   and command execution that `docker exec` cannot.

## Non-Goals

- SSH agent forwarding, port forwarding, sftp (can be added later).
- Remote/multi-machine access.
- Replacing `vibepit run` (remains unchanged during migration; will become sugar
  for `up && ssh` later).

## Design

### New Commands

Four new commands are added alongside the existing ones:

#### `vibepit up`

Starts the proxy and sandbox containers for the current project directory. Reuses
the existing setup logic from `RunAction` (config loading, image pulling, network
creation, proxy start, sandbox creation) but does not attach a shell. The CLI
exits after containers are running.

The sandbox container entrypoint is overridden at creation time to
`/vibepit vibed` instead of the default shell entrypoint. The vibepit binary is
bind-mounted into the sandbox read-only, same pattern as the proxy container.

Cleanup defers from the current `run` command do not apply -- containers are left
running until explicit `vibepit down`.

Both proxy and sandbox containers are created without a restart policy (no
`RestartPolicyUnlessStopped`). Sessions are ephemeral: they survive CLI exit but
not Docker daemon restart or host reboot. The proxy container's current
`RestartPolicyUnlessStopped` must be removed for `vibepit up` sessions to keep
restart semantics aligned. Session persistence across reboots is a future
concern. Note: the `vibepit run` path retains the existing proxy restart policy
for backward compatibility during migration.

If a session is already running for the project, `vibepit up` prints a message
("session already running") and exits successfully.

On macOS, the vibepit binary mounted into the sandbox is the embedded Linux
binary (extracted from `embed/proxy/`), same as the proxy container pattern in
the existing `RunAction`.

#### `vibepit down`

Finds the running session for the current project directory and performs full
cleanup: stops and removes the sandbox and proxy containers, removes the network,
and removes session credentials from the state directory.

`vibepit down` discovers containers by `vibepit.session-id` label across both
roles (proxy and sandbox). It finds the session ID from whichever container
matches `vibepit.project.dir={projectRoot}`, then queries all containers with
that session ID. Cleanup is best-effort: if only one container exists (the other
crashed or was manually removed), it is cleaned up along with the network and
credentials. The existing `CleanupSessionCredentials` function from
`cmd/session.go` handles credential directory removal.

#### `vibepit ssh`

Connects to the running sandbox via SSH.

- **Interactive mode** (default): requests a PTY with the current terminal size
  and `TERM` value, spawns a login shell. Forwards `SIGWINCH` as SSH
  window-change requests. Puts the local terminal in raw mode.
- **Command mode** (`vibepit ssh -- cmd args`): no PTY, pipes stdout/stderr,
  returns the remote exit code.

The client uses `golang.org/x/crypto/ssh`. No host `ssh` binary is required.

`vibepit ssh` discovers the sandbox container directly by filtering for
`vibepit.role=sandbox` and `vibepit.project.dir={projectRoot}`. This is
independent of the proxy container — if the proxy is gone but the sandbox is
still alive, SSH still works. If no sandbox is running, prints an error and
exits non-zero.

#### `vibepit vibed` (internal)

Internal subcommand that runs inside the sandbox container as the SSH server.
Not intended for direct user invocation (similar to `vibepit proxy`).

### SSH Server (`vibed`)

The `vibed` server uses `charmbracelet/ssh` to provide an SSH server inside the
sandbox container.

**Startup:**
- Loads the host key from `/etc/vibepit/sshd/host-key` (bind-mounted, read-only).
- Reads the authorized client public key from the `VIBEPIT_SSH_PUBKEY`
  environment variable.
- Listens on `0.0.0.0:2222`.

**Session handling:**
- PTY sessions: uses `creack/pty` to spawn `/bin/bash --login` with the
  requested terminal size. Wires stdin/stdout/stderr between the SSH channel and
  the PTY. Handles window-change events by resizing the PTY.
- Command sessions: executes the command directly with argv semantics (no
  shell wrapping). The environment will not include profile/bashrc setup
  (PATH additions, Homebrew shellenv, etc.) — this matches `docker exec`
  behavior. Users who need the login environment can explicitly run
  `vibepit ssh -- bash -lc "cmd"`. Pipes I/O, returns the exit code.
- All sessions run as the `code` user with the standard environment (HOME, proxy
  vars, PATH, etc.). The container's Dockerfile `USER` directive sets `code` as
  the default user; overriding the entrypoint preserves this. The `vibed`
  container is created with `Tty: false` and `OpenStdin: false` (unlike the
  interactive `run` path) since `vibed` is a daemon, not an interactive shell.

**Lifecycle:**
- The sandbox container runs with `Init: true` (docker-init/tini as PID 1).
  `vibed` runs as a child of the init process. Docker-init handles zombie
  reaping for spawned shell processes and forwards SIGTERM to `vibed` on
  container stop.
- `vibepit down` stops the container, which sends SIGTERM to `vibed` via
  docker-init. `vibed` closes all active SSH sessions and exits.

### SSH Client (`vibepit ssh`)

The SSH client is implemented in Go using `golang.org/x/crypto/ssh`.

**Connection flow:**
1. Discover the running sandbox container by project label.
2. Inspect the container to get the published SSH port on `127.0.0.1`.
3. Load the private key from `$XDG_STATE_HOME/vibepit/sessions/{sessionID}/ssh-key`.
4. Load the host public key from
   `$XDG_STATE_HOME/vibepit/sessions/{sessionID}/host-key.pub`.
5. Connect to `127.0.0.1:{publishedPort}` with public key auth and pinned host
   key verification.

**Host key verification:** The client configures `ssh.FixedHostKey()` using the
host public key from the credentials directory. No `~/.ssh/known_hosts`
involvement. Since both keys are generated by `vibepit up`, trust is established
by construction.

**Terminal handling:** The raw terminal mode, resize signal watching, and
bidirectional I/O copy logic from `container/terminal.go` is adapted for SSH
channels. The structure is nearly identical -- swapping the Docker hijacked
connection for an SSH channel.

### Key Management

All keys are generated at `vibepit up` time and stored in the session
credentials directory alongside the existing mTLS certificates.

Session credentials are stored under `$XDG_STATE_HOME/vibepit/sessions/{sessionID}/`
(typically `~/.local/state/vibepit/{sessionID}/`). This survives logout/reboot,
matching the durability of Docker containers. Stale credential directories (from
sessions whose containers were removed by Docker restart or manual cleanup) are
harmless and can be cleaned up opportunistically by `vibepit up` or
`vibepit down`. As part of this work, the existing mTLS credentials used by `monitor` and
`allow-*` also move from `$XDG_RUNTIME_DIR` to `$XDG_STATE_HOME`. The
`sessionBaseDir()` function in `cmd/session.go` changes from `xdg.RuntimeDir`
to `xdg.StateHome`, so all session credentials (mTLS and SSH) share the same
directory and cleanup path.

**Generated at `vibepit up`:**
- `$XDG_STATE_HOME/vibepit/sessions/{sessionID}/ssh-key` -- Ed25519 client private key
  (mode 0600)
- `$XDG_STATE_HOME/vibepit/sessions/{sessionID}/ssh-key.pub` -- client public key
- `$XDG_STATE_HOME/vibepit/sessions/{sessionID}/host-key` -- Ed25519 host private key
  (mode 0600)
- `$XDG_STATE_HOME/vibepit/sessions/{sessionID}/host-key.pub` -- host public key

**Injected into sandbox container:**
- Host keypair bind-mounted as individual files read-only at
  `/etc/vibepit/sshd/host-key` and `/etc/vibepit/sshd/host-key.pub`. The
  Dockerfile must create `/etc/vibepit/sshd/` since the sandbox runs with a
  read-only root filesystem.
- Client public key passed via `VIBEPIT_SSH_PUBKEY` environment variable.

### Network Connectivity

The sandbox SSH port (2222) is published to `127.0.0.1:{random}` on the host,
the same pattern used for the proxy control API port. This works identically on
Linux and macOS (where containers run inside a VM and internal network IPs are
not directly reachable).

Port publishing requires that the container's primary network is `bridge`. The
current sandbox container joins the isolated session network directly. For
`vibepit up`, the sandbox container must use `bridge` as its primary network
(with `PortBindings` and `ExposedPorts` for port 2222), then connect to the
isolated session network afterward via `NetworkConnect` -- the same two-step
pattern used by the proxy container in `StartProxyContainer`.

`vibepit ssh` reads the published port from container inspect and connects to
`127.0.0.1:{port}`. The dynamically allocated host port avoids conflicts when
multiple sessions are running.

### Container Labels

The existing `vibepit.session-id` label (defined as `LabelSessionID` in
`container/client.go`) must be added to sandbox containers. Currently it is only
set on proxy containers. Both proxy and sandbox containers will carry this label,
enabling `vibepit down` to discover both containers for a session reliably.

`SandboxContainerConfig` needs a `SessionID` field added, and
`CreateSandboxContainer` must include the label in the container's label map.

Existing labels remain unchanged:
- `vibepit.project.dir` -- project directory path
- `vibepit.role` -- `proxy` or `sandbox`
- `vibepit.session-id` -- session identifier (already on proxy, added to sandbox)
- `vibepit` -- marker label

### Entrypoint Handling

The Dockerfile entrypoint (`entrypoint.sh`) stays unchanged. It continues to
run home initialization and launch a login shell.

`vibepit up` overrides the entrypoint at container creation time. As a
prerequisite, the home initialization logic currently inline in `entrypoint.sh`
(rsync of home template, `.vibepit-initialized` check) must be factored into an
`init_home` function in `entrypoint-lib.sh`. The `vp_status` dependency from
`lib.sh` must also be available.

The entrypoint override then becomes:
`["/bin/bash", "-c", "source /etc/vibepit/lib.sh && source /etc/vibepit/entrypoint-lib.sh && migrate_linuxbrew_volume && init_home && exec /vibepit vibed"]`

`entrypoint.sh` itself is updated to call the same `init_home` function,
keeping both paths consistent.

`vibepit run` continues to use the default shell entrypoint.

### Changes to Existing Code

**Unchanged:**
- `vibepit run` -- still uses `docker exec` attach path
- `vibepit proxy` -- unchanged
- `vibepit monitor`, `allow-http`, `allow-dns`, `sessions` -- unchanged
  (they call `sessionDir()` which transparently picks up the new base path)
- Network creation, volume management -- unchanged
- mTLS credential generation -- unchanged (only the storage location changes)
- Image Dockerfile -- only addition: `RUN mkdir -p /etc/vibepit/sshd`

**New files:**
- `cmd/up.go` -- `up` command
- `cmd/down.go` -- `down` command
- `cmd/ssh.go` -- `ssh` command
- `cmd/vibed.go` -- `vibed` internal subcommand
- `sshd/` package -- SSH server logic (charmbracelet/ssh, PTY handling, auth)

**Modified files:**
- `cmd/root.go` -- register new commands
- `container/client.go` -- add `SessionID` field and a `Daemon bool` field to
  `SandboxContainerConfig`. When `Daemon` is true, `CreateSandboxContainer`
  sets `Tty: false`, `OpenStdin: false`, uses bridge as primary network with
  SSH port publishing, connects to the isolated network via `NetworkConnect`,
  and applies the overridden entrypoint. Add `vibepit.session-id` label to
  `CreateSandboxContainer`. New methods for sandbox discovery by role label,
  session discovery by session ID, and reading the published SSH port.
- `cmd/session.go` -- change `sessionBaseDir()` to return
  `$XDG_STATE_HOME/vibepit/sessions` (instead of `$XDG_RUNTIME_DIR/vibepit`).
- `image/entrypoint-lib.sh` -- add `init_home` function (extracted from
  `entrypoint.sh`).
- `image/entrypoint.sh` -- call `init_home` instead of inline logic.
- Possibly a key generation helper alongside `proxy/mtls.go` or in a shared
  package.

### New Dependencies

- `charmbracelet/ssh` -- SSH server library
- `creack/pty` -- PTY allocation for shell spawning in `vibed`
- `golang.org/x/crypto/ssh` -- SSH client library (likely already a transitive
  dependency)

## Migration Path

1. Ship `up`, `down`, `ssh`, and `vibed` as new commands alongside `run`.
2. Users can try the new workflow: `vibepit up` then `vibepit ssh`.
3. Once validated, `vibepit run` becomes sugar for `vibepit up && vibepit ssh`.
4. The `docker exec` attach path in `run` can be removed.
