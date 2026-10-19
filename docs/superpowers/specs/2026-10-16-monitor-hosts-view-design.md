# Monitor Hosts View Design

## Status

Proposed -- 2026-10-16

## Problem

`vibepit monitor` shows a chronological tail of raw proxy log entries. Under
real traffic the same blocked host appears dozens of times and fresh blocks
scroll by between allowed requests. The common task is "find the hosts that
were just blocked and allow them", and the tail makes that slow.

## Intent

Open the monitor, see the most recently blocked hosts at the top, select one,
allow it, move on. The landing screen becomes an aggregated per-host overview
with counts and status. Each row can expand to show details. The raw log
remains available on a second tab.

## Goals

- Aggregated hosts overview as the default monitor screen.
- One row per (source, domain, port): HTTP and DNS for the same domain are
  separate rows.
- Columns: status dot, last seen, host, source, count, status.
- Expandable row details (status, host, port, source, first/last seen with
  action, allowed/blocked counts, block reason).
- Allow from the hosts view with the existing keys: `a` (session), `A`
  (session + save to project config).
- Sort cycling with one key: newest first → by count → by host.
- Raw chronological log kept as a second tab.
- History survives closing and reopening the monitor (aggregated in the proxy,
  not the client).

## Non-Goals

- Blocking or revoking allowed hosts. Planned as a separate later step.
- "Network Rules" / "Filesystem Rules" tabs from the reference screenshots.
  Vibepit has no counterpart.
- Filtering or search.
- Merging DNS and HTTP rows for the same domain.
- Persisting host history across proxy/session restarts.

## Anti-Goals

- Must not look like a clone of the reference screenshots. Follow Vibepit's
  visual style: lowercase labels, Vibepit palette, existing header/footer.
- Must not change allow semantics or config persistence behavior.

## Screen Layout

Top to bottom, inside the existing `tui.Window` (header and footer
unchanged in structure):

1. Tab bar: `HOSTS (n)` and `LOGS (n)`. Uppercase. Active tab bold/bright,
   inactive dim. `n` is the host count / log entry count.
2. Underline below the active tab only.
3. Summary line (HOSTS tab only): `N hosts  ● X allowed  ● Y blocked`.
4. Column header (HOSTS tab only), lowercase:
   `last seen`, `host`, `source`, `count`, `status`. The active sort column
   carries an arrow (`↓` for last seen and count, `↑` for host).
5. Host rows.
6. Footer: existing spinner/flash on the left, keys on the right.

### Host row

```
➔ ● 20:00:33  archive.ubuntu.com:80                   proxy     a allow  A allow+save     3  blocked
  ● 19:59:22  security.ubuntu.com:80                  proxy                               1  blocked
```

- Selection marker and highlight via `tui.LineStyle`, as today.
- Status dot colored by status.
- Last seen: `15:04:05`, time only, like today's log. Shown in orange for a
  few seconds after new activity, otherwise dim.
- Host: `domain:port` for proxy rows, bare domain for DNS rows. Bold when
  blocked; dim when allowed and not selected. Truncate to fit width.
- Source: `proxy` or `dns`.
- On the selected row only, an inline key hint before the count:
  `a allow  A allow+save` for allowable blocked rows (see Allow Behavior),
  `A save` for session-allowed rows, nothing otherwise. The host column gets
  space first; the hint is left out when it does not fit (the footer has the
  same keys).
- Count: total requests (allowed + blocked), right-aligned, digits grouped
  with `,` (`1,234,567`); the column widens to fit. Dropped when the
  longest host would not fit otherwise; the details still show it. Sorting
  by count keeps the column and truncates hosts instead.
- Status, lowercase, bold:

| Status | Meaning | Color |
|---|---|---|
| `blocked` | last request was blocked, not allowed by user | `ColorError` (purple) |
| `allowed` | last request was allowed, not allowed by user in this monitor | `ColorCyan` |
| `allowed*` | allowed with `a` in this monitor (session only), no block since | `ColorOrange` |
| `saved` | allowed with `A` in this monitor (saved to project config), no block since | `ColorOrange` |

The status color also applies to the dot. The summary line counts `blocked`
rows as blocked and every other row as allowed.

### Expanded details

`enter` (or space) toggles details for the selected row. One row expanded at
a time. Details render below the row between two dashed separator lines,
indented, with lowercase labels:

```
      status:     blocked
      host:       archive.ubuntu.com
      port:       80                     (`-` for DNS rows)
      source:     HTTP proxy             (or `DNS query`)
      first seen: 19:59:20  blocked
      last seen:  20:00:33  blocked
      count:      3 total  0 allowed  3 blocked
      reason:     domain not in allowlist
```

- `reason:` is shown only while the status is `blocked`, using the last
  block's `Reason` from the log entry.
- For `allowed*`/`saved`, the status line adds a dim note:
  `(this session only)` or `(saved to .vibepit/network.yaml)`.
- Detail lines count toward the viewport. Scrolling must keep the selected
  row and its details visible where they fit.

### LOGS tab

Today's monitor screen: chronological tail via `GET /logs`, same rendering.
Two changes, both from sharing allow state with the HOSTS tab:

- The orange `a`/`A` marker applies to every log line of a host the user
  allowed in this monitor (past and future lines), not just the line it was
  pressed on. Lines logged as blocked after the allow show as blocked.
- Allow keys follow the same allowability rule as the HOSTS tab (see Allow
  Behavior). `a` on another line of an already-allowed host flashes
  `already allowed`.

## Keys

| Key | HOSTS tab | LOGS tab |
|---|---|---|
| `↑/↓ k/j`, `Home/End g/G` | navigate (existing `tui.Cursor`) | as today |
| `enter` / space | expand/collapse details | -- |
| `a` | allow for session | as today |
| `A` | allow + save (or save a session-allowed row) | as today |
| `s` | cycle sort: newest → count → host | -- |
| `tab` | switch to LOGS | switch to HOSTS |
| `esc` | clear selection, else back to session selector | same |
| `q` / `ctrl+c` | quit | same |

Footer keys are contextual, as today. HOSTS tab shows: allow keys when
applicable, `enter details`/`enter collapse`, `s sort: <current>`,
`tab logs`, `esc sessions`, navigation, `q quit`.

`a` on an already-allowed row flashes `already allowed`, as today. `a`/`A` on
a blocked row that is not allowable flashes `cannot allow: <reason>`.

## Sorting

- **newest** (default): by last seen, newest first.
- **count**: by total count, highest first; ties by newest.
- **host**: by domain A→Z; ties by source, then port.

The selection sticks to the selected host (by key), not the row index,
when rows re-sort because of new traffic or a sort change. The expanded row
also follows its host.

## Selection

Rows move as traffic arrives, so a cursor that follows them could hand the
allow keys a host the user never looked at. A selection therefore only
exists while the user is picking, on both tabs:

- No selection by default. HOSTS shows the newest rows from the top; LOGS
  tails.
- The first navigation key starts a selection: `j`/`k`/arrows select the top
  row (HOSTS) or the newest entry (LOGS); `g`/`G` and page keys also move.
- The selection stays on its host or entry. It never moves to another one:
  when its host leaves the table, the selection ends.
- After 6s without a key press the HOSTS selection ends and HOSTS scrolls
  back to the top. A selection on the expanded row stays until it is
  collapsed; moving the selection to another row ends that exemption.
  LOGS keeps its selection and scroll position: log lines never move, so an
  idle cursor cannot end up on a different entry.
- `a`/`A` without a selection flash `select a host first` (`select an entry
  first` on LOGS).
- `esc` clears the visible tab's selection (and collapses details); with no
  selection it returns to the session selector. The footer shows `esc clear`
  or `esc sessions` accordingly.

## Allow Behavior

Unchanged from today. Reuse `allowCmd`'s logic and `allowValueForEntry`:

- Proxy rows allow `domain:port` via `AllowHTTP`; `A` also appends to project
  `allow-http`.
- DNS rows allow `domain` via `AllowDNS`; `A` also appends to project
  `allow-dns`.

**Allowable:** a blocked row/entry offers `a`/`A` only when its (last) block
reason is "domain not in allowlist". Blocks caused by a blocked CIDR range or a
failed DNS resolution are not fixed by allowlisting, so no allow keys and no
inline hint are shown for them. This is a change from today, where every block
offers `a`. Use a typed reason (see Proxy / Control API), not string matching.

**Allowed or blocked** comes from the proxy: `/hosts` reports per host whether
the allowlist admits it at snapshot time (`allowlisted`). A host is blocked
when its last request was blocked and either the allowlist does not admit it
or the block was not an allowlist block (CIDR and resolution checks run after
the allowlist passed). Allows made with `vibepit allow-http`, from another
monitor, or before a monitor restart therefore count, and an allow the proxy
lost shows as blocked.

**User-allow status** (`allowed*`/`saved`) is client-side state in the monitor,
keyed by host key (source, domain, port), shared by both tabs. It only labels
hosts that are not blocked. A successful allow marks the host allowlisted
until the next poll; a poll that started before the allow finished is
dropped so the row does not flip back. A temp result never replaces a save
that finished first.

`allowResultMsg` must be keyed by host key, not row index, because rows
re-sort while the request is in flight.

## Proxy / Control API

### Host aggregation in `LogBuffer`

Replace the domain-only `DomainStats` map with per-host stats keyed by
(source, domain, port):

```go
type HostStats struct {
    Domain     string    `json:"domain"`
    Port       string    `json:"port,omitempty"`
    Source     Source    `json:"source"`
    Allowed    int       `json:"allowed"`
    Blocked    int       `json:"blocked"`
    FirstSeen  time.Time `json:"first_seen"`
    FirstAction Action   `json:"first_action"`
    LastSeen   time.Time `json:"last_seen"`
    LastAction Action    `json:"last_action"`
    LastBlockAt     time.Time `json:"last_block_at,omitempty"`
    LastBlockReason string    `json:"last_block_reason,omitempty"`
    LastBlockCode   BlockCode `json:"last_block_code,omitempty"`
}
```

- Add a typed `BlockCode` (e.g. `not_allowlisted`, `blocked_cidr`,
  `resolution_failed`) on `LogEntry` alongside the human-readable `Reason`, set
  by `proxy/http.go` and `proxy/dns.go`. The monitor uses it for the
  allowability rule on both tabs.

- Updated in `LogBuffer.Add` under the existing mutex.
- Independent of the 10k-entry ring buffer: hosts do not disappear when old
  log entries are overwritten.
- Capped at 2,000 hosts. When full, evict the least recently seen host. The
  sandbox can generate unlimited unique DNS names; the table must not grow
  without bound in a security component.

### Endpoint

- Add `GET /hosts` returning `[]HostStats` (unordered; the client sorts).
- Add `ControlClient.Hosts()`.
- Remove `GET /stats`, `LogBuffer.Stats`, `ControlClient.Stats` and
  `DomainStats`. Only tests use them.

### Monitor polling

- The monitor polls `GET /logs?after=` every `pollInterval` (1s). It polls
  `GET /hosts` every second while the HOSTS tab is shown, every 5s otherwise
  to keep the tab count current, and at once when switching to the tab.
  Separate in-flight flags per poll.
- Only the logs poll decides whether the session is connected. `/hosts` is
  heavier and can time out on a large table while the session is fine; its
  failures show as `cannot load hosts: ...` on the HOSTS tab.
- `/hosts` responses replace the host list; the client re-sorts and restores
  selection by key.
- `LOGS (n)` counts entries loaded in the client (as today: last 25 at open,
  then everything after). No proxy-side total.
- Disconnect handling (grace period, back to session selector) triggers on a
  failure of the logs poll.
- **Older proxy:** a session started before this change has no `/hosts`
  (404). Treat 404 as "unsupported", not a disconnect: the HOSTS tab shows
  `proxy does not support the hosts view, restart the session` and the monitor
  opens on LOGS, which keeps working.

## Constraints

- Go, Bubble Tea v2, lipgloss v2, existing `tui` package primitives.
- Colors from `tui` (`ColorCyan`, `ColorError`, `ColorOrange`, `ColorField`,
  `ColorHighlight`). No new palette.
- Must work in the compact header layout (short terminals).

## Verification

- `proxy`: table-driven tests for host aggregation (key separation of DNS vs
  proxy and per port, first/last seen and action, last block reason, counts)
  and `GET /hosts`.
- `proxy`: host table cap and eviction; `BlockCode` set for each block path.
- `cmd`: monitor screen tests in the style of `monitor_ui_test.go`: sorting
  and sort cycling, selection stickiness across re-sorts, expand/collapse,
  allow status transitions (including flip back to `blocked`), allowability
  per block code on both tabs, tab switching, footer keys per state, 404
  fallback.
- `make test` and `make test-integration`.

## Decisions Made During Review (author's calls)

- Host table capped at 2,000, least-recently-seen eviction, full list per poll.
- Both endpoints polled continuously; `LOGS (n)` counts client-loaded entries.
- Allow state shared by host key across tabs; LOGS markers follow the host.
- 404 on `/hosts` means older proxy: hosts view disabled, LOGS still works.
- Typed `BlockCode` added instead of matching reason strings.
- Lowercase detail labels and status values; uppercase tab names (user).
- Vibepit palette instead of the screenshot's green/red.
- Time-only timestamps (`15:04:05`).
- `GET /stats` and `DomainStats` removed.

## Left to the Builder

- Splitting `monitor_ui.go` into hosts/log screen files and the tab
  container.
- Column width math and truncation at narrow widths.
- How expanded detail lines integrate with `tui.Cursor` scrolling.
- Exact duration of the "fresh activity" highlight on last seen.
