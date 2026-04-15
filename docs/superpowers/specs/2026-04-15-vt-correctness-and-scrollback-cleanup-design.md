# VT Correctness and Scrollback Cleanup

## Problem

Three correctness and hygiene issues in the persistent PTY session layer, all
concentrated in `session/session.go` and its scrollback helper:

1. **Claude Code renders wrong on SSH reattach.** The VT emulator
   (`charmbracelet/x/vt`) does not implement Insert/Replace Mode (IRM,
   `ESC[4h`/`ESC[4l`). Applications that rely on IRM for mid-line text insertion
   -- including Claude Code, which is the primary sandbox workload -- have the
   wrong screen state in the emulator. Because scrollback replay on reattach
   reads from the emulator (`session.go:130,135`), the replayed screen is
   visibly garbled. Live output through the PTY fan-out is unaffected; only
   reattach replay is wrong.
2. **`Attach` race after shell exit.** When the shell exits, `pump` snapshots
   the client list under the session lock, unlocks, then calls `closeOutput` on
   each client (`session.go:313-322`). A concurrent `Attach` between the
   unlock and `s.exited = true` (`session.go:341-346`) adds a new client whose
   output channel is never closed. The client blocks on `Read` until the SSH
   keepalive (2s tick, 3s reply timeout) kills the connection.
3. **`vteInput` silently drops data under load.** The async VTE feed uses
   `select/default` on a 1024-slot channel (`session.go:301-307`). When the
   channel fills, pump drops the data for the VTE but still writes to
   `s.scrollback` and fans out to clients. VTE state desyncs from truth,
   corrupting reattach replay. The comment above the select ("Use select to
   avoid panic if the channel is closed") is also wrong -- `select/default`
   does not prevent panic on send to a closed channel. The channel is only
   safe because of the separate `pumpDone` handshake in `waitForExit`.

Separately, the package carries dead code:

4. **`session/scrollback.go` (line-oriented ring buffer) is never read in
   production.** `pump` writes to it, `feedVTE` toggles its pause flag, but
   `Snapshot()` is only called from tests. Replay uses `renderVTEScrollback`
   (which reads from the VT emulator instead), and an inline comment at
   `session.go:132-134` acknowledges that using the custom buffer would
   duplicate content.

5. **`renderVTEScrollback` strips color and breaks on wide characters.** It
   writes `cell.Content` or a space per column (`session.go:370-394`). Style
   information is discarded, and a 2-wide CJK cell followed by a
   continuation cell (`Width == 0`, empty `Content`) emits the character
   followed by a stray space that misaligns the rest of the line.

## Design

The design has three areas, each independently deployable but landing as one
change because they touch the same file and compose cleanly.

### Area A — VT library swap

Replace `github.com/charmbracelet/x/vt` with
`github.com/unixshells/vt-go v0.2.0`. `vt-go` is a fork that adds IRM support
(see its README). The API surface vibepit uses is identical between the two
packages:

- `NewSafeEmulator(cols, rows int) *SafeEmulator`
- `SafeEmulator.Write([]byte) (int, error)`
- `SafeEmulator.Read([]byte) (int, error)`
- `SafeEmulator.Render() string`
- `SafeEmulator.Resize(cols, rows int)`
- `SafeEmulator.Close() error`
- `SafeEmulator.IsAltScreen() bool`
- `SafeEmulator.ScrollbackLen() int`
- `SafeEmulator.ScrollbackCellAt(x, y int) *uv.Cell`
- `SafeEmulator.CursorPosition() image.Point`
- `SafeEmulator.Width() int`

Both packages depend on `charmbracelet/ultraviolet` for cell types, so
downstream code that touches `cell.Style`, `cell.Content`, `cell.Width` needs
no change.

### Area B — Session lifecycle fixes

#### B1. Close attach-after-exit race

In `Session.Attach` (`session/session.go:125`), check `s.exited` under `s.mu`
before proceeding. If the session has already exited, construct a client, close
its output channel, and return without adding it to `s.clients`:

```go
func (s *Session) Attach(cols, rows uint16) *Client {
    s.mu.Lock()
    if s.exited {
        c := newClient(s)
        s.mu.Unlock()
        c.closeOutput()
        return c
    }
    // ... existing body unchanged
}
```

The caller's `io.Copy(sess, client)` then exits immediately on EOF and the SSH
handler closes cleanly. No API signature change; no change needed in
`sshd/server.go`. `Detach` on this client is a harmless no-op because it is
never in `s.clients`, and `Close` is idempotent via `sync.Once`.

**The guard is only sound if `s.exited` flips atomically with the
`closeOutput` snapshot.** Today `s.exited` is set in `waitForExit` after
`<-s.pumpDone`. That ordering is insufficient: pump can observe PTY EOF
(delivered when the shell's fds close, independent of `cmd.Wait`) and run
its entire error-path snapshot *before* `waitForExit` wakes from
`cmd.Wait`. In that window, an `Attach` with the guard still sees
`s.exited == false`, adds a client to `s.clients`, and nothing remaining
will close its output channel — same bug as today, just relocated.

**The fix lives in B2:** pump sets `s.exited = true` inside the same
critical section as the final client snapshot. Any `Attach` that acquires
`s.mu` after that either sees the flag (and rejects) or it got in before
pump's lock and is included in pump's snapshot (and its output is closed).
Lock ordering makes the race impossible. `waitForExit` no longer sets
`s.exited`; it only records `exitCode` and `exitedAt` at the end.

#### B2. Remove the async VTE feed

Delete `vteInput` and `feedVTE`. Inline `vte.Write(data)` into pump's critical
section. `SafeEmulator` already takes its own internal lock, so concurrent
reads from `drainVTE` and `Render` remain safe *while pump is alive*.

**`pumpDone` stays.** Without the `vteInput` channel we no longer need it for
channel-close ordering, but we still need it as a teardown barrier: pump can
be mid-`s.vte.Write(data)` on the final PTY read chunk at the moment
`waitForExit` runs, and calling `s.vte.Close()` concurrently with that Write
races on the emulator's internal state. `waitForExit` must wait for pump to
return before closing the emulator.

**New pump body.** Note the `s.exited = true` inside the error-path critical
section — this is what makes B1's `Attach` guard race-free:

```go
func (s *Session) pump() {
    defer close(s.pumpDone)
    buf := make([]byte, 32*1024)
    for {
        n, err := s.ptmx.Read(buf)
        if n > 0 {
            data := make([]byte, n)
            copy(data, buf[:n])

            s.mu.Lock()
            s.vte.Write(data)
            clients := append([]*Client(nil), s.clients...)
            s.mu.Unlock()

            for _, c := range clients {
                c.deliver(data)
            }
        }
        if err != nil {
            s.mu.Lock()
            s.exited = true // atomic with the snapshot; Attach now rejects any late-comer
            clients := append([]*Client(nil), s.clients...)
            s.mu.Unlock()
            for _, c := range clients {
                c.closeOutput()
            }
            return
        }
    }
}
```

**`waitForExit` shutdown sequence:**

1. `s.cmd.Wait()` returns — shell has exited. (Pump may have *already* run
   its error path by now if PTY EOF was delivered first. That's fine; pump
   has set `s.exited` and closed client outputs in that case.)
2. `s.ptmx.Close()` — idempotent. If pump is still alive (because the shell
   exited without closing the slave fds in a way that delivered EOF to
   master yet), this forces pump's next `Read` to return an error.
3. `<-s.pumpDone` — wait for pump to finish its last `s.vte.Write` call and
   return. **This barrier is required.** Closing the emulator while pump is
   still writing to it races on `SafeEmulator`'s internal state, and `-race`
   will flag it.
4. `s.vte.Close()` — unblocks `drainVTE`. Safe now because no writer is
   live.
5. Acquire `s.mu`, record `exitCode` and `exitedAt`, read `len(s.clients)`
   to decide whether to start the tombstone timer. **`s.exited` is not set
   here** — pump did that in step 1's error path.

What is gone compared to today: the `close(s.vteInput)` call (no channel),
the `feedVTE` goroutine itself, and `waitForExit`'s responsibility for
setting `s.exited` (moved into pump's critical section so it is atomic
with the close-outputs snapshot). The `<-pumpDone` barrier is preserved,
only its role has narrowed — it used to also serialize channel close; now
it only serializes VTE close.

**Why not set `s.exited` in `waitForExit` before `<-s.pumpDone` instead?**
That was the first attempt. It doesn't work because pump can observe PTY
EOF and complete its entire snapshot-then-closeOutput path before
`waitForExit` even wakes from `cmd.Wait`. `Attach` that runs in the
window between pump exiting and `waitForExit` setting the flag would
still escape.  The only serialization point that covers every ordering is
pump's own critical section, where adding to `s.clients` (Attach) and
snapshotting `s.clients` (pump) already contend for `s.mu`.

### Area C — Scrollback cleanup and color-preserving replay

#### C1. Delete the line-buffered scrollback

After B2 the scrollback pause toggle is already gone (it lived in
`feedVTE`). Remove the remaining plumbing:

- `session/scrollback.go` -- the entire file.
- `session/scrollback_test.go` -- tests of the deleted type.
- `Session.scrollback` field and its `NewScrollback(10000)` initializer.
- `s.scrollback.Write(data)` call in pump.

Nothing outside the `session` package references the type.

#### C2. Style-preserving `renderVTEScrollback`

Rewrite `renderVTEScrollback` (`session.go:370-394`) to emit SGR sequences via
`ultraviolet.StyleDiff`, skip continuation cells, and preserve wide
characters. Pseudocode:

```go
func renderVTEScrollback(vte *vt.SafeEmulator) []byte {
    lines := vte.ScrollbackLen()
    if lines == 0 {
        return nil
    }
    width := vte.Width()
    var buf bytes.Buffer
    var prev uv.Style
    for y := range lines {
        // Find the last non-blank cell so we can trim right-edge fill.
        lastCol := -1
        for x := width - 1; x >= 0; x-- {
            c := vte.ScrollbackCellAt(x, y)
            if c != nil && c.Width > 0 && c.Content != "" && c.Content != " " {
                lastCol = x
                break
            }
        }
        for x := 0; x <= lastCol; x++ {
            cell := vte.ScrollbackCellAt(x, y)
            if cell == nil || cell.Width == 0 {
                continue // nil or wide-char continuation cell
            }
            if !cell.Style.Equal(&prev) {
                buf.WriteString(uv.StyleDiff(&prev, &cell.Style))
                prev = cell.Style
            }
            if cell.Content == "" {
                buf.WriteByte(' ')
            } else {
                buf.WriteString(cell.Content)
            }
        }
        buf.WriteByte('\n')
    }
    buf.WriteString(ansi.ResetStyle)
    return buf.Bytes()
}
```

Key decisions:

- `StyleDiff(prev, cell.Style)` emits only the transitions, keeping the replay
  blob small.
- Continuation cells (`Width == 0`) are skipped so wide characters occupy the
  correct number of visual columns.
- Trailing-blank trimming is done by finding `lastCol` before the output
  loop rather than emitting then trimming. This also avoids trailing SGR
  transitions for whitespace.
- Final `ansi.ResetStyle` ensures the immediately-following `vte.Render()`
  replay (`session.go:169`) starts from a clean attribute slate.

## Data Flow on Reattach (post-change)

Unchanged from today in structure; the delta is what `renderVTEScrollback`
returns.

```
SSH client connects
 -> handlePTYSession selects/creates session
 -> target.Attach(cols, rows)
      (new B1 guard: if exited, return pre-closed client; caller sees EOF)
    -> snapshot altScreen, vte.Render(), cursor, scrollback
    -> append to s.clients, promote to writer if first
    -> deliver replay blob:
         * alt screen:   [?1049h [2J    + Ctrl-L to PTY (unchanged)
         * primary:      ESC c + styled-scrollback + vte.Render() + cursor
                         ESC[y;xH
 -> io.Copy(sess, client) until EOF
```

## Error Handling

- **B1** client returned with closed output: `io.Copy` returns `io.EOF` on
  first read; SSH handler calls `sess.Exit(0)`. No error path surfaced to the
  caller because there is no new information to communicate; the session was
  simply gone by the time Attach ran.
- **B2** pump now runs `vte.Write` inline. If `vte.Write` returns an error
  (it does not today in practice; `SafeEmulator.Write` never fails), it is
  already ignored via `//nolint:errcheck`-style discard in existing code and
  we preserve that behavior.
- **C2** if `vte.ScrollbackCellAt` returns `nil` (can happen at buffer
  boundaries), the cell is skipped. No panic, no stray output.

## Testing

### Functional tests

| Change | Test |
|--------|------|
| A (VT swap) | Unit test: write `ESC[4h` + `"ABC"` with cursor mid-line and confirm existing cells shifted right. Fails against `x/vt`, passes against `vt-go`. Serves as a regression guard if we ever swap back. |
| A (VT swap) | `make test` + `make test-integration` to exercise all existing VT-backed paths. |
| B1 (attach race) | Unit tests covering both orderings: (a) create a session, wait for shell exit, confirm `s.exited` is observable, then `Attach` -- assert the returned client's `Read` returns `io.EOF` immediately. (b) A stress test that hammers `Attach` in a tight goroutine loop while simultaneously triggering shell exit, repeated at high iteration count. Assert every returned client's `Read` returns `io.EOF` within a short timeout — no client ever hangs. Would have caught the version of B1 that only set `s.exited` in `waitForExit` after `<-pumpDone`. |
| B2 (sync VTE) | Unit test: write a large burst (~1 MB of output) through the session and assert `vte.Render()` contains the full tail content. Would have caught the drop-on-full bug. |
| C1 (dead code removal) | Deletion only; covered by `make test` continuing to pass. |
| C2 (styled replay) | Unit test: write red-bold text that scrolls off, call `renderVTEScrollback`, assert output contains `\x1b[` and the expected content in order. Second case: write one CJK character followed by ASCII, assert column count matches expected and no stray space follows the wide char. |

### Race detector

Add an explicit race-detector run to CI and to the local verification recipe:

```
go test -race ./session ./sshd ./cmd
```

The teardown race that motivated B2's `pumpDone` barrier (pump writing to the
emulator at the same moment `waitForExit` closes it) is exactly the class of
bug `-race` catches; functional tests alone will not flag it because it
manifests as corrupted VT state, not a hard failure. This run must be green
before the change lands.

### VT-as-scrollback-authority semantics

Now that the VT emulator is the sole source of replay history (the custom
line buffer is gone), pin down two behaviors the design depends on. These
are pure characterization tests — they assert what `vt-go` actually does so
we notice if a future bump changes it.

1. **ED 2 (`ESC[2J`) pushes cleared content into scrollback.** Write
   several lines of visible content, issue `ESC[2J`, then assert
   `ScrollbackLen()` grew by roughly the number of previously-visible rows
   and the scrollback cells contain the expected content. If `vt-go` does
   not do this (some emulators don't), we need to know — replay after a
   `clear`-style sequence would otherwise lose the preceding visible
   content.
2. **Alt-screen output does not pollute main-screen scrollback.** Enter
   alt-screen (`ESC[?1049h`), write a screen of output, leave alt-screen
   (`ESC[?1049l`). Assert `ScrollbackLen()` is unchanged from before the
   alt-screen enter. This is standard terminal behavior and one of the
   reasons the old pause flag on the custom buffer existed. With that flag
   gone, we rely on the VT emulator to enforce the invariant itself; the
   test pins it down.

### Manual verification (host only; not runnable in the nested sandbox)

- `vibepit up`, `vibepit ssh`, run Claude Code, detach, re-`ssh`, confirm the
  replayed screen matches what was visible before detach.
- Repeat with `ls --color` output that has been scrolled into history; confirm
  colors survive reattach.

## Changes

| File | Change |
|------|--------|
| `go.mod` / `go.sum` | Replace `charmbracelet/x/vt` with `unixshells/vt-go v0.2.0`. |
| `session/session.go` | Update `vt` import path (A). Add `s.exited` guard in `Attach` (B1). Inline `vte.Write` in `pump`; set `s.exited = true` inside pump's error-path critical section atomically with the client snapshot; remove the `s.exited = true` assignment from `waitForExit`; delete `vteInput` and `feedVTE`; keep `pumpDone` but narrow its role to the VT-close barrier; reorder `waitForExit` shutdown as `ptmx.Close` → `<-pumpDone` → `vte.Close` → record exit metadata (B2). Remove `s.scrollback` field, initializer, and write (C1). Rewrite `renderVTEScrollback` (C2). |
| `session/scrollback.go` | Delete. |
| `session/scrollback_test.go` | Delete. |
| `session/session_test.go` | Add tests for B1, B2, and C2 (three cases as described above). |
| `session/session_test.go` | Add IRM regression test (A). |
| `session/session_test.go` | Add VT-authority semantics tests (ED 2 → scrollback, alt-screen isolation). |
| CI workflow or Makefile | Add `go test -race ./session ./sshd ./cmd` to the automated verification path. |

## Constraints and Risks

- `unixshells/vt-go v0.2.0` is a third-party fork with no stated maintenance
  cadence. Our usage surface is small and uses the same cell types as
  upstream, so swapping back is low-friction if it goes stale. Not a one-way
  door.
- `go mod tidy` may shift the resolved version of `charmbracelet/ultraviolet`
  depending on what `vt-go` pins. No callsite change required regardless;
  `uv.Cell`, `uv.Style`, `uv.StyleDiff` are stable in the versions we've
  inspected locally.
- B2's synchronous VTE write adds VTE parsing latency to the pump's inner
  loop. `SafeEmulator.Write` is fast relative to the syscalls already in the
  loop; this is the same pattern used by latch and other VT-backed
  multiplexers. Not expected to show up in benchmarks.
- No runtime verification is possible in the nested-sandbox development
  environment. All automated validation is `make test` / `make test-integration`
  / `go test`. Manual SSH reattach checks are host-only.

## Out of Scope

- Silent writer promotion on detach (`session.go:212-216`) — flagged during
  brainstorming but deferred as a UX question, not a correctness bug.
- Interactive scroll mode (latch-style server-composited scroll viewport).
- Hot-reload of `authorized_keys` in `sshd/server.go`.
- Connection tracker / kick functionality in the `monitor` TUI.
- Tombstone-aware reattach UX (showing exit code when the selected session
  exited between selector build and attach).
