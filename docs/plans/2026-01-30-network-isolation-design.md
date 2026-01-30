# Network Isolation Design

Replace the `bin/vibepit` shell script with a Go CLI that orchestrates a
private container network. All internet access from the development container
passes through a filtering proxy. DNS queries are also filtered.

## Goals

- Prevent malicious agent exfiltration of sensitive project data.
- Prevent compromised packages from phoning home to unauthorized domains.
- Maintain a good developer experience with clear error messages and easy
  allowlist management.

## Architecture

The Go CLI has three modes:

- `vibepit` (default) -- orchestrates two containers and a private Docker
  network, then attaches an interactive shell.
- `vibepit proxy` -- runs inside the proxy container. HTTP CONNECT proxy, DNS
  server, and control API.
- `vibepit monitor` -- connects to the running proxy and streams logs, shows
  blocked domains, and provides interactive commands.

```
 Terminal 1                    Terminal 2 (optional)
 +--------------+              +------------------+
 | vibepit       |              | vibepit monitor   |
 | (interactive  |              | (proxy logs,      |
 |  shell into   |              |  blocked domains, |
 |  dev container)|              |  admin commands)  |
 +------+--------+              +------+-----------+
        |                              |
 +------v--------+   +----------------v+
 | vibepit        |   | proxy container  |
 | container      |   | (scratch image)  |
 | vibepit-net    |   | bridge +         |
 | only           |   | vibepit-net      |
 +----------------+   +-----------------+
```

The proxy container connects to both the default bridge network (internet
access) and a private `vibepit-net` (created with `--internal`). The vibepit
container connects only to `vibepit-net`.

### Proxy container

The proxy container is a scratch container with no OS. The host's `vibepit`
binary is mounted into the container and executed in proxy mode. Configuration
is mounted as a file. No custom image needs to be built or published.

### Container image

Everything inside the `image/` directory stays unchanged. The existing
Dockerfile, entrypoint, installer scripts, and shell profile are not modified.

## Proxy Server

The `vibepit proxy` subcommand starts three servers inside the proxy container.

### HTTP CONNECT proxy (port 3128)

Listens for HTTP CONNECT requests. Extracts the target domain and checks it
against the merged allowlist. Allowed requests get a TCP tunnel to the target.
Blocked requests receive an HTTP 403 response with a message like:

    domain "evil.com" is not in the allowlist
    add it to .vibepit/network.yaml or run: vibepit monitor

Also handles plain HTTP requests (non-CONNECT) with the same domain filtering.
Every request (allowed and blocked) is logged to an internal ring buffer with
domain, timestamp, and outcome.

### DNS server (port 53)

Listens for UDP and TCP DNS queries. Checks the queried domain against the
merged allowlist and the `dns-only` list. Allowed domains are forwarded to an
upstream resolver and the response is returned. Blocked domains receive
NXDOMAIN. Queries are logged the same way as proxy requests.

### Control API (port 3129)

HTTP API accessible on `vibepit-net` only.

- `GET /logs` -- stream log entries.
- `GET /stats` -- blocked and allowed counts per domain.
- `GET /config` -- current merged allowlist.
- Future: `POST /allow` for temporary allowlist additions.

This API is what `vibepit monitor` connects to. A future local web UI can use
the same API.

## Domain Matching

Two matching modes:

- **Exact with automatic subdomains.** Listing `github.com` allows
  `github.com` and all subdomains like `api.github.com`,
  `raw.github.com`, etc.
- **Wildcard syntax.** Listing `*.example.com` allows subdomains of
  `example.com` but not `example.com` itself.

Both modes apply to the HTTP proxy and the DNS server.

## Configuration

### Global config (`~/.config/vibepit/config.yaml`)

```yaml
allow:
  - github.com
  - api.github.com
  - objects.githubusercontent.com

dns-only:
  - example.com

presets:
  node:
    allow:
      - registry.npmjs.org
      - nodejs.org
  python:
    allow:
      - pypi.org
      - files.pythonhosted.org
  go:
    allow:
      - proxy.golang.org
      - sum.golang.org
      - storage.googleapis.com
```

### Project config (`.vibepit/network.yaml`)

```yaml
presets:
  - node
  - go

allow:
  - api.openai.com
  - api.anthropic.com

dns-only:
  - internal.corp.example.com
```

### Merging

Global `allow` + project `allow` + all referenced preset `allow` lists are
combined into a single set. Same for `dns-only`. Project config is additive
only -- it cannot remove global entries.

### CLI overrides

```
vibepit --allow extra.example.com --preset python
```

### First-run setup

When no `.vibepit/network.yaml` exists in the project, the CLI prompts the
user to select presets interactively (multi-select). The selection is written
to `.vibepit/network.yaml`.

## Container Orchestration

When the user runs `vibepit`, the Go binary:

1. Loads configuration by merging global config, project config, CLI flags,
   and preset definitions.
2. Runs first-run setup if no project config exists.
3. Checks for an existing session and attaches to it if found.
4. Creates `vibepit-net` as an internal Docker network.
5. Starts the proxy container: scratch base, host `vibepit` binary mounted
   in, config mounted as a file, connected to both bridge and `vibepit-net`,
   static IP on `vibepit-net`.
6. Starts the vibepit container: same image and options as today, connected to
   `vibepit-net` only, `HTTP_PROXY` and `HTTPS_PROXY` set to the proxy,
   `--dns` set to the proxy's IP, `NO_PROXY=localhost,127.0.0.1`.
7. Attaches an interactive shell.
8. On exit, stops both containers and removes the network.

## Go CLI Structure

```
vibepit [options] [/path/to/project]   launcher mode (default)
vibepit proxy                          proxy server mode
vibepit monitor                        proxy logs and admin
```

Package layout:

```
cmd/          subcommand entry points
container/    Docker/Podman client, container lifecycle, network management
proxy/        HTTP proxy, DNS server, allowlist matching, control API
config/       config loading, merging, preset definitions
```

Single static binary (`CGO_ENABLED=0`), multi-arch (amd64 + arm64).
