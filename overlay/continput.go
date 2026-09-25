package overlay

import (
	"io"
	"sync"
)

const (
	// containerInputRoom is how much queued input makes the stdin pump wait
	// for the container: the backpressure a plain copy would have.
	containerInputRoom = 64 << 10
	// containerInputMax bounds the queue. Stdin waits long before it, so
	// only the shadow's answers to an app that stopped reading reach it.
	containerInputMax = 4 << 20
)

// containerInput queues the container's input for one writer goroutine, in
// order across its two sources: the stdin pump and the shadow's answers.
// Both write while holding a lock, so neither may wait for a container
// that stopped reading, or the output pump and the leave would stall.
type containerInput struct {
	w io.Writer

	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	busy   bool // a write to the container is in flight
	failed bool // the container's input is gone; drop everything
	closed bool
}

func newContainerInput(w io.Writer) *containerInput {
	c := &containerInput{w: w}
	c.cond = sync.NewCond(&c.mu)
	return c
}

// Write queues p and never blocks. Past containerInputMax, p is dropped.
func (c *containerInput) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, io.ErrClosedPipe
	}
	if !c.failed && len(c.buf)+len(p) <= containerInputMax {
		c.buf = append(c.buf, p...)
		c.cond.Broadcast()
	}
	return len(p), nil
}

// run writes the queue to the container until close.
func (c *containerInput) run() {
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
		c.buf, c.busy = nil, true
		c.mu.Unlock()
		_, err := c.w.Write(p)
		c.mu.Lock()
		c.busy = false
		if err != nil {
			c.failed = true
			c.buf = nil
		}
		c.cond.Broadcast()
	}
}

// waitRoom blocks while the queue holds containerInputRoom or more. Callers
// must not hold a lock.
func (c *containerInput) waitRoom() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.buf) >= containerInputRoom && !c.failed && !c.closed {
		c.cond.Wait()
	}
}

// flush blocks until everything queued reached the container, so a
// half-close comes after the last key.
func (c *containerInput) flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for (len(c.buf) > 0 || c.busy) && !c.failed && !c.closed {
		c.cond.Wait()
	}
}

// close drops what is queued and ends run once a write in flight returns.
func (c *containerInput) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.buf = nil
	c.cond.Broadcast()
}
