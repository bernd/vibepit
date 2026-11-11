package overlay

import (
	"bytes"
	"io"
	"sync"
)

const (
	// sessionInputRoom is how much pending input makes the stdin pump wait
	// for the session: the backpressure a plain copy would have.
	sessionInputRoom = 64 << 10
	// sessionInputChunk is the most one write to the session takes, so
	// room comes back as a slow session reads, not only after a whole
	// burst. Half the room lets stdin be read while a chunk is written.
	sessionInputChunk = sessionInputRoom / 2
	// sessionInputMax caps the shadow's answers: past it they are dropped.
	// Stdin is never dropped. Its pump checks for room after each read,
	// so what is pending, the write in flight included, passes the limit
	// by about one read, plus a few bytes of replies and reports the input
	// side passes on outside that check. While the handoff waits for the
	// barrier reply the pump waits only at sessionInputMax.
	sessionInputMax = 4 << 20
)

// sessionInput queues the session's input for one writer goroutine, in
// order across its two sources: the stdin pump and the shadow's answers.
// Both write while holding a lock, so neither may wait for a session
// that stopped reading, or the output pump and the leave would stall.
type sessionInput struct {
	w io.Writer

	mu   sync.Mutex
	cond *sync.Cond
	q    bytes.Buffer
	// inflight is the size of the write to the session in flight. It
	// counts toward the room: a session stalled on a write must not let
	// stdin read past what the queue bounds.
	inflight int
	failed   bool // the session's input is gone; drop everything
	closed   bool
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
	if !c.failed && (!bounded || c.pendingLocked()+len(p) <= sessionInputMax) {
		c.q.Write(p)
		c.cond.Broadcast()
	}
	return len(p), nil
}

// run writes the queue to the session, at most sessionInputChunk at a
// time, until close.
func (c *sessionInput) run() {
	p := make([]byte, sessionInputChunk)
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		for c.q.Len() == 0 && !c.closed {
			c.cond.Wait()
		}
		if c.closed {
			return
		}
		n, _ := c.q.Read(p)
		if c.q.Len() == 0 && c.q.Cap() > sessionInputRoom {
			// Larger buffers only come from bursts; let them go.
			c.q = bytes.Buffer{}
		}
		c.inflight = n
		c.mu.Unlock()
		_, err := c.w.Write(p[:n])
		c.mu.Lock()
		c.inflight = 0
		if err != nil {
			c.failed = true
			c.q = bytes.Buffer{}
		}
		c.cond.Broadcast()
	}
}

// pendingLocked is what hasn't reached the session: the queue and the
// write in flight.
func (c *sessionInput) pendingLocked() int {
	return c.q.Len() + c.inflight
}

// hasRoom tells whether less than limit is pending.
func (c *sessionInput) hasRoom(limit int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.roomLocked(limit)
}

// waitRoom blocks until less than limit is pending or stop is closed.
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

// roomLocked tells whether less than limit is pending. A failed or
// closed queue always has room: it drops what it gets.
func (c *sessionInput) roomLocked(limit int) bool {
	return c.pendingLocked() < limit || c.failed || c.closed
}

// flush blocks until everything queued reached the session, so a
// half-close comes after the last key.
func (c *sessionInput) flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.pendingLocked() > 0 && !c.failed && !c.closed {
		c.cond.Wait()
	}
}

// close drops what is queued and ends run once a write in flight returns.
func (c *sessionInput) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.q = bytes.Buffer{}
	c.cond.Broadcast()
}
