# Control API mTLS Design

## Problem

The proxy control API (port 3129) has no authentication. Any process on the
`vibepit-net` Docker network -- including the sandboxed dev container -- can
call `POST /allow` to add arbitrary domains to the allowlist, defeating network
isolation entirely.

## Goal

Provide a cryptographic guarantee that only the CLI (running on the host) can
access the control API. A process inside the sandbox should have zero ability
to modify the allowlist, even if it discovers the proxy's IP and port.

## Design

### Overview

When `vibepit` launches, it generates an ephemeral certificate authority and
issues two Ed25519 certificates: one for the proxy (server) and one for the CLI
(client). The control API listens with TLS 1.3 on a random localhost-only port,
requiring client certificates signed by the ephemeral CA. The CA private key is
discarded immediately after signing.

### Key Generation Flow

All cryptographic material is generated in memory before any containers start.

1. **Ephemeral CA** -- Ed25519 keypair + self-signed X.509 certificate. Default
   validity: 30 days. Configurable via `--tls-lifetime` flag or `tls-lifetime`
   in config YAML. The CA cert is the trust root for both sides.
2. **Proxy server cert** -- Ed25519 keypair, signed by the CA. SAN:
   `127.0.0.1`. Extended Key Usage: server auth. Same validity as the CA.
3. **CLI client cert** -- Ed25519 keypair, signed by the CA. Extended Key Usage:
   client auth. Same validity as the CA.
4. **Discard CA private key** -- Only the CA certificate (public) is retained
   for verification.

### Proxy Container Setup

The proxy container receives TLS material via three environment variables
containing PEM-encoded data:

- `VIBEPIT_PROXY_TLS_KEY` -- Proxy server private key (Ed25519)
- `VIBEPIT_PROXY_TLS_CERT` -- Proxy server certificate (signed by ephemeral CA)
- `VIBEPIT_PROXY_CA_CERT` -- CA certificate (for verifying the CLI's client cert)

The control API port is published to the host's loopback only:

```
-p 127.0.0.1:<random>:3129
```

On startup, the proxy parses the three env vars and configures a `tls.Config`
with:

- `MinVersion: tls.VersionTLS13`
- `ClientAuth: tls.RequireAndVerifyClientCert`
- `ClientCAs` pool containing only the ephemeral CA cert
- `Certificates` loaded from the server key + cert

The data plane (HTTP proxy on port 3128, DNS on port 53) remains unchanged --
plain TCP/UDP on `vibepit-net`, no TLS. Only the control API gets mTLS.

### CLI Side

The CLI retains in memory during the launcher process lifetime:

- The CA certificate (to verify the proxy's server cert)
- The CLI client key + cert (to authenticate to the proxy)
- The random localhost port chosen at launch

Since subcommands like `vibepit allow` and `vibepit monitor` run as separate
process invocations, the TLS material and connection details must be
discoverable:

**Port** -- Stored as a Docker label on the proxy container:
`x-vibepit.control-port=<port>`. Discovered the same way the proxy is found
today (Docker label queries).

**TLS material** -- Written to a session-scoped temporary directory:
`$XDG_RUNTIME_DIR/vibepit/<session-id>/`. Contains:

- `ca.pem` -- CA certificate
- `client-key.pem` -- CLI client private key
- `client-cert.pem` -- CLI client certificate

The directory is created with `0700` permissions. On Linux,
`$XDG_RUNTIME_DIR` (`/run/user/<uid>`) lives on a tmpfs and never touches
persistent storage. The directory is cleaned up when the launcher exits.

**Session ID** -- Stored as a Docker label on the proxy container:
`x-vibepit.session-id=<id>`. Subcommands read this label to locate the
correct runtime directory.

### CLI Requests

When `vibepit allow` or `vibepit monitor` run:

1. Query Docker for containers with `x-vibepit=true` and
   `x-vibepit.role=proxy`.
2. If multiple sessions are active, show an interactive selector (using
   `huh`) with each session identified by its project directory (from the
   existing `x-vibepit.project.dir` label). If only one session exists,
   auto-select it. A `--session` flag allows explicit selection by ID or
   project path for scripting use.
3. Read the control port from the `x-vibepit.control-port` label.
4. Read the session ID from the `x-vibepit.session-id` label.
5. Load TLS material from `$XDG_RUNTIME_DIR/vibepit/<session-id>/`.
6. Build a `tls.Config` with:
   - `RootCAs` containing the ephemeral CA cert
   - `Certificates` loaded from the CLI client key + cert
   - `MinVersion: tls.VersionTLS13`
7. Make HTTPS requests to `https://127.0.0.1:<port>/...`.

### Session Management

**`vibepit sessions`** -- New command that lists all active vibepit sessions
with project directory, session ID, and status.

When multiple sessions are active, `vibepit allow` and `vibepit monitor`
show an interactive `huh` selector:

```
Select a session:
> /home/bernd/Code/vibepit
  /home/bernd/Code/other-project
```

### New Docker Labels

| Label                       | Value               | Purpose                          |
|-----------------------------|---------------------|----------------------------------|
| `x-vibepit.control-port`   | Random host port    | CLI discovers control API port   |
| `x-vibepit.session-id`     | UUID or similar     | CLI locates TLS material on disk |

Existing labels (`x-vibepit`, `x-vibepit.role`, `x-vibepit.project.dir`)
remain unchanged.

### Security Properties

The control API is protected by three layers:

1. **Network isolation** -- The dev container is only on `vibepit-net`
   (internal). The control API is published to `127.0.0.1` on the host. No
   network path exists from the dev container to the control API.

2. **TLS 1.3 encryption** -- All control API traffic is encrypted. No
   eavesdropping by other processes on the host.

3. **mTLS authentication** -- The proxy requires a client certificate signed
   by the ephemeral CA. Only the CLI possesses a valid client cert. Other
   local clients (browsers, curl) are rejected at the TLS handshake.

**To bypass this, an attacker inside the sandbox would need to:**

- Escape the container network namespace to reach `127.0.0.1` on the host
- Obtain the client key + cert from `$XDG_RUNTIME_DIR` on the host filesystem

Both require a container escape, which is outside the threat model.

### Configuration

| Setting          | Flag               | Config key       | Default  |
|------------------|---------------------|------------------|----------|
| Cert lifetime    | `--tls-lifetime`    | `tls-lifetime`   | `720h` (30 days) |
