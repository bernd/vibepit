# Held Connections Design

## Status

Proposed -- 2026-10-26

## Problem

When the agent requests something that isn't on the allowlist, the proxy
refuses straight away. HTTPS gets a rejected CONNECT and plain HTTP gets a
403. The `--prompt` overlay and `monitor` only learn about the block when
they next poll `/logs`, which they do once a second. So by the time the user
allows the target, the agent has already seen the failure, and the user
has to tell the agent to try again.

A typical case: the agent fetches a documentation page whose host isn't on
the allowlist. The user sees the request in the overlay (or in `monitor`
when no overlay is attached) and allows it, but the agent already got its
error.

## Intent

When someone is watching, the proxy holds an un-allowlisted HTTP/HTTPS
request for a few seconds rather than refusing it. If the user allows it
in that window, the request goes through and the agent never notices
anything happened. If the user denies it, or the time runs out, the request
fails exactly as it does today. When nobody is watching, nothing changes.

## Goals

- Hold HTTP and HTTPS requests that are blocked only because the target is
  not on the allowlist (`BlockNotAllowlisted`), for a configurable time
  that defaults to 10 seconds.
- Hold only while at least one watcher is connected, meaning an overlay
  (`run --prompt` / `connect --prompt`) or a `monitor`. With no watcher,
  block immediately as today.
- A new one-way server-sent events (SSE) stream on the existing mTLS
  control API. Clients use it to learn about held requests and new log
  entries straight away, and an open stream is what makes a client count
  as a watcher.
- The overlay and `monitor` switch from polling `/logs` to the stream.
- Allow (`a`), allow+save (`A`) and deny (`n`) release every held request
  for that target, on any client.
- The overlay approve screen shows a held state with a countdown. After a
  timeout it stays open as a regular blocked prompt.
- The monitor highlights held requests on both tabs, and lets the user
  select one and allow or deny it.

## Non-Goals

- Holding DNS queries. Blocked lookups keep getting NXDOMAIN straight away.
  Programs that go through `HTTP_PROXY` never resolve names themselves, and
  resolvers time out and retry after about 5 seconds, which would fight a
  longer hold.
- Holding requests blocked for reasons that allowing can't fix: CIDR
  blocks, failed resolution, and IPv6 literals (anything where
  `AllowFixes` is false).
- Two-way communication over the stream. Decisions keep going through the
  existing POST endpoints.
- Waking the user up when they're away. No notifications or sounds.

## Anti-Goals

- **Holding when nobody can decide.** If no watcher is connected, a held
  request only slows the agent down. That must never happen.
- **Weakening filtering.** An allowed held request goes through the full
  filter again, including the CIDR check, exactly as a fresh request would.
  A hold never lets anything through that a fresh request after the same
  allow wouldn't get through.
- **Letting the sandbox exhaust the proxy.** The sandbox picks the hosts it
  asks for. Holding must be bounded so a flood of unique hosts can't pile
  up goroutines or connections without limit.
- **Breaking older sessions.** A client newer than the proxy (for example
  a long-running `up` session started before an update) must keep working.

## Decided by the user

- The hold applies to HTTP/HTTPS only.
- The default is 10 seconds, and it's configurable.
- No watcher means fail immediately, as today.
- Presence and push go through SSE on the control API. A one-way stream is
  enough.
- In the monitor, held requests are highlighted and the user selects them
  and approves or denies them. The monitor gets no pop-up prompt.
- When the hold times out, the overlay prompt stays open as a regular
  blocked prompt, because allowing still lets the agent's next attempt
  through.
- Several held targets appear as a list in the overlay. The user decides
  the first one, it disappears, and the next one moves up until the list
  is empty. Items in the list can't be selected.
- Dismissing with `esc` is not a decision. The next hold for a dismissed
  target prompts again.

## Behaviour

### Proxy

For each CONNECT or plain HTTP request:

1. Run the existing `filter`. If it allows the request, carry on as today.
   If it blocks the request, carry on as today unless **both** of these
   hold: the code is `BlockNotAllowlisted`, **and**
   `AllowFixes(hostKey, code)` is true. IPv6 literals get
   `BlockNotAllowlisted` too, but the allowlist can't hold them, so a
   hold could never be approved.
2. Take the hold registry's lock. Steps 3–5 happen under it, so they
   can't interleave with an allow or deny notifying holds.
3. Check the live allowlist again. If it now allows the target, the
   request continues as allowed and gets no hold. This closes a race:
   `/allow-http` may have added the entry and notified the existing holds
   between step 1 and step 2. A hold registered afterwards would never be
   notified.
4. If the target is in the `DenySet`, block immediately without holding,
   since the user has already said no. Do the same if no watcher is
   connected or the hold timeout is 0.
5. Otherwise hold the request. Log one entry with a new action `hold`
   (alongside `allow` and `block`) and push it to every stream. Concurrent
   or repeated requests for the same target that arrive while it is held
   join the existing hold. They share its deadline and add no further `hold`
   entries.
6. The hold ends at the first of:
   - **Allowed:** the target was added to the live allowlist, through
     `/allow-http` from any client, including allow+save. Every waiting
     request runs `filter` again and continues on its result, so the CIDR
     check still applies.
   - **Denied:** `/deny` for the target. Every waiting request is blocked.
   - **Timed out:** the deadline passed. Every waiting request is blocked.
   - **The client went away:** the request's context was cancelled. That
     request stops waiting, and the others are unaffected. Once the last
     waiting request is gone, the hold ends as **abandoned**. Nothing
     more is logged, since nothing was blocked or allowed.
   - **Nobody is watching any more:** the last stream closed. Every
     waiting request is blocked immediately, so nothing sits out the rest
     of its deadline with no one able to decide.
7. Every request that ends up allowed or blocked gets its own normal
   `allow` or `block` entry, with its usual reason, exactly as an unheld
   request would. `hold` entries don't count toward `HostStats` allowed or
   blocked counts, so the hosts view counts requests as it does today.
8. When a hold ends, the proxy records the outcome on the `hold` entry
   itself, in a new `outcome` field (empty while the hold is open). This
   is the only change ever made to a stored entry. It makes outcomes
   replayable: `/logs` and the stream's history (see Stream) return a
   hold entry with whatever outcome it has at read time. The ending is
   also pushed live as a `hold-end` event.

`/allow-http` and `/deny` always update the allowlist or `DenySet`
**before** they take the registry lock to notify holds. Combined with step
3, every request either sees the decision or is registered in time to be
notified.

A request that arrives after its target's hold timed out starts a new hold,
provided a watcher is still connected and the target isn't denied.

### Presence

A watcher is an open SSE stream. The proxy counts open streams, and a
stream counts from the moment it opens until its connection closes.
Whether the client can actually show a prompt doesn't matter, so the
stream must not be opened by clients that never prompt (`allow-http`,
`allow-dns`, `status`).

### Stream

`GET /events` on the control API, using the same mTLS as every other
endpoint. Each event carries JSON. The events are:

- `log`: one `LogEntry`, sent for every new entry, holds included. This
  replaces the clients' `/logs?after=N` polling.
- `cursor`: `{"id": N}`, sent once per stream right after the history.
  N is the highest log ID that existed when the history was read: the
  history boundary. Every entry up to N was either sent as history or
  deliberately skipped by the tail, and every entry above N arrives live.
- `hold-open`: a `hold` entry for a hold that's open right now. Sent only
  as part of the snapshot below, never for new holds, which arrive as
  `log`. It doesn't take part in the ID ordering of `log` events.
- `hold-end`: the target, how the hold ended and the ID of the `hold`
  entry it ends. The ways a hold can end are `allowed`, `denied`,
  `timeout`, `abandoned` and `unwatched`. Clients treat a value they
  don't recognise as `timeout`, so a newer proxy can add outcomes later.

A hold entry carries its deadline, so clients can show a countdown without
their own clock having to agree with the proxy's beyond a second or so.

**History and live events are handed over without a gap.** A client never
combines a separate `/logs` call with the stream. The stream itself
replays history:

- `GET /events?tail=N` sends the newest N entries first. The monitor uses
  25 (`TailSize`) and the overlay uses 0.
- `GET /events?after=N` sends every entry with an ID above N first. It is
  used for reconnects.

**Log events are published in ID order.** The proxy publishes each entry
while still holding the log's lock, which is the same lock that assigns
IDs. Publishing never blocks, so this costs nothing. Publishing after
releasing the lock would let concurrent writers publish ID 12 before
ID 11. The stream would then drop 11 as already sent.

The handler subscribes to live events **before** reading history. It
reads the history and its boundary under one lock, sends the history,
then sends the boundary as a `cursor` event. Next it sends a snapshot of every hold open at that
moment, as `hold-open` events. This snapshot doesn't depend on the tail
size or the log ring, so a watcher that connects late (an overlay with
`tail=0`, say) still sees holds it could decide. It then sends live
events, skipping `log` events whose ID is at or below the boundary or was
already sent. A hold that
opens or ends after the subscription also arrives as a live `log` or
`hold-end`. Clients apply all of these idempotently by hold entry ID. Nothing logged between the history read and
the live stream can be lost. A `hold-end` that happened in that window
reaches the client either through the replayed entry's `outcome`, through
the live event, or through both. Clients apply both idempotently.

**On reconnect** a client asks for
`after = min(cursor, oldest hold ID it still thinks is open − 1)`. Here
`cursor` is the highest of the stream's `cursor` event and the IDs of the
log events it has received. A `tail=0` client that reconnects before any
new entry arrives therefore resumes from the boundary rather than from
0, so it doesn't replay the blocks it skipped on purpose.
That way hold entries that ended while it was disconnected come back with
their outcome. Entries it already has are applied for their outcome only,
never appended twice. A hold whose entry has already left the log ring is
treated as ended once its deadline passes.

**Stream resources are bounded:**

- Publishing never blocks proxy requests. Each stream has a bounded queue
  of 256 events. A stream whose queue overflows is disconnected, and its
  client reconnects with `after`, so nothing is lost.
- Every write has its own 10-second deadline, so a client that stops
  reading is dropped once its socket buffers fill.
- The proxy writes a keepalive comment every 15 seconds. That bounds how
  long a dead client goes unnoticed by the write deadline, and it lets
  clients notice a dead proxy: a client that reads nothing for 30
  seconds reconnects.
- A peer that vanishes without closing its connection (blackholed) is
  caught by the TCP keepalive Go enables on accepted connections by
  default. Until then it counts as a watcher, so holds still end at
  their deadline. Accepting that bound is deliberate.

### Configuration

A new `hold-timeout` key is added to both the global and the project config.
It takes a Go duration string (`10s`). The project value overrides the
global one, the default is `10s`, and `0` turns holding off. It's carried
to the proxy in `ProxyConfig` like the other settings.

### Overlay (`--prompt`)

- `runBlockPrompter` takes entries from the stream instead of polling
  `/logs`. A `hold` entry prompts like an allowable block does today.
- **Several targets at once (user's decision).** The approve screen
  works on a queue of undecided targets in arrival order. The first is
  shown in full and is the one the keys act on. The rest are listed
  underneath under "also waiting (N)", one line each with their own
  status and countdown, and they can't be selected. Allowing or denying
  the first removes it and the next one moves up. The screen closes when
  the queue is empty. Targets that come in while the screen is open are
  added to the end of the list, so the screen doesn't close and reopen
  for each one. A target decided elsewhere (another overlay, the monitor,
  `allow-http`) drops out of the list wherever it is.
- Each target has two states. **Held:** a yellow `held` label
  (`tui.ColorYellow`) and the line "the agent is waiting · Ns left",
  counting down from the deadline. A listed target shows `held Ns`.
  **Blocked:** today's screen, with a listed target showing `blocked`. A
  target moves from held to blocked when its countdown reaches zero or a
  `hold-end` arrives with an outcome other than `allowed` or `denied`. It
  stays in the queue, because allowing it still lets the agent's next
  attempt through. If a new hold starts for a target that's already
  queued, it goes back to held with the new deadline and keeps its place.
- A target queued from a plain block (no hold) looks as it does today.
- The keys stay the same. `esc` dismisses the whole screen without
  deciding anything. The held requests keep waiting until they time out
  or another client decides.
- **A dismissal is not a decision (user's decision).** `blockWatcher`
  forgets every target that was in the dismissed queue, so the next hold
  or block for any of them prompts again. The once-per-session rule still
  covers targets that were decided. The `maxPromptTries` /
  `maxPromptTargets` limits stay as they are.
- The screen keeps its once-a-second `/check` poll for decisions made
  elsewhere, now checking every queued target, and also uses `hold-end`
  events. `hold-end` can't replace `/check`. A later allow or deny
  produces no `hold-end` for:
  - queued targets whose hold already timed out;
  - DNS blocks;
  - requests blocked immediately because holding is off or over its
    limits.

### Monitor

- The log tab and the hosts tab take new entries from the stream. The hosts
  tab can keep polling `/hosts` for its aggregates.
- **Log tab:** a `hold` entry shows a yellow `?` symbol and "held · waiting
  for a decision (Ns)" in place of the reason. Once the hold has ended
  (through its `hold-end` or the entry's `outcome`), the line stops
  counting down and shows how the hold ended, for example "held · allowed"
  or "held · timed out". The request's own `allow`/`block` entries appear
  as normal lines below it.
- **Hosts tab:** a held host shows the status `held Ns` in yellow with a
  faint yellow row tint. The summary line gains `● N held`. Held hosts stay
  pinned at the top whatever the sort order.
- When a held row is selected, the footer offers `a` allow, `A` allow+save
  and `n` deny. Deny is new in the monitor, posts to `/deny`, and is shown
  only on held rows.

The mockups are in `hold-mockups.html` at the repo root. It's a throwaway
file and shouldn't be committed.

### Older proxies

If `/events` returns 404, the proxy predates this feature, which only
happens for a session started before an upgrade. The client doesn't fall
back to polling `/logs` (the user's decision after implementation, to keep the
client small). Prompts stop, and `startPrompterLoop` notes "the proxy
predates prompts; restart the session to get them" after the session.
The monitor's logs tab shows "proxy does not support the live log,
restart the session", and the hosts tab keeps working. This follows the
existing `ErrUnsupportedEndpoint` pattern. An older client attached to a
newer proxy polls and never opens a stream, so it doesn't count as a
watcher, and the proxy keeps blocking immediately for it.

## Constraints

- The control server sets `ReadTimeout` 15s and `WriteTimeout` 30s
  (`proxy/server.go`). Left alone, these would cut every stream off and
  end holds as `unwatched`. The `/events` handler must clear the read
  deadline and set a fresh write deadline before every write, through
  `http.ResponseController`.
- `ControlAPI` wraps every response in `responseState` (`proxy/api.go`),
  which has neither `Flush` nor `Unwrap`. `http.ResponseController` then
  returns `ErrNotSupported` for flushing and for deadlines. `responseState`
  must gain `Unwrap() http.ResponseWriter`, and flushing must count as
  `written` for the panic handler.
- `ControlClient` uses a 5-second `http.Client` timeout. The stream needs a
  client without an overall timeout. It should use a separate client or
  request on the same TLS config, with the connection's lifetime tied to
  the caller's context.
- goproxy runs the CONNECT and request handlers synchronously on the
  request's goroutine, so holding means blocking in the handler. That's
  fine, but the wait must also end when the request's context is
  cancelled.
- Nested sandbox development has no container runtime, so verification is
  unit and integration tests (`make test`, `make test-integration`).

## My calls (not discussed with the user)

- Config key name `hold-timeout`, with a duration string, project over
  global, and `0` to disable.
- Denied targets are never held.
- Concurrent requests for one target share one hold and produce one prompt.
- Held requests that end up blocked are logged as normal `block` entries,
  so counts and the hosts view stay consistent.
- `esc` on a held prompt doesn't end the hold, and it dismisses the whole
  queue, not just the first target.
- The overlay queue is kept in arrival order, and a listed target whose
  time runs out stays in the list, marked blocked.
- Hold limits: at most 32 targets held at once, and at most 64 waiting
  requests in total. Past either limit, requests are blocked immediately
  as today. The numbers are guesses meant to stop abuse, and normal use
  shouldn't come near them.
- Held hosts are pinned at the top of the hosts tab.
- Older proxies get no fallback: prompts stop and the monitor's logs tab says to restart the session (the user's decision, after implementation).
- When the last watcher disconnects, holds end immediately (`unwatched`).
  When every waiting request goes away, the hold ends (`abandoned`).
- A `hold` line shows how the hold ended, and the request's outcome is a
  separate normal line. This differs slightly from the first mockup,
  where the held line itself turned into the outcome line. The `hold`
  entry's `outcome` field is filled in when the hold ends, so that outcomes
  can be replayed. Nothing else in a stored entry ever changes.
- Stream queue of 256 events per client, disconnect on overflow, a
  10-second write deadline, a 15-second keepalive, and a client-side
  reconnect after 30 seconds of silence.

## Left to the builder

- Event wire format details beyond what's listed.
- How the proxy wakes waiting requests on allow, whether the allowlist
  notifies a hold registry or the API handler does it.
- Whether the monitor's hosts tab also moves its `/hosts` polling onto
  stream events.
- Test structure, following the repo's table-driven and testify
  conventions.
