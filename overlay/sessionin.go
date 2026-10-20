package overlay

import (
	"io"
	"sync"
)

const (
	// sessionInputRoom is how much queued input makes the stdin pump wait
	// for the session: the backpressure a plain copy would have.
	sessionInputRoom = 64 << 10
	// sessionInputMax bounds the queue. Past it the shadow's answers are
	// dropped. Stdin is never dropped: its pump waits for room, at
	// sessionInputMax only while the handoff waits for the barrier reply.
	sessionInputMax = 4 << 20
)

// sessionInput queues the session's input for one writer goroutine, in
// order across its two sources: the stdin pump and the shadow's answers.
// Both write while holding a lock, so neither may wait for a session
// that stopped reading, or the output pump and the leave would stall.
type sessionInput struct {
	w io.Writer

	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	spare  []byte // the buffer of the last write, reused for the next queue
	busy   bool   // a write to the session is in flight
	failed bool   // the session's input is gone; drop everything
	closed bool
}

func newSessionInput(w io.Writer) *sessionInput {
	c := &sessionInput{w: w}
	c.cond = sync.NewCond(&c.mu)
	return c
}

// Write queues stdin's p and never blocks or drops it: the stdin pump
// bounds the queue by waiting for room before it reads more.
func (c *sessionInput) Write(p []byte) (int, error) {
	return c.queue(p, false)
}

// writeAnswers queues the shadow's answers and never blocks. Past
// sessionInputMax they are dropped.
func (c *sessionInput) writeAnswers(p []byte) {
	_, _ = c.queue(p, true)
}

func (c *sessionInput) queue(p []byte, bounded bool) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, io.ErrClosedPipe
	}
	if !c.failed && (!bounded || len(c.buf)+len(p) <= sessionInputMax) {
		c.buf = append(c.buf, p...)
		c.cond.Broadcast()
	}
	return len(p), nil
}

// run writes the queue to the session until close.
func (c *sessionInput) run() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		for len(c.buf) == 0 && !c.closed {
			c.cond.Wait()
		}
		if c.closed {
			return
		}
		p := c.buf
		c.buf, c.spare, c.busy = c.spare, nil, true
		c.mu.Unlock()
		_, err := c.w.Write(p)
		c.mu.Lock()
		c.busy = false
		if cap(p) <= sessionInputRoom {
			// Larger buffers only come from bursts; let them go.
			c.spare = p[:0]
		}
		if err != nil {
			c.failed = true
			c.buf = nil
		}
		c.cond.Broadcast()
	}
}

// hasRoom tells whether the queue holds less than limit.
func (c *sessionInput) hasRoom(limit int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.roomLocked(limit)
}

// waitRoom blocks until the queue holds less than limit or stop is closed.
// Callers must not hold a lock.
func (c *sessionInput) waitRoom(limit int, stop <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.roomLocked(limit) {
		return
	}
	// The cond can't wait on a channel: wake it when stop closes.
	waited := make(chan struct{})
	defer close(waited)
	go func() {
		select {
		case <-stop:
			c.mu.Lock()
			c.cond.Broadcast()
			c.mu.Unlock()
		case <-waited:
		}
	}()
	for !c.roomLocked(limit) && !isClosed(stop) {
		c.cond.Wait()
	}
}

// roomLocked tells whether the queue holds less than limit. A failed or
// closed queue always has room: it drops what it gets.
func (c *sessionInput) roomLocked(limit int) bool {
	return len(c.buf) < limit || c.failed || c.closed
}

// flush blocks until everything queued reached the session, so a
// half-close comes after the last key.
func (c *sessionInput) flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for (len(c.buf) > 0 || c.busy) && !c.failed && !c.closed {
		c.cond.Wait()
	}
}

// close drops what is queued and ends run once a write in flight returns.
func (c *sessionInput) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.buf = nil
	c.cond.Broadcast()
}
