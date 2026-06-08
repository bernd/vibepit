package session

import (
	"io"
	"sync"
	"sync/atomic"
)

// DetachReason explains why a client stopped being attached to its session.
// It lets the SSH handler tell a genuine shell exit apart from a forced
// detach: a genuine exit closes the session's output channel (leaving the
// reason DetachNone), whereas any Close-with-reason marks why the client was
// dropped. The handler reports a clean exit status only for DetachNone, so a
// client that merely lost its connection is never told the shell exited.
type DetachReason int

const (
	// DetachNone is the zero value: the client was not closed with a reason.
	// When a client's Read ends while still DetachNone, the session's output
	// channel was closed because the shell process exited.
	DetachNone DetachReason = iota
	// DetachDisconnect: the SSH connection went away (channel/context closed).
	DetachDisconnect
	// DetachKeepalive: the server's keepalive declared the client dead, e.g.
	// after a laptop suspend/resume stalled the connection past the timeout.
	DetachKeepalive
	// DetachSlowConsumer: the client could not keep up with PTY output and was
	// dropped to protect the pump.
	DetachSlowConsumer
)

// Label returns a short description of an abnormal detach reason worth
// surfacing in UIs, or "" for reasons that aren't — a normal disconnect (the
// expected case) or none. The empty string is the signal to omit the
// annotation entirely.
func (r DetachReason) Label() string {
	switch r {
	case DetachKeepalive:
		return "connection lost"
	case DetachSlowConsumer:
		return "dropped - slow"
	default:
		return ""
	}
}

// Client represents a connection to a session. It implements
// io.ReadWriteCloser. Reads return PTY output; writes send PTY input (only
// if this client is the session's writer).
type Client struct {
	session   *Session
	output    chan []byte
	done      chan struct{}
	pending   []byte // unread remainder from the last channel receive
	closeOnce sync.Once
	// detachReason is written once inside closeOnce. The reader (the SSH
	// handler) may observe EOF via the output channel closing rather than
	// done — e.g. a shell exit racing a keepalive close — so the read is not
	// ordered after close(done). An atomic makes that read race-free.
	detachReason atomic.Int32

	// outputMu guards output channel send/close to prevent send-on-closed
	// races between deliver (replay path in Attach) and closeOutput (pump
	// error path). The mutex is only contended at session exit time.
	outputMu     sync.Mutex
	outputClosed bool
}

func newClient(s *Session) *Client {
	return &Client{
		session: s,
		output:  make(chan []byte, 1024),
		done:    make(chan struct{}),
	}
}

// Read returns the next chunk of PTY output. It blocks until data is
// available or the client is closed. Supports partial reads — if the
// caller's buffer is smaller than the available data, the remainder is
// preserved for the next Read call.
//
// Read is not safe for concurrent use. It assumes a single reader
// goroutine (typically io.Copy in the SSH handler).
func (c *Client) Read(p []byte) (int, error) {
	// Serve remaining bytes from a previous partial read.
	if len(c.pending) > 0 {
		n := copy(p, c.pending)
		c.pending = c.pending[n:]
		return n, nil
	}

	select {
	case data, ok := <-c.output:
		if !ok {
			return 0, io.EOF
		}
		n := copy(p, data)
		if n < len(data) {
			c.pending = data[n:]
		}
		return n, nil
	case <-c.done:
		return 0, io.EOF
	}
}

// Write sends input to the session's PTY. Returns an error if this client
// is not the writer.
func (c *Client) Write(p []byte) (int, error) {
	return c.session.WriteInput(c, p)
}

// Close detaches the client from the session, recording the detach as an
// ordinary connection teardown.
func (c *Client) Close() error {
	return c.CloseWithReason(DetachDisconnect)
}

// CloseWithReason detaches the client and records why. Only the first call
// takes effect (Close is once), so the reason reflects the cause that won the
// race to close. The reason is written before c.done is closed, so any reader
// that observes the done/output close (and reads DetachReason afterward) sees
// it without a data race.
func (c *Client) CloseWithReason(reason DetachReason) error {
	c.closeOnce.Do(func() {
		c.detachReason.Store(int32(reason))
		close(c.done)
		c.session.Detach(c)
	})
	return nil
}

// DetachReason reports why the client detached. It is meaningful only after
// the client has closed; callers read it after observing EOF from Read.
func (c *Client) DetachReason() DetachReason {
	return DetachReason(c.detachReason.Load())
}

// deliver sends data to the client's output channel. If the channel is full
// (slow consumer), the client is disconnected to prevent stalling the pump
// and blocking all other attached clients.
func (c *Client) deliver(data []byte) {
	c.outputMu.Lock()
	if c.outputClosed {
		c.outputMu.Unlock()
		return
	}
	select {
	case c.output <- data:
		c.outputMu.Unlock()
		return
	case <-c.done:
		c.outputMu.Unlock()
		return
	default:
	}
	c.outputMu.Unlock()
	// Channel full — slow consumer. Disconnect to protect the pump.
	c.CloseWithReason(DetachSlowConsumer) //nolint:errcheck
}

func (c *Client) closeOutput() {
	c.outputMu.Lock()
	defer c.outputMu.Unlock()
	if !c.outputClosed {
		c.outputClosed = true
		close(c.output)
	}
}
