# Architecture

Vibepit runs entirely on your local machine — there is no cloud service, no remote API, and no account required. It orchestrates three components — a host CLI, a proxy container, and a sandbox container — connected by an isolated Docker network. This page explains how these pieces fit together and why each design choice exists.

## Overview

When you run `vibepit`, the CLI orchestrates a small cluster of containers on your machine:

```
┌─────────────────────────────────────────────────┐
│  Host                                           │
│                                                 │
│  ┌───────────┐                                  │
│  │ vibepit   │  creates network, starts         │
│  │ CLI       │  containers, manages credentials │
│  └─────┬─────┘                                  │
│        │                                        │
│  ──────┼──── isolated network (10.x.x.0/24) ──  │
│        │                                        │
│  ┌─────┴─────┐       ┌──────────────┐           │
│  │  Proxy    │       │  Sandbox     │           │
│  │  Container│◄──────│  Container   │           │
│  │           │       │  (your code) │           │
│  └─────┬─────┘       └──────────────┘           │
│        │                                        │
│        ▼                                        │
│    Internet (filtered)                          │
└─────────────────────────────────────────────────┘
```

The sandbox container has no direct internet access. All outbound traffic — DNS queries and HTTP/HTTPS requests — must pass through the proxy, which enforces allowlist rules before forwarding anything.

## Host CLI

The `vibepit` binary runs on your host machine and orchestrates everything:

1. **Creates an isolated network** with a random subnet.
2. **Starts the proxy container** on that network with a static IP.
3. **Starts the sandbox container** configured to route all traffic through the proxy.
4. **Generates ephemeral mTLS credentials** so that only the CLI can talk to the proxy's control API.
5. **Attaches your terminal** to the sandbox container's shell.

After setup, the CLI also provides runtime commands — `allow-http`, `allow-dns`, and `monitor` — that communicate with the proxy's control API to modify the allowlist or observe traffic. See the [CLI Reference](../reference/cli.md) for command details.

## Isolated Network

Each session gets its own internal Docker bridge network with a random `10.x.x.0/24` subnet. The network is created with `Internal: true`, which means Docker does not attach a gateway to the external network — containers on this network have no direct route to the internet.

This is the foundation of Vibepit's network isolation. Rather than trying to block specific destinations, the architecture starts from zero access and requires all traffic to pass through the proxy. The proxy is the only component connected to both the isolated network and the default bridge network, making it the sole path to the outside world.

The random subnet avoids collisions with other Docker networks on the host. In the unlikely event of a collision (~1 in 65,000), network creation fails with a "pool overlaps" error and you can retry.

## Proxy Container

The proxy runs on a minimal distroless base image (`gcr.io/distroless/base-debian13`) with no shell, no package manager, and no unnecessary libraries. It runs the `vibepit proxy` binary, which is bind-mounted from the host along with its configuration file.

The proxy container is connected to two networks: the isolated session network (with a static IP so the sandbox container can find it) and the default bridge network (so it can reach the internet). This dual-homed setup is what makes it the gatekeeper for all outbound traffic.

### Three services

The proxy runs three services in a single process:

1. **HTTP proxy** (dynamic port) — handles both HTTP and HTTPS (via `CONNECT` tunneling). Every request is checked against the HTTP allowlist, which matches on domain and port. Requests to non-allowed destinations are rejected. A separate CIDR blocklist prevents connections to private and link-local IP ranges, blocking attempts to reach the Docker host or other local services.

2. **DNS server** (port 53) — receives all DNS queries from the sandbox container. Allowed domains are forwarded to an upstream resolver (defaults to `9.9.9.9`). Everything else returns `NXDOMAIN`, preventing DNS-based data exfiltration.

3. **Control API** (dynamic port) — an mTLS-secured HTTP API used by the CLI's `allow-http`, `allow-dns`, and `monitor` commands. The port is published to `127.0.0.1` on the host, so only local processes with the correct client certificate can connect. See [Control API](#control-api) below for details.

All three services share the same allowlist, which is updated atomically at runtime when you use `allow-http` or `allow-dns`.

## Sandbox Container

The sandbox container is your workspace. It runs an Ubuntu-based image with common development tools, configured with several hardening measures:

- **Read-only root filesystem** — the container's root filesystem is mounted read-only. A writable `/tmp` (tmpfs) is available for temporary files.
- **Dropped capabilities** — all Linux capabilities are dropped (`CAP_DROP: ALL`).
- **No new privileges** — the `no-new-privileges` security option prevents privilege escalation via setuid binaries or other mechanisms.
- **Non-root user** — processes run as the `code` user, matched to your host UID.
- **Init process** — an init process (tini) runs as PID 1 to handle signal forwarding and zombie reaping.

The container has your project directory bind-mounted (read-write) and a persistent `vibepit-home` volume mounted at `/home/code`. This volume survives across sessions, preserving installed tools, shell history, and configuration. The project's `.vibepit` configuration directory is hidden inside the sandbox to prevent the agent from modifying its own allowlist rules.

Environment variables `HTTP_PROXY` and `HTTPS_PROXY` are set to point at the proxy container's static IP. DNS is configured through the container runtime's DNS settings to use the proxy's DNS server on port 53. Together, these ensure all outbound traffic is routed through the proxy without any additional configuration.

For a complete description of the security controls, see [Security Model](security-model.md).

## Data Flow

All outbound traffic from the sandbox container follows two paths:

**DNS queries:** The sandbox container's DNS is configured to use the proxy container's IP on port 53. When a process inside the container resolves a hostname, the query goes to the proxy's DNS server. If the domain is on the DNS allowlist, the query is forwarded to the upstream resolver (`9.9.9.9` by default). Otherwise, the proxy returns `NXDOMAIN`.

**HTTP/HTTPS requests:** The `HTTP_PROXY` and `HTTPS_PROXY` environment variables direct all HTTP traffic through the proxy. For HTTPS, the proxy uses `CONNECT` tunneling — it sees the destination hostname and port but not the encrypted payload. The proxy checks the destination against the HTTP allowlist and the CIDR blocklist before establishing the connection. Blocked requests receive an immediate rejection.

The CIDR blocklist is applied to resolved IP addresses and blocks all private ranges (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`), loopback (`127.0.0.0/8`), and link-local addresses by default. This prevents the sandbox container from reaching the Docker host, other containers, or local network services, even if a domain resolves to a private IP.

**Host access via `host.vibepit`:** The one exception to CIDR blocking is the synthetic hostname `host.vibepit`. The proxy's DNS server resolves it to the host machine's gateway IP, and the HTTP proxy forwards requests to it — but only on ports explicitly listed in `allow-host-ports` in the project config. This lets the sandbox reach local development services (databases, API servers) without opening up all private IP ranges. See [Configure Network Presets](../how-to/configure-presets.md#allow-host-ports) for configuration details.

## Session Lifecycle

A session progresses through these stages:

### Startup

1. The CLI generates a session ID and creates an internal Docker network with a random `10.x.x.0/24` subnet.
2. Ephemeral mTLS credentials (Ed25519 CA, server cert, client cert) are generated. The CA private key is used to sign both certificates and then discarded — it never touches disk.
3. The proxy container is created on both the session network and the bridge network, with the server certificate and CA cert passed via environment variables.
4. The sandbox container is created on the session network only, with proxy environment variables pointing to the proxy's static IP.
5. Client credentials (CA cert, client cert, client key) are written to `$XDG_RUNTIME_DIR/vibepit/<sessionID>/` so that `allow-http`, `allow-dns`, and `monitor` can authenticate to the control API from separate processes.
6. The CLI attaches your terminal to the sandbox container and starts it.

### Reattach

If you run `vibepit` again in the same project directory while a session is still running, the CLI detects the existing sandbox container and opens a new shell (`exec`) inside it rather than creating a new session. This means multiple terminal windows can share one session.

### Cleanup

When you exit the shell, the sandbox container's entrypoint exits and the container stops. The CLI then:

1. Removes the sandbox container.
2. Removes the proxy container.
3. Removes the session network.
4. Deletes the credential files from `$XDG_RUNTIME_DIR/vibepit/<sessionID>/`.

## Control API

The control API uses mutual TLS (mTLS) with ephemeral Ed25519 certificates to authenticate CLI commands. This prevents unauthorized processes from modifying the allowlist or reading traffic logs.

The trust model works as follows:

1. At session startup, the CLI generates an ephemeral CA and uses it to sign a server certificate (for the proxy) and a client certificate (for the CLI).
2. The CA private key is discarded immediately after signing. No new certificates can be issued for this session.
3. The server certificate has a SAN of `127.0.0.1` and is used by the control API listener.
4. The client certificate is stored in the session directory and loaded by `allow-http`, `allow-dns`, and `monitor` when they connect.
5. Both sides require TLS 1.3 and verify the peer certificate against the ephemeral CA.

The control API port is published only to `127.0.0.1`, so it is not reachable from the network. Combined with mTLS, this means only the user who started the session can issue control commands.
