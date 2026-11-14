---
description: Defense-in-depth controls in Vibepit including container hardening, network isolation, CIDR blocking, DNS filtering, and mTLS authentication.
---

# Security Model

Vibepit layers several independent controls around an AI coding agent. No single layer is enough on its own. Each one limits the damage if another fails. This page explains what each control does and why it exists.

For a catalog of specific threats and mitigations, see the [Threat Model](threat-model.md). For how the components fit together at runtime, see the [Architecture](architecture.md).

## Default-deny network posture

A Vibepit session starts with no network access. DNS resolution, HTTP/HTTPS requests, and direct IP connections all fail until you allowlist them. You declare every external dependency up front, so the list of hosts the agent can reach is written down in your config and the same in every session.

## Container hardening

The sandbox container runs with these kernel-level restrictions:

**Read-only root filesystem.** Vibepit mounts the container's root filesystem read-only (`ReadonlyRootfs: true`), so the agent cannot change system binaries, libraries, or configuration files. A writable `/tmp` (tmpfs) holds scratch data, and the home directory is a persistent volume, but the OS layer stays unchanged.

**All Linux capabilities dropped.** The container starts with every Linux capability removed (`CapDrop: ["ALL"]`). Without `CAP_NET_RAW`, `CAP_SYS_ADMIN`, and `CAP_DAC_OVERRIDE`, a process cannot open raw sockets, manipulate filesystem namespaces, or bypass file permissions.

**`no-new-privileges`.** This security option stops processes inside the container from gaining privileges through setuid or setgid binaries. Even if such a binary exists on a mounted volume, running it does not raise privileges.

**Non-root `code` user.** The sandbox container runs as the unprivileged `code` user. An attacker who escapes the process sandbox but stays inside the container has no root privileges, which limits what they can do to the host kernel.

**Init process.** The container runs an init process (`Init: true`) as PID 1. It forwards signals to child processes and reaps zombie processes, so long agent sessions don't accumulate dead processes.

## Network isolation

Each session creates an internal Docker bridge network (`Internal: true`). Containers on an internal network have no default route to the host or the internet. The only way out is the proxy container, which connects to both the internal session network and the default bridge network. All DNS and HTTP/HTTPS traffic from the sandbox goes through this proxy, which applies the allowlist.

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

These blocks apply by default, whatever the allowlist says. An allowlisted domain therefore cannot reach internal services through DNS rebinding or other IP-level attacks. To block more ranges, add them with `block-cidr`.

To reach a range that is otherwise blocked, add it to `allow-cidr`. The proxy permits any IP inside an `allow-cidr` range, even if a blocked range also contains it.

## DNS filtering

The proxy runs a DNS server on port 53, and it is the only resolver the sandbox containers use. Only domains that match an allowlist rule get a valid response. The proxy refuses all other queries.

Allowlist rules support two forms:

- **Exact match**: `example.com` matches only `example.com`.
- **Wildcard**: `*.example.com` matches exactly one subdomain label (such as `api.example.com`) but does **not** match the apex domain `example.com` or deeper subdomains like `a.b.example.com`. Use `**.example.com` to match one or more subdomain levels.

You can add DNS rules at startup through configuration or at runtime with the `allow-dns` command. Rules only add access. The proxy swaps in the new rule set atomically without locks, so an update never blocks a query in progress.

## HTTP/HTTPS filtering

The proxy filters all HTTP and HTTPS traffic against a `domain:port` allowlist. HTTPS connections use the `CONNECT` method, so the proxy sees the target hostname and port without terminating TLS.

Each rule has a domain pattern and a port pattern:

- **Domain matching** uses the same exact and wildcard rules as DNS filtering.
- **Port patterns** accept an exact port number or `*` for any port.

A request must match both the domain and the port of at least one rule. Rules only add access. You can add them at startup or at runtime with the `allow-http` command, but you cannot remove them during a session.

## mTLS control API

The proxy exposes a control API for runtime administration, such as adding allowlist entries and streaming logs. Mutual TLS (mTLS) protects this API, so neither the sandbox container nor any other process without the client certificate can send control commands.

Each session generates ephemeral Ed25519 key pairs:

1. An ephemeral CA signs a server certificate and a client certificate.
2. Vibepit discards the CA private key right after signing. Nobody can issue more certificates for the session.
3. The server certificate's Subject Alternative Name (SAN) is `127.0.0.1`, so it is only reachable from the host.
4. The minimum TLS version is 1.3.
5. The server requires client certificates and verifies them against the ephemeral CA (`RequireAndVerifyClientCert`).

Because the CA key is gone after signing, an attacker who compromises the proxy at runtime cannot mint new client certificates. Vibepit passes server credentials to the proxy container through environment variables, and they never touch disk. It writes client credentials (CA cert, client cert, client key) to `$XDG_STATE_HOME/vibepit/sessions/<sessionID>/` with `0600` permissions, so CLI subcommands running as separate processes can authenticate. Vibepit deletes these files when the session ends.

## SSH authentication

In daemon mode (`vibepit up`), the sandbox container runs an SSH server. SSH authentication uses ephemeral Ed25519 keypairs generated for each session:

1. At session startup, Vibepit generates two keypairs, a **client keypair** and a **host keypair**.
2. The client private key and host public key stay on the host in `$XDG_STATE_HOME/vibepit/sessions/<sessionID>/`, with `0600` permissions on private keys and `0644` on public keys.
3. Vibepit bind-mounts the host private key read-only into the sandbox container and passes the client public key in an environment variable.
4. The server accepts only public key authentication. Passwords don't work.
5. The SSH server checks the connecting client's key against the single authorized public key.
6. The SSH client (`vibepit connect`) checks the server's host key against the stored public key, which prevents MITM attacks.

`vibepit down` deletes all SSH credentials. Because keys are never reused across sessions, a leaked key cannot open another session.

## SSH network access

The host cannot reach the SSH port directly. The proxy container forwards it and publishes it on a random port bound to `127.0.0.1`, so only the local machine can connect.

## Proxy image

The proxy container runs on `gcr.io/distroless/base-debian13`. Distroless images contain no shell, no package manager, and no OS-level utilities. This keeps the proxy's attack surface small. An attacker who gets code execution inside the proxy container finds no tools to escalate or pivot with.

## What this is not

Vibepit is not VM-level isolation. The sandbox container shares the host kernel, which means:

- **Container escapes** through kernel vulnerabilities can grant host access.
- **Runtime bugs** in Docker or Podman can weaken isolation.
- **Host misconfiguration**, such as mounting the Docker socket into the container, can bypass all controls.

Vibepit is defense in depth, a set of independent controls that each reduce risk. It is not absolute containment. For workloads that need stronger isolation, run Vibepit inside a VM.

### Container runtimes and VM boundaries

Some container runtimes run containers inside a lightweight Linux VM:

- **Docker Desktop** (macOS, Windows, and Linux) runs a VM using the platform's hypervisor (Apple Virtualization framework, Hyper-V/WSL2, or KVM).
- **Podman Machine** (macOS, Windows) runs a Fedora CoreOS VM.

With a VM, a container escape lands in the VM guest, not directly on your host. That boundary does not exist with **Docker Engine** or **Podman** on Linux (including Podman Desktop), where containers run natively and share the host kernel.

These VMs are built for developer convenience, not as security boundaries. File sharing, socket forwarding, and networking features cross the VM boundary, and the project directory is bind-mounted through it. Vibepit does not control or harden the VM, so don't count on it as a security control. It does make it less likely that a container escape reaches your host.
