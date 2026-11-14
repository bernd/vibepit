---
description: How the Vibepit CLI, proxy container, sandbox container, and isolated Docker network work together to filter agent traffic.
---

# Architecture

Vibepit runs entirely on your local machine. There is no cloud service, no remote API, and no account. It has three components, a host CLI, a proxy container, and a sandbox container, connected by an isolated Docker network. This page explains how they fit together and why each design choice exists.

## Overview

When you run `vibepit`, the CLI starts a small set of containers on your machine:

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

The sandbox container has no direct internet access. All outbound traffic, both DNS queries and HTTP/HTTPS requests, goes through the proxy, which checks it against the allowlist before forwarding anything.

## Host CLI

The `vibepit` binary runs on your host machine and sets up everything:

1. **Creates an isolated network** with a random subnet.
2. **Starts the proxy container** on that network with a static IP.
3. **Starts the sandbox container** and routes all its traffic through the proxy.
4. **Generates ephemeral mTLS credentials** so that only the CLI can talk to the proxy's control API.
5. **Attaches your terminal** to the sandbox container's shell (in `run` mode) or **waits for the daemon** to accept connections (in `up` mode).

The CLI has two operating modes:

- **`run` mode** (default) attaches your terminal directly to the sandbox container. The session lasts as long as the first `vibepit` process. When you exit, the CLI removes the containers.
- **Daemon mode** (`up`/`connect`/`down`) starts the containers in the background with an SSH server. You connect with `vibepit connect`, and sessions inside the sandbox persist across disconnects. The containers run until you stop them with `vibepit down`.

After setup, the CLI also has runtime commands (`allow-http`, `allow-dns`, `monitor`, and `status`) that call the proxy's control API to change the allowlist, watch traffic, or show session state. See the [CLI Reference](../reference/cli.md) for command details.

## Isolated network

Each session gets its own internal Docker bridge network with a random `10.x.x.0/24` subnet. Vibepit creates the network with `Internal: true`, so Docker does not attach a gateway to the external network, and containers on it have no direct route to the internet.

Vibepit's network isolation depends on this. Instead of blocking specific destinations, the design starts from zero access and sends all traffic through the proxy. The proxy is the only component connected to both the isolated network and the default bridge network, so it is the only path out.

The random subnet avoids collisions with other Docker networks on the host. If a collision does happen (about 1 in 65,000), network creation fails with a "pool overlaps" error and you can retry.

## Proxy container

The proxy runs on a minimal distroless base image (`gcr.io/distroless/base-debian13`) with no shell, no package manager, and no unneeded libraries. It runs the `vibepit proxy` binary, which Vibepit bind-mounts from the host along with its configuration file.

The proxy container connects to two networks. On the isolated session network it has a static IP so the sandbox container can find it. On the default bridge network it can reach the internet. Because it sits on both, all outbound traffic has to pass through it.

### Three services

The proxy runs three services in a single process:

1. **HTTP proxy** (dynamic port) handles both HTTP and HTTPS (via `CONNECT` tunneling). It checks every request against the HTTP allowlist, which matches on domain and port, and rejects requests to destinations that aren't allowed. A separate CIDR blocklist rejects connections to private and link-local IP ranges, so requests cannot reach the Docker host or other local services.

2. **DNS server** (port 53) receives all DNS queries from the sandbox container. It forwards allowed domains to an upstream resolver (`9.9.9.9` by default) and answers everything else with `NXDOMAIN`, which blocks DNS-based data exfiltration.

3. **Control API** (dynamic port) is an mTLS-protected HTTP API that the CLI's `allow-http`, `allow-dns`, and `monitor` commands use. The port is published to `127.0.0.1` on the host, so only local processes with the correct client certificate can connect. See [Control API](#control-api) below for details.

All three services share the same allowlist. When you run `allow-http` or `allow-dns`, the proxy updates it atomically.

## Sandbox container

The sandbox container is where you and the agent work. It runs an Ubuntu-based image with common development tools and these hardening settings:

- **Read-only root filesystem.** The container's root filesystem is read-only. A writable `/tmp` (tmpfs) holds temporary files.
- **Dropped capabilities.** All Linux capabilities are dropped (`CAP_DROP: ALL`).
- **No new privileges.** The `no-new-privileges` security option blocks privilege escalation through setuid binaries and similar mechanisms.
- **Non-root user.** Processes run as the `code` user, which has your host UID.
- **Init process.** An init process (tini) runs as PID 1 to forward signals and reap zombie processes.

Vibepit bind-mounts your project directory read-write and mounts two persistent volumes, `vibepit-home` at `/home/code` and `vibepit-linuxbrew` at `/home/linuxbrew`. The volumes survive across sessions and keep shell state and installed Homebrew tools. Vibepit hides the project's `.vibepit` configuration directory inside the sandbox, so the agent cannot change its own allowlist rules.

The `HTTP_PROXY` and `HTTPS_PROXY` environment variables point at the proxy container's static IP. The container runtime's DNS settings point the sandbox at the proxy's DNS server on port 53. With both in place, all outbound traffic goes through the proxy without any setup inside the container.

For a complete description of the security controls, see [Security Model](security-model.md).

## Data flow

Outbound traffic from the sandbox container takes one of two paths.

**DNS queries.** The sandbox container uses the proxy container's IP on port 53 as its DNS server. When a process inside the container resolves a hostname, the query goes to the proxy's DNS server. If the domain is on the DNS allowlist, the proxy forwards the query to the upstream resolver (`9.9.9.9` by default). Otherwise, it returns `NXDOMAIN`.

**HTTP/HTTPS requests.** The `HTTP_PROXY` and `HTTPS_PROXY` environment variables send all HTTP traffic through the proxy. For HTTPS, the proxy uses `CONNECT` tunneling. It sees the destination hostname and port but not the encrypted payload. The proxy checks the destination against the HTTP allowlist and the CIDR blocklist before it opens the connection. It rejects blocked requests immediately, except while a `--prompt` overlay or the `monitor` is attached. In that case the proxy holds a request to an unlisted host for up to `hold-timeout` so you can allow it first.

The proxy applies the CIDR blocklist to resolved IP addresses. By default it blocks all private ranges (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`), loopback (`127.0.0.0/8`), and link-local addresses. The sandbox container cannot reach the Docker host, other containers, or local network services, even if a domain resolves to a private IP.

**Host access via `host.vibepit`.** The one exception to CIDR blocking is the synthetic hostname `host.vibepit`. The proxy's DNS server resolves it to the host machine's gateway IP, and the HTTP proxy forwards requests to it, but only on ports listed in `allow-host-ports` in the project config. The sandbox can then reach local development services such as databases and API servers without opening all private IP ranges. See [Configure Network Presets](../how-to/configure-presets.md#allow-host-ports) for configuration details.

## SSH server

In daemon mode (`vibepit up`), the sandbox container runs an SSH server instead of attaching a terminal. The server listens on port 2222 inside the sandbox container.

### Authentication

Each Vibepit session generates two ephemeral Ed25519 keypairs:

- A **client keypair.** The private key stays on the host. The sandbox container gets the public key as the authorized key.
- A **host keypair.** Vibepit bind-mounts the private key into the sandbox container. The public key stays on the host for host key verification.

Vibepit stores both keypairs in the session credentials directory (`$XDG_STATE_HOME/vibepit/sessions/<sessionID>/`). When you run `vibepit connect`, it authenticates with the client private key and checks the server's host key against the stored public key.

### Port forwarding

The sandbox container does not publish the SSH port. The proxy container forwards TCP traffic from a random host port to the sandbox's port 2222, so the sandbox container stays on the internal network with no direct host connection.

### Session management

The SSH server manages persistent shell sessions inside the sandbox. Each shell session is a PTY-backed process that keeps running when the SSH client disconnects:

- When you run `vibepit connect` and detached sessions exist, the server shows a selector where you can reattach to an existing session or start a new one.
- Sessions last until the shell process exits or the sandbox container stops.
- The server allows at most 50 concurrent sessions.

### Command execution

`vibepit exec` runs a command without an interactive shell. For example, with `vibepit exec ls -la`, the server runs the command through the user's shell and returns its exit code without creating a persistent session.

## Session lifecycle

Each operating mode has its own session lifecycle.

### Run mode (default)

#### Startup

1. The CLI generates a session ID and creates an internal Docker network with a random `10.x.x.0/24` subnet.
2. The CLI generates ephemeral mTLS credentials (Ed25519 CA, server cert, client cert). It signs both certificates with the CA private key and then discards the key, which never touches disk.
3. The CLI creates the proxy container on both the session network and the bridge network, and passes the server certificate and CA cert in environment variables.
4. The CLI creates the sandbox container on the session network only, with proxy environment variables pointing to the proxy's static IP.
5. The CLI writes client credentials (CA cert, client cert, client key) to `$XDG_STATE_HOME/vibepit/sessions/<sessionID>/`, so that `allow-http`, `allow-dns`, and `monitor` can authenticate to the control API from separate processes.
6. The CLI attaches your terminal to the sandbox container and starts it.

#### Reattach

If you run `vibepit` again in the same project directory while a session is running, the CLI finds the existing sandbox container and opens a new shell (`exec`) inside it instead of creating a new session. Several terminal windows can share one session this way.

#### Cleanup

When you exit the shell, the sandbox container's entrypoint exits and the container stops. The CLI then:

1. Removes the sandbox container.
2. Removes the proxy container.
3. Removes the session network.
4. Deletes the credential files from the session directory.

### Daemon mode (`up`/`connect`/`down`)

#### Startup

1. Steps 1 to 5 are the same as in run mode.
2. The CLI generates ephemeral Ed25519 SSH keypairs and writes them to the session credentials directory. See [SSH server > Authentication](#authentication) for details.
3. The sandbox container runs the `vibed` entrypoint instead of a shell. `vibed` sets up the sandbox environment and starts the SSH server.
4. The CLI waits until the SSH daemon accepts connections, prints the SSH address, and exits.

#### Connect

You connect to the running session with `vibepit connect`. See [SSH server](#ssh-server) for authentication, session management, and command execution details.

#### Cleanup

Containers run until you stop them with `vibepit down`, which:

1. Stops and removes all session containers (sandbox and proxy).
2. Removes the session network.
3. Deletes all credential files (mTLS and SSH keys) from the session directory.

## Control API

The control API uses mutual TLS (mTLS) with ephemeral Ed25519 certificates to authenticate CLI commands. Processes without the client certificate cannot change the allowlist or read traffic logs.

The trust chain works like this:

1. At session startup, the CLI generates an ephemeral CA and uses it to sign a server certificate (for the proxy) and a client certificate (for the CLI).
2. The CLI discards the CA private key right after signing. Nobody can issue new certificates for this session.
3. The server certificate has a SAN of `127.0.0.1`, and the control API listener uses it.
4. The CLI stores the client certificate in the session directory, and `allow-http`, `allow-dns`, and `monitor` load it when they connect.
5. Both sides require TLS 1.3 and verify the peer certificate against the ephemeral CA.

Besides request/response endpoints, the control API has a `GET /events` server-sent-events stream. The `monitor` and the `--prompt` overlay subscribe to it to see traffic and held requests as they happen. While a subscriber is connected, the proxy assumes someone is there to decide on held requests.

The control API port is published only to `127.0.0.1`, so other machines on the network cannot reach it. Together with mTLS, this means only the user who started the session can send control commands.
