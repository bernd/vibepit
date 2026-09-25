package overlay

import (
	"io"
	"sync"
)

// bufPipe is an in-memory pipe whose Write never blocks, so the stdin pump
// can hand bytes to a prompt that isn't reading yet without stalling.
// Writes past max buffered bytes are dropped.
type bufPipe struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	max    int
	closed bool
}

func newBufPipe(max int) *bufPipe {
	p := &bufPipe{max: max}
	p.cond = sync.NewCond(&p.mu)
	return p
}

// Write buffers what fits and reports the whole of b as written. It fails
// only after Close.
func (p *bufPipe) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, io.ErrClosedPipe
	}
	if room := p.max - len(p.buf); room > 0 {
		p.buf = append(p.buf, b[:min(len(b), room)]...)
		p.cond.Broadcast()
	}
	return len(b), nil
}

// Read blocks until data is buffered or the pipe is closed. After Close it
// returns what is left, then io.EOF.
func (p *bufPipe) Read(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.buf) == 0 && !p.closed {
		p.cond.Wait()
	}
	if len(p.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(b, p.buf)
	p.buf = p.buf[n:]
	return n, nil
}

func (p *bufPipe) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.cond.Broadcast()
	return nil
}
