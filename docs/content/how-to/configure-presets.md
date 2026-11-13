---
description: Manage network presets, allowlist entries, and host port access in Vibepit project and global configuration files.
---

# Configure Network Presets

Vibepit filters all network traffic from the sandbox container through a proxy.
Only domains you explicitly allow — via presets or individual entries — are
reachable, unless a matching deny rule blocks them. Configuration lives in YAML
files at the project and global level.

## The project config file

Project network configuration is stored in `.vibepit/network.yaml` at your
project root. The `.vibepit` directory is hidden inside the sandbox, so the
agent cannot read or modify its own allowlist rules. A typical file looks like
this:

```yaml
presets:
  - default
  - pkg-go

allow-http:
  - api.openai.com:443

allow-dns:
  - internal.corp.example.com
```

- **`presets`** — named bundles of domains for common ecosystems (e.g.,
  `pkg-go` covers Go module proxies and the Go playground).
- **`allow-http`** — individual `domain:port` entries the HTTP proxy allows.
- **`allow-dns`** — domains the sandbox DNS service may resolve.
- **`deny-http`** — `domain:port` patterns blocked by the HTTP proxy.
- **`deny-dns`** — domain patterns blocked by the sandbox DNS service.

## First-run preset selector

The first time you run `vibepit` in a project that has no
`.vibepit/network.yaml`, an interactive preset selector appears. It:

1. Scans project files to auto-detect relevant presets (for example, a
   `go.mod` file triggers the `pkg-go` preset).
2. Pre-selects the `default` preset and any detected presets.
3. Lets you toggle additional presets before confirming.

After you confirm, the selector writes `.vibepit/network.yaml` with your
choices.

## Reconfigure presets

To re-run the interactive preset selector at any time:

```bash
vibepit run --reconfigure
```

Or use the short flag:

```bash
vibepit run -r
```

The selector opens with your current presets pre-checked. After you confirm,
the file is rewritten with the new preset selection. Existing `allow-http`,
`allow-dns`, `deny-http`, and `deny-dns` entries are preserved.

## Manual entries

You can add `allow-http` and `allow-dns` entries directly to the config file.
These entries use the same wildcard syntax as the CLI commands:

```yaml
allow-http:
  - api.example.com:443
  - "*.cdn.example.com:443"
  - "**.example.com:443"

allow-dns:
  - "*.internal.example.com"
```

`*` matches exactly one subdomain label, `**` matches one or more labels, and
neither matches the apex domain. Ports must be an exact number or `*` for any
port. See the
[Monitor and Allowlist](allowlist-and-monitor.md) guide for full wildcard
details.

## Deny rules

Both the global config and `.vibepit/network.yaml` accept deny rules:

```yaml
deny-http:
  - telemetry.example.com:443
  - '*.tracking.example.com:*'
deny-dns:
  - telemetry.example.com
  - '**.tracking.example.com'
```

Global and project deny lists are merged and deduplicated. Rules use the same
syntax as their corresponding allow lists and are validated before containers
start. A matching deny overrides every allow, including presets, CLI allows,
and the special `host.vibepit` allowance. Explicitly denied requests fail
without entering the hold queue or opening a prompt.

Denies also follow aliases: an allowed name whose CNAME chain includes a
denied name is refused, for DNS queries against `deny-dns` and for proxied
requests against `deny-http`. With HTTP deny rules configured, the proxy makes
one extra DNS query per allowed request to see the chain, and refuses the
request if that query fails.

The two protocols are independent: an HTTP deny does not deny DNS resolution,
and a DNS deny governs the sandbox DNS service, not the HTTP proxy's own
upstream resolution. Add both rules when you want both scopes blocked.

Use [`deny-http` or `deny-dns`](allowlist-and-monitor.md#deny-domains) to apply
rules to a running proxy and save them, or press `N` in the prompt or monitor.
Saved rules preserve unrelated YAML keys and comments; missing project config
files are created. Manual edits take effect after restarting the session.
To remove a saved deny, edit the file that contains it and restart; adding an
allow cannot override it. Existing established tunnels are not terminated;
denies govern new requests.

## Allow host ports

By default, the sandbox cannot reach services running on your host machine —
private IP ranges are blocked by the CIDR blocklist. The `allow-host-ports`
setting creates a controlled exception for specific ports.

Inside the sandbox, the hostname `host.vibepit` resolves to your host machine.
Requests to `host.vibepit` on a listed port bypass the CIDR blocklist and are
forwarded to the corresponding port on the host. Requests to unlisted ports are
blocked.

This is useful when your project depends on a local service — for example, a
database or a development API server:

```yaml
allow-host-ports:
  - 3000
  - 5432
```

With this configuration, `curl http://host.vibepit:3000` works inside the
sandbox, but `curl http://host.vibepit:8080` is blocked.

`allow-host-ports` is a project config setting only — it is not available in the
global config or via CLI flags.

## Hold blocked requests

While a `--prompt` overlay or the `monitor` is attached, the proxy doesn't
fail a request to an unlisted host right away. It holds the request for up to
`hold-timeout`, so you can allow the host before the agent sees an error. The
default is `10s`:

```yaml
hold-timeout: 15s
```

The value is a Go duration such as `10s` or `1m`. Use `0` to turn holding
off, so blocked requests fail right away as without a prompt.
`hold-timeout` works in both the global and the project config, and the
project value overrides the global one.

Only HTTP and HTTPS requests are held, never DNS lookups. Without an attached
prompt or monitor nothing is held. See the
[blocked connection prompt](../reference/cli.md#blocked-connection-prompt)
for what you see while a request waits.

## Prompt by default

To get the
[blocked connection prompt](../reference/cli.md#blocked-connection-prompt)
in every `run` and `connect` session without passing `--prompt`, set this in
the [global config](#global-config):

```yaml
prompt: true
```

The value must be `true` or `false`. The flag overrides it: `--prompt=false`
turns the prompt off for one session.

`prompt` is a global config setting only. Whether to prompt is your own
preference, so in a project config, which is usually shared with the
repository, it has no effect.

With the prompt on by default, every session
[holds blocked requests](#hold-blocked-requests) for up to `hold-timeout`
instead of failing them right away.

If vibepit can't set up a prompt that only the config asks for, it prints a
warning and starts the session without it. With `--prompt` the command
stops with an error instead.

## Limit sandbox memory

The `memory` setting caps the memory available to the sandbox container,
equivalent to `docker run --memory`. Values use the same format, such as
`512m` or `16g`:

```yaml
memory: 16g
```

`memory` works in both the global and the project config. The `--memory` CLI
flag overrides both, and the project value overrides the global one. If none is
set, the container has no memory limit. Set `memory: 0` (or `--memory 0`) to
lift a limit that a lower-precedence config imposes. The smallest accepted
limit is `6m`; a bare number is interpreted as bytes.

The limit is applied when the sandbox container is created. Running
`vibepit run --memory` against a session that is already running attaches to
the existing container and warns that the flag was ignored; use `vibepit down`
first to apply a new limit.

A few things count toward the limit or change how it behaves:

- `/tmp` in the sandbox is a tmpfs, so files written there use container
  memory. Large temporary files can push the sandbox into the limit.
- Like `docker run --memory`, swap is not limited separately. If the host has
  swap enabled, the container can use up to the same amount again in swap.
- Rootless Docker and rootless Podman can only enforce memory limits on hosts
  with cgroup v2. On cgroup v1 the limit is ignored, and vibepit warns at
  startup that the runtime did not apply it.

## Global config

Global settings apply to every project. The global config file is located at:

```
$XDG_CONFIG_HOME/vibepit/config.yaml
```

It supports the following keys:

```yaml
allow-http:
  - api.example.com:443

allow-dns:
  - internal.corp.example.com

deny-http:
  - telemetry.example.com:443

deny-dns:
  - telemetry.example.com

block-cidr:
  - 10.0.0.0/8

allow-cidr:
  - 100.64.0.0/10

memory: 16g

hold-timeout: 15s

prompt: true
```

## Where each setting comes from

Each configuration key has a specific source. Settings are not merged
uniformly — each key follows its own rules:

| Key | Source |
|---|---|
| `presets` | Project config. Expanded into HTTP allow entries after loading. |
| `allow-http` | Global config + project config + CLI flags, then preset entries appended after explicit entries. |
| `allow-dns` | Global config + project config. No CLI or preset layer. |
| `deny-http` | Global config + project config, deduplicated; overrides matching HTTP allows. |
| `deny-dns` | Global config + project config, deduplicated; overrides matching DNS allows. |
| `block-cidr` | Global config only. Adds custom ranges to the default IP blocklist. |
| `allow-cidr` | Global config only. Overrides the blocklist for the listed ranges — see the [security model](../explanations/security-model.md#cidr-blocking) for the risks. |
| `allow-host-ports` | Project config only. |
| `hold-timeout` | Project config, else global config, else `10s`. |
| `prompt` | CLI flag, else global config, else `false`. Not read from the project config. |
| `memory` | CLI flag, else project config, else global config. |

## Further reading

See the [Network Presets](../reference/presets.md) reference for the full
preset catalog, including which domains each preset covers.
