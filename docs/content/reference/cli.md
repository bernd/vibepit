---
description: Complete reference for vibepit commands, flags, and arguments including run, up, down, connect, exec, status, allow-http, allow-dns, monitor, update, and self-update.
---

# CLI Reference

`vibepit` is a single binary that manages sandbox sessions, network filtering,
and runtime administration. When you run `vibepit` without a subcommand, it
defaults to the [`run`](#run) command.

```
vibepit [global-flags] [command] [command-flags] [arguments]
```

## Global flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--debug` | bool | `false` | Enable debug output |

---

## `run`

Start a new sandbox session or attach to an existing one.

```
vibepit run [flags] [project-path]
```

### Arguments

| Argument | Description |
|----------|-------------|
| `project-path` | Path to the project directory. Defaults to the current working directory. |

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-L`, `--local` | bool | `false` | Use the local `vibepit:latest` image instead of the published one. Required when you [build a custom image](../how-to/troubleshooting.md#sandbox-image-not-found) for an unsupported UID/GID combination. |
| `-a`, `--allow` | string (repeatable) | | Additional `domain:port` entries to allow through the proxy (e.g. `api.example.com:443`) |
| `-p`, `--preset` | string (repeatable) | | Additional network presets to activate |
| `-r`, `--reconfigure` | bool | `false` | Re-run the network preset selector |
| `-P`, `--prompt` | string | auto | Show an allow/deny prompt when the proxy blocks a connection: `inline`, `kitty`, or `off`. By default, a kitty overlay in kitty with remote control enabled, off otherwise. See [Blocked connection prompt](#blocked-connection-prompt). |

### Behavior

- If `project-path` is omitted, `vibepit` uses the current working directory.
- If the directory is inside a Git repository, `vibepit` resolves to the
  repository root and uses that as the project directory.
- `vibepit` refuses to run if the resolved project directory is your home
  directory.
- If a session is already running for the same project directory, `vibepit`
  attaches to it instead of starting a new one.
- On first run in a project, `vibepit` launches an interactive setup flow to
  select network presets. Pass `--reconfigure` to re-run this selector later.
- Entries passed with `--allow` and `--preset` are merged with any entries
  saved in the project configuration file.
- In kitty, blocked connections open an allow/deny prompt over the terminal.
  With `--prompt inline`, they do in any terminal.
  See [Blocked connection prompt](#blocked-connection-prompt).

### Blocked connection prompt

When the proxy blocks a connection, `run` and `connect` can ask you right away
whether to allow it. The prompt closes again once you decide. `--prompt`
selects how it is shown:

| Mode | Description |
|------|-------------|
| `inline` | `vibepit` draws the prompt itself, over the running agent, in the terminal running the sandbox. Works in any terminal. `run` only. [EXPERIMENTAL] |
| `kitty` | The prompt opens in a [kitty](https://sw.kovidgoyal.net/kitty/) overlay window that covers the terminal running the sandbox. `run` falls back to `inline` when kitty remote control is unavailable. |
| `off` | No prompt. |

Without `--prompt`, the kitty overlay switches on automatically when all of
these are true:

- The terminal is kitty and remote control is enabled. Add both settings to
  `kitty.conf`:

    ```
    allow_remote_control socket-only
    listen_on unix:@mykitty
    ```

    The `@` prefix creates an abstract socket, which only exists on Linux. On
    macOS, point `listen_on` at a file path instead:

    ```
    allow_remote_control socket-only
    listen_on unix:/tmp/mykitty
    ```

    kitty appends its process ID to the path, so each kitty instance gets its
    own socket.

- The `kitten` binary is in `PATH`.

Pass `--prompt off` to turn it off. With an explicit mode, `vibepit` exits
with an error if the prompt cannot be set up, for example because the session's
control API is unavailable. Without the flag, it prints a warning and continues
without prompting.

Without `--prompt`, `vibepit` never picks `inline` on its own. Pass
`--prompt inline` to use it.

When the agent is in the middle
of writing a control sequence, the prompt waits for it to finish, up to one
second. Problems with the prompt are logged to `vibepit/prompt-logs/<session>.log`
in your state directory: `$XDG_STATE_HOME` if set, otherwise
`~/.local/state` on Linux and `~/Library/Application Support` on macOS. Each
log holds up to 1 MiB, and is removed 7 days after its last line.

The inline prompt pauses the agent's output while it is shown and replays it
afterwards. The agent is not stopped: output beyond 1 MiB makes its writes wait
until the prompt closes. Keys go to the prompt while it is shown, and to the
agent again afterwards. For an agent on the terminal's normal screen, like a
shell, the screen is restored exactly. A full-screen agent, like an editor, is
asked to redraw by a brief resize.

The prompt shows the blocked domain and port for HTTP(S) requests, or the
domain for DNS queries, together with the reason for the block.

| Key | Action |
|-----|--------|
| `a` | Allow for the rest of the session |
| `A` | Allow and save to the project configuration. Asks again: `y` saves, `q` dismisses, any other key goes back. |
| `n` | Deny. Other clients stop asking about this target for the rest of the session. |
| `Esc`, `q` | Dismiss without deciding. Other clients still ask. |

Behavior details:

- The prompt ignores keys until you press an answer key (`a`, `A`, `n`,
  `q`, `Esc`) on its own: after a short pause, and not followed by more
  typing. It takes effect after a moment, so holding a key does not count
  either. Other keys, like `Enter`, never unlock the prompt. Typing and
  pasting meant for the agent cannot answer it, and do not reach the agent
  either. After that first key, the prompt takes keys as usual.
- The blocked request has already failed when the prompt appears. Retry it
  after allowing.
- Each target prompts at most once per client and session, no matter how often
  the agent retries.
- Prompts for different targets open one after another, never on top of each
  other.
- When several clients are attached to one session, each shows the prompt. As
  soon as one of them allows or denies, the others close within about a second.
- Denied targets are held in memory by the proxy. They are forgotten when the
  session stops. To allow a denied target later, use
  [`allow-http`](#allow-http), [`allow-dns`](#allow-dns), or
  [`monitor`](#monitor).
- IPv6 address targets can be denied or dismissed, but not allowed. The
  allowlist does not support IPv6 literals.

### Examples

```bash
# Start a session in the current directory
vibepit

# Start a session for a specific project
vibepit run ~/projects/my-app

# Use a locally built image
vibepit run -L

# Allow access to an additional domain
vibepit run -a api.example.com:443

# Allow multiple domains and enable a preset
vibepit run -a api.example.com:443 -a cdn.example.com:443 -p vcs-github

# Re-run the network preset selector
vibepit run -r

# Start without the blocked connection prompt in kitty
vibepit run --prompt off

# Prompt for blocked connections in any terminal
vibepit run -P inline
```

---

## `up`

Start a sandbox session in daemon mode. The sandbox and proxy containers run in
the background with an SSH server, and `vibepit` returns immediately after the
session is ready.

```
vibepit up [flags] [project-path]
```

### Arguments

| Argument | Description |
|----------|-------------|
| `project-path` | Path to the project directory. Defaults to the current working directory. |

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-L`, `--local` | bool | `false` | Use the local `vibepit:latest` image instead of the published one. |
| `-a`, `--allow` | string (repeatable) | | Additional `domain:port` entries to allow through the proxy (e.g. `api.example.com:443`) |
| `-p`, `--preset` | string (repeatable) | | Additional network presets to activate |
| `-r`, `--reconfigure` | bool | `false` | Re-run the network preset selector |

### Behavior

- Creates an isolated network, proxy container, and sandbox container, the same
  as [`run`](#run).
- Generates ephemeral SSH keypairs (one for the client, one for the host)
  and stores them in `$XDG_STATE_HOME/vibepit/sessions/<sessionID>/`.
- The sandbox container runs an SSH server on port 2222 (internal). The port is
  forwarded through the proxy container and published to `127.0.0.1` on a random
  host port.
- Waits for the SSH daemon to accept connections before returning.
- If a session is already running for the same project directory, prints a
  message and exits without starting a new one.
- If orphaned containers from a previous session are detected, exits with an
  error asking you to run `vibepit down` first.

### Examples

```bash
# Start a daemon-mode session in the current directory
vibepit up

# Start with a network preset
vibepit up -p vcs-github

# Start for a specific project
vibepit up ~/projects/my-app
```

---

## `down`

Stop and remove sandbox and proxy containers for a session.

```
vibepit down [project-path]
```

### Arguments

| Argument | Description |
|----------|-------------|
| `project-path` | Path to the project directory. Defaults to the current working directory. |

### Behavior

- Finds the running session for the project directory.
- Stops and removes all containers belonging to the session (sandbox and proxy).
- Removes the session network.
- Deletes session credentials (mTLS and SSH keys) from
  `$XDG_STATE_HOME/vibepit/sessions/<sessionID>/`.
- If some containers cannot be removed, credentials are preserved so you can
  retry.
- Also detects orphaned containers (e.g., proxy still running after sandbox
  crashed) and cleans them up.

### Examples

```bash
# Stop the session for the current directory
vibepit down

# Stop the session for a specific project
vibepit down ~/projects/my-app
```

---

## `connect`

Aliases: `c`

Connect to a running sandbox.

```
vibepit connect
```

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-b`, `--bar` | bool | `false` | Enable status bar [EXPERIMENTAL] |
| `-P`, `--prompt` | string | auto | Show an allow/deny prompt when the proxy blocks a connection: `kitty` or `off`. By default, a kitty overlay in kitty with remote control enabled, off otherwise. `inline` is not supported by `connect` yet. See [Blocked connection prompt](#blocked-connection-prompt). |

### Behavior

- Resolves the project root from the current working directory and finds the
  running session.
- Loads the SSH client key from the session credentials directory.
- Connects to `127.0.0.1` on the published SSH port with public key
  authentication.
- Requests a PTY, starts a shell, and forwards terminal resize events
  (`SIGWINCH`).
- When detached sessions exist inside the sandbox, the SSH server presents a
  session selector. You can reattach to a previous session or start a new one.
- In kitty, blocked connections open an allow/deny prompt over the terminal.
  See [Blocked connection prompt](#blocked-connection-prompt).

### Examples

```bash
# Open an interactive shell
vibepit connect

# Connect without the blocked connection prompt in kitty
vibepit connect --prompt=false
```

---

## `exec`

Execute a command in the sandbox.

```
vibepit exec <command...>
```

### Arguments

| Argument | Description |
|----------|-------------|
| `command...` | The remote command to execute. |

### Behavior

- Resolves the project root from the current working directory and finds the
  running session.
- Loads the SSH client key from the session credentials directory.
- Connects to `127.0.0.1` on the published SSH port with public key
  authentication.
- Executes the command on the remote side and returns its exit code. Stdin,
  stdout, and stderr are forwarded.

### Examples

```bash
# Run a single command
vibepit exec ls -la

# Run a command that reads a file
vibepit exec cat /etc/os-release
```

---

## `status`

Show session status.

```
vibepit status [project-path]
```

### Arguments

| Argument | Description |
|----------|-------------|
| `project-path` | Path to the project directory. Defaults to the current working directory. |

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-v`, `--verbose` | bool | `false` | Enable verbose output |

### Output

When run inside a project directory (or given a project path), displays the
session status for that project. When no session is found for the current
project, or when run outside a project directory, displays status for all
running sessions on the machine.

Example output:

```
  Session   cq1abc2def3gh4ij
  Project   /home/user/my-project
  Sandbox   running: vibepit-sandbox-cq1abc2def3gh4ij (up 2m30s)
    Proxy   running: vibepit-proxy-cq1abc2def3gh4ij (up 2m31s)
      API   127.0.0.1:41923
      SSH   127.0.0.1:52847
```

If no sessions are running at all, prints `No active session for <path>` (when
in a project) or `No active sessions` (when outside a project).

---

## `allow-http`

Add HTTP(S) allowlist entries for a running session. By default, entries are
also persisted to the project configuration file so they apply on future runs.

```
vibepit allow-http [flags] <domain:port-pattern>...
```

### Arguments

| Argument | Description |
|----------|-------------|
| `domain:port-pattern` | One or more domain-and-port patterns to allow. Required. The port is not optional — use `example.com:443` for HTTPS, `example.com:80` for HTTP, or `example.com:*` for any port. |

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--no-save` | bool | `false` | Skip persisting the entries to the project config |
| `--session` | string | | Session ID or project path (skips interactive selection) |

### Wildcard semantics

`*` matches exactly one DNS label. `**` matches one or more labels. Both can
appear in any position but at most one `**` per pattern.

| Pattern | Matches | Does not match |
|---|---|---|
| `*.example.com:443` | `api.example.com` | `example.com`, `a.b.example.com` |
| `**.example.com:443` | `api.example.com`, `a.b.example.com` | `example.com` |
| `bedrock.*.amazonaws.com:443` | `bedrock.us-east-1.amazonaws.com` | `bedrock.a.b.amazonaws.com` |

Ports must be an exact number or `*` for any port.

### Examples

```bash
# Allow a single domain
vibepit allow-http api.example.com:443

# Allow all subdomains (one level) of a domain
vibepit allow-http '*.example.com:443'

# Allow multiple entries without saving to config
vibepit allow-http --no-save api.example.com:443 cdn.example.com:443

# Target a specific session
vibepit allow-http --session my-session-id api.example.com:443
```

---

## `allow-dns`

Add DNS allowlist entries for a running session. By default, entries are also
persisted to the project configuration file so they apply on future runs.

```
vibepit allow-dns [flags] <domain-pattern>...
```

### Arguments

| Argument | Description |
|----------|-------------|
| `domain-pattern` | One or more domain patterns to allow DNS resolution for. Required. |

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--no-save` | bool | `false` | Skip persisting the entries to the project config |
| `--session` | string | | Session ID or project path (skips interactive selection) |

### Wildcard semantics

`*` matches exactly one DNS label. `**` matches one or more labels. Both can
appear in any position but at most one `**` per pattern.

| Pattern | Matches | Does not match |
|---|---|---|
| `*.example.com` | `api.example.com` | `example.com`, `a.b.example.com` |
| `**.example.com` | `api.example.com`, `a.b.example.com` | `example.com` |

### Examples

```bash
# Allow DNS resolution for a domain
vibepit allow-dns example.com

# Allow DNS resolution for all subdomains
vibepit allow-dns '*.example.com'

# Allow without saving to config
vibepit allow-dns --no-save example.com
```

---

## `monitor`

Aliases: `m`, `tv`

Open an interactive terminal UI for viewing proxy logs and performing admin
actions on a running session.

```
vibepit monitor [flags]
```

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--session` | string | | Session ID or project path (skips interactive selection) |

### Behavior

- If `--session` is not provided and multiple sessions are running,
  `vibepit` presents an interactive session selector.
- If only one session is running, `vibepit` connects to it directly.

---

## `update`

Update the vibepit binary and pull the latest container images.

```
vibepit update [flags]
```

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--bin` | bool | `false` | Update the binary only (skip image pull) |
| `--images` | bool | `false` | Update container images only (skip binary update) |
| `--check` | bool | `false` | Check for available updates without installing |
| `--list` | bool | `false` | List available releases |
| `--use` | string | | Install a specific version (implies `--bin`) |
| `--pre` | bool | `false` | Use the prerelease channel |
| `-y`, `--yes` | bool | `false` | Skip the confirmation prompt |

### Behavior

By default, `vibepit update` updates both the binary and the container images.
Use `--bin` or `--images` to update only one. The two flags are mutually
exclusive.

**Binary update:**

- Checks the release channel for a newer version and downloads it.
- Verifies the download with SHA-256 checksums and cosign signature bundles
  (when available).
- Replaces the running binary in place.
- If vibepit was installed via a package manager (Homebrew, Snap, Nix, etc.),
  the binary update is skipped with a message to use the package manager instead.
- Use `--use <version>` to install a specific version regardless of what the
  channel reports as latest.
- Use `--pre` to check the prerelease channel instead of the stable channel.

**Image update:**

- Pulls the latest sandbox image for your UID/GID combination (e.g.,
  `ghcr.io/bernd/vibepit:r1-uid-1000-gid-1000`).
- Pulls the latest proxy base image.

### Examples

```bash
# Update everything (binary + images)
vibepit update

# Check if an update is available
vibepit update --check

# List available releases
vibepit update --list

# Update binary only, skip confirmation
vibepit update --bin -y

# Update images only
vibepit update --images

# Install a specific version
vibepit update --use 0.2.0

# Check the prerelease channel
vibepit update --check --pre
```

