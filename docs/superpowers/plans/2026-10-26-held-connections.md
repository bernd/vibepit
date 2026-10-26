# Held Connections Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** While a `--prompt` overlay or `monitor` is connected, the proxy holds un-allowlisted HTTP/HTTPS requests for a configurable time (default 10s) so the user can allow them before the agent sees a failure.

**Architecture:** The proxy gets an event hub. Its subscribers are the watchers, and a hold registry makes held requests wait on a per-target hold that ends on allow, deny, timeout, abandonment or loss of all watchers. The control API exposes the hub as a server-sent-events stream (`GET /events`). On the client side, a reconnecting feed replaces `/logs` polling in the overlay and the monitor. The overlay's approve screen works through a queue of targets, and the monitor highlights held rows and can deny them.

**Tech Stack:** Go 1.26, `net/http` (SSE via `http.ResponseController`), goproxy, Bubble Tea v2 / lipgloss v2, koanf, testify.

**Spec:** `docs/superpowers/specs/2026-10-26-held-connections-design.md`. Read it before any task.

## Global Constraints

- Only HTTP/HTTPS proxy requests are held, never DNS.
- Only blocks with code `BlockNotAllowlisted` where `AllowFixes(key, code)` is true are held.
- Config key `hold-timeout`: a Go duration string. Project overrides global, the default is `10s`, and `0` disables holding.
- Hold limits: `maxHeldTargets = 32` and `maxHeldRequests = 64`. Past either limit a request blocks immediately.
- With no open `/events` stream, a request blocks immediately, exactly as today.
- An allowed held request runs `filter` again (including the CIDR check) before it continues.
- `hold` entries never count toward `HostStats.Allowed`/`Blocked`. The only change ever made to a stored entry is filling in a `hold` entry's `outcome` when its hold ends.
- Log events are published in ID order: the `OnAdd` hook runs under the log's lock.
- Hold registration re-checks the allowlist and the `DenySet` under the registry lock. `/allow-http` and `/deny` update their list before taking that lock to notify.
- Every new stream gets a `cursor` event (the history boundary, read under the same lock as the history) right after its history. Clients reconnect from it, never from 0.
- Every new stream gets a `hold-open` snapshot of all open holds after its history, whatever the tail size.
- Clients never combine `/logs` with the stream. `/events` replays history itself (`tail=N` on first connect, `after=N` on reconnect), and the server subscribes before reading that history.
- Stream bounds: a 256-event queue per subscriber with disconnect on overflow, a 10s deadline per write, a 15s keepalive, and a client reconnect after 30s of silence.
- Hold outcomes on the wire: `allowed`, `denied`, `timeout`, `abandoned`, `unwatched`. Clients treat unknown values as `timeout`.
- Held colour is `tui.ColorYellow`.
- `esc` on the approve screen is not a decision. Dismissed targets prompt again on their next hold or block.
- A newer client against an older proxy (`/events` returns 404) falls back to polling `/logs` with no holds.
- Follow AGENTS.md: gofmt, comments explain why, table-driven tests with testify, `any` over `interface{}`.
- `hold-mockups.html` in the repo root is a throwaway mockup. Never commit it.

## Review Focus

1. **A stream open for minutes.** The control server has `ReadTimeout` 15s and `WriteTimeout` 30s, and a stream must outlive both. *Test in Task 6.*
2. **The agent drops a held request** (client timeout, Ctrl-C). Only that request stops waiting: no log entry, no leaked goroutine, other waiters unaffected, and once the last one goes the hold ends `abandoned`. *Tests in Tasks 4 and 5.*
3. **The last watcher quits mid-hold** (monitor closed, session ended). Waiting requests fail immediately rather than at the deadline. *Tests in Tasks 4 and 6.*
4. **A wildcard allow.** `*.example.com:443` from `allow-http` releases every matching held target. A released target that resolves into a blocked CIDR is still blocked. *Tests in Tasks 5 and 6.*
5. **A stuck stream reader** (monitor suspended with Ctrl-Z). Publishing never blocks the proxy. The slow subscriber is dropped, and its client reconnects without losing or duplicating lines, getting the outcomes of holds that ended meanwhile. *Tests in Tasks 3, 6 and 7.*

---

### Task 1: `hold-timeout` configuration

**Files:**
- Modify: `config/config.go` (`GlobalConfig`, `ProjectConfig`, `MergedConfig`, `Merge`)
- Modify: `proxy/server.go` (`ProxyConfig`, `NewServer`, `Server`)
- Test: `config/config_test.go`, `proxy/server_test.go` (create if absent)

**Interfaces:**
- Produces: `config.DefaultHoldTimeout = "10s"`. `MergedConfig.HoldTimeout string` and `ProxyConfig.HoldTimeout string`, both tagged `json:"hold-timeout,omitempty"`. `(s *Server) holdTimeout() time.Duration` returns 0 when the field is empty, since configs written by tests and older CLIs mean "no holding".

- [ ] **Step 1: Write the failing tests**

`TestMergeHoldTimeout`, table-driven over project/global YAML values:
- `global=""`, `project=""` → `"10s"`
- `global="5s"` → `"5s"`
- `global="5s"`, `project="20s"` → `"20s"`
- `project: 0` (a YAML integer) → `"0"`
- `project="abc"` → error containing `hold-timeout`
- `project="-1s"` → error containing `hold-timeout`

Write each case's YAML to temp files and go through `Load` + `Merge`, so koanf's weak typing of `0` is covered.

`TestNewServerHoldTimeout`: a config JSON with `"hold-timeout":"3s"` → `holdTimeout()` is `3*time.Second`. With the key absent → `0`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./config/ ./proxy/ -run 'HoldTimeout' -v`
Expected: FAIL (fields undefined)

- [ ] **Step 3: Implement**

- Add a `HoldTimeout string` field tagged `koanf:"hold-timeout"` to both config structs.
- In `Merge`, pick project → global → `DefaultHoldTimeout`. Validate with `time.ParseDuration`, rejecting negative values. Wrap errors as `fmt.Errorf("hold-timeout: %w", err)`.
- Add `ProxyConfig.HoldTimeout`. `NewServer` parses it and returns an error for invalid values. Store the parsed duration on `Server`.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./config/ ./proxy/ -run 'HoldTimeout' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add config/ proxy/server.go proxy/server_test.go
git commit -m "Add hold-timeout setting carried to the proxy"
```

---

### Task 2: Hold entries in the log

**Files:**
- Modify: `proxy/log.go`
- Test: `proxy/log_test.go`

**Interfaces:**
- Produces:
  - `ActionHold Action = "hold"`.
  - `LogEntry.Deadline time.Time` tagged `json:"deadline,omitzero"`.
  - `type HoldOutcome string` with constants:

    | Constant | Value |
    |---|---|
    | `HoldNone` | `""` |
    | `HoldAllowed` | `"allowed"` |
    | `HoldDenied` | `"denied"` |
    | `HoldTimeout` | `"timeout"` |
    | `HoldAbandoned` | `"abandoned"` |
    | `HoldUnwatched` | `"unwatched"` |

    These live in `log.go` because the entry carries one.
  - `LogEntry.Outcome HoldOutcome` tagged `json:"outcome,omitempty"`.
  - `(b *LogBuffer) TailAt(n int) ([]LogEntry, uint64)` and `(b *LogBuffer) EntriesAfterAt(after uint64) ([]LogEntry, uint64)`. Each is the same as `Tail`/`EntriesAfter`, and also returns the highest assigned ID (`nextID-1`), read under the same lock: the history boundary.
  - `(b *LogBuffer) SetHoldOutcome(id uint64, o HoldOutcome) bool`. It sets the outcome on the buffered `hold` entry with that ID. It returns false when the entry is no longer buffered or isn't a hold. IDs are contiguous, so the ring index is computed, not searched.
  - `(b *LogBuffer) Add(entry LogEntry) uint64` now returns the assigned ID. Existing callers ignore it.
  - `(b *LogBuffer) OnAdd(f func(LogEntry))` registers one hook. It is called **while `b.mu` is held**, with the entry including its ID, so hooks see entries in ID order. The hook must not block and must not call back into the `LogBuffer`. `EventHub.Publish` meets both.
  - `LogEntry.Allowable()` is also true for `ActionHold` entries that `AllowFixes`.

- [ ] **Step 1: Write the failing tests**

- `TestLogBuffer_HoldEntriesDontCountInHosts`: add a hold entry for `docs.example.com:443`, then a block entry for the same host. `Hosts()` returns exactly one host with `Blocked == 1`, `Allowed == 0` and `LastAction == ActionBlock`. A hold entry alone adds no host at all.
- `TestLogBuffer_AddReturnsID`: two `Add` calls return `1` and `2`.
- `TestLogBuffer_OnAdd`: the hook receives every entry with its ID.
- `TestLogBuffer_OnAddInIDOrder`:
  - The hook sleeps 50ms when it sees ID 1. Two goroutines call `Add` at once (the second starts once the hook has entered for ID 1).
  - The hook observes IDs 1 then 2.
  - A run of 50 goroutines × 20 adds yields strictly increasing IDs in the hook. Run it with `-race`.
- Extend the `Allowable` table in `log_test.go:286` with a hold entry (`Code: BlockNotAllowlisted`) → true.
- JSON round-trip: a zero `Deadline` and an empty `Outcome` are omitted, and set ones survive.
- `TestLogBuffer_HistoryBoundary`:
  - With entries 1–100, `TailAt(0)` returns no entries and `100`, and `TailAt(2)` returns 99 and 100 with `100`.
  - `EntriesAfterAt(98)` returns 99 and 100 with `100`.
  - On an empty buffer both return `0`.
- `TestLogBuffer_SetHoldOutcome`:
  - It sets the outcome on a hold entry, visible through `EntriesAfter`/`Tail`.
  - It returns false for a block entry and for an ID that has been overwritten in a ring of capacity 3.
  - The hook from `OnAdd` is not called again.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./proxy/ -run 'LogBuffer|Allowable' -v`
Expected: FAIL

- [ ] **Step 3: Implement**

- `recordHostLocked` returns early for `ActionHold`.
- `Add` calls the hook before unlocking.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./proxy/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add proxy/log.go proxy/log_test.go
git commit -m "Log held requests without counting them as allowed or blocked"
```

---

### Task 3: Event hub and SSE encoding

**Files:**
- Create: `proxy/events.go`
- Test: `proxy/events_test.go`

**Interfaces:**
- Consumes: `HoldOutcome` (Task 2).
- Produces:
```go
type EventType string
const (
	EventLog      EventType = "log"       // data: LogEntry
	EventCursor   EventType = "cursor"    // data: {"id":N}: the history boundary
	EventHoldOpen EventType = "hold-open" // data: LogEntry (a hold entry); snapshot only
	EventHoldEnd  EventType = "hold-end"  // data: HoldEnd
)
type HoldEnd struct {
	ID uint64 `json:"id"` // the hold entry it ends
	HostKey
	Outcome HoldOutcome `json:"outcome"`
}
type Event struct {
	Type    EventType
	Log     LogEntry // EventLog and EventHoldOpen
	HoldEnd HoldEnd
	Cursor  uint64 // EventCursor
}
func NewEventHub() *EventHub
func (h *EventHub) Subscribe() (events <-chan Event, unsubscribe func())
func (h *EventHub) Publish(e Event)
func (h *EventHub) Watching() bool
func (h *EventHub) OnEmpty(f func())
func WriteEvent(w io.Writer, e Event) error
func ReadEvent(r *bufio.Reader) (Event, error)
```
- `eventBuffer = 256` per subscriber.
- `Publish` never blocks. A subscriber whose buffer is full is removed and its channel closed.
- `OnEmpty`'s hook runs in a new goroutine whenever the subscriber count drops to 0, whether from an unsubscribe or a drop. It runs in a goroutine because `Publish` may be called with other locks held.
- `unsubscribe` is idempotent.
- Wire format: `event: <type>\ndata: <json>\n\n`.
- `ReadEvent` skips comment lines (starting with `:`) and blank keepalives. It returns `io.EOF` at the end of the stream and an error for an unknown `event:` line. Clients skip unknown event types: `ReadEvent` returns them with `Type` set and nothing decoded, and callers ignore them.

- [ ] **Step 1: Write the failing tests**

- `TestEventHub_SubscribeAndPublish`: two subscribers both receive a published event. `Watching()` is true while either is subscribed and false after both unsubscribe.
- `TestEventHub_DropsSlowSubscriber`: one subscriber never reads. Publishing `eventBuffer+1` events returns promptly (wrap it in a 1s timeout) and that subscriber's channel ends up closed. A second, draining subscriber gets all events.
- `TestEventHub_OnEmpty`: the hook fires once when the last subscriber leaves (wait on a channel with a timeout). It doesn't fire when one of two leaves.
- `TestEventRoundTrip`, table-driven over the four event types: `WriteEvent` followed by `ReadEvent` yields the same `Event`. Input preceded by `": keepalive\n\n"` still decodes the next event.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./proxy/ -run 'EventHub|EventRoundTrip' -v`
Expected: FAIL

- [ ] **Step 3: Implement `proxy/events.go`**

- [ ] **Step 4: Run them to verify they pass**

Run: `go test -race ./proxy/ -run 'EventHub|EventRoundTrip' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add proxy/events.go proxy/events_test.go
git commit -m "Add the proxy event hub and its SSE encoding"
```

---

### Task 4: Hold registry

**Files:**
- Create: `proxy/hold.go`
- Test: `proxy/hold_test.go`

**Interfaces:**
- Consumes: `EventHub`, `HoldEnd`, `HoldOutcome` (Task 3). `LogBuffer.Add` returns the ID (Task 2). `DenySet` (`proxy/deny.go`).
- Produces:
```go
const (
	maxHeldTargets  = 32
	maxHeldRequests = 64
)
func NewHolds(timeout time.Duration, log *LogBuffer, hub *EventHub, denied *DenySet, allows func(Target) bool) *Holds
// Wait holds a request for k until the hold ends and returns how it ended.
// HoldNone: not held (timeout 0, nobody watching, denied, AllowFixes false,
// or over a limit). HoldAllowed without waiting: the allowlist admits the
// target by the time the registry lock is taken. HoldAbandoned: this
// request's ctx ended first.
func (h *Holds) Wait(ctx context.Context, k HostKey) HoldOutcome
func (h *Holds) End(t Target, o HoldOutcome)
func (h *Holds) EndAllowed()     // ends each open hold whose target the allowlist now admits, as HoldAllowed
func (h *Holds) Open() []LogEntry // the hold entries of all open holds, for the stream's snapshot
```

Behaviour:
- **A new hold** logs one entry: `Action: ActionHold`, `Source: SourceProxy`, `Reason: "domain not in allowlist"`, `Code: BlockNotAllowlisted`, `Deadline: now+timeout`. It keeps the returned ID and arms `time.AfterFunc(timeout, …HoldTimeout)`.
- **Under the registry lock, in this order:** check `allows(target)` (true → return `HoldAllowed` at once, with no hold entry); then `denied.Denied`, `hub.Watching()`, the timeout and the limits (any failing → `HoldNone`); then join or create the hold. The caller must have updated the allowlist or `DenySet` before calling `EndAllowed`/`End`, so a decision is never missed.
- **Admission:** a hold needs both `BlockNotAllowlisted` (the caller only calls `Wait` for that code) **and** `AllowFixes(k, BlockNotAllowlisted)`. `Wait` checks the latter itself.
- **Joining:** concurrent `Wait`s on the same target share the hold. No second entry is logged.
- **Ending** (any path) happens once: stop the timer, remove the hold, call `log.SetHoldOutcome(id, outcome)`, wake every waiter with the outcome, then publish `Event{Type: EventHoldEnd}`. The outcome is stored before the event goes out, so a stream's history read never lags the live event.
- **The last waiter leaving** before the end ends the hold as `HoldAbandoned`.
- `NewHolds` registers `hub.OnEmpty`. The hook re-checks `hub.Watching()` under the registry lock, and if nobody is watching it ends every open hold as `HoldUnwatched`.
- `Wait` checks `hub.Watching()` under the registry lock as well.

- [ ] **Step 1: Write the failing tests**

Use a real `EventHub` with one subscriber to make a watcher, `NewLogBuffer(100)`, and a timeout of 200ms unless noted. Each case runs `Wait` in goroutines and asserts outcomes with `require.Eventually`.

- `TestHolds_NotHeld`, table-driven, `Wait` returns `HoldNone` immediately with no log entry when:
  - the timeout is `0`;
  - there are no subscribers;
  - the target is in `DenySet`;
  - the host is an IPv6 literal (`::1`, port `443`).
- `TestHolds_Timeout`:
  - The result is `HoldTimeout` after ≥200ms.
  - Exactly one `hold` entry is logged, with `Deadline` ≈ start+200ms. Afterwards its stored `Outcome` is `timeout`.
  - The subscriber receives `EventLog` (the hold) then `EventHoldEnd{ID: holdID, Outcome: "timeout"}`.
- `TestHolds_ConcurrentRequestsShareOneHold`: 3 `Wait`s on one target produce one `hold` entry, and `End(t, HoldAllowed)` returns `HoldAllowed` to all 3.
- `TestHolds_EndAllowedMatchesWildcard`: holds on `a.example.com:443`, `b.example.com:443` and `other.org:443`. Adding `*.example.com:443` to the allowlist and calling `EndAllowed` ends the first two `allowed`, and `other.org` stays open.
- `TestHolds_AllowedBeforeRegistration`: add the target to the allowlist and call `EndAllowed` *before* `Wait`. `Wait` returns `HoldAllowed` immediately and logs no `hold` entry.
- `TestHolds_AllowRaceNeverStrands`: with a timeout of 5s, run 200 iterations of a `Wait` goroutine racing an `allowlist.Add` + `EndAllowed` goroutine, each on a fresh target. Every `Wait` returns `HoldAllowed` within 1s. Run it with `-race`.
- `TestHolds_DeniedBeforeRegistration`: `DenySet.Add` followed by `End(t, HoldDenied)` before `Wait` makes `Wait` return `HoldNone` at once.
- `TestHolds_Open`: returns the hold entries (with `Deadline`) of open holds, and drops ended ones.
- `TestHolds_Denied`: `End(t, HoldDenied)` returns `HoldDenied`.
- `TestHolds_CancelledRequestLeavesOthers`: two waiters. Cancelling one's ctx returns `HoldAbandoned` to it while the other keeps waiting. Cancelling the second ends the hold, and a `HoldEnd{Outcome: "abandoned"}` is published. No extra log entries.
- `TestHolds_LastWatcherLeaving`: with a 10s timeout, unsubscribing the only subscriber returns `HoldUnwatched` within 1s.
- `TestHolds_Limits`: 32 targets held, so a 33rd target returns `HoldNone` at once. Separately, 64 waiters on one target, so a 65th returns `HoldNone`.
- `TestHolds_NewHoldAfterEnd`: after a timeout, a new `Wait` on the same target logs a second `hold` entry with a new ID.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./proxy/ -run 'Holds' -v`
Expected: FAIL

- [ ] **Step 3: Implement `proxy/hold.go`**

- [ ] **Step 4: Run them to verify they pass**

Run: `go test -race -count=3 ./proxy/ -run 'Holds' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add proxy/hold.go proxy/hold_test.go
git commit -m "Add the hold registry for un-allowlisted requests"
```

---

### Task 5: Holding in the HTTP proxy

**Files:**
- Modify: `proxy/http.go`
- Test: `proxy/http_test.go`

**Interfaces:**
- Consumes: `Holds.Wait` (Task 4).
- Produces:
  - `(p *HTTPProxy) SetHolds(h *Holds)`, following the `SetHostVibepit` pattern. A nil value keeps today's behaviour.
  - `checkRequest(ctx context.Context, hostname, port string) filterResult`. Both handlers pass `ctx.Req.Context()`.

`checkRequest` logic:
1. Run `filter`.
2. If the result is `BlockNotAllowlisted` and holds are set, call `Wait(ctx, HostKey{SourceProxy, hostname, port})`:
   - `HoldAllowed` → run `filter` again and use that result.
   - `HoldAbandoned` → return the block without logging.
   - Anything else → keep the block.
3. Log the final result as today.

- [ ] **Step 1: Write the failing tests**

Run the proxy through `httptest.NewServer(p.Handler())` with a hub subscriber present.

- `TestHTTPProxy_HeldThenAllowed`: the backend is an `httptest` server on 127.0.0.1, with `NewCIDRBlocker(nil, []string{"127.0.0.0/8"})` so loopback passes. The request is made in a goroutine. Wait for the `hold` log entry, then `al.Add([]string{host})` + `holds.EndAllowed()`. Expect status 200 and body `ok`. The log holds `hold` followed by `allow`, with no `block`.
- `TestHTTPProxy_HeldThenCIDRBlocked`: the same, but with the default CIDR blocker. After the allow, the response is 403 with body "resolves to a blocked IP" and the final log entry has `Code == BlockCIDR`.
- `TestHTTPProxy_HeldCONNECTDenied`: an HTTPS request (`client.Get("https://denied.example:443/")`) is denied via `End`. The client gets an error (CONNECT rejected), and the log holds `hold` then `block` with `BlockNotAllowlisted`.
- `TestHTTPProxy_HoldTimesOut`: with a timeout of 200ms, plain HTTP gets 403 "not in the allowlist" after ≥200ms.
- `TestHTTPProxy_NoWatcherBlocksImmediately`: with no subscriber, a 403 arrives in under 100ms and no `hold` entry is logged.
- `TestHTTPProxy_AbandonedHoldNotLogged`: the client's request ctx is cancelled after the `hold` entry appears. No `block` entry follows (wait for `HoldEnd{abandoned}` on the subscriber, then assert the log).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./proxy/ -run 'HTTPProxy_' -v`
Expected: FAIL

- [ ] **Step 3: Implement**

- [ ] **Step 4: Run them to verify they pass**

Run: `go test -race ./proxy/ -v`
Expected: PASS (all existing proxy tests too)

- [ ] **Step 5: Commit**

```bash
git add proxy/http.go proxy/http_test.go
git commit -m "Hold un-allowlisted HTTP and HTTPS requests while someone watches"
```

---

### Task 6: `/events` stream, ending holds from the API, server wiring

**Files:**
- Modify: `proxy/api.go` (including `responseState`), `proxy/server.go`
- Test: `proxy/api_test.go`, `integration_test.go`

**Interfaces:**
- Consumes: `EventHub`, `WriteEvent` (Task 3). `Holds` (Task 4). `HTTPProxy.SetHolds` (Task 5). `LogBuffer.OnAdd` (Task 2).
- Produces:
  - `(a *ControlAPI) SetEvents(hub *EventHub, holds *Holds)`.
  - `(a *ControlAPI) Denied() *DenySet` (returns `&a.denied`).
  - `GET /events?tail=N` (first connect) or `GET /events?after=N` (reconnect). `tail` is capped at `TailSize`, and neither parameter means `tail=0`. 404 while no hub is set.
  - `responseState` gains `Unwrap() http.ResponseWriter` and `Flush()`. `Flush` sets `written` and delegates through `http.NewResponseController(rw.ResponseWriter).Flush()`. Without these, `http.ResponseController` returns `ErrNotSupported` for both flushing and deadlines.
  - `var eventsKeepalive = 15 * time.Second` (a `var` so tests can shorten it) and `const eventsWriteTimeout = 10 * time.Second`.

`/events` handler order:
1. Subscribe.
2. Clear the connection's read deadline and set a per-write deadline of `eventsWriteTimeout` via `http.NewResponseController`, so the stream outlives the server's `ReadTimeout`/`WriteTimeout`.
3. Send headers `Content-Type: text/event-stream` and `Cache-Control: no-cache`, then flush.
4. Read history and its boundary atomically with `log.EntriesAfterAt(after)` or `log.TailAt(tail)`, and send the entries as `EventLog`s. Hold entries carry their current `Outcome`.
4a. Send `Event{Type: EventCursor, Cursor: boundary}` and start the live "already sent" mark at the boundary.
4b. Send `holds.Open()` as `EventHoldOpen` events, whatever the tail size.
5. Stream live events, skipping `EventLog`s with an ID at or below the last one sent. Flush after each event. `hold-end` events are always sent, and clients apply them idempotently.
7. Write `": keepalive\n\n"` every `eventsKeepalive`.
8. Return when the subscriber channel closes (the subscriber was dropped), on a write error, or when the request ctx ends.

Other changes:
- `handleAllowHTTP` calls `holds.EndAllowed()` after `Add` has returned.
- `handleDeny` calls `holds.End(t, HoldDenied)` after `denied.Add`.
- In `Server.Run`: create the hub, call `log.OnAdd(func(e LogEntry){ hub.Publish(Event{Type: EventLog, Log: e}) })`, then `holds := NewHolds(s.holdTimeout(), log, hub, controlAPI.Denied(), func(t Target) bool { return allowlist.Allows(t.Host, t.Port) })`, `controlAPI.SetEvents(hub, holds)`, and `httpProxy.SetHolds(holds)`.

- [ ] **Step 1: Write the failing tests**

In `proxy/api_test.go`:
- `TestControlAPI_EventsUnsupportedWithoutHub`: 404.
- `TestControlAPI_EventsHistoryThenLive`, table-driven:
  - `after=1` with 3 entries yields 2 and 3, then a live entry.
  - `tail=2` yields 2 and 3, then a live entry.
  - No parameter yields only the live entry.
  - No duplicate IDs in any case.
- `TestControlAPI_EventsNoGapUnderLoad`: a goroutine adds 500 entries while `/events?after=0` opens. The client reads until ID 500 and sees IDs 1–500 exactly once each, in order.
- `TestControlAPI_EventsCursor`: with entries 1–100, `/events` (tail 0) yields no `log` events and `cursor` 100. An entry added afterwards arrives live as 101. `after=98` yields 99 and 100, then `cursor` 100.
- `TestControlAPI_EventsSnapshotsOpenHolds`: a hold is open behind 30 newer log entries. A new `/events` (no parameter, i.e. tail 0) yields a `hold-open` with that hold's entry before any live event. A second, already-ended hold is not in the snapshot.
- `TestControlAPI_EventsReplayHoldOutcome`: a hold that ended `denied` before the stream opened is replayed by `after=0` with `Outcome == "denied"`.
- `TestResponseState_SupportsResponseController`: through `ControlAPI.ServeHTTP`, a handler's `http.NewResponseController(w).Flush()` and `SetWriteDeadline` return nil.
- `TestControlAPI_EventsOutlivesServerTimeouts`:
  - Use an `httptest.NewUnstartedServer` whose `Config` has `ReadTimeout`/`WriteTimeout` of 200ms, and set `eventsKeepalive` low for the test (make it a package `var`).
  - The stream still delivers an event published after 1s.
- `TestControlAPI_AllowHTTPEndsMatchingHolds`: start a hold via `holds.Wait` in a goroutine. `POST /allow-http {"entries":["*.example.com:443"]}` makes `Wait` return `HoldAllowed`.
- `TestControlAPI_DenyEndsHold`: `POST /deny` makes `Wait` return `HoldDenied`.
- `TestControlAPI_ClosingStreamEndsHolds`: a hold is open with a 10s timeout and the stream is the only subscriber. Closing the client response body makes `Wait` return `HoldUnwatched` within 1s.

In `integration_test.go`, add a subtest `held request is released by allow`:
- Configure the server with `HoldTimeout: "5s"`, `AllowCIDR: ["127.0.0.0/8"]` and a local backend.
- Open `/events` over mTLS.
- In a goroutine, make a proxied plain HTTP request to the backend's `127.0.0.1:port`.
- Read events until a `log` with `action == "hold"` arrives, then `POST /allow-http` for the target.
- The request returns 200 within 2s. A `hold-end` with outcome `allowed` arrives.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./proxy/ -run 'ControlAPI_' -v`
Expected: FAIL

- [ ] **Step 3: Implement**

- [ ] **Step 4: Run them to verify they pass**

Run: `make test && make test-integration`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add proxy/api.go proxy/api_test.go proxy/server.go integration_test.go
git commit -m "Stream proxy events over the control API and release holds on decisions"
```

---

### Task 7: Client event stream and reconnecting feed

**Files:**
- Modify: `cmd/control.go`
- Create: `cmd/feed.go`
- Test: `cmd/control_test.go`, `cmd/feed_test.go`

**Interfaces:**
- Consumes: `proxy.ReadEvent`, `proxy.Event` (Task 3). `GET /events` (Task 6).
- Produces:
```go
// on ControlClient: a stream client with no overall timeout, sharing the TLS transport
stream *http.Client
func (c *ControlClient) EventsTail(ctx context.Context, n int) (*EventStream, error)       // 404 → ErrUnsupportedEndpoint
func (c *ControlClient) EventsAfter(ctx context.Context, after uint64) (*EventStream, error) // 404 → ErrUnsupportedEndpoint
func (s *EventStream) Next() (proxy.Event, error)
func (s *EventStream) Close() error
func (c *ControlClient) DenyTarget(t proxy.Target) error // Deny(entry) delegates to it

// feedBatch is what the feed delivers: events, or a connection error.
// An empty batch with Err == nil reports a (re)connect.
type feedBatch struct {
	Events []proxy.Event
	Err    error
}
func startFeed(ctx context.Context, c *ControlClient, tail int, retry time.Duration) <-chan feedBatch
var feedIdleTimeout = 30 * time.Second // reconnect after this long without reading anything; var for tests
```

Feed behaviour:
- **First connect:** `EventsTail(tail)`.
- **Reconnects:** `EventsAfter(min(lastID, oldestOpenHoldID-1))`. `lastID` is the highest of every `cursor` event and every log ID received. `cursor` events update `lastID` and are not passed on. The feed tracks the IDs of hold entries that are still open: a hold entry without an `Outcome`, until a matching `hold-end` or a replayed outcome arrives.
- **`hold-open` events** are passed on as they are, and they count as open holds for the reconnect rule.
- **Replayed entries the feed already delivered** (ID ≤ lastID) are never passed on as log events. If one is a hold entry that now carries an `Outcome`, the feed passes on a synthetic `EventHoldEnd` for it instead. Consumers therefore never see a duplicate.
- **Errors:** on any stream error, or when nothing has been read for `feedIdleTimeout`, close the stream, send `feedBatch{Err}`, wait `retry` and reconnect.
- **Older proxies:** on `ErrUnsupportedEndpoint`, switch for good to polling every `retry`, wrapping entries as `EventLog` events. The first poll is `Logs()`, trimmed to the newest `tail` entries. Its last ID (untrimmed) becomes the cursor, and polling continues with `LogsAfter(last)`.
- Group events that arrive while the consumer is busy into one batch.
- Close the channel when `ctx` ends.
- Update `testControlClient` in `cmd/control_test.go` to also set `stream: srv.Client()`.

- [ ] **Step 1: Write the failing tests**

- `TestControlClient_Events`: against a real `ControlAPI` with a hub (`SetEvents`), `EventsTail(1)` returns the newest entry and then a published log event.
- `TestControlClient_EventsUnsupported`: a `ControlAPI` without a hub → `errors.Is(err, ErrUnsupportedEndpoint)`.
- `TestStartFeed_ReconnectsFromLastID`:
  - Use a test handler that serves the real `/events` but closes the first stream after log entry 2.
  - Add entries 3 and 4 while it's disconnected.
  - The feed delivers 1, 2, an `Err` batch, a connect batch, then 3 and 4. Each ID arrives exactly once.
- `TestStartFeed_ReplaysOutcomeOfHoldEndedWhileDisconnected`:
  - The feed has seen hold entry 5 (open) and entries up to 7. The stream drops.
  - The hold ends `denied`, and entry 8 is logged.
  - After the reconnect the feed delivers `EventHoldEnd{ID: 5, Outcome: "denied"}` and entry 8, but never entries 5–7 again.
- `TestStartFeed_TailZeroReconnectBeforeFirstLog`:
  - The proxy holds entries 1–100. The feed starts with tail 0.
  - The test handler drops the stream after the `cursor` event, before any new entry.
  - The reconnect requests `after=100` (record the query in the handler), and the feed never delivers entries 1–100.
- `TestStartFeed_ReconnectsWhenIdle`: with `feedIdleTimeout` set to 100ms and a server that sends headers and then nothing, the feed sends an `Err` batch and reconnects.
- `TestStartFeed_PollsOlderProxy`: a `ControlAPI` without a hub, so the entries come via polling, in order and once each.
- `TestStartFeed_StopsOnCancel`: the channel closes after cancel.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/ -run 'ControlClient_Events|StartFeed' -v`
Expected: FAIL

- [ ] **Step 3: Implement**

- [ ] **Step 4: Run them to verify they pass**

Run: `go test -race ./cmd/ -run 'ControlClient|StartFeed' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/control.go cmd/control_test.go cmd/feed.go cmd/feed_test.go
git commit -m "Read the proxy event stream with reconnect and polling fallback"
```

---

### Task 8: Client-side hold tracking

**Files:**
- Create: `cmd/holds.go`
- Test: `cmd/holds_test.go`

**Interfaces:**
- Consumes: `proxy.Event`, `proxy.HoldEnd`, `proxy.ActionHold`.
- Produces:
```go
// heldTargets mirrors the proxy's open holds as seen on the event stream.
type heldTargets struct{ /* open map[proxy.HostKey]openHold; ended map[uint64]proxy.HoldOutcome */ }
type openHold struct {
	ID       uint64
	Deadline time.Time
	Entry    proxy.LogEntry
}
func (h *heldTargets) Apply(e proxy.Event)
func (h *heldTargets) Open(k proxy.HostKey, now time.Time) (openHold, bool) // false once ended or past Deadline
func (h *heldTargets) Outcome(id uint64, now time.Time) (proxy.HoldOutcome, bool) // false while still open
func (h *heldTargets) Count(now time.Time) int
func holdOutcomeLabel(o proxy.HoldOutcome) string
```

Rules:
- A `hold` log entry with an empty `Outcome` opens the hold, replacing any older one for the same key. One with an `Outcome` (from history) records that outcome by ID and opens nothing. A `hold-open` opens the hold the same way, unless an outcome is already recorded for its ID.
- A `HoldEnd` closes it only when the IDs match, and always records the outcome by ID.
- A passed deadline reads as `HoldTimeout`.
- Unknown outcomes read as `HoldTimeout`.
- Labels:

  | Outcome | Label |
  |---|---|
  | `allowed` | `allowed` |
  | `denied` | `denied` |
  | `timeout` | `timed out` |
  | `abandoned` | `request gone` |
  | `unwatched` | `nobody watching` |

- [ ] **Step 1: Write the failing tests**

Write `TestHeldTargets` as a table of event sequences, each with a `now`, checking `Open`/`Outcome`/`Count`. Cover:
- open, then end;
- an end with a mismatched ID;
- deadline expiry;
- a history entry that already carries an outcome;
- a `hold-open` for a hold that isn't in the history;
- a re-hold that replaces the older one;
- an unknown outcome.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/ -run HeldTargets -v`
Expected: FAIL

- [ ] **Step 3: Implement**

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/ -run HeldTargets -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/holds.go cmd/holds_test.go
git commit -m "Track open holds on the client from stream events"
```

---

### Task 9: Approve screen over a queue of targets

**Files:**
- Create: `cmd/prompt_queue.go`
- Modify: `cmd/approve_ui.go`
- Test: `cmd/prompt_queue_test.go`, `cmd/approve_ui_test.go`

**Interfaces:**
- Consumes: `proxy.HoldEnd`, `ControlClient.Check`/`Deny`, `allowEntry`.
- Produces:
```go
type promptItem struct {
	Entry    proxy.LogEntry // the hold entry when held; its ID matches HoldEnd.ID
	Deadline time.Time      // zero: plain block
}
func (it promptItem) Held(now time.Time) bool
func newPromptQueue() *promptQueue // safe for concurrent use
func (q *promptQueue) Add(e proxy.LogEntry) bool // false when the target was already queued
func (q *promptQueue) Has(t proxy.Target) bool
func (q *promptQueue) EndHold(end proxy.HoldEnd)
func (q *promptQueue) Remove(t proxy.Target)
func (q *promptQueue) Items() []promptItem
func (q *promptQueue) Len() int
func (q *promptQueue) Clear() []promptItem
func newApproveScreen(session *SessionInfo, client *ControlClient, q *promptQueue) *approveScreen
```

Queue rules:
- `Add` appends a new target.
- For a target already queued, a `hold` entry replaces `Entry`/`Deadline` in place, and a `block` entry changes nothing.
- A `hold` entry that already carries an `Outcome` counts as a plain block (zero `Deadline`).
- `EndHold` with `allowed`/`denied` removes the target. Any other outcome clears `Deadline` when `end.ID == Entry.ID`.

Screen behaviour:
- The keys act on `Items()[0]`. A successful allow or deny removes it, and the screen quits once the queue is empty.
- On every `tui.TickMsg` the screen quits when `Len() == 0` and it isn't busy and shows no error, since the queue may have been emptied elsewhere.
- The `/check` poll (every `pollInterval`) stays, checks every queued target and removes the decided ones. It can't be replaced by `hold-end`: allows and denies of targets with no open hold (timed out, DNS, blocked over the limits) produce no event.
- **View of the first item:**
  - Held: a yellow bold `held` label, then the line `the agent is waiting · Ns left`, where N is the whole seconds left, rounded up.
  - Not held: today's purple `blocked` view, unchanged.
- **View of the rest:**
  - A blank line, then `also waiting (N)` in `ColorField`.
  - One line per item: `? <target>  held Ns` in yellow, or `x <target>  blocked` in `ColorError`.
  - Show as many items as fit in `w.VpHeight()`, then `… N more`.
- Targets are sanitized with `tui.SanitizeText` as today.

- [ ] **Step 1: Write the failing tests**

`TestPromptQueue`, table-driven over these sequences:
- add, then add the same target (no duplicate);
- a hold refresh keeps its position;
- `EndHold` `allowed` removes the target;
- `EndHold` `timeout` clears the deadline;
- `EndHold` with a stale ID changes nothing;
- `Clear` returns the items and empties the queue.

In `approve_ui_test.go`:
- Adapt `makeApproveSetup` to take a queue.
- `TestApproveScreen_HeldCountdown`: the view contains `held`, `docs.example.com:443` and `the agent is waiting · 7s left` for a deadline 6.2s ahead.
- `TestApproveScreen_HeldBecomesBlockedAtDeadline`: once `now` passes the deadline, the view contains `blocked` and no `waiting`.
- `TestApproveScreen_ListsOtherTargets`: three queued targets give `also waiting (2)` and both targets.
- `TestApproveScreen_DecidesHeadThenNext`: `a` allows the first, and the screen stays open on the second. `n` denies the second and the screen quits. Assert the `/allow-http` and `/deny` calls on the test proxy.
- `TestApproveScreen_QuitsWhenQueueEmptiedElsewhere`: `q.Remove` on the only target, then a tick, gives `tea.Quit`.
- Keep the existing tests passing (single-item queue).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/ -run 'PromptQueue|ApproveScreen' -v`
Expected: FAIL

- [ ] **Step 3: Implement**

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/ -run 'PromptQueue|ApproveScreen' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/prompt_queue.go cmd/prompt_queue_test.go cmd/approve_ui.go cmd/approve_ui_test.go
git commit -m "Show held targets with a countdown and queue several in one prompt"
```

---

### Task 10: Prompter on the event feed

**Files:**
- Modify: `cmd/prompt.go`
- Test: `cmd/prompt_test.go`

**Interfaces:**
- Consumes: `startFeed`/`feedBatch` (Task 7). `promptQueue` (Task 9). `blockWatcher` (existing).
- Produces:
  - `type promptFunc func(ctx context.Context, q *promptQueue) error`.
  - `runBlockPrompter(ctx, client, interval, prompt)`, same signature. `interval` is now the priming retry and the feed's `retry`.

Loop:
1. Call `startFeed(ctx, client, 0, interval)`. Tail 0 skips everything blocked before the prompter started. It replaces the old priming loop, since the feed retries by itself.
2. For each batch:
   - **An allowable `log` event, or a `hold-open` event** (treated like its hold entry): if `q.Has(target)`, call `q.Add(entry)` to refresh. Otherwise pass it through `bw.Next`. Fresh entries that `Check` reports as decided are skipped, and the rest go to `q.Add`.
   - **A `hold-end` event:** `q.EndHold`.
   - Ignore `Err` batches.
3. When nothing is showing and `q.Len() > 0`, run `prompt(ctx, q)` in a goroutine.
4. When it returns:

   | Result | Action |
   |---|---|
   | `nil` with the queue non-empty (dismissed) | `Forget` every target from `q.Clear()` |
   | `nil` with the queue empty | nothing |
   | `ErrUnavailable` | return it |
   | `ErrBarrierTimeout` / `ErrSessionBusy` | `Forget` all and clear |
   | any other error | `Retry` all and clear |

5. On `ctx.Done()` while a prompt shows, wait for it to return before returning.
6. `errTooManyTargets` still returns after the batch.

Also update `startBlockPrompter`'s `show` and `loggedPrompt` for the queue signature. `loggedPrompt` names the first queued target in its note, or `prompt: %v` when the queue is empty.

- [ ] **Step 1: Write the failing tests**

Adapt the existing `TestRunBlockPrompter*` tests to the queue-based `promptFunc` and a hub-backed test proxy. Turn `TestRunBlockPrompter_RetriesPriming` into a test that a feed that can't connect at first still prompts once it does, and that blocks from before the start never prompt. Add:
- `TestRunBlockPrompter_QueuesTargetsWhileShowing`:
  - A fake prompt blocks until released.
  - Two more targets are held while it shows.
  - Exactly one `prompt` call is made, and `q.Len() == 3` before release.
- `TestRunBlockPrompter_DismissPromptsAgain`:
  - A fake prompt returns nil without draining the queue.
  - The next block of the same target calls `prompt` again.
- `TestRunBlockPrompter_DecidedTargetsDontPromptAgain`: a fake prompt removes the head (simulating allow), and the next block of that target doesn't prompt.
- `TestRunBlockPrompter_HoldEndUpdatesQueue`: a `hold-end` `timeout` for a queued hold clears its deadline (observe it through `q.Items()` in the fake prompt).
- `TestRunBlockPrompter_OlderProxyStillPrompts`: a `ControlAPI` without a hub still prompts for a block via polling.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/ -run 'RunBlockPrompter|StartBlockPrompter|LoggedPrompt' -v`
Expected: FAIL

- [ ] **Step 3: Implement**

- [ ] **Step 4: Run them to verify they pass**

Run: `go test -race ./cmd/ -run 'RunBlockPrompter|StartBlockPrompter|LoggedPrompt|BlockWatcher' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/prompt.go cmd/prompt_test.go
git commit -m "Drive the approve prompt from the event stream"
```

---

### Task 11: Monitor on the feed, held log lines, deny

**Files:**
- Modify: `cmd/monitor_ui.go`, `cmd/monitor_logs.go`, `cmd/monitor_allow.go`
- Test: `cmd/monitor_ui_test.go`

**Interfaces:**
- Consumes: `startFeed`/`feedBatch` (Task 7). `heldTargets`/`holdOutcomeLabel` (Task 8). `ControlClient.DenyTarget` (Task 7).
- Produces:
  - `monitorScreen` fields `held heldTargets`, `feed <-chan feedBatch` and `stopFeed context.CancelFunc`.
  - `feedMsg{from *monitorScreen; batch feedBatch; closed bool}` and `denyResultMsg{from *monitorScreen; key proxy.HostKey; err error}`.
  - `(s allowState) forHeld(k proxy.HostKey) allowTarget`, with `blocked: true` and `code: BlockNotAllowlisted`.
  - `renderLogLine(e proxy.LogEntry, status allowStatus, highlighted bool, held *heldTargets, now time.Time) string`.

Behaviour:
- **First load:** start the feed with `tail = proxy.TailSize`. It replaces the separate `Logs()` call, so tail and live events arrive without a gap.
- **Each `feedMsg`:**
  - `Err` → `connectionLost`.
  - An empty successful batch → `connected`.
  - Events → `held.Apply` each one, append only the `EventLog` entries to `logs` (`hold-open` adds no log line but makes the host show as held), and re-arm the wait cmd.
- The per-tick `/logs` poll is removed. `/hosts` polling stays as it is.
- `transitionBack` and quitting call `stopFeed`.
- **Log line for a `hold` entry:**
  - While open: a yellow bold `?` and a yellow `held · waiting for a decision (Ns)`.
  - Once ended: a `?` and `held · <label>` in `ColorField`.
- `forEntry` treats `ActionHold` as blocked.
- **`n` key:** when the selected log line or host row is an open hold, call `DenyTarget`. On success, flash `denied <target>`. On error, flash it for `allowErrorFlash`. On anything else, `n` does nothing.
- **Footer** for a selected open hold: `a allow`, `A allow+save`, `n deny`.

- [ ] **Step 1: Write the failing tests**

Use the existing monitor test harness with a hub-backed `ControlAPI`:
- `TestMonitor_StreamsLogEntries`: an entry added to the log after the first load shows in the log tab without a tick-driven poll.
- `TestMonitor_HeldLogLine`: a hold event renders `held · waiting for a decision (7s)`, and after a `hold-end` `allowed` it renders `held · allowed`.
- `TestMonitor_DenyHeldEntry`: select the held line and press `n`. The proxy's `DenySet` contains the target and the flash says `denied`.
- `TestMonitor_NIgnoredOnNonHeld`: `n` on a plain block line makes no `/deny` call.
- `TestMonitor_FeedErrorShowsDisconnect`: an `Err` batch sets the window error as `connectionLost` does today.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/ -run 'Monitor' -v`
Expected: FAIL

- [ ] **Step 3: Implement**

- [ ] **Step 4: Run them to verify they pass**

Run: `go test -race ./cmd/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/monitor_ui.go cmd/monitor_logs.go cmd/monitor_allow.go cmd/monitor_ui_test.go
git commit -m "Stream monitor logs, show held requests and deny them"
```

---

### Task 12: Monitor hosts tab with held rows

**Files:**
- Modify: `cmd/monitor_hosts.go`, `cmd/monitor_ui.go`
- Test: `cmd/monitor_hosts_test.go`

**Interfaces:**
- Consumes: `heldTargets` (Task 8). `forHeld` and `n` handling (Task 11).
- Produces:
  - `(v *hostsView) setHosts(hosts []proxy.HostStats, held *heldTargets, now time.Time)`.
  - `(v *hostsView) render(allows allowState, held *heldTargets, width, height int, now time.Time) string`.

Rules:
- **Pinning:** held hosts sort before all others whatever the sort mode, by hold ID ascending.
- **Missing rows:** a held host missing from `/hosts` gets a row synthesized from its hold entry (`LastSeen` = entry time, counts 0). The row is merged in again on every `setHosts`, and also when a hold event arrives.
- **The row:**
  - status text `held Ns` in bold yellow, in place of `st.label()`;
  - a yellow `●`;
  - the row background tinted with a dim yellow (`lipgloss.Color("#2a2e22")` when selected, `#1f2119` otherwise);
  - the host text in bold.
- **Summary:** `● N held` in yellow after the blocked count, only when N > 0. Held hosts are not also counted as blocked.
- Selection follows the host as it does today. A row that stops being held re-sorts like any other.

- [ ] **Step 1: Write the failing tests**

- `TestHostsView_HeldPinnedFirst`: held `zzz.example:443` sorts above newer non-held hosts in every sort mode.
- `TestHostsView_SynthesizesHeldRow`: a hold for a host absent from the `/hosts` result still renders a row with `held 7s`.
- `TestHostsView_SummaryCountsHeld`: the summary contains `● 1 held` and the blocked count excludes it.
- `TestHostsView_HeldRowFooterKeys`: a selected held row offers `a`, `A` and `n`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/ -run 'HostsView' -v`
Expected: FAIL

- [ ] **Step 3: Implement**

- [ ] **Step 4: Run them to verify they pass**

Run: `make test`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/monitor_hosts.go cmd/monitor_hosts_test.go cmd/monitor_ui.go
git commit -m "Pin and highlight held hosts in the monitor"
```

---

### Task 13: User documentation

**Files:**
- Modify: `docs/content/reference/cli.md` (the "Blocked connection prompt" and `monitor` sections)
- Modify: `docs/content/how-to/configure-presets.md` (add a `hold-timeout` section next to `allow-host-ports`)
- Modify: `AGENTS.md` (Proxy key files: add `proxy/events.go` and `proxy/hold.go`. Control API: mention `/events`)

Content to get across:
- While a prompt or monitor is attached, the agent's request waits up to `hold-timeout` (10s by default). Allowing within that time lets it through.
- The list behaviour: decide the first target, and the next one moves up.
- `esc` dismisses, and the next attempt asks again.
- After the time runs out the prompt stays open as a blocked prompt.
- DNS lookups aren't held.
- In the monitor: held rows are yellow, and `n` denies a held request.
- Remove the bullet "The blocked request has already failed when the prompt appears", and rewrite "Each target prompts at most once per session" to say a dismissed target asks again.

- [ ] **Step 1: Edit the docs**
- [ ] **Step 2: Verify**

Run: `make test` (no Go change expected). Then run `make docs-install && make docs-build` if docs tooling is available; otherwise skip and say so.
Expected: PASS, or a stated skip.

- [ ] **Step 3: Commit**

```bash
git add docs/content AGENTS.md
git commit -m "Document held connections and hold-timeout"
```
