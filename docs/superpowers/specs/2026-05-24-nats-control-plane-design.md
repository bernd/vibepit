# NATS Control Plane: Replace HTTP Control API with Embedded NATS Server

## Overview

Replace the HTTP+mTLS control API on the proxy container with an embedded NATS
server. Preserve today's five endpoints as NATS subjects (request/reply for
queries and mutations, subscription for live log delivery). Reuse the existing
mTLS material for transport security and use TLS cert mapping for per-role
permissions. This is a transport swap only — feature behavior (logs, stats,
config, allow-http, allow-dns) is unchanged.

The motivation is to enable future bidirectional messaging patterns (e.g.,
proxy-initiated approval requests surfaced through ward's status bar, or
in-sandbox observability publishing) without inventing them in this spec. This
spec establishes the bus and the role/permission schema so those follow-ups are
additive.

## Scope

In scope:
- Replace `proxy/api.go` (HTTP control API) and the third HTTP server in
  `proxy/server.go` with an embedded NATS server.
- Define the subject map mirroring today's endpoints.
- Refactor `cmd/control.go`'s `ControlClient` to use NATS as transport while
  keeping its public method surface.
- Mint two client certs per session (`vibepit-user`, `vibepit-sandbox`) and
  configure NATS users with per-role permissions.
- Flip live log delivery from polling to subscription.
- Replace the `LogEntry.ID` integer counter with a producer-assigned ULID.

Out of scope (explicit, deferred to follow-up specs):
- Proxy → ward bidirectional block-approval flow.
- Sandbox-side NATS client (`vibed` connecting as `sandbox`). The cert is
  minted but not distributed.
- JetStream / persistence across proxy restarts.
- Cross-session bus multiplexing or multi-tenant accounts.

## Architecture

### Placement

The NATS server is embedded directly in the proxy binary via
`github.com/nats-io/nats-server/v2/server`. It runs in-process alongside the
existing HTTP proxy, DNS server, and SSH forwarder, and listens on the same
published port that `ControlAPIPort` exposes today. The field name
`ControlAPIPort` and JSON tag `control-api-port` are kept — only the protocol
underneath changes.

### Components

```
HTTPProxy ─┐
           ├─► publish "logs.events" (entry+ULID) ──┬─► LogBuffer (store + dedupe by ULID)
DNSServer ─┘                                        ├─► monitor TUI (subscriber)
                                                    └─► ward (subscriber — future use)

CLI ──► request "logs.history" ──► LogBuffer
CLI ──► request "stats"         ──► LogBuffer
CLI ──► request "allow.http"    ──► AllowlistHandler ──► HTTPAllowlist
CLI ──► request "allow.dns"     ──► AllowlistHandler ──► DNSAllowlist
CLI ──► request "config"        ──► ConfigHandler    ──► ProxyConfig
```

`HTTPProxy` and `DNSServer` lose their `*LogBuffer` field. They take a thin
publisher dependency (a `*nats.Conn` or `LogPublisher` interface) and stamp
each entry with a ULID drawn from a shared monotonic source. They never import
`LogBuffer`.

`LogBuffer` becomes a pure consumer plus history responder. It subscribes to
`logs.events`, stores entries in its ring (deduping by ULID), and answers
`logs.history` and `stats` request/reply.

`AllowlistHandler` and `ConfigHandler` are small new types in `proxy/bus.go`
holding the allowlist references / config; each registers a single subject.

A new `proxy/bus.go` wraps embedding/lifecycle of the nats-server and
registers in-process handlers. The `ControlAPI` type is deleted; its glue
moves into the bus handlers.

### Roles, Certificates, and Permissions

The session mints two client certs at startup, both signed by the same
ephemeral CA from `proxy/mtls.go`:

| Cert CN | Purpose | Distribution |
|---|---|---|
| `vibepit-user` | All host-side tools: `allow-http`, `allow-dns`, `monitor`, `ward` | Written to the same location today's single client cert is written. |
| `vibepit-sandbox` | Future in-sandbox `vibed` observability publisher. | Minted but not distributed in this spec. Sits unused until a follow-up wires sandbox-side delivery. |

NATS is configured with `TLSMap: true` (`verify_and_map`), extracting the
cert CN to map to a NATS user. The user/permission config:

```
user (CN=vibepit-user):
  publish:   ["allow.http", "allow.dns", "logs.history", "stats", "config"]
  subscribe: ["logs.events", "_INBOX.>"]

sandbox (CN=vibepit-sandbox):
  publish:   ["obs.>"]
  subscribe: ["_INBOX.>"]
```

In-process subscribers (LogBuffer, AllowlistHandler, ConfigHandler) connect
via `nats.InProcessServer(ns)`, which bypasses the TLS listener entirely (the
client is a Go object connected directly to the server). They authenticate as
a dedicated `internal` user with broad publish/subscribe permissions, using a
process-local credential generated at startup that never leaves the proxy
binary. External connections are required to present a verified client cert
by `TLSRequired: true` and `ClientAuth: RequireAndVerifyClientCert`.

The cert generation in `proxy/mtls.go` extends to mint multiple client certs
with distinct CNs. `WriteSessionCredentials` writes per-role files
(`user-cert.pem` / `user-key.pem`, `sandbox-cert.pem` / `sandbox-key.pem`).
`LoadSessionTLSConfig` extends to take a role string (`"user"` today,
`"sandbox"` later) and load the corresponding pair. The shared `ca.pem` is
unchanged.

## Subject Map

| Today (HTTP) | NATS subject | Pattern |
|---|---|---|
| `GET /logs` (initial + polling backfill) | `logs.history` | request/reply |
| `GET /logs` (live tail, was polled) | `logs.events` | subscribe (push) |
| `GET /stats` | `stats` | request/reply |
| `GET /config` | `config` | request/reply |
| `POST /allow-http` | `allow.http` | request/reply |
| `POST /allow-dns` | `allow.dns` | request/reply |
| — (reserved) | `obs.>` | publish (sandbox role) |

## Wire Format

JSON throughout. The reply convention follows the NATS Service framework:
success returns the typed result as the raw JSON body; errors return an empty
body with service headers.

```
Nats-Service-Error-Code: <int>   (absent on success)
Nats-Service-Error:       <human message>   (absent on success)
```

Error codes used: `400` for invalid request payloads, `500` for handler-internal
failures. Auth and subject-not-found errors are handled by NATS itself at the
protocol layer and never reach a handler.

Subject-specific payloads:

| Subject | Request body | Reply body (success) |
|---|---|---|
| `logs.history` | `{"since": "2026-05-24T10:00:00Z"}` (omitempty → full ring) | `[]LogEntry` |
| `stats` | `{}` | `map[string]DomainStats` |
| `config` | `{}` | `MergedConfig` |
| `allow.http` | `{"entries": [...]}` | `{"added": [...]}` |
| `allow.dns` | `{"entries": [...]}` | `{"added": [...]}` |
| `logs.events` (pub) | — | `LogEntry` (raw, no envelope) |

`logs.events` is published "naked" because there is no reply and no need for
status indication — failures during publish drop the entry, which matches
today's HTTP polling semantics.

### Log Entry ID: ULID

`LogEntry.ID` changes from `uint64` to `string`, containing a ULID (26-char
Crockford base32, 48-bit ms timestamp + 80-bit random/monotonic). Producers
(`HTTPProxy`, `DNSServer`) draw IDs from a shared `ulid.MonotonicReader`
guarded by a mutex, so all entries from this process are globally monotonic
without inter-producer coordination beyond the shared source.

ULID rationale over alternatives:
- Millisecond timestamp resolution narrows the `since` cursor's replay window
  for reconnect backfill.
- Lexicographically sortable and monotonic within the same millisecond when
  using the monotonic variant.
- Zero transitive deps (`github.com/oklog/ulid/v2`).

### Backfill Cursor

`logs.history` accepts an optional `since` timestamp. On startup or reconnect,
the client sends `since = lastSeenTime - safetyMargin` (default `1 * time.Second`
— enough to cover loopback latency and near-simultaneous publishers, since
client and proxy share a clock) and dedupes the response against its
locally-known ULID set. Initial startup with no prior state sends no `since`
and gets the full ring (up to `LogBufferCapacity = 10000`).

## Server Lifecycle

### Startup (`proxy.Server.Run`)

1. Load TLS material (server cert + CA) from env vars; load the existing
   `MTLSCredentials` machinery extended to mint two client certs.
2. Construct embedded `nats-server` with:
   - `Host: "0.0.0.0"`, `Port: cfg.ControlAPIPort`
   - `Users: []*server.User{userU, sandboxU, internalU}`
   - `TLSConfig: tlsCfg`, `TLSMap: true`, `TLSRequired: true`
   - `JetStream: false`, `NoSigs: true`
3. Start the server; wait for `ReadyForConnections(5 * time.Second)`.
4. Connect the in-process `internal` client (loopback TLS, or
   `nats.InProcessServer(ns)`). Register subscribers: `LogBuffer` on
   `logs.events`, handlers on `logs.history`, `stats`, `config`,
   `allow.http`, `allow.dns`. Call `nc.Flush()` to ack registrations.
5. Start the HTTP proxy, DNS server, and SSH forwarder (only after step 4
   completes, so no log entry can be published before `LogBuffer` is
   subscribed).

### Shutdown (`ctx.Done()`)

1. Stop HTTP proxy, DNS server, SSH forwarder with the existing 5-second
   deadline.
2. `nc.Drain()` on the in-process client — blocks until handler queues empty.
3. `ns.Shutdown()` on the embedded NATS server.

Bus errors flow to the existing `errCh` in `Server.Run` like HTTP/DNS errors.

## Host Client Refactor (`cmd/control.go`)

`ControlClient`'s public surface stays identical except for the log delivery
changes — `monitor`, `allow-http`, `allow-dns`, and `monitor_ui` need no
structural changes for the non-log methods.

```go
type ControlClient struct {
    nc *nats.Conn
}

func NewControlClient(session *SessionInfo) (*ControlClient, error) {
    tlsCfg, err := LoadSessionTLSConfig(session.SessionID, "user")
    if err != nil { return nil, err }
    nc, err := nats.Connect(
        fmt.Sprintf("tls://127.0.0.1:%s", session.ControlPort),
        nats.Secure(tlsCfg),
        nats.Timeout(5*time.Second),
        nats.MaxReconnects(-1),
        nats.ReconnectWait(500*time.Millisecond),
    )
    if err != nil { return nil, err }
    return &ControlClient{nc: nc}, nil
}
```

A shared reply decoder handles service-error headers:

```go
func decodeReply(msg *nats.Msg, into any) error {
    if code := msg.Header.Get("Nats-Service-Error-Code"); code != "" {
        return fmt.Errorf("%s: %s", code, msg.Header.Get("Nats-Service-Error"))
    }
    if into == nil { return nil }
    return json.Unmarshal(msg.Data, into)
}
```

Method changes:

| Method | New behavior |
|---|---|
| `Logs()` | `Request("logs.history", {}, 5s)` + `decodeReply` |
| `LogsAfter(id uint64)` | **Removed.** Replaced by `LogsSince(t time.Time)` |
| `LogsSince(t time.Time) ([]LogEntry, error)` | **New.** Backfill with optional `since`. Zero `time.Time` → full ring. |
| `SubscribeLogs(ch chan<- LogEntry) (unsub func(), error)` | **New.** `nc.Subscribe("logs.events", ...)`, decodes into channel. |
| `OnReconnect(fn func())` | **New.** Thin wrapper over `nats.ReconnectHandler`; lets the UI run gap-fill logic on reconnect without owning the `*nats.Conn` directly. |
| `Stats()` / `Config()` / `AllowHTTP()` / `AllowDNS()` | Same signatures; internals use `Request` + `decodeReply`. |
| `Close()` | `nc.Close()` instead of `CloseIdleConnections()`. |

### Monitor TUI Changes

`cmd/monitor_ui.go`:
- Startup: call `LogsSince(time.Time{})` once for backfill, record the
  highest ULID seen.
- Call `SubscribeLogs(ch)`; the Bubble Tea loop receives a "new log entry"
  message from the channel and appends after dedupe.
- The existing `pollLogsCmd(afterID)` and poll cadence are removed — delivery
  is purely event-driven.
- Register a reconnect callback via `ControlClient.OnReconnect`: on
  reconnect, call `LogsSince(lastSeenTime - safetyMargin)` and dedupe to
  fill the gap. The subscription itself is auto-resumed by the NATS client.

### CLI Tools

`allow-http`, `allow-dns`, and `update` need no structural changes — they are
short-lived, so reconnect logic does not apply. Initial dial either succeeds
or returns a user-visible error.

## Testing

### Proxy package

- New `proxy/bus_test.go` — starts an embedded NATS server in-process, dials
  a test client, asserts each subject's handler. Replaces `api_test.go`.
- `LogBuffer` tests: publish to `logs.events` from a test client, assert ring
  contents and dedupe behavior; request `logs.history` with various `since`
  values; request `stats`.
- `HTTPProxy` and `DNSServer` tests stop reaching into `LogBuffer`. They
  assert via a test subscriber on `logs.events`.
- `proxy/mtls_test.go` extends to cover dual-cert minting and CN-to-user
  mapping (load `vibepit-sandbox` cert, attempt to publish to `allow.http`,
  expect permission denied).

### Host package

- `cmd/control_test.go` boots an in-process bus + handlers and exercises
  `ControlClient` end-to-end (no HTTP test server). Covers happy path and
  service-error-header path.
- `cmd/monitor_ui_test.go` replaces the mock HTTP server pattern with a test
  bus; live-log delivery is driven by publishing to `logs.events` from the
  test. Reconnect/dedupe is its own subtest.

### Integration

`integration_test.go` keeps its end-to-end shape (real container, real bus).
URLs and transport are adjusted; the scenarios are unchanged.

## Files Touched

New:
- `proxy/bus.go` — embedded NATS server lifecycle, in-process client,
  subscriber registration, `AllowlistHandler`, `ConfigHandler`.
- `proxy/bus_test.go` — replaces `api_test.go`.

Modified:
- `proxy/server.go` — startup sequence, lifecycle, drop control HTTP server.
- `proxy/log.go` — `LogEntry.ID` to string (ULID); add subscriber loop; add
  `since` filtering for history; drop direct `Append` from external callers.
- `proxy/http.go`, `proxy/dns.go` — take publisher dependency, stamp ULIDs.
- `proxy/mtls.go` — mint two client certs per session; extend
  `LoadSessionTLSConfig` with a role argument.
- `cmd/control.go` — swap `http.Client` for `*nats.Conn`; new
  `LogsSince`/`SubscribeLogs`; remove `LogsAfter`.
- `cmd/monitor_ui.go` — subscribe-driven log loop, reconnect handler.
- `cmd/bootstrap.go` — adjust cert distribution for the renamed/extended
  `LoadSessionTLSConfig`.
- `cmd/session.go` — `WriteSessionCredentials` writes per-role cert pairs;
  `LoadSessionTLSConfig` takes a role argument.

Deleted:
- `proxy/api.go`, `proxy/api_test.go`.

## New Dependencies

- `github.com/nats-io/nats-server/v2`
- `github.com/nats-io/nats.go`
- `github.com/oklog/ulid/v2`

`nats-server` is the largest add (a few MB of compiled code with JetStream
disabled). Acceptable for what the bus enables.

## Migration Notes

This is a single-commit, non-backward-compatible swap of internal transport.
There is no version negotiation between client and server, no overlap period,
and no migration shim — every CLI invocation and every running session share
one binary, so a single release flips both sides simultaneously.

Users running an older client against a newer proxy (or vice versa) during
upgrade will see connection errors; they should re-run their session under
the new binary. This matches today's behavior on any proxy-side change.
