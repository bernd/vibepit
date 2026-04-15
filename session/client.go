package session

import (
	"io"
	"sync"
)

// Client represents a connection to a session. It implements
// io.ReadWriteCloser. Reads return PTY output; writes send PTY input (only
// if this client is the session's writer).
type Client struct {
	session   *Session
	output    chan []byte
	done      chan struct{}
	pending   []byte // unread remainder from the last channel receive
	closeOnce sync.Once

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

// Close detaches the client from the session.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
		c.session.Detach(c)
	})
	return nil
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
	c.Close() //nolint:errcheck
}

func (c *Client) closeOutput() {
	c.outputMu.Lock()
	defer c.outputMu.Unlock()
	if !c.outputClosed {
		c.outputClosed = true
		close(c.output)
	}
}
