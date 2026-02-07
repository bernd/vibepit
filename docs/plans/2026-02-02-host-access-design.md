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

The DNS server must bypass CIDR validation for `host.vibepit` responses. The
proxy IP is in 10.0.0.0/8 (a blocked range), but returning it is safe here
because it points to the proxy itself, not to an arbitrary private host.

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
- SSH keys are passed to the proxy container via environment variables,
  consistent with how mTLS credentials are delivered.
- The proxy starts its SSH server with the known host key and accepts only the
  known client public key. No passwords, no TOFU.
- The host-side SSH client verifies the server's host key and authenticates
  with the client private key. All trust is pre-established.

**Interface binding:**
- The SSH server binds to the proxy container's bridge network interface only,
  not 0.0.0.0. This prevents the dev container from reaching the SSH server
  at all, eliminating it as an attack surface from the sandbox.
- The SSH port is published to the host on `127.0.0.1:<random>`, matching the
  existing pattern used by the control API port.

**Port forwarding:**
- After starting the proxy container, the host-side vibepit binary connects as
  an SSH client to the proxy's SSH server (via the published port).
- For each port in `allow-host-ports`, it requests a remote forward: the proxy
  listens on `<proxy-internal-ip>:<port>` and tunnels connections back through
  the SSH channel to `127.0.0.1:<port>` on the host.
- The forward destination is hardcoded to `127.0.0.1`. The SSH server must
  reject any remote forward request targeting a different address.
- The SSH connection stays open for the lifetime of the session.
- SSH keepalives are enabled (e.g., every 15s) to detect dead connections
  through Docker networking. If the connection drops, the host-side client
  reconnects with exponential backoff and re-establishes all forwards.
- Startup synchronization: the host polls the published SSH port until the
  server accepts connections before requesting forwards.

**From the sandbox's perspective:**
- `host.vibepit:9200` resolves to `<proxy-ip>:9200`.
- The proxy is listening on that port via SSH remote forward.
- Connection goes through the tunnel to `host:9200`.
- Works for any TCP protocol (databases, gRPC, custom protocols, etc.).
- UDP is not supported (SSH port forwarding is TCP-only).

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

**Validation:** Ports that conflict with proxy services (53, 3128, 3129, and
the SSH server port) are rejected at config load time with a clear error
message.

### Access Control

| Scenario                       | HTTP/HTTPS                  | Non-HTTP TCP              |
| ------------------------------ | --------------------------- | ------------------------- |
| Port in `allow-host-ports`     | Auto-allowed through proxy  | Forwarded via SSH tunnel  |
| Port NOT in `allow-host-ports` | Subject to normal allowlist | Connection refused        |

**CIDR exemptions for `host.vibepit`:**

The proxy's CIDR blocker normally prevents connections to private IP ranges.
Two targeted exemptions are needed for host access:

1. **DNS responses:** The DNS server skips CIDR validation when returning the
   proxy IP for `host.vibepit` (the response is synthetic, not forwarded).
2. **HTTP proxy outbound:** When the HTTP handler connects to the host gateway
   IP on behalf of a `host.vibepit` request, it bypasses CIDR blocking for
   that specific connection. This applies only when the port is in
   `allow-host-ports` or the domain+port has been explicitly allowed via
   `vibepit allow`.

These exemptions are scoped narrowly — only `host.vibepit` benefits, and only
for configured ports. Other private-IP access remains blocked.

**Auto-allow mechanism:** The HTTP handler checks `allow-host-ports` as a
dynamic supplement to the allowlist. When a request targets
`host.vibepit:<port>` and the port is in `allow-host-ports`, the request is
allowed without requiring an explicit allowlist entry. This applies to both
CONNECT (HTTPS) and plain HTTP requests.

**HTTP-only host access (no tunnel):** Users can allow HTTP access to a host
port without an SSH tunnel: `vibepit allow host.vibepit:8080`. This adds an
allowlist entry AND a CIDR exemption for the host gateway IP on that port.
The proxy connects to the host gateway directly over the bridge network. No
entry in `allow-host-ports` is needed — this is pure HTTP proxying, just with
the CIDR block lifted for that specific target.

### Future Work

- Dynamic port forwarding via `vibepit allow` / monitor TUI at runtime
  (the SSH protocol supports adding forwards over an existing connection).
- UDP forwarding if demand arises (would require a separate mechanism outside
  SSH, e.g., a userspace UDP relay).

## Implementation

1. **`proxy/dns.go`** -- Intercept queries for `host.vibepit`, return proxy IP.
   Skip CIDR validation for this synthetic response.
2. **`proxy/ssh.go`** (new) -- Embedded SSH server: public key auth, remote
   port forward handling. Reject forwards to non-127.0.0.1 destinations.
   Bind to bridge interface only.
3. **`proxy/server.go`** -- Start SSH server alongside other services.
4. **`proxy/http.go`** -- When request targets `host.vibepit`, resolve to
   actual host gateway IP for outbound. Auto-allow if port is in
   `allow-host-ports`. Bypass CIDR blocking for allowed `host.vibepit` requests.
5. **`proxy/cidr.go`** -- Add a scoped exemption mechanism for `host.vibepit`
   connections (not a general bypass).
6. **`config/config.go`** -- Add `AllowHostPorts` field. Validate against
   reserved proxy ports.
7. **`cmd/run.go`** -- After proxy starts, connect as SSH client and set up
   remote forwards. Generate SSH keypair alongside mTLS creds. Implement
   keepalives and reconnection logic.
8. **`container/client.go`** -- Publish SSH port from proxy container on
   `127.0.0.1:<random>`.
