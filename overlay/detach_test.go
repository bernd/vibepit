package overlay

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bernd/vibepit/vt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (h *harness) detach() {
	h.t.Helper()
	require.NoError(h.t, h.term.detachAt(context.Background()))
}

func (h *harness) detachAsync(ctx context.Context) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- h.term.detachAt(ctx) }()
	return ch
}

func (h *harness) waitDetachPending() {
	h.t.Helper()
	require.Eventually(h.t, func() bool {
		var pending bool
		h.locked(func(t *Terminal) { pending = t.detach != nil })
		return pending
	}, 5*time.Second, time.Millisecond)
}

// snapshotted reports whether a leave was a snapshot: only the snapshot
// resets the keyboard state.
func (h *harness) snapshotted() bool { return strings.Contains(h.stdout.String(), keyboardReset) }

func TestDetachCutsAtGround(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.app("abc\xe2")
	done := h.detachAsync(context.Background())
	h.waitDetachPending()
	h.app("\x82\xacX")
	require.NoError(t, h.result(done))
	assert.Equal(t, "abc\xe2\x82\xac"+barrierQuery, h.stdout.String(), "forwarding stops right after the character")
	h.term.leave(false)
	assert.False(t, h.snapshotted())
	assert.Equal(t, "abc€X", firstLine(h.real))
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestDetachAtGroundIsImmediate(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.app("ab")
	h.detach()
	assert.Equal(t, "ab"+barrierQuery, h.stdout.String())
	h.locked(func(tm *Terminal) {
		assert.False(t, tm.attached)
		assert.False(t, tm.cut.forced)
		assert.True(t, tm.cut.barrierSent)
	})
	h.app("cd")
	assert.Equal(t, "ab"+barrierQuery, h.stdout.String(), "output is logged, not forwarded")
	h.term.leave(false)
	assert.Equal(t, "abcd", firstLine(h.real))
}

func TestForcedCutAfterTheByteBudget(t *testing.T) {
	h := newHarness(t, 40, 5, withTiming(func(tm *timing) { tm.groundBytes = 16 }))
	h.app("\x1b]2;")
	done := h.detachAsync(context.Background())
	h.waitDetachPending()
	h.app(strings.Repeat("y", 20))
	require.NoError(t, h.result(done))
	assert.True(t, strings.HasSuffix(h.stdout.String(), "\x18"+barrierQuery), "CAN aborts the sequence on the real terminal")
	h.app("\x07after")
	h.term.leave(false)
	assert.True(t, h.snapshotted(), "a forced cut rules out raw replay")
	assert.Equal(t, "after", firstLine(h.real))
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestForcedCutAfterTheTimeBudget(t *testing.T) {
	h := newHarness(t, 20, 5, withTiming(func(tm *timing) { tm.groundWait = 20 * time.Millisecond }))
	h.app("\x1b]2;stall")
	start := time.Now()
	h.detach()
	assert.GreaterOrEqual(t, time.Since(start), 20*time.Millisecond)
	h.locked(func(tm *Terminal) { assert.True(t, tm.cut.forced) })
	assert.True(t, strings.HasSuffix(h.stdout.String(), "\x18"+barrierQuery))
	h.term.leave(false)
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestCutTurnsSynchronizedOutputOff(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.app("\x1b[?2026hframe")
	h.detach()
	assert.True(t, strings.HasSuffix(h.stdout.String(), "\x1b[?2026l"+barrierQuery))
	h.app(" more")
	h.term.leave(false)
	assert.False(t, h.snapshotted())
	on, err := h.real.Mode(2026, false)
	require.NoError(t, err)
	assert.True(t, on, "raw replay restores the app's mode")
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestShadowAnswersGoToTheContainerWhileDetached(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.detach()
	h.app("\x1b[6n")
	h.waitContainer("\x1b[1;1R")
	h.term.leave(false)
	assert.True(t, h.snapshotted(), "a replayed query would be answered twice")
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, "\x1b[1;1R", h.toCont.String(), "answered exactly once")
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestLogOverflowForcesASnapshot(t *testing.T) {
	h := newHarness(t, 20, 5, withTiming(func(tm *timing) { tm.logMax = 8 }))
	h.detach()
	h.app("0123456789")
	h.term.leave(false)
	assert.True(t, h.snapshotted())
	assert.Equal(t, "0123456789", firstLine(h.real))
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestResizeWhileDetachedForcesASnapshot(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.app("before")
	h.detach()
	h.resize(24, 6)
	h.app(" after")
	h.term.leave(false)
	assert.True(t, h.snapshotted())
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestSameSizeResizeKeepsRawReplay(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.detach()
	h.term.Resize()
	h.term.leave(false)
	assert.False(t, h.snapshotted())
}

func TestResyncWhenTheContinuationIsUnavailable(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.app("a")
	h.detach()
	h.app("\x1b]2;" + strings.Repeat("y", shadowContinuationBytes+1024))
	h.resize(24, 6) // rules out raw replay
	h.term.leave(false)
	h.locked(func(tm *Terminal) { assert.True(t, tm.resyncing) })
	h.app("yyy\x07after")
	h.locked(func(tm *Terminal) { assert.False(t, tm.resyncing) })
	assert.Equal(t, "aafter", firstLine(h.real), "the tail of the sequence doesn't show as text")
}

func TestSnapshotCompletesAnUnfinishedSequence(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.detach()
	h.app("abc\x1b[3")
	h.resize(24, 6)
	h.term.leave(false)
	h.app("1mRED\x1b[0m")
	assert.Equal(t, "abcRED", firstLine(h.real))
	assertSameTerminal(t, h.term.shadow, h.real)
}

func TestShadowFailureLeavesWithAReset(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.detach()
	h.resize(24, 6) // rules out raw replay
	h.locked(func(tm *Terminal) { tm.shadowErr = errors.New("trap") })
	h.term.leave(false)
	assert.True(t, strings.HasSuffix(h.stdout.String(), ris))
	require.Eventually(t, func() bool {
		return slices.Equal(h.resizeLog(), []string{"24x6", "24x5", "24x6"})
	}, 5*time.Second, time.Millisecond, "a size change makes the app repaint")
	assert.ErrorIs(t, h.term.detachAt(context.Background()), ErrUnavailable)
}

func TestShadowFailureStillReplaysRaw(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.detach()
	h.app("x")
	h.locked(func(tm *Terminal) { tm.shadowErr = errors.New("trap") })
	h.term.leave(false)
	assert.True(t, strings.HasSuffix(h.stdout.String(), "x"), "raw replay doesn't need the shadow")
}

func TestSnapshotOutOfMemoryResetsWithoutFailingTheShadow(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.locked(func(tm *Terminal) {
		tm.snapshot = func(*vt.Terminal, *cutState, entered) ([]byte, bool, error) {
			return nil, false, fmt.Errorf("format: %w", vt.ErrOutOfMemory)
		}
	})
	h.detach()
	h.resize(24, 6)
	h.term.leave(false)
	assert.True(t, strings.HasSuffix(h.stdout.String(), ris))
	h.locked(func(tm *Terminal) { assert.NoError(t, tm.shadowErr) })
	h.detach()
	h.term.leave(false)
}

func TestDetachCancelledBeforeGround(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.app("\x1b]2;x")
	ctx, cancel := context.WithCancel(context.Background())
	done := h.detachAsync(ctx)
	h.waitDetachPending()
	cancel()
	assert.ErrorIs(t, h.result(done), context.Canceled)
	assert.Equal(t, "\x1b]2;x", h.stdout.String(), "nothing written")
	h.app("\x07ok")
	assert.Equal(t, "\x1b]2;x\x07ok", h.stdout.String(), "still attached")
}

func TestDetachUnavailableWithoutShadow(t *testing.T) {
	h := newHarness(t, 20, 5, withoutShadow())
	assert.ErrorIs(t, h.term.detachAt(context.Background()), ErrUnavailable)
}

func TestDetachAfterStdinEOF(t *testing.T) {
	h := newHarness(t, 20, 5)
	require.NoError(t, h.stdin.Close())
	<-h.term.in.Done()
	assert.ErrorIs(t, h.term.detachAt(context.Background()), ErrClosed)
}

func TestLateBarrierReplyIsStripped(t *testing.T) {
	h := newHarness(t, 20, 5, silentTerminal())
	h.detach()
	h.term.leave(false)
	h.keys("a\x1b[0nb")
	h.waitContainer("ab")
	h.keys("\x1b[0n")
	h.waitContainer("ab\x1b[0n")
}

func TestDetachWhileTheSessionEnds(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.app("$ ")
	// A Show that won showMu just as the output ended: finish has closed
	// done and waits for showMu.
	h.term.showMu.Lock()
	h.out.close()
	require.Eventually(t, h.term.isDone, 5*time.Second, time.Millisecond)
	written := h.stdout.String()
	err := h.term.detachAt(context.Background())
	h.term.showMu.Unlock()
	assert.ErrorIs(t, err, ErrClosed)
	assert.Equal(t, written, h.stdout.String(), "nothing written")
}

func TestNudgeKeepsAResizeDuringItsGap(t *testing.T) {
	h := newHarness(t, 20, 5, withTiming(func(tm *timing) { tm.nudgeGap = 100 * time.Millisecond }))
	nudged := make(chan struct{})
	go func() {
		h.term.nudge()
		close(nudged)
	}()
	require.Eventually(t, func() bool { return slices.Contains(h.resizeLog(), "20x4") }, 5*time.Second, time.Millisecond)
	h.resize(30, 8) // SIGWINCH in the nudge's gap
	<-nudged
	log := h.resizeLog()
	assert.Equal(t, "30x8", log[len(log)-1], "the container ends at the real size")
}
