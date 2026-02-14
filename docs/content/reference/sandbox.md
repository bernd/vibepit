# Sandbox Environment

This page describes the environment inside the sandbox container — what is
mounted, which environment variables are set, and how the container is
configured.

## User and home directory

The sandbox runs as the `code` user. The home directory is `/home/code`, backed
by a persistent Docker volume (`vibepit-home`) that survives across sessions.

The UID and GID of the `code` user match your host user, so file ownership is
consistent between the host and the container.

## Mounts

| Path | Type | Writable | Persists |
|------|------|----------|----------|
| Your project directory (original absolute path) | Bind mount | Yes | Yes (host filesystem) |
| `/home/code` | Docker volume | Yes | Yes (across sessions) |
| `/tmp` | tmpfs | Yes | No (cleared on container stop) |
| `/` (everything else) | Container image | No (read-only) | No |

The project's `.vibepit` configuration directory is hidden inside the sandbox
to prevent the agent from reading or modifying its own allowlist rules.

## Environment variables

The following environment variables are set automatically inside the sandbox
container:

### Proxy variables

| Variable | Value |
|----------|-------|
| `HTTP_PROXY` | `http://<proxy-ip>:<proxy-port>` |
| `HTTPS_PROXY` | `http://<proxy-ip>:<proxy-port>` |
| `http_proxy` | `http://<proxy-ip>:<proxy-port>` |
| `https_proxy` | `http://<proxy-ip>:<proxy-port>` |
| `NO_PROXY` | `localhost,127.0.0.1` |
| `no_proxy` | `localhost,127.0.0.1` |

Both uppercase and lowercase variants are provided for compatibility with
different tools and libraries. Tools that respect these variables (curl, pip,
npm, and most language package managers) route traffic through the filtering
proxy automatically.

### Other variables

| Variable | Value |
|----------|-------|
| `TERM` | Inherited from your host (e.g., `xterm-256color`) |
| `COLORTERM` | Inherited from your host (if set) |
| `LANG` | `en_US.UTF-8` |
| `LC_ALL` | `en_US.UTF-8` |
| `VIBEPIT_PROJECT_DIR` | Absolute path to your project directory |

## DNS

DNS is configured through the container runtime's DNS settings to use the
proxy's DNS server. All DNS queries from the sandbox are resolved by the proxy,
which filters them against the DNS allowlist. Only allowlisted domains receive
valid responses; all other queries return `NXDOMAIN`.

## Hostname

The sandbox container's hostname is `vibes`.

## Container hardening

| Setting | Value |
|---------|-------|
| Root filesystem | Read-only |
| Capabilities | All dropped (`CAP_DROP: ALL`) |
| Security options | `no-new-privileges` |
| Init process | Enabled (tini) |

For a full description of these controls, see the
[Security Model](../explanations/security-model.md).
