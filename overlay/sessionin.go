package overlay

import (
	"io"
	"sync"
)

const (
	// sessionInputRoom is how much queued input makes the stdin pump wait
	// for the session: the backpressure a plain copy would have.
	sessionInputRoom = 64 << 10
	// sessionInputMax bounds the queue. Stdin waits long before it, so
	// only the shadow's answers to an app that stopped reading reach it.
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

// Write queues p and never blocks. Past sessionInputMax, p is dropped.
func (c *sessionInput) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, io.ErrClosedPipe
	}
	if !c.failed && len(c.buf)+len(p) <= sessionInputMax {
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

// waitRoom blocks while the queue holds sessionInputRoom or more. Callers
// must not hold a lock.
func (c *sessionInput) waitRoom() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.buf) >= sessionInputRoom && !c.failed && !c.closed {
		c.cond.Wait()
	}
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
