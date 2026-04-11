# SSH Session Selector Redesign

## Problem

The SSH session selector has three UX issues:

1. The first SSH connection skips the selector, but every subsequent connection
   shows it -- even when no other SSH connection is active.
2. Exited sessions linger as tombstones for up to 1 hour. Because
   `Manager.List()` includes them, they inflate the session count and trigger
   the selector unnecessarily.
3. Exited sessions appear in the selector as "not selectable", adding noise
   without value.

The root cause is that `handlePTYSession` uses `len(mgr.List()) == 0` to decide
whether to show the selector, and `List()` returns all sessions including exited
ones.

## Design

### Filter logic

Change the selector decision in `sshd/server.go:handlePTYSession` to consider
only detached sessions:

- **0 detached sessions** -- auto-create a new session. This covers the
  first-connect case, the "all sessions are attached" case, and the "only exited
  tombstones exist" case.
- **1+ detached sessions** -- show the selector.

Filter the session list from `mgr.List()` at the call site in
`handlePTYSession`, keeping only sessions with `Status == "detached"`. Pass this
filtered list to the selector model.

### Selector display

The selector shows only detached sessions plus a "new session" option. Each
session line displays: session ID, command, "detached X ago".

Remove the exited-session handling from the selector:
- Remove the "not selectable" label and the `info.Status == "exited"` guard in
  the enter key handler.
- Remove the `formatStatus` cases for "attached" and "exited" -- the selector
  only ever sees detached sessions now.

### Unchanged

- `session.Manager`, tombstone/cleanup mechanism, and state file remain
  unchanged. Exited sessions still appear in the state file
  (`/tmp/vibed-sessions.json`) for external observability via the `sessions`
  command.
- The take-over confirmation flow is removed from the selector since attached
  sessions are no longer shown. The take-over mechanism in `session.Session`
  itself is unaffected.

## Changes

| File | Change |
|------|--------|
| `sshd/server.go` | Filter `mgr.List()` to detached-only before the selector decision |
| `sshd/selector.go` | Remove exited/attached handling, simplify `formatStatus` |
