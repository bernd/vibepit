package overlay

import (
	"bytes"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// syncBuffer is a bytes.Buffer the pump goroutines can share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// chunkReader hands its reader one queued chunk per Read, so tests control
// read boundaries. push returns once the reader is back for the next
// chunk, which means it has handled this one.
type chunkReader struct {
	chunks  chan []byte
	next    chan struct{}
	closed  chan struct{}
	once    sync.Once
	pending []byte
	started bool
}

func newChunkReader() *chunkReader {
	return &chunkReader{
		chunks: make(chan []byte),
		next:   make(chan struct{}),
		closed: make(chan struct{}),
	}
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.pending) == 0 {
		if r.started {
			select {
			case r.next <- struct{}{}:
			case <-r.closed:
				return 0, io.EOF
			}
		}
		select {
		case c := <-r.chunks:
			r.pending, r.started = c, true
		case <-r.closed:
			return 0, io.EOF
		}
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

// push hands chunk to the reader and waits until it has handled it. It
// reports false after a timeout or once the reader is closed, and is safe
// from any goroutine.
func (r *chunkReader) push(chunk string) bool {
	select {
	case r.chunks <- []byte(chunk):
	case <-r.closed:
		return false
	case <-time.After(5 * time.Second):
		return false
	}
	select {
	case <-r.next:
		return true
	case <-r.closed:
		return false
	case <-time.After(5 * time.Second):
		return false
	}
}

// send is push for the test goroutine.
func (r *chunkReader) send(t *testing.T, chunk string) {
	t.Helper()
	require.True(t, r.push(chunk), "reader didn't handle %q", chunk)
}

// close makes the reader's next Read return io.EOF.
func (r *chunkReader) close() { r.once.Do(func() { close(r.closed) }) }

// readN reads exactly n bytes from r, failing after a timeout.
func readN(t *testing.T, r io.Reader, n int) string {
	t.Helper()
	buf := make([]byte, n)
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(r, buf)
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatalf("reading %d bytes timed out", n)
	}
	return string(buf)
}
