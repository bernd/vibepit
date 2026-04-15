# VT Correctness and Scrollback Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix Claude Code rendering on SSH reattach, close the attach-after-exit race, eliminate the lossy async VTE feed, and replace the dead line-buffered scrollback with a style-preserving, wide-char-correct VT-backed replay.

**Architecture:** The PTY session layer (`session/session.go`) currently runs three goroutines around the VT emulator: `pump` (PTY → clients + scrollback), `feedVTE` (async VTE writer), `drainVTE` (emulator response reader). This plan collapses `feedVTE` into `pump`, deletes the unused custom line-buffered scrollback, moves `s.exited = true` into pump's error-path critical section so `Attach` can race-safely reject late-comers, and swaps the VT library to a fork that implements IRM so Claude Code renders correctly in replay. The VT emulator becomes the single source of truth for scrollback, and a new cell-based renderer emits SGR sequences to preserve color.

**Tech Stack:** Go 1.26, `github.com/unixshells/vt-go` (fork of `charmbracelet/x/vt`), `github.com/charmbracelet/ultraviolet` (cell/style types), `github.com/charmbracelet/x/ansi` (SGR constants), `github.com/creack/pty`, `github.com/stretchr/testify`.

**Spec:** `docs/superpowers/specs/2026-04-15-vt-correctness-and-scrollback-cleanup-design.md`

---

## Before starting

Run the full test suite once to establish a green baseline:

```bash
make test
```

Expected: all tests pass. If any existing test is already failing on this branch, stop and resolve that first — you need a clean baseline to tell whether your changes regressed anything.

---

## Task 1: IRM regression test against current `x/vt`

**Why:** Before swapping the VT library, write a test that captures the IRM bug. It should **fail** against the current `charmbracelet/x/vt`, then **pass** after the swap in Task 2. This proves the fix works and leaves a guard against regressions.

**Files:**
- Create: `session/vt_irm_test.go`

- [ ] **Step 1: Write the failing test**

Create `session/vt_irm_test.go` with this content:

```go
package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/charmbracelet/x/vt"
)

// TestVT_InsertReplaceMode verifies the VT emulator implements IRM
// (ESC[4h / ESC[4l). When insert mode is on, typing a character at the
// cursor shifts existing cells right instead of overwriting. Applications
// like Claude Code rely on this for correct mid-line insertion; replay on
// SSH reattach reads the emulator's screen state, so a missing IRM
// implementation shows up as garbled text after reconnect.
//
// This test is expected to FAIL against charmbracelet/x/vt, which does not
// implement IRM, and PASS against unixshells/vt-go, which does.
func TestVT_InsertReplaceMode(t *testing.T) {
	e := vt.NewSafeEmulator(20, 2)
	defer e.Close() //nolint:errcheck

	// Write "ABCDE" at row 0. Cursor ends at column 5.
	_, err := e.Write([]byte("ABCDE"))
	require.NoError(t, err)

	// Move cursor back to column 2 (CHA = Cursor Horizontal Absolute, 1-based).
	_, err = e.Write([]byte("\x1b[3G"))
	require.NoError(t, err)

	// Enable Insert/Replace Mode.
	_, err = e.Write([]byte("\x1b[4h"))
	require.NoError(t, err)

	// Type 'X'. With IRM on, this should shift "CDE" right one column
	// and place 'X' at column 2, producing "ABXCDE".
	_, err = e.Write([]byte("X"))
	require.NoError(t, err)

	// Read back row 0.
	var got []byte
	for col := 0; col < 6; col++ {
		c := e.CellAt(col, 0)
		require.NotNil(t, c, "cell at col %d nil", col)
		got = append(got, c.Content...)
	}
	assert.Equal(t, "ABXCDE", string(got),
		"IRM-mode insert should shift existing cells right, got %q", string(got))
}
```

- [ ] **Step 2: Run the test and confirm it fails**

Run:
```bash
go test ./session -run TestVT_InsertReplaceMode -v
```

Expected: **FAIL**. The output row will look like `ABXDE<space>` or similar — `X` overwrote `C` because `x/vt` does not honor `ESC[4h`.

If the test passes, investigate before continuing. Either the dependency has been updated with IRM support, or the test is wrong.

- [ ] **Step 3: Commit the failing test**

```bash
git add session/vt_irm_test.go
git commit -m "test: add IRM regression test (currently failing against x/vt)"
```

---

## Task 2: Swap to `unixshells/vt-go`

**Why:** `unixshells/vt-go v0.2.0` is a fork of `charmbracelet/x/vt` that adds IRM support. The public API we use is identical — `NewSafeEmulator`, `Write`, `Read`, `Render`, `Resize`, `Close`, `IsAltScreen`, `ScrollbackLen`, `ScrollbackCellAt`, `CursorPosition`, `Width`, `CellAt` — so this is a mechanical import path change.

**Files:**
- Modify: `go.mod`, `go.sum`
- Modify: `session/session.go:15` (the `vt` import)
- Modify: `session/vt_irm_test.go` (the import written in Task 1)

- [ ] **Step 1: Verify no other file imports `charmbracelet/x/vt`**

Run:
```bash
grep -rn "charmbracelet/x/vt" --include="*.go" .
```

Expected: hits only in `session/session.go` and `session/vt_irm_test.go`. If any other file imports `x/vt`, list it — it also needs updating in the same commit.

- [ ] **Step 2: Update `go.mod`**

Replace the `charmbracelet/x/vt` line with the vt-go fork. Run:

```bash
go get github.com/unixshells/vt-go@v0.2.0
go mod edit -droprequire github.com/charmbracelet/x/vt
go mod tidy
```

Verify the new module is present and the old one is gone:
```bash
grep -E "unixshells/vt-go|charmbracelet/x/vt" go.mod
```

Expected: one line for `github.com/unixshells/vt-go v0.2.0`, no line for `charmbracelet/x/vt`.

- [ ] **Step 3: Update the `vt` import in `session/session.go`**

Find this line (currently `session/session.go:15`):
```go
	"github.com/charmbracelet/x/vt"
```

Replace with:
```go
	vt "github.com/unixshells/vt-go"
```

(The `vt` alias is explicit because the module path no longer ends in `vt`.)

- [ ] **Step 4: Update the `vt` import in `session/vt_irm_test.go`**

Change the import in the test file from Task 1 to:
```go
	vt "github.com/unixshells/vt-go"
```

- [ ] **Step 5: Run the IRM test — now passing**

```bash
go test ./session -run TestVT_InsertReplaceMode -v
```

Expected: **PASS**. Row 0 now reads `ABXCDE`.

- [ ] **Step 6: Run the full session package tests**

```bash
go test ./session -v
```

Expected: all tests pass. If anything regressed, the likely causes are (a) a transitively-pinned `ultraviolet` version mismatch between the two vt packages, or (b) a subtle behavioral difference in the fork. Resolve before continuing.

- [ ] **Step 7: Run the full test suite**

```bash
make test
```

Expected: all green.

- [ ] **Step 8: Commit**

```bash
git add go.mod go.sum session/session.go session/vt_irm_test.go
git commit -m "feat(session): switch VT emulator to unixshells/vt-go for IRM support"
```

---

## Task 3: Pin down VT-authority scrollback semantics

**Why:** The next tasks remove the custom line-buffered scrollback and make `vt-go`'s internal scrollback the single source of truth for reattach replay. Before that lands, characterize two behaviors the design relies on: (1) clearing the screen with `ESC[2J` pushes the cleared content into scrollback, and (2) alt-screen output doesn't leak into main-screen scrollback. These are pure characterization tests — they assert what `vt-go` actually does so a future dependency bump surfaces any regression.

**Files:**
- Create: `session/vt_scrollback_test.go`

- [ ] **Step 1: Write the ED 2 test**

Create `session/vt_scrollback_test.go`:

```go
package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	vt "github.com/unixshells/vt-go"
)

// TestVT_ED2PushesToScrollback characterizes the emulator's behavior when an
// application issues ED 2 (ESC[2J — "clear entire screen"). We rely on the
// cleared content going into scrollback so that reattach replay still shows
// output that was visible on screen before a `clear`.
func TestVT_ED2PushesToScrollback(t *testing.T) {
	e := vt.NewSafeEmulator(10, 5)
	defer e.Close() //nolint:errcheck

	// Fill rows with distinguishable content.
	_, err := e.Write([]byte("row1\r\nrow2\r\nrow3\r\n"))
	require.NoError(t, err)

	before := e.ScrollbackLen()

	// ESC[2J — erase entire screen.
	_, err = e.Write([]byte("\x1b[2J"))
	require.NoError(t, err)

	after := e.ScrollbackLen()

	assert.Greater(t, after, before,
		"ED 2 should push cleared visible content into scrollback; "+
			"scrollback len went from %d to %d", before, after)
}
```

- [ ] **Step 2: Write the alt-screen isolation test**

Append to the same file:

```go
// TestVT_AltScreenDoesNotPolluteScrollback characterizes the emulator's
// behavior when an application enters and leaves alt-screen mode (vim, less,
// htop). Lines written inside alt-screen must not appear in the main-screen
// scrollback; the old custom scrollback buffer had an explicit pause flag
// for this reason. With the VT emulator as the sole source of truth, we
// depend on the library to enforce this itself.
func TestVT_AltScreenDoesNotPolluteScrollback(t *testing.T) {
	e := vt.NewSafeEmulator(10, 5)
	defer e.Close() //nolint:errcheck

	// Seed the main screen and capture scrollback len.
	_, err := e.Write([]byte("main1\r\nmain2\r\n"))
	require.NoError(t, err)
	before := e.ScrollbackLen()

	// Enter alt-screen, write a screen of content, leave alt-screen.
	_, err = e.Write([]byte("\x1b[?1049h"))
	require.NoError(t, err)
	for i := 0; i < 20; i++ {
		_, err = e.Write([]byte("altline\r\n"))
		require.NoError(t, err)
	}
	_, err = e.Write([]byte("\x1b[?1049l"))
	require.NoError(t, err)

	after := e.ScrollbackLen()
	assert.Equal(t, before, after,
		"alt-screen output must not enter main-screen scrollback; "+
			"scrollback len changed from %d to %d", before, after)
}
```

- [ ] **Step 3: Run the tests**

```bash
go test ./session -run TestVT_ -v
```

Expected: both PASS. If either fails, stop — the design assumes these behaviors, and a gap means we need to decide whether to work around the library or fix it upstream. Surface the result to the human reviewer; do not proceed.

- [ ] **Step 4: Commit**

```bash
git add session/vt_scrollback_test.go
git commit -m "test(session): characterize VT-go scrollback semantics (ED 2, alt-screen)"
```

---

## Task 4: Inline the VTE write and close the attach-after-exit race

**Why:** This is the core correctness change. Two problems addressed together because they overlap in `pump` and `waitForExit`:

1. The async `vteInput` channel silently drops data under load (`select/default`), causing the VT emulator's screen state to desync from what clients actually saw. With the custom scrollback gone, replay reads from the emulator; desync corrupts replay.
2. `waitForExit` sets `s.exited = true` *after* `<-pumpDone`, but pump can run its entire final-snapshot-and-close path before `waitForExit` even wakes from `cmd.Wait` (PTY EOF and `cmd.Wait` return are independent events). `Attach` that hits in that window sees `s.exited == false`, gets added to `s.clients`, and nothing remains to close its output channel — `Read` hangs until SSH keepalive.

The fix: inline `vte.Write` into pump's critical section (removes `vteInput`, `feedVTE`, and the drop bug), and set `s.exited = true` inside pump's *error-path* critical section atomically with the client snapshot (closes the race via `s.mu` ordering — `Attach` either gets in before pump's lock and is included in the snapshot, or sees the flag and rejects).

**Files:**
- Modify: `session/session.go` (pump, feedVTE, waitForExit, Attach, newSession)
- Modify: `session/manager_test.go` (add tests)

### 4a. Write the burst-replay test (for B2)

- [ ] **Step 1: Add the burst test to `session/manager_test.go`**

Append this test at the end of `session/manager_test.go`:

```go
// TestSession_VTEDoesNotDropUnderBurst verifies that a large burst of shell
// output does not cause the VT emulator state to desync from reality.
// Historically, pump fed the emulator through a 1024-slot async channel and
// dropped writes with select/default when it filled; replay on reattach
// then showed stale screen state. This test fails under the old async
// design and passes with synchronous in-pump vte.Write.
func TestSession_VTEDoesNotDropUnderBurst(t *testing.T) {
	m := testManager(50)
	s, err := m.Create(80, 24, nil)
	require.NoError(t, err)

	c := s.Attach(80, 24)

	// Burst 2000 numbered lines through the shell. /bin/sh with a for
	// loop keeps this fast and self-contained.
	_, err = c.Write([]byte(
		"for i in $(seq 1 2000); do echo line_$i; done; echo DONE\n"))
	require.NoError(t, err)

	// Drain the client until we see DONE, then let the emulator settle.
	buf := make([]byte, 32*1024)
	deadline := time.After(10 * time.Second)
	var seen strings.Builder
	for !strings.Contains(seen.String(), "DONE") {
		readDone := make(chan int, 1)
		go func() {
			n, _ := c.Read(buf)
			readDone <- n
		}()
		select {
		case n := <-readDone:
			seen.WriteString(string(buf[:n]))
		case <-deadline:
			t.Fatal("timeout waiting for DONE marker")
		}
	}
	time.Sleep(50 * time.Millisecond)

	// The VT emulator should have the tail of the burst in its combined
	// (scrollback + screen) history. Check that a line near the end is
	// present somewhere — either in scrollback or on the current screen.
	needle := "line_1995"
	found := false

	// Check current screen.
	s.mu.Lock()
	screen := s.vte.Render()
	s.mu.Unlock()
	if strings.Contains(screen, needle) {
		found = true
	}

	// Check scrollback cells.
	if !found {
		s.mu.Lock()
		sbLen := s.vte.ScrollbackLen()
		width := s.vte.Width()
		for y := 0; y < sbLen && !found; y++ {
			var line []byte
			for x := 0; x < width; x++ {
				cell := s.vte.ScrollbackCellAt(x, y)
				if cell != nil && cell.Content != "" {
					line = append(line, cell.Content...)
				}
			}
			if strings.Contains(string(line), needle) {
				found = true
			}
		}
		s.mu.Unlock()
	}

	assert.True(t, found,
		"VT emulator should contain %q somewhere in scrollback or screen "+
			"after a 2000-line burst; it is missing, which indicates the "+
			"async vteInput feed dropped data", needle)

	c.Close()
}
```

- [ ] **Step 2: Run the test — expected to be flaky or fail**

```bash
go test ./session -run TestSession_VTEDoesNotDropUnderBurst -v -count=5
```

Expected: **FAIL** or flakily fail on at least one iteration under the current async-drop design, because `vteInput` is 1024-buffered and a 2000-line burst can exceed that when the feedVTE goroutine is scheduled poorly. If it passes reliably on your machine, do not skip the subsequent fix — the drop path is still a real hazard under load.

Note: do not commit yet — the fix and test land together.

### 4b. Write the attach-race stress test (for B1)

- [ ] **Step 3: Add the attach-race stress test to `session/manager_test.go`**

Append this test after the burst test:

```go
// TestSession_AttachAfterExitNeverHangs is a stress test for the
// attach-after-exit race. It runs a tight loop that triggers shell exit
// while concurrently calling Attach, and asserts every returned client
// reaches EOF within a short timeout. Under the old design (s.exited set
// only in waitForExit after <-pumpDone), pump could finish its final
// client snapshot before waitForExit set the flag, leaving a window where
// Attach added a client whose output was never closed; that client's Read
// would block until SSH keepalive (~5s). This test would hang under the
// old design.
func TestSession_AttachAfterExitNeverHangs(t *testing.T) {
	const iterations = 200
	for i := 0; i < iterations; i++ {
		m := testManager(50)
		s, err := m.Create(80, 24, nil)
		require.NoError(t, err)

		// Writer triggers exit as soon as it can.
		writer := s.Attach(80, 24)
		go func() {
			_, _ = writer.Write([]byte("exit\n"))
		}()

		// Attempt Attach in a tight loop until shell exits. Every
		// returned client must reach EOF within a second, regardless
		// of whether it attached before or after pump closed outputs.
		deadline := time.After(3 * time.Second)
		for {
			c := s.Attach(80, 24)
			readDone := make(chan struct{})
			go func() {
				defer close(readDone)
				buf := make([]byte, 4096)
				for {
					_, err := c.Read(buf)
					if err != nil {
						return
					}
				}
			}()
			select {
			case <-readDone:
				c.Close()
			case <-time.After(1 * time.Second):
				t.Fatalf("iteration %d: Attach returned a client whose "+
					"Read hung; attach-after-exit race not closed", i)
			}

			if s.Exited() {
				// One final Attach *after* exited is observable — must
				// also see EOF fast.
				c2 := s.Attach(80, 24)
				final := make(chan struct{})
				go func() {
					defer close(final)
					buf := make([]byte, 4096)
					_, _ = c2.Read(buf)
				}()
				select {
				case <-final:
					c2.Close()
				case <-time.After(1 * time.Second):
					t.Fatalf("iteration %d: post-exit Attach hung on Read", i)
				}
				break
			}
			select {
			case <-deadline:
				t.Fatalf("iteration %d: shell did not exit in time", i)
			default:
			}
		}
		writer.Close()
	}
}
```

- [ ] **Step 4: Run the stress test — expected to hang or fail**

```bash
go test ./session -run TestSession_AttachAfterExitNeverHangs -v -timeout 30s
```

Expected: **FAIL** with `Read hung` or `post-exit Attach hung on Read` on at least one iteration (usually within the first 50). The 30s top-level timeout backstops a true hang. If it passes 200 iterations, still proceed — the race is narrow and timing-dependent; the fix is still correct.

### 4c. Apply the fix

- [ ] **Step 5: Update the `Session` struct and `newSession`**

Open `session/session.go`. Find the struct definition (around line 22-45):

```go
type Session struct {
	id        string
	cmd       *exec.Cmd
	ptmx      *os.File
	createdAt time.Time
	manager   *Manager

	mu         sync.Mutex
	clients    []*Client
	writer     *Client
	exited     bool
	exitCode   int
	exitedAt   time.Time
	detachedAt time.Time
	vte        *vt.SafeEmulator
	scrollback *Scrollback
	cols       uint16
	rows       uint16

	vteInput    chan []byte   // async VTE feed channel
	pumpDone    chan struct{} // closed when pump goroutine exits
	cleanup     chan struct{}
	cleanupOnce sync.Once
}
```

Remove the `vteInput` field. Keep `pumpDone`. (The `scrollback` field also goes away but that's Task 5 — leave it for now to keep tasks atomic.) The struct becomes:

```go
type Session struct {
	id        string
	cmd       *exec.Cmd
	ptmx      *os.File
	createdAt time.Time
	manager   *Manager

	mu         sync.Mutex
	clients    []*Client
	writer     *Client
	exited     bool
	exitCode   int
	exitedAt   time.Time
	detachedAt time.Time
	vte        *vt.SafeEmulator
	scrollback *Scrollback
	cols       uint16
	rows       uint16

	pumpDone    chan struct{} // closed when pump goroutine exits; barrier for vte.Close
	cleanup     chan struct{}
	cleanupOnce sync.Once
}
```

Then find `newSession` (around line 47) and remove the `vteInput: make(chan []byte, 1024)` line. Remove the `go s.feedVTE()` line. The goroutine list should become just `go s.pump()`, `go s.drainVTE()`, `go s.waitForExit()`.

- [ ] **Step 6: Rewrite `pump` to inline VTE and set `s.exited` in the error path**

Replace the entire `pump` method (currently around lines 283-325):

```go
// pump reads PTY output, feeds it into the VTE emulator under s.mu, and
// fans out to all attached clients. On PTY read error (shell exit or
// ptmx.Close from waitForExit), it sets s.exited = true atomically with
// the final client snapshot and closes every client's output channel.
// Setting s.exited inside the error-path critical section is what makes
// Attach's s.exited guard race-free: any Attach that holds s.mu after
// pump's error path has run sees the flag; any that ran before is
// included in pump's snapshot.
func (s *Session) pump() {
	defer close(s.pumpDone)
	buf := make([]byte, 32*1024)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])

			s.mu.Lock()
			s.vte.Write(data) //nolint:errcheck
			s.scrollback.Write(data) // removed in Task 5 (C1)
			clients := append([]*Client(nil), s.clients...)
			s.mu.Unlock()

			for _, c := range clients {
				c.deliver(data)
			}
		}
		if err != nil {
			s.mu.Lock()
			s.exited = true
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

- [ ] **Step 7: Delete `feedVTE`**

Remove the entire `feedVTE` method (currently around lines 260-269). It is no longer called.

- [ ] **Step 8: Rewrite `waitForExit`**

Replace the entire `waitForExit` method (currently around lines 329-355) with:

```go
// waitForExit waits for the shell process to exit, tears down the PTY and
// VTE in an order that avoids racing pump's final write, and records exit
// metadata. s.exited is set by pump in its error-path critical section;
// this function only records exitCode/exitedAt and decides whether to
// start the tombstone timer.
func (s *Session) waitForExit() {
	exitCode := 0
	if err := s.cmd.Wait(); err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	// Close the PTY; unblocks pump's Read if it wasn't already EOF.
	s.ptmx.Close() //nolint:errcheck

	// Wait for pump to finish its last vte.Write before closing the VTE.
	// Required: closing SafeEmulator while pump is mid-Write races on
	// the emulator's internal state (-race flags it).
	<-s.pumpDone

	// Now safe to close the VTE; drainVTE will unblock.
	s.vte.Close() //nolint:errcheck

	s.mu.Lock()
	s.exitCode = exitCode
	s.exitedAt = time.Now()
	hasClients := len(s.clients) > 0
	s.mu.Unlock()

	if s.manager != nil {
		s.manager.onSessionChanged()
	}

	if !hasClients {
		go s.expireTombstone()
	}
}
```

- [ ] **Step 9: Add the `Attach` guard**

Find `Attach` (around line 125). At the top of the function, immediately after `s.mu.Lock()`, add:

```go
func (s *Session) Attach(cols, rows uint16) *Client {
	s.mu.Lock()

	// If the session has already exited (pump or waitForExit set the flag),
	// return a client with its output pre-closed so the caller sees clean
	// EOF. Do not add to s.clients — nothing will ever drive it.
	if s.exited {
		c := newClient(s)
		s.mu.Unlock()
		c.closeOutput()
		return c
	}

	// ... existing body unchanged from here ...
```

Leave the rest of `Attach` exactly as it is.

- [ ] **Step 10: Verify `s.exited` is no longer assigned in `waitForExit`**

Run:
```bash
grep -n "s.exited = true" session/session.go
```

Expected output: exactly two lines — one in `pump` (error path) and one in `Attach`'s guard (wait, no — `Attach` reads it, doesn't assign it). So: exactly one line, in `pump`. If there's a stray assignment still in `waitForExit`, remove it.

- [ ] **Step 11: Run the targeted tests**

```bash
go test ./session -run "TestSession_VTEDoesNotDropUnderBurst|TestSession_AttachAfterExitNeverHangs" -v -count=3
```

Expected: both PASS across all three runs. If the stress test still fails, re-inspect step 6 — `s.exited = true` must be *inside* the `s.mu.Lock()` … `s.mu.Unlock()` block in the error path, atomic with the snapshot.

- [ ] **Step 12: Run the race detector**

```bash
go test -race ./session
```

Expected: PASS with no race warnings. If `-race` flags the VTE, the `<-s.pumpDone` barrier in `waitForExit` is missing or mis-ordered. Re-check step 8.

- [ ] **Step 13: Run the full test suite**

```bash
make test
```

Expected: all green.

- [ ] **Step 14: Commit**

```bash
git add session/session.go session/manager_test.go
git commit -m "fix(session): inline VTE write in pump, close attach-after-exit race

- Remove vteInput channel and feedVTE goroutine; pump writes to the VTE
  emulator synchronously under s.mu. Eliminates the silent-drop path that
  could desync emulator state from truth under burst load.
- Set s.exited = true inside pump's error-path critical section, atomic
  with the final client snapshot. Attach's s.exited guard can now race
  with shell exit: any Attach that holds s.mu after pump's error path
  sees the flag and rejects; any that ran before is in pump's snapshot.
- waitForExit no longer sets s.exited; it only records exit metadata.
- Keep pumpDone as the VT-close barrier (prevents SafeEmulator close
  racing pump's final Write)."
```

---

## Task 5: Delete the dead line-buffered scrollback

**Why:** `session/scrollback.go` defines a line-oriented ring buffer that's written to by `pump` but never read by production code — only by its own tests. Replay uses `renderVTEScrollback`, which reads the VT emulator's scrollback instead. A comment at `session.go:132-134` already acknowledges that using the custom buffer would duplicate content. After Task 4, the alt-screen pause flag is irrelevant too (it lived in the now-deleted `feedVTE`).

**Files:**
- Delete: `session/scrollback.go`
- Delete: `session/scrollback_test.go`
- Modify: `session/session.go` (remove `scrollback` field, initializer, write)

- [ ] **Step 1: Confirm nothing outside `session/` uses `Scrollback`**

```bash
grep -rn "session.Scrollback\|session.NewScrollback" --include="*.go" .
```

Expected: no hits. If anything does reference it, stop and surface the finding — the deletion may need a separate refactor first.

- [ ] **Step 2: Delete the scrollback files**

```bash
rm session/scrollback.go session/scrollback_test.go
```

- [ ] **Step 3: Remove the `scrollback` field from the struct**

In `session/session.go`, find the `Session` struct. Remove the line:

```go
	scrollback *Scrollback
```

- [ ] **Step 4: Remove the initializer in `newSession`**

Find:
```go
		scrollback: NewScrollback(10000),
```

Delete that line.

- [ ] **Step 5: Remove the write call in `pump`**

In the `pump` body (from Task 4, step 6), remove the line:

```go
			s.scrollback.Write(data) // removed in Task 5 (C1)
```

The critical section becomes:

```go
			s.mu.Lock()
			s.vte.Write(data) //nolint:errcheck
			clients := append([]*Client(nil), s.clients...)
			s.mu.Unlock()
```

- [ ] **Step 6: Verify the package compiles**

```bash
go build ./session
```

Expected: no errors. If the compiler complains about an unused import (`strings` etc.) that was only used by scrollback code, remove the import.

- [ ] **Step 7: Run the full session tests**

```bash
go test ./session -v
```

Expected: all PASS. The deleted `TestScrollback_*` tests are gone, so the test count drops — that's expected.

- [ ] **Step 8: Commit**

```bash
git add -u session/
git commit -m "refactor(session): delete unused line-buffered scrollback

The custom Scrollback struct was written to by pump but only ever read
by its own tests — replay uses renderVTEScrollback, which reads the VT
emulator. After Task 4 removed feedVTE, the pause-flag logic tied to
alt-screen is also irrelevant. The VT emulator is now the single source
of truth for scrollback."
```

---

## Task 6: Style-preserving, wide-char-correct `renderVTEScrollback`

**Why:** The current `renderVTEScrollback` reads cells from the VT scrollback and emits `cell.Content` or a space, discarding all style information. On SSH reattach, scrollback comes back monochrome. It also miscounts wide characters: a 2-column CJK cell occupies `x` and `x+1` where the second cell has `Width == 0` and empty `Content`, and the current code emits a space there, misaligning the rest of the line. The fix walks cells, uses `ultraviolet.StyleDiff` to emit minimal SGR transitions, and skips continuation cells.

**Files:**
- Modify: `session/session.go` (rewrite `renderVTEScrollback`; update imports)
- Modify: `session/manager_test.go` (add tests)

- [ ] **Step 1: Write the styled-replay test**

Append to `session/manager_test.go`:

```go
// TestRenderVTEScrollback_PreservesStyle writes red-bold text that scrolls
// off-screen, then asserts the rendered scrollback contains SGR escape
// sequences (so the content is not monochrome) and the expected characters.
func TestRenderVTEScrollback_PreservesStyle(t *testing.T) {
	vte := vt.NewSafeEmulator(20, 3)
	defer vte.Close() //nolint:errcheck

	// Write 5 lines of red-bold text. With a 3-row screen, 2 lines
	// scroll off into scrollback.
	for i := 0; i < 5; i++ {
		line := fmt.Sprintf("\x1b[1;31mhello%d\x1b[0m\r\n", i)
		_, err := vte.Write([]byte(line))
		require.NoError(t, err)
	}

	out := renderVTEScrollback(vte)
	s := string(out)

	assert.Contains(t, s, "\x1b[", "styled scrollback should contain SGR sequences")
	assert.Contains(t, s, "hello0", "content should be preserved")
	assert.Contains(t, s, "hello1", "content should be preserved")
}
```

Add the `"fmt"` import and the `vt "github.com/unixshells/vt-go"` import at the top of the file if not already present. Run:

```bash
grep -E '^\s*"fmt"|unixshells/vt-go' session/manager_test.go
```

If `fmt` is missing from the import block, add it. Same for the vt alias.

- [ ] **Step 2: Write the wide-character test**

Append:

```go
// TestRenderVTEScrollback_WideCharacters writes a 2-column CJK character
// followed by ASCII, scrolls it off, and asserts the rendered scrollback
// emits the wide character exactly once without a trailing stray space
// (which would happen if the continuation cell with Width==0 were emitted
// as a space).
func TestRenderVTEScrollback_WideCharacters(t *testing.T) {
	vte := vt.NewSafeEmulator(10, 2)
	defer vte.Close() //nolint:errcheck

	// "あ" is 3 UTF-8 bytes, displays in 2 columns. Followed by "BC".
	_, err := vte.Write([]byte("あBC\r\n"))
	require.NoError(t, err)
	// Push line into scrollback with extra output.
	_, err = vte.Write([]byte("x\r\ny\r\nz\r\n"))
	require.NoError(t, err)

	out := renderVTEScrollback(vte)
	s := string(out)

	// Strip SGR sequences for the content check.
	clean := stripSGR(s)
	assert.Contains(t, clean, "あBC",
		"wide char must be followed immediately by BC without a stray "+
			"space from its continuation cell. Got (stripped): %q", clean)

	// And "あ" should appear exactly once — emitting it twice would
	// happen if we didn't skip the continuation cell correctly.
	assert.Equal(t, 1, strings.Count(clean, "あ"),
		"wide char should appear exactly once; got (stripped): %q", clean)
}

// stripSGR removes CSI SGR sequences (ESC[...m) from s for text-only
// content assertions. Does not try to be a full ANSI parser.
func stripSGR(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			// Skip until final byte 'm' (or end of string).
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}
```

- [ ] **Step 3: Run the tests — expected to fail**

```bash
go test ./session -run "TestRenderVTEScrollback_" -v
```

Expected: both FAIL. The style test fails because the current renderer doesn't emit SGR. The wide-char test fails because it emits an extra space for the continuation cell, so `stripSGR(out)` contains `"あ BC"` and/or `strings.Count` is wrong.

- [ ] **Step 4: Rewrite `renderVTEScrollback`**

Find the existing `renderVTEScrollback` function (currently around lines 366-394). Replace the entire function (and its existing `bytes` / `vt` imports if they differ) with:

```go
// renderVTEScrollback renders the VTE emulator's scrollback buffer (lines
// that have scrolled off the visible screen) as an ANSI byte stream with
// styles preserved. Uses StyleDiff to emit the minimal SGR transition
// between cells. Skips wide-character continuation cells (Width == 0) so
// CJK content aligns correctly. Appends an explicit ResetStyle at the end
// so the subsequent vte.Render() blob starts with a clean SGR state.
func renderVTEScrollback(vte *vt.SafeEmulator) []byte {
	lines := vte.ScrollbackLen()
	if lines == 0 {
		return nil
	}
	width := vte.Width()

	var buf bytes.Buffer
	var prev uv.Style
	for y := 0; y < lines; y++ {
		// Find last non-blank column so we drop right-edge fill.
		lastCol := -1
		for x := width - 1; x >= 0; x-- {
			c := vte.ScrollbackCellAt(x, y)
			if c == nil {
				continue
			}
			if c.Width == 0 {
				continue // continuation cell
			}
			if c.Content == "" || c.Content == " " {
				continue
			}
			lastCol = x
			break
		}
		for x := 0; x <= lastCol; x++ {
			cell := vte.ScrollbackCellAt(x, y)
			if cell == nil || cell.Width == 0 {
				continue
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

- [ ] **Step 5: Add the new imports**

At the top of `session/session.go`, ensure these imports are present alongside the existing ones:

```go
	"github.com/charmbracelet/x/ansi"
	uv "github.com/charmbracelet/ultraviolet"
```

Run `goimports -w session/session.go` if available, or add them manually. Then:

```bash
go build ./session
```

Expected: no errors.

- [ ] **Step 6: Run the new tests**

```bash
go test ./session -run "TestRenderVTEScrollback_" -v
```

Expected: both PASS.

- [ ] **Step 7: Run the replay regression test**

```bash
go test ./session -run TestSession_ReplayOnAttach -v
```

Expected: PASS. The replay test in `manager_test.go:264` exercises the full attach-replay path; if the new renderer broke it, the content assertion will fail.

- [ ] **Step 8: Run the full session tests under `-race`**

```bash
go test -race ./session
```

Expected: all PASS, no race warnings.

- [ ] **Step 9: Commit**

```bash
git add session/session.go session/manager_test.go
git commit -m "feat(session): preserve style and wide-char alignment in scrollback replay

Rewrites renderVTEScrollback to emit SGR transitions via StyleDiff,
skip wide-character continuation cells, and terminate with ResetStyle.
Colors and attributes now survive SSH reattach; CJK content no longer
misaligns the rest of the line."
```

---

## Task 7: Wire the race detector into CI

**Why:** The teardown race in Task 4 was the kind of bug `-race` catches first. Functional tests alone can miss it because the symptom is corrupted VT state, not a hard crash. Guarantee future regressions get caught in CI.

**Files:**
- Modify: `Makefile` (add a `test-race` target)
- Modify: `.github/workflows/build.yml` (run the new target)

- [ ] **Step 1: Add the `test-race` Make target**

Open `Makefile`. Find the `.PHONY` line at the top:

```make
.PHONY: build test test-integration clean release-build release-archive release-publish docs-install docs-build docs-serve
```

Add `test-race` to it:

```make
.PHONY: build test test-race test-integration clean release-build release-archive release-publish docs-install docs-build docs-serve
```

Find the `test:` target:

```make
test:
	go test ./...
```

Add a new target immediately below it:

```make
test-race:
	go test -race ./session ./sshd ./cmd
```

(Scoped to the three packages where the PTY/SSH concurrency lives; `go test -race ./...` would also work but adds noticeable CI time for no new coverage.)

- [ ] **Step 2: Run the target locally to confirm it passes**

```bash
make test-race
```

Expected: all three packages PASS with no race warnings. If any fail, stop — a flag from `-race` is a real bug that must be fixed before this plan is complete.

- [ ] **Step 3: Add the step to the CI workflow**

Open `.github/workflows/build.yml`. Find the `Tests` step (line 37-38):

```yaml
      - name: "Tests"
        run: "make test"
```

Immediately after it, add:

```yaml
      - name: "Race Tests"
        run: "make test-race"
```

The block becomes:

```yaml
      - name: "Tests"
        run: "make test"

      - name: "Race Tests"
        run: "make test-race"

      - name: "Integration Tests"
        run: "make test-integration"
```

- [ ] **Step 4: Commit**

```bash
git add Makefile .github/workflows/build.yml
git commit -m "ci: add go test -race for session/sshd/cmd

Catches the teardown race class of bug (pump writing to the VT emulator
while shutdown closes it) that motivated the pumpDone barrier. Functional
tests alone miss it because the symptom is corrupted VT state, not a
hard failure."
```

---

## Final verification

- [ ] **Step 1: Full local test sweep**

```bash
make test && make test-race && make test-integration
```

Expected: all green.

- [ ] **Step 2: Inspect what changed**

```bash
git log --oneline main..HEAD
```

Expected: seven commits, one per task, each with a descriptive conventional-commit message.

- [ ] **Step 3: Confirm no stray `charmbracelet/x/vt` or dead references remain**

```bash
grep -rn "charmbracelet/x/vt\|session\.Scrollback\|vteInput\|feedVTE" --include="*.go" . || echo "clean"
```

Expected: `clean`.

- [ ] **Step 4 (host only, not in nested sandbox): Manual reattach smoke test**

On a host with Docker/Podman:

```bash
go run . up
go run . ssh
# Run Claude Code, type mid-line text, observe rendering.
# Ctrl-D to detach.
go run . ssh
# The previous session's screen and scrollback should replay correctly,
# with colors intact and no IRM-related garbling.
go run . down
```

Skip this step if running inside a nested vibepit sandbox — container runtime commands will fail by design.
