package netpipe

import (
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tcpPair returns the two ends of a loopback TCP connection.
func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close() //nolint:errcheck
	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := l.Accept()
		accepted <- c
	}()
	dialed, err := net.Dial("tcp", l.Addr().String())
	require.NoError(t, err)
	server := <-accepted
	require.NotNil(t, server)
	t.Cleanup(func() {
		dialed.Close() //nolint:errcheck
		server.Close() //nolint:errcheck
	})
	return dialed, server
}

// relay sets up client <-> [a Join b] <-> service over TCP and returns the
// outer ends plus a channel closed when Join returns.
func relay(t *testing.T) (client, service net.Conn, joined <-chan struct{}) {
	t.Helper()
	client, a := tcpPair(t)
	b, service := tcpPair(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		Join(a, b)
	}()
	return client, service, done
}

func waitClosed(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("Join did not return")
	}
}

func TestJoinHalfCloseKeepsResponse(t *testing.T) {
	client, service, joined := relay(t)

	_, err := client.Write([]byte("request"))
	require.NoError(t, err)
	require.NoError(t, client.(*net.TCPConn).CloseWrite())

	// The service sees the request and the client's EOF...
	got, err := io.ReadAll(service)
	require.NoError(t, err)
	assert.Equal(t, "request", string(got))

	// ...and can still answer after it.
	_, err = service.Write([]byte("response"))
	require.NoError(t, err)
	require.NoError(t, service.Close())

	got, err = io.ReadAll(client)
	require.NoError(t, err)
	assert.Equal(t, "response", string(got))
	waitClosed(t, joined)
}

func TestJoinResetClosesBoth(t *testing.T) {
	client, service, joined := relay(t)

	// An RST instead of a FIN is a copy error, not EOF.
	require.NoError(t, client.(*net.TCPConn).SetLinger(0))
	require.NoError(t, client.Close())

	// The service never writes; it must still see its side closed.
	service.SetReadDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck
	_, err := io.ReadAll(service)
	var netErr net.Error
	if errors.As(err, &netErr) {
		require.False(t, netErr.Timeout(), "service side was not closed")
	}
	waitClosed(t, joined)
}

// plainRWC is a ReadWriteCloser without CloseWrite.
type plainRWC struct {
	io.Reader
	io.Writer
	closed chan struct{}
	once   sync.Once
}

func (p *plainRWC) Close() error {
	p.once.Do(func() { close(p.closed) })
	return nil
}

func TestJoinWithoutCloseWriteClosesBoth(t *testing.T) {
	client, a := tcpPair(t)
	// b cannot be told about the client's EOF, and its own reads block
	// until it is closed, so Join must close both instead of waiting.
	b := &plainRWC{Writer: io.Discard, closed: make(chan struct{})}
	b.Reader = blockUntilClosed{b.closed}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Join(a, b)
	}()

	require.NoError(t, client.(*net.TCPConn).CloseWrite())
	waitClosed(t, done)
	waitClosed(t, b.closed)
	client.SetReadDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck
	_, err := io.ReadAll(client)
	assert.NoError(t, err, "client should see EOF")
}

type blockUntilClosed struct{ closed <-chan struct{} }

func (r blockUntilClosed) Read([]byte) (int, error) {
	<-r.closed
	return 0, io.EOF
}

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("boom") }

func TestJoinRecoversPanic(t *testing.T) {
	_, a := tcpPair(t)
	b := &plainRWC{Reader: panicReader{}, Writer: io.Discard, closed: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Join(a, b)
	}()
	waitClosed(t, done)
	waitClosed(t, b.closed)
}

// flakyListener fails Accept with err a set number of times, then reports
// closed.
type flakyListener struct {
	net.Listener
	err      error
	failures int
}

func (l *flakyListener) Accept() (net.Conn, error) {
	if l.failures > 0 {
		l.failures--
		return nil, l.err
	}
	return nil, net.ErrClosed
}

func TestAcceptLoopBacksOff(t *testing.T) {
	// Wrapped the way net.TCPListener reports it.
	emfile := &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept", syscall.EMFILE)}
	var delays []time.Duration
	err := acceptLoop(&flakyListener{err: emfile, failures: 10}, func(net.Conn) {}, func(d time.Duration) {
		delays = append(delays, d)
	})
	require.NoError(t, err)
	assert.Equal(t, []time.Duration{
		5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond,
		40 * time.Millisecond, 80 * time.Millisecond, 160 * time.Millisecond,
		320 * time.Millisecond, 640 * time.Millisecond, time.Second, time.Second,
	}, delays)
}

func TestAcceptLoopReturnsPermanentError(t *testing.T) {
	permanent := errors.New("listener broken")
	err := acceptLoop(&flakyListener{err: permanent, failures: 1}, func(net.Conn) {}, func(time.Duration) {
		t.Fatal("permanent error must not be retried")
	})
	assert.ErrorIs(t, err, permanent)
}

func TestAcceptLoopServesUntilClosed(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	handled := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- AcceptLoop(l, func(c net.Conn) {
			c.Close() //nolint:errcheck
			handled <- struct{}{}
		})
	}()

	c, err := net.Dial("tcp", l.Addr().String())
	require.NoError(t, err)
	c.Close() //nolint:errcheck
	<-handled

	require.NoError(t, l.Close())
	assert.NoError(t, <-done)
}
