package overlay

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bernd/vibepit/vt"
)

// can aborts the escape sequence a terminal is in the middle of.
const can = 0x18

// detachAt is T1: it stops forwarding output at a byte where the shadow's
// parser is at ground, or after the budget with a forced cut. Without an
// error the terminal is detached, and the caller must end with leave.
func (t *Terminal) detachAt(ctx context.Context) error {
	if isClosed(t.in.Done()) {
		return ErrClosed
	}
	t.mu.Lock()
	switch {
	case t.closed:
		t.mu.Unlock()
		return ErrClosed
	case t.shadowErr != nil:
		err := t.shadowErr
		t.mu.Unlock()
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	req := &detachReq{done: make(chan struct{})}
	t.detach = req
	switch ground, err := t.shadow.AtGround(); {
	case err != nil:
		t.failLocked(err)
		t.endDetachLocked(fmt.Errorf("%w: %w", ErrUnavailable, err))
	case ground:
		t.cutLocked(false)
	}
	t.mu.Unlock()

	timer := time.NewTimer(t.timing.groundWait)
	defer timer.Stop()
	var stop error
	select {
	case <-req.done:
	case <-timer.C:
	case <-ctx.Done():
		stop = ctx.Err()
	case <-t.done:
		stop = ErrClosed
	}
	t.mu.Lock()
	if t.detach == req {
		if stop != nil {
			t.endDetachLocked(stop)
		} else {
			// No ground within the time budget, e.g. a stalled OSC.
			t.cutLocked(true)
		}
	}
	t.mu.Unlock()
	<-req.done
	return req.err
}

// endDetachLocked finishes the pending T1; err says why no cut happened.
func (t *Terminal) endDetachLocked(err error) {
	t.detach.err = err
	close(t.detach.done)
	t.detach = nil
}

// cutLocked is T1 at the current byte: capture the cut, stop forwarding,
// and send the barrier query. After a forced cut, CAN aborts the sequence
// the real terminal is in the middle of.
func (t *Terminal) cutLocked(forced bool) {
	c, err := captureCut(t.shadow)
	if err != nil {
		t.failLocked(err)
		t.endDetachLocked(fmt.Errorf("%w: %w", ErrUnavailable, err))
		return
	}
	c.forced = forced
	c.barrierSent = !t.barrierUnsupported
	t.cut = c
	t.attached = false
	t.log = rawLog{}
	t.answered, t.resized = false, false
	// Drain before the query goes out: a local terminal can answer before
	// the next statement runs.
	t.in.Drain()
	var b []byte
	if forced {
		b = append(b, can)
	}
	if c.modes[modeSync] {
		// The real terminal must not hold back drawing during the prompt.
		b = append(b, "\x1b[?2026l"...)
	}
	if c.barrierSent {
		b = append(b, barrierQuery...)
	}
	_, _ = t.cfg.Stdout.Write(b)
	t.endDetachLocked(nil)
}

// advanceDetachLocked forwards p up to the byte where the shadow reaches
// ground, cuts there, and returns the rest for the shadow and the log.
func (t *Terminal) advanceDetachLocked(p []byte) []byte {
	ground, err := t.shadow.AtGround()
	if err == nil && !ground {
		if len(p) == 0 {
			return p
		}
		var n int
		n, ground, err = t.shadow.WriteUntilGround(p)
		if err == nil {
			t.flushAnswersLocked()
			_, _ = t.cfg.Stdout.Write(p[:n])
			t.detach.forwarded += n
			p = p[n:]
		}
	}
	switch {
	case err != nil:
		t.failLocked(err)
		t.endDetachLocked(fmt.Errorf("%w: %w", ErrUnavailable, err))
	case ground:
		t.cutLocked(false)
	case t.detach.forwarded >= t.timing.groundBytes:
		t.cutLocked(true)
	}
	return p
}

// resyncLocked drops output up to the next ground: the real terminal never
// got the start of the sequence the shadow is in.
func (t *Terminal) resyncLocked(p []byte) []byte {
	if len(p) == 0 {
		return p
	}
	ground, err := t.shadow.AtGround()
	if err == nil && !ground {
		var n int
		n, ground, err = t.shadow.WriteUntilGround(p)
		if err == nil {
			t.flushAnswersLocked()
			p = p[n:]
		}
	}
	if err != nil {
		t.failLocked(err)
		ground = true
	}
	if ground {
		t.resyncing = false
	}
	return p
}

// leave is T3. Holding t.mu, and the InputMux lock through Release, it
// writes the leave sequence, attaches output and hands input back, so no
// container output and no input slips in between. drawn tells whether the
// enter sequence was written.
func (t *Terminal) leave(drawn bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.cut
	var e entered
	if drawn {
		e = enterFor(c)
	}
	var strip time.Duration
	if !drawn && c.barrierSent {
		// The barrier reply may still come; it must not reach the app.
		strip = t.timing.lateStrip
	}
	var nudge bool
	t.in.Release(strip, func() {
		out, resync, reset := t.leaveBytesLocked(c, e, drawn)
		_, _ = t.cfg.Stdout.Write(out)
		t.attached = true
		t.resyncing = resync
		nudge = reset
	})
	t.cut = nil
	t.log = rawLog{}
	if nudge {
		go t.nudge()
	}
}

// leaveBytesLocked picks the leave: raw replay when the log reproduces the
// screen exactly, else a snapshot from the shadow, else a reset.
func (t *Terminal) leaveBytesLocked(c *cutState, e entered, drawn bool) (out []byte, resync, reset bool) {
	raw := !c.forced && // the real terminal may show a replacement character
		!t.log.overflow && // bytes are missing
		!t.resized && // the log was written for another geometry
		!t.answered && // a replayed query would be answered twice
		!(drawn && c.alt) // the prompt drew over the app's alternate screen
	if raw {
		return rawLeave(c, e, t.log.buf), false, false
	}
	if t.shadowErr == nil {
		out, resync, err := t.snapshot(t.shadow, c, e)
		if err == nil {
			return out, resync, false
		}
		// A screen too large to format under the memory limit leaves the
		// shadow usable; anything else means it can't be trusted.
		if !errors.Is(err, vt.ErrOutOfMemory) {
			t.failLocked(err)
		}
		t.logf("overlay: snapshot restore failed, resetting the terminal: %v", err)
	}
	return resetLeave(e), false, true
}

// nudge makes the app repaint after a reset: a size change delivers
// SIGWINCH, which the kernel skips when the size stays the same.
func (t *Terminal) nudge() {
	if t.cfg.Resize == nil {
		return
	}
	t.mu.Lock()
	cols, rows := t.cols, t.rows
	t.mu.Unlock()
	if rows > 1 {
		t.cfg.Resize(cols, rows-1)
	} else {
		t.cfg.Resize(cols+1, rows)
	}
	time.Sleep(t.timing.nudgeGap)
	t.cfg.Resize(cols, rows)
}
