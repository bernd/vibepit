---
description: Inspect proxy traffic in real time and manage HTTP, HTTPS, and DNS allowlist entries for a running Vibepit session.
---

# Monitor and Allowlist

These commands need a running session. Start one with `vibepit run` if you
have not already.

## Open the monitor

Launch the interactive monitor to inspect proxy traffic in real time:

```bash
vibepit monitor
```

<div class="vp-cast" data-cast="../../assets/casts/monitor.cast" data-poster="npt:0:13"></div>

The monitor has two tabs. Press **`tab`** to switch between them.

**`HOSTS`** (the default) shows one row per host: each HTTP(S) `domain:port`
and each DNS `domain` gets its own row. Rows show when the host was last seen,
the source (`proxy` or `dns`), how many requests it made, and its status:

- **`blocked`**: the proxy blocked the most recent request, and nobody has
  allowed the host since.
- **`rejected`**: you rejected a held request to the host with `n` this
  session. It stays blocked and is not held again, but an allow still fixes it.
  The summary line counts these as "● N rejected".
- **`denied`**: an enforced deny rule matches the host. The summary line
  counts these separately as "● N denied".
- **`allowed`**: the allowlist admits the host, either by an existing rule or
  by one added since its last request, such as with `vibepit allow-http`.
- **`allowed*`**: you allowed it from this monitor for this session.
- **`saved`**: you allowed it from this monitor and saved it to your project
  configuration.

An enforced deny replaces an allowed status immediately, even before another
request arrives, and the host details show the deny reason.

The most recently active hosts are at the top, so new blocks show up first.
Press **`s`** to cycle the sort order between newest, host name, and count.
Press **`enter`** on a row to show details such as first and last seen, the
allowed/blocked counts, and why the host was blocked.

**`LOGS`** shows the live stream of proxy log entries. Each line shows a
timestamp, source (HTTP or DNS), domain, port (for HTTP entries), and whether
the request was allowed or blocked:

- **`+`**: an existing rule allowed the request.
- **`x`**: the proxy blocked the request.
- **`-`**: an enforced deny rule blocked the request.

While the monitor is open, the proxy holds requests to hosts that are not in
the allowlist for up to `hold-timeout`, so you can decide before the agent sees
a failure. Held requests are yellow on both tabs, and the hosts tab pins held
hosts at the top. Press **`a`** or **`A`** to allow a held request, or **`n`**
(`reject`) to reject it and stop prompts for this target for the rest of the
session. Press **`N`** to enforce a deny and save it. See [Hold blocked requests](configure-presets.md#hold-blocked-requests).

### Allow domains from the monitor

You can add allowlist entries from either monitor tab:

1. Navigate to a blocked host or entry using the arrow keys.
2. Press **`a`** to allow the domain for the current session only.
3. Press **`A`** (shift) to allow the domain **and** save it to your project
   configuration for future sessions.

Nothing is selected until you press a navigation key. The selection then stays
on the host or entry you picked while new traffic arrives. On the hosts tab it
disappears after 6 seconds without a key press, because rows re-sort as traffic
arrives and the allow keys must never act on a row that moved into place on its
own. While the selection is on an expanded row, it stays until you collapse
the row. Log lines never move, so the logs tab keeps its selection and scroll
position while you read. Press **`esc`** to clear the selection right away.
With nothing selected, `esc` returns to the session list.

After you allow a host, its status or entry marker changes, and the footer
confirms the action.

The allow keys only work on requests blocked because the domain is not in the
allowlist. An allowlist entry cannot help when the domain resolves to a blocked
IP range or does not resolve at all. You also cannot allow an explicitly denied
target. If a host you allowed keeps getting blocked for one of those reasons,
its status switches back to `blocked` and its details show the reason.

## Add HTTP(S) allowlist entries

Give the sandbox access to an HTTP or HTTPS endpoint with `allow-http`. Each
entry takes the form `domain:port-pattern`:

```bash
vibepit allow-http api.example.com:443
```

!!! note "The port is required"
    Every `allow-http` entry needs a port pattern.
    Vibepit rejects `vibepit allow-http example.com`. Use
    `example.com:443` for HTTPS, `example.com:80` for HTTP, or
    `example.com:*` for any port.

You can add multiple entries in a single command:

```bash
vibepit allow-http api.example.com:443 registry.npmjs.org:443
```

### Wildcard domains

`*` matches exactly one subdomain label. `**` matches one or more labels:

| Pattern | Matches | Does not match |
|---|---|---|
| `*.example.com:443` | `api.example.com:443` | `example.com:443`, `a.b.example.com:443` |
| `**.example.com:443` | `api.example.com:443`, `a.b.example.com:443` | `example.com:443` |
| `bedrock.*.amazonaws.com:443` | `bedrock.us-east-1.amazonaws.com:443` | `bedrock.a.b.amazonaws.com:443` |

To allow both the apex and all subdomains, add two entries:

```bash
vibepit allow-http example.com:443 "**.example.com:443"
```

### Port patterns

| Pattern | Effect |
|---|---|
| `443` | Matches port 443 only |
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

Wildcards work the same as in HTTP entries: `*.example.com` matches
exactly one subdomain label, `**.example.com` matches one or more labels.
Neither matches the apex domain.

## Deny domains

Enforce a deny immediately and save it to the selected session's project config:

```bash
vibepit deny-http telemetry.example.com:443 '*.tracking.example.com:*'
vibepit deny-dns telemetry.example.com '**.tracking.example.com'
```

Both commands need a running proxy and accept multiple entries. HTTP entries
need a port, and DNS entries have none. They use the wildcard rules above.
A matching deny overrides allows and presets. An HTTP deny does not affect DNS,
and a DNS deny does not affect HTTP. See [Deny rules](configure-presets.md#deny-rules).

In the prompt, **`N`** applies to the displayed target. In either monitor tab,
select a valid HTTP or DNS target and press **`N`** (`deny+save`), including an
allowed host or a request whose hold expired. It denies the exact domain and,
for HTTP, the selected port. Matching held requests fail immediately. The host
status updates to show the deny, and old log entries stay as they were. The
allow keys cannot override an enforced deny.

Lowercase **`n`** (`reject`) only rejects a held request and stops further
prompts for that target during the session. In the monitor it works only on
held requests. It does not install an enforced deny or save a rule, and a later
allow undoes it. Uppercase **`N`** installs an enforced rule that blocks new
matching requests even if you allow the target later.

On success, the output confirms that the deny is active and saved. If saving
fails, the error says the deny is active in the running proxy but not saved. If
the proxy fails to apply the deny, Vibepit does not save it either. To remove a
saved deny, edit its config file and restart the session. Tunnels that are
already open stay open until they close.

## Skip saving to config

By default, these commands save every allow or deny entry to your project
configuration file (`.vibepit/network.yaml`), so it applies to future sessions.
To add an entry for the current session only, pass `--no-save`:

```bash
vibepit allow-http --no-save staging.example.com:443
vibepit allow-dns --no-save staging.example.com
vibepit deny-http --no-save telemetry.example.com:443
vibepit deny-dns --no-save telemetry.example.com
```

`deny-http --no-save` and `deny-dns --no-save` still enforce their rules until
the proxy stops. Unlike lowercase `n`, they block the target even if you allow
it later.

## Target a specific session

With a single running session, `allow-http`, `allow-dns`, `deny-http`,
`deny-dns`, and `monitor` connect to it. If several sessions are running, pick
one with `--session`:

```bash
vibepit monitor --session <session-id>
vibepit allow-http --session <session-id> api.example.com:443
vibepit allow-dns --session <session-id> internal.example.com
vibepit deny-http --session <session-id> telemetry.example.com:443
vibepit deny-dns --session <session-id> telemetry.example.com
```

Retrieve session IDs with:

```bash
vibepit status
```

If several sessions are running and you omit `--session`, a selector asks you
to pick one.

See the [CLI Reference](../reference/cli.md) for full flag details on all
commands.
