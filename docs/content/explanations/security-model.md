# Security Model

Vibepit applies defense-in-depth controls to reduce risk when you run AI coding agents. No single layer is sufficient on its own; each control limits the blast radius if another fails. This page explains what each control does and why it exists.

For a catalog of specific threats and mitigations, see the [Threat Model](threat-model.md). For how the components fit together at runtime, see the [Architecture](architecture.md).

## Default-deny network posture

A Vibepit session starts with no network access. DNS resolution, HTTP/HTTPS requests, and direct IP connections all fail unless you explicitly allowlist them. This forces you to declare every external dependency up front, making the agent's network surface auditable and reproducible.

## Container hardening

The sandbox container runs with several kernel-level restrictions:

**Read-only root filesystem.** The container's root filesystem is mounted read-only (`ReadonlyRootfs: true`). This prevents the agent from persisting modifications to system binaries, libraries, or configuration files. A writable `/tmp` (mounted as tmpfs) is available for transient scratch data, and the home directory is a persistent volume, but the OS layer itself cannot be altered.

**All Linux capabilities dropped.** The container starts with every Linux capability removed (`CapDrop: ["ALL"]`). Capabilities like `CAP_NET_RAW`, `CAP_SYS_ADMIN`, and `CAP_DAC_OVERRIDE` are unavailable, which prevents raw socket creation, filesystem namespace manipulation, and permission bypass.

**`no-new-privileges`.** The `no-new-privileges` security option prevents processes inside the container from gaining additional privileges through setuid or setgid binaries. Even if such a binary exists on a mounted volume, executing it will not escalate privileges.

**Non-root `code` user.** The dev container runs as the unprivileged `code` user. If an attacker escapes the process sandbox but remains inside the container, they operate without root privileges, limiting what they can access on the host kernel.

**Init process.** The container runs with an init process (`Init: true`) as PID 1. This ensures proper signal forwarding to child processes and reaps zombie processes, preventing resource leaks during long-running agent sessions.

## Network isolation

Each session creates an internal Docker bridge network (`Internal: true`). Containers on an internal network have no default route to the host or the internet. The only path to the outside is through the proxy container, which is dual-homed: it connects to both the internal session network and the default bridge network. All DNS and HTTP/HTTPS traffic from the sandbox routes through this proxy, where it is subject to allowlist filtering.

## CIDR blocking

Even if a domain resolves and passes the allowlist, the proxy blocks connections to private and reserved IP ranges:

| CIDR | Purpose |
|---|---|
| `10.0.0.0/8` | Private network (RFC 1918) |
| `172.16.0.0/12` | Private network (RFC 1918) |
| `192.168.0.0/16` | Private network (RFC 1918) |
| `127.0.0.0/8` | Localhost / loopback |
| `169.254.0.0/16` | Link-local (APIPA) |
| `fc00::/7` | IPv6 unique local addresses |
| `fe80::/10` | IPv6 link-local addresses |
| `::1/128` | IPv6 loopback |

These blocks apply unconditionally, regardless of allowlist rules. They prevent an allowlisted domain from being used to reach internal services via DNS rebinding or other IP-level attacks. The `CIDRBlocker` also accepts additional custom ranges if your environment requires broader restrictions.

## DNS filtering

The proxy runs a DNS server on port 53, configured as the sole resolver for sandbox containers. Only domains that match an allowlist rule receive a valid response; all other queries are refused.

Allowlist rules support two forms:

- **Exact match**: `example.com` matches only `example.com`.
- **Wildcard**: `*.example.com` matches any subdomain of `example.com` (such as `api.example.com` or `cdn.assets.example.com`) but does **not** match the apex domain `example.com` itself.

You can add DNS rules at startup via configuration or at runtime using the `allow-dns` command. Rules are additive and applied atomically using lock-free concurrency, so updates do not block in-flight queries.

## HTTP/HTTPS filtering

The proxy filters all HTTP and HTTPS traffic using a `domain:port` allowlist. HTTPS connections use the `CONNECT` method, so the proxy sees the target hostname and port without terminating TLS.

Each rule specifies a domain pattern and a port pattern:

- **Domain matching** follows the same exact and wildcard semantics as DNS filtering.
- **Port patterns** support digits and `*` globs. For example, `443` matches only port 443, while `8*` matches any port starting with 8 (80, 8080, 8443, etc.).

A request must match both the domain and port components of at least one rule to be allowed. Rules are purely additive: you can add them at startup or at runtime with the `allow-http` command, but you cannot remove them during a session.

## mTLS control API

The proxy exposes a control API for runtime administration (adding allowlist entries, streaming logs). This API is secured with mutual TLS (mTLS) to prevent the sandbox container or other processes from issuing unauthorized control commands.

Each session generates ephemeral Ed25519 key pairs:

1. An ephemeral CA signs a server certificate and a client certificate.
2. The CA private key is discarded immediately after signing. No additional certificates can be issued for the session.
3. The server certificate's Subject Alternative Name (SAN) is restricted to `127.0.0.1`, so it is only reachable from the host.
4. TLS 1.3 is enforced as the minimum version.
5. The server requires and verifies client certificates against the ephemeral CA (`RequireAndVerifyClientCert`).

Because the CA key is discarded after signing, an attacker who compromises the proxy at runtime cannot mint new client certificates. The credentials exist only in memory for the lifetime of the session.

## Proxy image

The proxy container runs on `gcr.io/distroless/base-debian13`. Distroless images contain no shell, no package manager, and no OS-level utilities. This minimizes the attack surface of the proxy itself: even if an attacker achieves code execution inside the proxy container, there are no tools available to escalate or pivot.

## What this is not

Vibepit is not VM-level isolation. The sandbox container shares the host kernel, which means:

- **Container escapes** through kernel vulnerabilities can grant host access.
- **Runtime bugs** in Docker or Podman can weaken isolation guarantees.
- **Host misconfiguration** (such as mounting the Docker socket into the container) can bypass all controls.

Treat Vibepit as defense in depth: multiple independent controls that collectively reduce risk. It is not absolute containment. For workloads that require stronger isolation guarantees, consider running Vibepit inside a VM.
