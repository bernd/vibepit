# CLI Reference

`vibepit` defaults to the `run` command.

## Root Command

```bash
vibepit [global-flags] [command] [command-flags]
```

Global flags:

- `--debug`: enable debug output

## Commands

### `run`

Start or attach to a sandbox session.

```bash
vibepit run [flags] [project-path]
```

Flags:

- `-L`, `--local`: use local `vibepit:latest` image
- `-a`, `--allow`: add `domain:port` entries
- `-p`, `--preset`: enable additional network presets
- `-r`, `--reconfigure`: rerun setup selector

### `allow-http`

Add HTTP(S) allowlist entries for a running session.

```bash
vibepit allow-http [--session <id>] [--no-save] <domain:port-pattern>...
```

### `allow-dns`

Add DNS allowlist entries for a running session.

```bash
vibepit allow-dns [--session <id>] [--no-save] <domain-pattern>...
```

### `sessions`

List active sessions.

```bash
vibepit sessions
```

### `monitor`

Open the interactive monitor for logs and admin actions.

```bash
vibepit monitor [--session <id>]
```

### `update`

Update binary and pull latest container image.

```bash
vibepit update
```

### `proxy`

Internal command used inside the proxy container.
