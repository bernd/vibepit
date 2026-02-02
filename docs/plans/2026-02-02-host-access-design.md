# Host Access via SSH Port Forwarding

## Problem

Software running in the sandbox often needs to access services on the host
(databases, API servers, LLM inference, MCP servers, etc.). Currently, the
sandbox runs on a Docker internal network with no way to reach the host.
HTTP-aware tools go through the proxy, which has host access via the bridge
network, but non-HTTP TCP traffic has no path at all.

## Design

### DNS: `host.vibepit`

A synthetic DNS name `host.vibepit` always resolves to the proxy's IP on the
internal network. This is unconditional and does not depend on any
configuration.

- HTTP-aware tools (curl, language HTTP clients) send requests through the
  HTTP proxy. The proxy resolves `host.vibepit` internally to the actual host
  gateway IP for outbound connections. The domain is subject to normal
  allowlist filtering.
- Non-HTTP tools connecting to `host.vibepit:<port>` connect to the proxy IP
  directly, which only works if that port is forwarded via SSH.

### SSH Server and Port Forwarding

The proxy binary gains an embedded SSH server using `golang.org/x/crypto/ssh`.
It starts alongside the existing HTTP proxy, DNS server, and control API.

**Authentication:**
- All SSH keys are pre-generated on the host alongside the existing mTLS
  credentials (ephemeral, per-session, stored in the same runtime directory).
- The SSH host keypair and client public key are injected into the proxy
  container via bind mount / config.
- The proxy starts its SSH server with the known host key and accepts only the
  known client public key. No passwords, no TOFU.
- The host-side SSH client verifies the server's host key and authenticates
  with the client private key. All trust is pre-established.

**Port forwarding:**
- After starting the proxy container, the host-side vibepit binary connects as
  an SSH client to the proxy's SSH server (via a published port).
- For each port in `allow-host-ports`, it requests a remote forward: the proxy
  listens on `<proxy-ip>:<port>` and tunnels connections back through the SSH
  channel to `localhost:<port>` on the host.
- The SSH connection stays open for the lifetime of the session.

**From the sandbox's perspective:**
- `host.vibepit:9200` resolves to `<proxy-ip>:9200`.
- The proxy is listening on that port via SSH remote forward.
- Connection goes through the tunnel to `host:9200`.
- Works for any TCP protocol (databases, gRPC, custom protocols, etc.).

### Configuration

A single new field in `.vibepit/network.yaml`:

```yaml
allow-host-ports:
  - 9200
  - 5432
  - 11434
```

No boolean flag. If the list is empty or absent, no SSH tunnels are created.
The SSH server always starts regardless of config.

### Access Control

| Scenario                       | HTTP/HTTPS                  | Non-HTTP TCP              |
| ------------------------------ | --------------------------- | ------------------------- |
| Port in `allow-host-ports`     | Auto-allowed through proxy  | Forwarded via SSH tunnel  |
| Port NOT in `allow-host-ports` | Subject to normal allowlist | Connection refused        |

When the proxy's HTTP handler evaluates a request to `host.vibepit:<port>`, it
checks if that port is in `allow-host-ports` and auto-allows if so. This
applies to both CONNECT (HTTPS) and plain HTTP requests. Ports not in
`allow-host-ports` require explicit allowlisting via `vibepit allow`,
the monitor TUI, or config.

For HTTP-only host access without port forwarding, users can allow the domain
like any other: `vibepit allow host.vibepit:8080`.

### Future Work

- Dynamic port forwarding via `vibepit allow` / monitor TUI at runtime
  (the SSH protocol supports adding forwards over an existing connection).

## Implementation

1. **`proxy/dns.go`** -- Intercept queries for `host.vibepit`, return proxy IP.
2. **`proxy/ssh.go`** (new) -- Embedded SSH server: host key generation,
   public key auth, remote port forward handling.
3. **`proxy/server.go`** -- Start SSH server alongside other services.
4. **`proxy/http.go`** -- When request targets `host.vibepit`, resolve to
   actual host gateway IP for outbound. Auto-allow if port is in
   `allow-host-ports`.
5. **`config/config.go`** -- Add `AllowHostPorts` field.
6. **`cmd/run.go`** -- After proxy starts, connect as SSH client and set up
   remote forwards. Generate SSH keypair alongside mTLS creds.
7. **`container/client.go`** -- Publish SSH port from proxy container.
