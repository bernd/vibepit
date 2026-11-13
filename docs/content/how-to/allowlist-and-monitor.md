---
description: Inspect proxy traffic in real time and manage HTTP, HTTPS, and DNS allowlist entries for a running Vibepit session.
---

# Monitor and Allowlist

You need at least one active session before using these commands — start one
with `vibepit run` if you have not already.

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

- **`blocked`** — the most recent request was blocked and has not been allowed
  since.
- **`rejected`** — you rejected a held request to the host with `n` this
  session. It stays blocked and is not held again, but an allow still fixes it.
  The summary line counts these as "● N rejected".
- **`denied`** — an enforced deny rule matches the host. The summary line
  counts these separately as "● N denied".
- **`allowed`** — the allowlist admits the host, either by an existing rule or
  by one added since its last request, such as with `vibepit allow-http`.
- **`allowed*`** — you allowed it from this monitor for this session.
- **`saved`** — you allowed it from this monitor and saved it to your project
  configuration.

An enforced deny overrides allow status immediately, even before another request
arrives, and the host details show the deny reason.

The most recently active hosts are at the top, so new blocks show up first.
Press **`s`** to cycle the sort order between newest, host name, and count.
Press **`enter`** on a row to show details such as first and last seen, the
allowed/blocked counts, and why the host was blocked.

**`LOGS`** shows the live stream of proxy log entries. Each line shows a
timestamp, source (HTTP or DNS), domain, port (for HTTP entries), and whether
the request was allowed or blocked:

- **`+`** — request was allowed by an existing rule.
- **`x`** — request was blocked.
- **`-`** — request was blocked by an enforced deny rule.

While the monitor is open, the proxy holds requests to hosts that are not in
the allowlist for up to `hold-timeout`, so you can decide before the agent sees
a failure. Held requests are yellow on both tabs, and the hosts tab pins held
hosts at the top. Press **`a`** or **`A`** to allow a held request, or **`n`**
(`reject`) to reject it and suppress prompts for this target for the session. Press **`N`**
to enforce a deny and save it. See [Hold blocked requests](configure-presets.md#hold-blocked-requests).

### Allow domains from the monitor

You can add allowlist entries directly from the monitor without leaving the TUI,
on either tab:

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
position while you read. Press **`esc`** to clear the selection right away;
with nothing selected, `esc` returns to the session list.

After allowing, the status or entry marker changes to reflect it, and the
footer confirms the action.

The allow keys are only offered for requests blocked because the domain is not
in the allowlist. Requests blocked because the domain resolves to a blocked IP
range or could not be resolved cannot be fixed with an allowlist entry.
Explicitly denied targets cannot be allowed either. If a host you allowed keeps getting blocked for one of those reasons, its status
switches back to `blocked` and its details show the reason.

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

Wildcard semantics are identical to HTTP entries: `*.example.com` matches
exactly one subdomain label, `**.example.com` matches one or more labels.
Neither matches the apex domain.

## Deny domains

Enforce a deny immediately and save it to the selected session's project config:

```bash
vibepit deny-http telemetry.example.com:443 '*.tracking.example.com:*'
vibepit deny-dns telemetry.example.com '**.tracking.example.com'
```

Both commands require a running proxy and accept multiple entries. HTTP entries
require a port; DNS entries have no port. They use the wildcard rules above.
Matching denies override allows and presets. HTTP and DNS denies have independent
scope; see [Deny rules](configure-presets.md#deny-rules).

In the prompt, **`N`** applies to the displayed target. In either monitor tab,
select a valid HTTP or DNS target and press **`N`** (`deny+save`), including an
allowed host or a request whose hold expired. It denies the exact domain and,
for HTTP, the selected port. Matching held requests fail immediately. Live host
status reflects the deny; old log entries remain historical records. The allow
keys cannot override an enforced deny.

Lowercase **`n`** (`reject`) only rejects a held request and suppresses further prompts
for that target during the session; in the monitor it is available only on held
requests. It does not install an enforced deny or save a rule. An allow can
clear this suppression. Uppercase **`N`** installs an enforced rule that prevents
new matching requests even if allowed later.

Success confirms the deny and save. If saving fails, the error says the runtime
deny remains active but was not saved. A runtime failure does not save the rule.
Remove a saved deny by editing its config file and restarting the session.
Existing established tunnels continue until they close.

## Skip saving to config

By default, every allow or deny entry you add with these commands is persisted
to your project configuration file
(`.vibepit/network.yaml`) so it applies to future sessions automatically. To
add an entry for the current session only, pass `--no-save`:

```bash
vibepit allow-http --no-save staging.example.com:443
vibepit allow-dns --no-save staging.example.com
vibepit deny-http --no-save telemetry.example.com:443
vibepit deny-dns --no-save telemetry.example.com
```

`deny-http --no-save` and `deny-dns --no-save` still enforce their rules for
the lifetime of the running proxy. They are stronger than lowercase `n` prompt
suppression.

## Target a specific session

When you have a single running session, `allow-http`, `allow-dns`, `deny-http`,
`deny-dns`, and `monitor` connect to it automatically. If multiple sessions are
running, specify which one with `--session`:

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

When multiple sessions exist and you omit `--session`, an interactive selector
appears so you can choose the target session.

See the [CLI Reference](../reference/cli.md) for full flag details on all
commands.
