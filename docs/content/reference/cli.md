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

A leading `*.` matches any subdomain but **not** the apex domain itself.
For example, `*.example.com:443` allows `api.example.com:443` and
`cdn.example.com:443`, but does not allow `example.com:443`.

### Examples

```bash
# Allow a single domain
vibepit allow-http api.example.com:443

# Allow all subdomains of a domain
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

A leading `*.` matches any subdomain but **not** the apex domain itself.
For example, `*.example.com` allows DNS resolution for `api.example.com` and
`cdn.example.com`, but does not allow `example.com`.

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

- Pulls the latest sandbox image for your UID/GID combination (e.g., `ghcr.io/bernd/vibepit:main-uid-1000-gid-1000`).
- Pulls the latest proxy base image (`gcr.io/distroless/base-debian13:latest`).

---

## Environment variables

The following environment variables are set automatically inside the sandbox
container to route traffic through the filtering proxy:

| Variable | Value |
|----------|-------|
| `HTTP_PROXY` | `http://<proxy-ip>:<proxy-port>` |
| `HTTPS_PROXY` | `http://<proxy-ip>:<proxy-port>` |
| `http_proxy` | `http://<proxy-ip>:<proxy-port>` |
| `https_proxy` | `http://<proxy-ip>:<proxy-port>` |

These variables are set to the same value. Both uppercase and lowercase variants
are provided for compatibility with different tools and libraries.
