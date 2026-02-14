# Monitor and Allowlist

This guide covers inspecting traffic and updating network permissions for a
running Vibepit session. You need at least one active session before using these
commands — start one with `vibepit run` if you have not already.

## Open the monitor

Launch the interactive monitor to inspect proxy traffic in real time:

```bash
vibepit monitor
```

The monitor displays a live stream of proxy log entries. Each line shows a
timestamp, source (HTTP or DNS), domain, port (for HTTP entries), and whether
the request was allowed or blocked:

- **`+`** — request was allowed by an existing rule.
- **`x`** — request was blocked.

### Allow domains from the monitor

You can add allowlist entries directly from the monitor without leaving the TUI:

1. Navigate to a blocked entry using the arrow keys.
2. Press **`a`** to allow the domain for the current session only.
3. Press **`A`** (shift) to allow the domain **and** save it to your project
   configuration for future sessions.

After allowing, the entry marker changes to reflect its new status, and the
footer confirms the action.

## Add HTTP(S) allowlist entries

Grant the sandbox access to an HTTP or HTTPS endpoint with `allow-http`. Each
entry takes the form `domain:port-pattern`:

```bash
vibepit allow-http api.example.com:443
```

!!! note "The port is required"
    Every `allow-http` entry needs a port pattern.
    `vibepit allow-http example.com` will be rejected — use
    `example.com:443` for HTTPS, `example.com:80` for HTTP, or
    `example.com:*` for any port.

You can add multiple entries in a single command:

```bash
vibepit allow-http api.example.com:443 registry.npmjs.org:443
```

### Wildcard domains

A `*.` prefix matches any subdomain but **not** the apex domain itself:

| Pattern | Matches | Does not match |
|---|---|---|
| `*.example.com:443` | `api.example.com:443`, `cdn.example.com:443` | `example.com:443` |

To allow both the apex and all subdomains, add two entries:

```bash
vibepit allow-http example.com:443 "*.example.com:443"
```

### Port patterns

The port segment supports digits and the `*` wildcard:

| Pattern | Effect |
|---|---|
| `443` | Matches port 443 only |
| `80*` | Matches ports 80, 800, 8080, etc. |
| `*` | Matches any port |

## Add DNS allowlist entries

Allow the sandbox to resolve a domain name with `allow-dns`. Entries are domain
patterns without a port:

```bash
vibepit allow-dns internal.example.com
```

Multiple entries work the same way:

```bash
vibepit allow-dns internal.example.com api.example.com
```

Wildcard semantics are identical to HTTP entries: `*.example.com` matches
subdomains only, not the apex domain.

## Skip saving to config

By default, every entry you add is persisted to your project configuration file
(`.vibepit/network.yaml`) so it applies to future sessions automatically. To
add an entry for the current session only, pass `--no-save`:

```bash
vibepit allow-http --no-save staging.example.com:443
vibepit allow-dns --no-save staging.example.com
```

## Target a specific session

When you have a single running session, `allow-http`, `allow-dns`, and
`monitor` connect to it automatically. If multiple sessions are running, specify
which one with `--session`:

```bash
vibepit monitor --session <session-id>
vibepit allow-http --session <session-id> api.example.com:443
vibepit allow-dns --session <session-id> internal.example.com
```

Retrieve session IDs with:

```bash
vibepit sessions
```

When multiple sessions exist and you omit `--session`, an interactive selector
appears so you can choose the target session.

See the [CLI Reference](../reference/cli.md) for full flag details on all
commands.
