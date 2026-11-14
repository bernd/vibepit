---
description: Manage network presets, allowlist entries, and host port access in Vibepit project and global configuration files.
---

# Configure Network Presets

Vibepit filters all network traffic from the sandbox container through a proxy.
Only domains you allow, through presets or individual entries, are reachable,
and a matching deny rule blocks even those. Configuration lives in YAML files at
the project and global level.

## The project config file

Project network configuration lives in `.vibepit/network.yaml` at your
project root. Vibepit hides the `.vibepit` directory inside the sandbox, so the
agent cannot read or change its own allowlist rules. A typical file looks like
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

- **`presets`**: named bundles of domains for common ecosystems. For example,
  `pkg-go` covers Go module proxies and the Go playground.
- **`allow-http`**: individual `domain:port` entries the HTTP proxy allows.
- **`allow-dns`**: domains the sandbox DNS service may resolve.
- **`deny-http`**: `domain:port` patterns the HTTP proxy blocks.
- **`deny-dns`**: domain patterns the sandbox DNS service blocks.

## First-run preset selector

The first time you run `vibepit` in a project that has no
`.vibepit/network.yaml`, an interactive preset selector appears. It:

1. Scans project files to detect matching presets (for example, a
   `go.mod` file triggers the `pkg-go` preset).
2. Pre-selects the `default` preset and any detected presets.
3. Lets you toggle other presets before you confirm.

After you confirm, the selector writes `.vibepit/network.yaml` with your
choices.

## Reconfigure presets

To run the preset selector again:

```bash
vibepit run --reconfigure
```

Or use the short flag:

```bash
vibepit run -r
```

The selector opens with your current presets checked. After you confirm, it
rewrites the file with the new presets and keeps the existing `allow-http`,
`allow-dns`, `deny-http`, and `deny-dns` entries.

## Manual entries

You can add `allow-http` and `allow-dns` entries to the config file by hand.
They use the same wildcard syntax as the CLI commands:

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

Vibepit merges the global and project deny lists and removes duplicates. Rules
use the same syntax as the matching allow lists, and Vibepit validates them
before it starts any container. A matching deny overrides every allow,
including presets, CLI allows, and the special `host.vibepit` allowance.
Explicitly denied requests fail right away and never wait in the hold queue or
open a prompt.

Denies also follow aliases. If an allowed name has a denied name in its CNAME
chain, Vibepit refuses it. DNS queries are checked against `deny-dns`, and
proxied requests against `deny-http`. With HTTP deny rules configured, the proxy makes
one extra DNS query per allowed request to see the chain, and refuses the
request if that query fails.

The two protocols are independent. An HTTP deny does not block DNS resolution,
and a DNS deny applies to the sandbox DNS service, not to the HTTP proxy's own
upstream lookups. Add both rules to block a host on both paths.

Use [`deny-http` or `deny-dns`](allowlist-and-monitor.md#deny-domains) to apply
rules to a running proxy and save them, or press `N` in the prompt or monitor.
Saving a rule keeps unrelated YAML keys and comments, and creates the project
config file if it is missing. Manual edits take effect after you restart the
session. To remove a saved deny, edit the file that contains it and restart.
Adding an allow cannot override it. A deny applies to new requests and does
not close tunnels that are already open.

## Allow host ports

By default, the sandbox cannot reach services running on your host machine,
because the CIDR blocklist covers private IP ranges. The `allow-host-ports`
setting makes an exception for the ports you list.

Inside the sandbox, the hostname `host.vibepit` resolves to your host machine.
The proxy forwards requests to `host.vibepit` on a listed port to the same port
on the host, skipping the CIDR blocklist. It blocks requests to unlisted ports.

Use this when your project depends on a local service, such as a database or a
development API server:

```yaml
allow-host-ports:
  - 3000
  - 5432
```

With this configuration, `curl http://host.vibepit:3000` works inside the
sandbox, but `curl http://host.vibepit:8080` is blocked.

`allow-host-ports` is a project config setting only. The global config and CLI
flags don't support it.

## Hold blocked requests

While a `--prompt` overlay or the `monitor` is attached, the proxy doesn't
fail a request to an unlisted host right away. It holds the request for up to
`hold-timeout`, so you can allow the host before the agent sees an error. The
default is `10s`:

```yaml
hold-timeout: 15s
```

The value is a Go duration such as `10s` or `1m`. Use `0` to turn holding
off, so blocked requests fail right away, as they do without a prompt.
`hold-timeout` works in both the global and the project config, and the
project value overrides the global one.

The proxy holds only HTTP and HTTPS requests, never DNS lookups. Without an
attached prompt or monitor, it holds nothing. See the
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

The value must be `true` or `false`. The flag overrides it, so `--prompt=false`
turns the prompt off for one session.

`prompt` is a global config setting only. Whether to prompt is a personal
preference, and project configs are usually committed to the repository, so
Vibepit ignores `prompt` in a project config.

With the prompt on by default, every session
[holds blocked requests](#hold-blocked-requests) for up to `hold-timeout`
instead of failing them right away.

If Vibepit can't set up a prompt that only the config asks for, it prints a
warning and starts the session without it. With `--prompt` the command
stops with an error instead.

## Limit sandbox memory

The `memory` setting caps the memory available to the sandbox container, like
`docker run --memory`. Values use the same format, such as `512m` or `16g`:

```yaml
memory: 16g
```

`memory` works in both the global and the project config. The `--memory` CLI
flag overrides both, and the project value overrides the global one. If none is
set, the container has no memory limit. Set `memory: 0` (or `--memory 0`) to
remove a limit set by a lower-precedence config. The smallest accepted limit is
`6m`, and Vibepit reads a bare number as bytes.

Vibepit applies the limit when it creates the sandbox container. If you run
`vibepit run --memory` while a session is already running, Vibepit attaches to
the existing container and warns that it ignored the flag. Run `vibepit down`
first to apply a new limit.

Some things count toward the limit or change how it behaves:

- `/tmp` in the sandbox is a tmpfs, so files written there use container
  memory. Large temporary files can push the sandbox into the limit.
- Like `docker run --memory`, swap is not limited separately. If the host has
  swap enabled, the container can use up to the same amount again in swap.
- Rootless Docker and rootless Podman can only enforce memory limits on hosts
  with cgroup v2. On cgroup v1 the runtime ignores the limit, and Vibepit warns
  at startup that it was not applied.

## Global config

Global settings apply to every project. The global config file is:

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

Vibepit does not merge all settings the same way. Each key follows its own
rules:

| Key | Source |
|---|---|
| `presets` | Project config. Expanded into HTTP allow entries after loading. |
| `allow-http` | Global config + project config + CLI flags, then preset entries appended after explicit entries. |
| `allow-dns` | Global config + project config. No CLI or preset layer. |
| `deny-http` | Global config + project config, deduplicated; overrides matching HTTP allows. |
| `deny-dns` | Global config + project config, deduplicated; overrides matching DNS allows. |
| `block-cidr` | Global config only. Adds custom ranges to the default IP blocklist. |
| `allow-cidr` | Global config only. Overrides the blocklist for the listed ranges. See the [security model](../explanations/security-model.md#cidr-blocking) for the risks. |
| `allow-host-ports` | Project config only. |
| `hold-timeout` | Project config, else global config, else `10s`. |
| `prompt` | CLI flag, else global config, else `false`. Not read from the project config. |
| `memory` | CLI flag, else project config, else global config. |

## Further reading

See the [Network Presets](../reference/presets.md) reference for the full
preset catalog, including which domains each preset covers.
