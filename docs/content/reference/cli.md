---
description: Complete reference for vibepit commands, flags, and arguments including run, up, down, ssh, status, allow-http, allow-dns, sessions, monitor, and update.
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

## `ssh`

Connect to a running sandbox via SSH.

```
vibepit ssh [command...]
```

### Arguments

| Argument | Description |
|----------|-------------|
| `command...` | Optional remote command to execute. If omitted, opens an interactive shell. |

### Behavior

- Resolves the project root from the current working directory and finds the
  running session.
- Loads the SSH client key from the session credentials directory.
- Connects to `127.0.0.1` on the published SSH port with public key
  authentication.
- **Interactive mode** (no arguments): requests a PTY, starts a shell, and
  forwards terminal resize events (`SIGWINCH`).
- **Command mode** (arguments given): executes the command on the remote side
  and returns its exit code. Stdin, stdout, and stderr are forwarded.
- When connecting interactively and detached sessions exist inside the sandbox,
  the SSH server presents a session selector. You can reattach to a previous
  session or start a new one.

### Examples

```bash
# Open an interactive shell
vibepit ssh

# Run a single command
vibepit ssh ls -la

# Run a command with pipes (quote to avoid local shell interpretation)
vibepit ssh cat /etc/os-release
```

---

## `status`

Show session status for the current project.

```
vibepit status [project-path]
```

### Arguments

| Argument | Description |
|----------|-------------|
| `project-path` | Path to the project directory. Defaults to the current working directory. |

### Output

Displays the session ID, project directory, per-container status with uptime,
and published ports (control API and SSH).

Example output:

```
  Session   cq1abc2def3gh4ij
  Project   /home/user/my-project
  Sandbox   running: vibepit-sandbox-cq1abc2def3gh4ij (up 2m30s)
    Proxy   running: vibepit-proxy-cq1abc2def3gh4ij (up 2m31s)
      API   127.0.0.1:41923
      SSH   127.0.0.1:52847
```

If no session is running, prints `No running session for <path>`.

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

## `sessions`

List all active sandbox sessions.

```
vibepit sessions
```

### Output format

Each line contains three columns:

| Column | Description |
|--------|-------------|
| Session ID | Unique identifier for the session |
| Project directory | Absolute path to the project |
| Control port | Host port for the session's control API |

Example output:

```
cq1abc2def3gh4ij   /home/user/my-project (port 41923)
```

If no sessions are running, the output is `No active sessions.`

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

Pull the latest sandbox and proxy container images.

```
vibepit update
```

### Behavior

- Pulls the latest sandbox image for your UID/GID combination (e.g., `ghcr.io/bernd/vibepit:r1-uid-1000-gid-1000`).
- Pulls the latest proxy base image (`gcr.io/distroless/base-debian13:latest`).

