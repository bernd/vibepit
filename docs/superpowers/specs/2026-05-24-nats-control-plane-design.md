# NATS Control Plane: Replace HTTP Control API with Embedded NATS Hub

## Overview

Replace the HTTP+mTLS control API on the proxy container with an embedded NATS
server that becomes the **central communication hub** for the proxy, sandbox,
and host. Preserve today's five control operations (queries, mutations, live
log delivery) over NATS subjects. Reuse the existing mTLS material for transport
security and use TLS cert mapping for per-role permissions.

Live logs move onto a **JetStream stream** (memory-backed) so that the stream
itself is the authoritative, ordered log ring and clients consume it with
ordered consumers — backfill and live tail in a single subscription. From the
user's perspective the features (logs, stats, config, allow-http, allow-dns) are
unchanged; the transport and the log-delivery mechanism change underneath.

The motivation is to establish a single bus that future bidirectional patterns
build on additively (e.g. proxy-initiated approval requests surfaced through
ward's status bar, in-sandbox observability publishing). This spec lays down the
bus, the JetStream log stream, and the role/permission schema; the follow-ups
are additive.

## Scope

In scope:
- Replace `proxy/api.go` (HTTP control API) and the third HTTP server in
  `proxy/server.go` with an embedded NATS server (`JetStream` enabled,
  **memory storage**).
- Create a memory-backed JetStream stream for logs; producers publish to it,
  consumers read it with ordered consumers.
- Define the subject map mirroring today's operations.
- Refactor `cmd/control.go`'s `ControlClient` to use NATS as transport while
  keeping its public method surface (with log methods simplified — see below).
- Mint three client certs per session (`vibepit-internal`, `vibepit-user`,
  `vibepit-sandbox`) and
  configure NATS users with per-role permissions.
- Flip live log delivery from polling to an ordered-consumer subscription.
- Replace `LogBuffer` (in-memory ring + history responder) with a small
  `StatsAggregator` (in-process stream consumer that folds entries into the
  `DomainStats` map and answers `stats`). The stream owns history.
- Remove the producer-assigned `LogEntry.ID` counter; ordering and dedup use the
  server-assigned JetStream **stream sequence**.

Out of scope (explicit, deferred to follow-up specs):
- Proxy → ward bidirectional block-approval flow.
- Sandbox-side NATS client (`vibed` connecting as `sandbox`). The cert is
  minted but not distributed.
- **Persistence across proxy restarts.** The log stream uses memory storage, so
  a proxy restart empties it — matching today's behavior. Adopting JetStream
  with memory storage is deliberately *not* the same as durable persistence;
  switching to file storage + a mounted volume later is additive.
- Cross-session bus multiplexing or multi-tenant accounts.

## Architecture

### Placement

The NATS server is embedded directly in the proxy binary via
`github.com/nats-io/nats-server/v2/server`. It runs in-process alongside the
existing HTTP proxy, DNS server, and SSH forwarder, and listens on the same
published port that `ControlAPIPort` exposes today. The field name
`ControlAPIPort` and JSON tag `control-api-port` are kept — only the protocol
underneath changes.

JetStream is enabled with **memory storage**. A `StoreDir` is still required for
JetStream account/metadata even when streams are memory-backed; it points at a
writable path in the proxy container (the proxy container has a writable rootfs;
`/tmp` is a safe choice). The NATS **monitoring HTTP port (8222) is explicitly
left disabled** so nothing unauthenticated is exposed.

### Components

```
HTTPProxy ─┐   Publish (core)              ┌─► monitor   (ordered consumer: history + live)
           ├─► "logs.events" ─► [JetStream ├─► ward      (ordered consumer — future use)
DNSServer ─┘                    log stream]└─► StatsAggregator (in-process consumer ─► DomainStats)
                                  ▲ authoritative ring (MaxMsgs, DiscardOld, memory)

CLI ──► request "stats"      ──► StatsAggregator ──► DomainStats
CLI ──► request "config"     ──► ConfigHandler   ──► ProxyConfig
CLI ──► request "allow.http" ──► AllowlistHandler ──► HTTPAllowlist
CLI ──► request "allow.dns"  ──► AllowlistHandler ──► DNSAllowlist
```

`HTTPProxy` and `DNSServer` lose their `*LogBuffer` field. They take a thin
publisher dependency (a `LogPublisher` over the proxy's internal NATS client) and
publish each entry to `logs.events` with core `nc.Publish`; the JetStream stream
captures that subject. They never import the stream or the aggregator.

The **JetStream stream** is the authoritative, ordered log ring. It is configured
with `Storage: Memory`, `MaxMsgs: 10000` (today's `LogBufferCapacity`),
`Discard: DiscardOld`, capturing subject `logs.events`. History is served by
replaying the stream; there is no separate history responder and no hand-rolled
ring.

`StatsAggregator` is a small in-process **durable** consumer over the stream. It
folds each delivered entry into the `map[string]*DomainStats` and answers the
`stats` request/reply. Because it consumes every delivered message once
(independent of stream retention), stats stay session-cumulative — not capped at
the 10k retained entries. It must be durable / keep up so it sees each message
exactly once (no drop-and-recreate that would double-count or miss).

`AllowlistHandler` and `ConfigHandler` are small new types in `proxy/bus.go`
holding the allowlist references / config; each registers a single subject.

A new `proxy/bus.go` wraps embedding/lifecycle of the nats-server, stream
creation, the internal client, and handler/consumer registration. The
`ControlAPI` type is deleted; its glue moves into the bus.

### Roles, Certificates, and Permissions

The session mints **three** client certs at startup, all signed by the same
ephemeral CA from `proxy/mtls.go`:

| Cert CN | Purpose | Distribution |
|---|---|---|
| `vibepit-internal` | The proxy's own connection — producers (`HTTPProxy`, `DNSServer`) and in-process handlers (`StatsAggregator`, `AllowlistHandler`, `ConfigHandler`). | Minted on the host; passed into the proxy container as env vars (`VIBEPIT_PROXY_INTERNAL_CERT/KEY`) alongside the server key. Never written to a session dir. See the credential-exposure note below. |
| `vibepit-user` | All host-side tools: `allow-http`, `allow-dns`, `monitor`, `ward` | Written to the same location today's single client cert is written. |
| `vibepit-sandbox` | Future in-sandbox `vibed` observability publisher. | Minted but **not written to disk** in this spec. Sits unused until a follow-up wires sandbox-side delivery. |

> **Credential exposure.** The `vibepit-internal` cert/key is the broadest
> identity on the bus (publish/subscribe `>`), and it is passed into the proxy
> container through environment variables, so it is readable by anything that can
> inspect the container's environment (`docker inspect`, the runtime's on-disk
> container config, `/proc/1/environ` inside the container, image/commit export).
> This is accepted rather than mitigated: the server private key must reach the
> proxy by the same channel regardless, and reading a container's environment
> already requires container-runtime access (≈ host root) or in-container root —
> a position from which the control bus is compromised anyway (the live process
> memory holds the same key). The credential is ephemeral (per session, scoped to
> `tls://127.0.0.1:ControlAPIPort`), and the host port is published only to
> `127.0.0.1`. Keeping the internal key out of the host/env entirely would
> require the proxy to mint its own internal CA in-process and merge it into the
> server's `ClientCAs`; that is a deliberate non-goal here given the threat model
> above.

NATS is configured with `TLSMap: true` (`verify_and_map`) and `TLSRequired:
true` / `ClientAuth: RequireAndVerifyClientCert`. **Every** connection — the
proxy's own included — is authenticated by mapping its client cert to a NATS
user. There is no password user and no `InProcessServer` connection (see the
verified note below for why).

> **Cert→user mapping is by full subject DN, not bare CN.** Verified against
> nats-server v2.14.2 (`server/auth.go:checkClientTLSCertSubject`): with no email
> or DNS SAN on the cert, the server matches the user against the certificate's
> RFC2253 subject DN string. A CN-only cert (`Subject.CommonName =
> "vibepit-user"`) maps to NATS user **`"CN=vibepit-user"`**, *not*
> `"vibepit-user"`. The `Username` fields below are therefore full DNs.

The user/permission config:

```
"CN=vibepit-internal":   # the proxy itself
  publish:   [">"]
  subscribe: [">"]

"CN=vibepit-user":       # host tools
  publish:
    - "stats"
    - "config"
    - "allow.http"
    - "allow.dns"
    # JetStream API for the ordered log consumer. Fully stream-scoped: the host
    # client uses the `jetstream` package and binds to VIBEPIT_LOGS by name, so
    # no account-wide "$JS.API.STREAM.NAMES" / "$JS.API.INFO" is needed.
    - "$JS.API.STREAM.INFO.VIBEPIT_LOGS"
    - "$JS.API.CONSUMER.CREATE.VIBEPIT_LOGS.>"
    - "$JS.API.CONSUMER.INFO.VIBEPIT_LOGS.>"
    - "$JS.API.CONSUMER.DELETE.VIBEPIT_LOGS.>"
    - "$JS.API.CONSUMER.MSG.NEXT.VIBEPIT_LOGS.>"
  subscribe:
    - "_INBOX.>"          # request/reply + ordered-consumer (pull) delivery

"CN=vibepit-sandbox":    # future obs publisher
  publish:   ["obs.>"]
  # Scoped reply inbox, NOT the shared _INBOX.> — the sandbox role must not be
  # able to read other clients' request replies or ordered-consumer log
  # deliveries. A future sandbox client dials with
  # nats.CustomInboxPrefix("_INBOX.sandbox").
  subscribe: ["_INBOX.sandbox.>"]
```

> **Why no InProcessServer.** The original plan had the proxy connect in-process
> with a password user. A prototype against nats-server v2.14.2 disproved it:
> with global `TLSMap: true`, the server requires a client cert on **every**
> connection (`auth.go`: *"User required in cert, no TLS connection state"*), and
> an `InProcessServer` connection has no TLS state, so it is rejected
> (`Authorization Violation`) regardless of any password. The fix — verified
> end-to-end — is to give the proxy its own `vibepit-internal` client cert and
> connect over the **loopback TLS listener** (`tls://127.0.0.1:ControlAPIPort`)
> like any other client. This keeps a single uniform cert-mapped auth model. The
> only cost is loopback TLS framing on the log path instead of an in-memory pipe
> — negligible for this volume.

> **Host client uses the `github.com/nats-io/nats.go/jetstream` package** (not
> the legacy `nats.go` JetStream context). It binds to `VIBEPIT_LOGS` by name
> (`js.Stream(ctx, "VIBEPIT_LOGS")`) and creates an ordered consumer on that
> stream handle, so the publish ACL is fully stream-scoped — no account-wide
> `$JS.API.STREAM.NAMES`/`$JS.API.INFO`. The required grants are exactly the five
> `$JS.API.*.VIBEPIT_LOGS*` subjects above, verified end-to-end.

Verified end-to-end by the prototype: all three certs map to their expected
users; the `vibepit-user` cert — with only the stream-scoped grants above —
binds the stream by name, creates an ordered consumer (`jetstream` package,
`DeliverAllPolicy`), and reads a backfilled log entry from the memory stream; the
sandbox cert is allowed to publish `obs.>` and is denied `allow.http` (the denial
arrives as an async permission violation — see the wire-format caveat).

The cert generation in `proxy/mtls.go` extends to mint the three client certs
with distinct CNs. `WriteSessionCredentials` writes only the `vibepit-user` pair
(`user-cert.pem` / `user-key.pem`) to the session dir; the `vibepit-internal`
pair is handed to the proxy container via env vars (`VIBEPIT_PROXY_INTERNAL_CERT`
/ `VIBEPIT_PROXY_INTERNAL_KEY`), and the `vibepit-sandbox` pair is minted but not
distributed this spec.
`LoadSessionTLSConfig` (in `cmd/session.go`) extends to take a role string
(`"user"` today, `"sandbox"` later) and load the corresponding pair. The shared
`ca.pem` is unchanged.

## Subject Map

| Today (HTTP) | NATS subject | Pattern |
|---|---|---|
| `GET /logs` (initial + polling backfill) | `logs.events` via JetStream **ordered consumer** (`DeliverAll`) | stream consume |
| `GET /logs` (live tail, was polled) | same ordered consumer (continues into live) | stream consume |
| `GET /stats` | `stats` | request/reply |
| `GET /config` | `config` | request/reply |
| `POST /allow-http` | `allow.http` | request/reply |
| `POST /allow-dns` | `allow.dns` | request/reply |
| — (reserved) | `obs.>` | publish (sandbox role) |

There is no `logs.history` subject. History and live tail are the same ordered
consumer: `DeliverAll` replays the retained stream in order, then transitions
seamlessly into live delivery. This removes the separate history endpoint, the
`since`/`safetyMargin` cursor logic, and the client-side dedup set.

## Wire Format

JSON throughout.

**Request/reply** (`stats`, `config`, `allow.http`, `allow.dns`) follows the
NATS service-error header convention (the header convention only — not the full
`micro` service framework): success returns the typed result as the raw JSON
body; errors return an empty body with headers:

```
Nats-Service-Error-Code: <int>   (absent on success)
Nats-Service-Error:       <human message>   (absent on success)
```

Error codes: `400` for invalid request payloads, `500` for handler-internal
failures.

> **Permission-denied UX caveat.** A NATS publish-permission violation is
> reported as an *asynchronous* connection error, not as a synchronous reply, so
> a denied request/reply call surfaces as a 5s **timeout**, not a clean error.
> `ControlClient` installs a `nats.ErrorHandler` to capture async permission
> violations and surface a useful message; the mTLS permission test asserts via
> that handler, not via the request return value.

**Log stream entries** are published to `logs.events` as raw `LogEntry` JSON
(no envelope, no ID field). Ordering and dedup use the JetStream stream sequence
from message metadata (`msg.Metadata().Sequence.Stream`), not a payload field.

Subject-specific payloads:

| Subject | Request body | Reply body (success) |
|---|---|---|
| `stats` | `{}` | `map[string]DomainStats` |
| `config` | `{}` | `MergedConfig` |
| `allow.http` | `{"entries": [...]}` | `{"added": [...]}` |
| `allow.dns` | `{"entries": [...]}` | `{"added": [...]}` |
| `logs.events` (pub) | `LogEntry` (raw) | — (consumed via stream) |

### Producer publish semantics

Producers (`HTTPProxy`, `DNSServer`) call `PublishLog` synchronously on the
proxy/DNS request path, so it **must never block**. `PublishLog` does only a
non-blocking hand-off: it sends the entry to a buffered channel and a single
dedicated publisher goroutine drains it, doing the `json.Marshal` and the publish
entirely off the request path. When the hand-off queue is full or an entry fails
to marshal or publish, the entry is **dropped** and a counter is incremented.
Logs and stats are not control data; an undercount under sustained overload is
the accepted trade-off for keeping the request path fast, and the drop counter is
surfaced in the `stats` reply (`StatsReply.Dropped`) so the undercount is visible
rather than silent.

> **As-built (revised after review):** the publisher goroutine uses **core
> `nc.Publish`**, not `js.PublishAsync`. The JetStream stream still captures the
> `logs.events` subject, so history and ordering are unchanged, but there is no
> per-message ack pipeline. The original design used `PublishAsync` with a
> `WithPublishAsyncMaxPending` window plus a `PublishAsyncPending()` pre-check to
> avoid the ~200ms stall-and-drop that a full ack window causes
> (`ErrTooManyStalledMsgs`). That machinery was removed: the producer is gated by
> real network I/O and cannot outrun an in-process, memory-backed consumer, so
> ack-based flow control never engaged and bought nothing here. The trade-off is
> weaker delivery confirmation (core publish confirms only that the client queued
> the bytes), which is acceptable for best-effort, memory-backed observability
> logs. Drops now come only from a full hand-off queue or a marshal/publish error.

### Log Entry ID

`LogEntry.ID` is **removed**. The producer-assigned `uint64` counter (and the
considered ULID alternative) are unnecessary: JetStream assigns a totally
ordered, monotonic `uint64` **stream sequence** server-side. Consumers use it for
ordering and dedup; if a stable display ID is wanted it is derived from the
stream sequence at consume time, not stored in the payload. This removes the
`nextID` state from producers entirely and drops the `github.com/oklog/ulid/v2`
dependency that an earlier revision proposed.

## Server Lifecycle

### Startup (`proxy.Server.Run`)

1. Load TLS material (server cert + CA) from env vars; load the existing
   `MTLSCredentials` machinery extended to mint three client certs
   (`vibepit-internal`, `vibepit-user`, `vibepit-sandbox`).
2. Construct embedded `nats-server` with:
   - `Host: "0.0.0.0"`, `Port: cfg.ControlAPIPort`
   - `Users: []*server.User{internalU, userU, sandboxU}` (keyed by full subject
     DN, e.g. `"CN=vibepit-internal"`)
   - `TLSConfig: tlsCfg`, `TLSMap: true`, `TLSRequired: true`
   - `JetStream: true`, `StoreDir: "/tmp/vibepit-js"` (memory streams; dir is
     for JS metadata)
   - monitoring port disabled, `NoSigs: true`
3. Start the server; wait for `ReadyForConnections(5 * time.Second)`.
4. Connect the proxy's own client over the loopback TLS listener
   (`tls://127.0.0.1:ControlAPIPort`) with the `vibepit-internal` cert. (Not
   `InProcessServer` — global `TLSMap` requires a cert on every connection; see
   the verified note under Roles.)
5. Create the JetStream log stream: `Name: VIBEPIT_LOGS`, `Subjects:
   ["logs.events"]`, `Storage: Memory`, `MaxMsgs: 10000`, `Discard: DiscardOld`.
6. Register the consumers/handlers on the internal connection: `StatsAggregator`
   (durable consumer over the stream, `DeliverAll`), and request/reply handlers
   on `stats`, `config`, `allow.http`, `allow.dns`. Call `nc.Flush()` to ack
   registrations.
7. Start the HTTP proxy, DNS server, and SSH forwarder (only after step 6, so
   the stream and aggregator exist before any entry is published). Producers
   publish over the same internal connection.

### Shutdown (`ctx.Done()`)

1. Stop HTTP proxy, DNS server, SSH forwarder with the existing 5-second
   deadline.
2. Flush buffered publishes to the server (`nc.FlushTimeout`), then `nc.Drain()`
   on the internal client — blocks until handler/consumer queues empty.
3. `ns.Shutdown()` on the embedded NATS server.

Bus errors flow to the existing `errCh` in `Server.Run` like HTTP/DNS errors.

## Host Client Refactor (`cmd/control.go`)

`ControlClient`'s public surface stays identical for the non-log methods; the log
methods simplify substantially because the ordered consumer subsumes
backfill+live+reconnect. It uses the `github.com/nats-io/nats.go/jetstream`
package (binds the log stream by name, keeping the publish ACL stream-scoped).

```go
import (
    "github.com/nats-io/nats.go"
    "github.com/nats-io/nats.go/jetstream"
)

type ControlClient struct {
    nc *nats.Conn
    js jetstream.JetStream
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
        nats.ErrorHandler(asyncErrHandler), // surfaces permission violations
    )
    if err != nil { return nil, err }
    js, err := jetstream.New(nc)
    if err != nil { nc.Close(); return nil, err }
    return &ControlClient{nc: nc, js: js}, nil
}
```

Shared reply decoder for the request/reply methods:

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
| `Logs()` | **Removed.** It had no production caller (test-only), and the ordered consumer subsumes one-shot history. Tests read history via `SubscribeLogs` (read-N-then-unsub) or a test ordered consumer. |
| `LogsAfter(id uint64)` | **Removed.** Its only production caller was the monitor poll loop; backfill is now part of the ordered consumer. |
| `LogsSince(t time.Time)` | **Not introduced.** No `since` cursor — the ordered consumer replays from the start of the retained stream. |
| `SubscribeLogs(ctx, ch chan<- LogEntry) (stop func(), done <-chan struct{}, error)` | **New (primitive).** `js.Stream(ctx, "VIBEPIT_LOGS")` → `stream.OrderedConsumer(ctx, ...)` → `cons.Consume(...)`, decoding each `jetstream.Msg` into the channel — a bounded tail of retained history then live entries, in stream order. `stop` calls `ConsumeContext.Stop()`; `ctx` bounds only the setup calls so a stalled subscribe is cancelable. |
| `WatchLogs(ctx) <-chan LogEvent` | **New (as-built; replaces direct `SubscribeLogs` use in the UI).** Built on `SubscribeLogs`, it owns the whole subscription lifecycle — open, retry on failure, and **resubscribe on reconnect** — and emits one ordered stream of `LogEvent`s, each either a log entry or a connection-state change. Canceling `ctx` closes the stream. See the as-built correction below. |
| `Stats()` / `Config()` / `AllowHTTP()` / `AllowDNS()` | Same signatures; internals use `Request` + `decodeReply`. |
| `Close()` | `nc.Close()`. |

### Monitor TUI Changes

`cmd/monitor_ui.go`:
- Call `WatchLogs(ctx)` once and render what arrives: a `LogEntryEvent` appends a
  line; a `LogConnEvent` drives the connection-status UI (error banner and the
  disconnect grace timer that returns to the session selector). Cancel `ctx` on
  teardown.
- `pollLogsCmd`, `pollCursor`, and the 1s poll cadence are **removed** — delivery
  is event-driven and ordered.
- The screen holds **no** subscription state — no consumer handle, no
  generation/epoch, no reconnect or retry logic. All of that lives behind
  `WatchLogs` in `ControlClient`.

> **As-built correction (revised after review):** the original design assumed the
> ordered consumer transparently survives reconnects ("gap recovery is internal
> to the ordered consumer"). That holds for a transient network blip to the *same*
> proxy, but **not** across a proxy restart: the memory-backed stream is recreated
> with sequence numbers reset to 1, so the old consumer's cursor points past the
> new head and silently delivers nothing. `WatchLogs` therefore tears the consumer
> down and opens a fresh one on every reconnect. To avoid replaying history twice
> when a retry and a reconnect coincide, `ControlClient` keys resubscription on a
> reconnect-generation counter and skips a redundant rebuild for a generation it
> already holds a live consumer for.

### CLI Tools

`allow-http`, `allow-dns`, and `update` need no structural changes — they are
short-lived request/reply callers. Initial dial either succeeds or returns a
user-visible error (the async error handler covers permission violations).

## Testing

### Proxy package

- New `proxy/bus_test.go` — starts an embedded NATS server with JetStream
  (memory) in-process, dials a test client, asserts each request/reply subject's
  handler and the log stream end-to-end. Replaces `api_test.go`.
- Log stream tests: publish to `logs.events` (core `nc.Publish`), assert an
  ordered consumer reads them in stream order; assert `MaxMsgs`/`DiscardOld`
  retention; assert the stream sequence is monotonic.
- `StatsAggregator` tests: publish entries, assert the `DomainStats` map folds
  correctly and that stats remain cumulative beyond the retained window.
- `HTTPProxy` and `DNSServer` tests stop reaching into a ring. They assert via a
  test ordered consumer on `logs.events`.
- `proxy/mtls_test.go` extends to cover dual-cert minting and CN-to-user mapping.
  The permission test loads the `vibepit-sandbox` cert, attempts to publish to
  `allow.http`, and asserts the violation arrives via the **async error handler**
  (not a synchronous denial; see the wire-format caveat).

### Host package

- `cmd/control_test.go` boots an in-process bus + JetStream + handlers and
  exercises `ControlClient` end-to-end (no HTTP test server). Covers happy path
  and service-error-header path.
- `cmd/monitor_ui_test.go` replaces the mock HTTP server pattern with a test
  bus; live-log delivery is driven by publishing to `logs.events`. Ordered
  delivery and post-reconnect dedup are their own subtests.

### Integration

`integration_test.go` keeps its end-to-end shape (real container, real bus).
Transport is adjusted; the scenarios are unchanged.

## Files Touched

New:
- `proxy/bus.go` — embedded NATS server + JetStream lifecycle, log stream
  creation, internal client, `StatsAggregator`, `AllowlistHandler`,
  `ConfigHandler`.
- `proxy/bus_test.go` — replaces `api_test.go`.

Modified:
- `proxy/server.go` — startup sequence (stream + consumers before producers),
  lifecycle, drop control HTTP server.
- `proxy/log.go` — `LogBuffer` ring/history responder removed; `StatsAggregator`
  added (in-process stream consumer + `stats` responder). `LogEntry.ID` removed.
- `proxy/http.go`, `proxy/dns.go` — take a `LogPublisher` dependency, publish to
  `logs.events` with core `nc.Publish`; drop `*LogBuffer` field.
- `proxy/mtls.go` — mint three client certs per session (`vibepit-internal`
  passed to the proxy container via env; `vibepit-user` written; `vibepit-sandbox`
  minted only). See the credential-exposure note above.
- `cmd/control.go` — swap `http.Client` for `*nats.Conn` + `jetstream.JetStream`;
  new ordered-consumer `SubscribeLogs`/`Logs`; remove `LogsAfter`; add async
  error handler.
- `cmd/monitor_ui.go` — ordered-consumer-driven log loop; remove polling.
- `cmd/bootstrap.go` — adjust cert distribution for the extended cert minting.
- `cmd/session.go` — `WriteSessionCredentials` writes the `vibepit-user` pair;
  `LoadSessionTLSConfig` takes a role argument.

Deleted:
- `proxy/api.go`, `proxy/api_test.go`.

## New Dependencies

- `github.com/nats-io/nats-server/v2` (embedded server; JetStream included)
- `github.com/nats-io/nats.go` (client) — host client and proxy use its
  `github.com/nats-io/nats.go/jetstream` subpackage (the newer API), not the
  legacy `nc.JetStream()` context.

Verified against `nats-server/v2 v2.14.2` + `nats.go v1.52.0` in the prototype.
`oklog/ulid` is **not** added — the JetStream stream sequence replaces the need
for a producer-generated orderable ID. `nats-server` is the largest add: a
measured **+9.4 MB stripped (+36%)** on the real binary (~26 MB → ~36 MB) when
linking `server.NewServer` + `jetstream.New`. Acceptable for a tool shipped as
release archives, but it nearly doubles download size — a conscious tradeoff for
what the central hub enables.

## Migration Notes

This is a single-commit, non-backward-compatible swap of internal transport.
There is no version negotiation between client and server, no overlap period,
and no migration shim — every CLI invocation and every running session share one
binary, so a single release flips both sides simultaneously.

> The proxy runs `vibepit proxy` from a **separately tagged container image**
> (`docker-publish.yml`). Confirm the released CLI binary and the image's proxy
> binary are version-locked; otherwise an image lag opens a NATS-client-vs-old-
> proxy skew window. Users hitting skew during upgrade see connection errors and
> should re-run their session under the new binary — matching today's behavior on
> any proxy-side change.
