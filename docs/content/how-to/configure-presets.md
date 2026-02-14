# Configure Network Presets

This guide explains how to configure which domains your sandbox can reach,
using presets and manual allowlist entries.

Vibepit filters all network traffic from the sandbox container through a proxy.
Only domains you explicitly allow — via presets or individual entries — are
reachable. Configuration lives in YAML files at the project and global level.

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
- **`allow-dns`** — domains that need DNS resolution but do not go through the
  HTTP proxy.

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
the file is rewritten with the new preset selection. Existing `allow-http` and
`allow-dns` entries are preserved.

## Manual entries

You can add `allow-http` and `allow-dns` entries directly to the config file.
These entries use the same wildcard syntax as the CLI commands:

```yaml
allow-http:
  - api.example.com:443
  - "*.cdn.example.com:443"
  - staging.example.com:80*

allow-dns:
  - "*.internal.example.com"
```

A `*.` prefix matches any subdomain but not the apex domain. Port patterns
support digits and `*` as a wildcard. See the
[Monitor and Allowlist](allowlist-and-monitor.md) guide for full wildcard
details.

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

block-cidr:
  - 10.0.0.0/8
```

## Where each setting comes from

Each configuration key has a specific source. Settings are not merged
uniformly — each key follows its own rules:

| Key | Source |
|---|---|
| `presets` | Project config. Expanded into HTTP allow entries after loading. |
| `allow-http` | Global config + project config + CLI flags, then preset entries appended after explicit entries. |
| `allow-dns` | Global config + project config. No CLI or preset layer. |
| `block-cidr` | Global config only. |
| `allow-host-ports` | Project config only. |

## Further reading

See the [Network Presets](../reference/presets.md) reference for the full
preset catalog, including which domains each preset covers.
