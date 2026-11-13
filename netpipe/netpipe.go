// Package netpipe joins two byte streams and runs TCP accept loops. It is
// shared by the proxy's SSH forwarder, vibed's direct-tcpip handler and the
// client side of "connect -L", which all relay connections the same way.
package netpipe

import (
	"errors"
	"io"
	"log"
	"net"
	"sync"
	"syscall"
	"time"
)

// closeWriter is implemented by *net.TCPConn, *net.UnixConn, ssh.Channel
// and the net.Conn returned by ssh.Client.Dial.
type closeWriter interface {
	CloseWrite() error
}

// Join copies a to b and b to a until both directions are done, then closes
// both. A clean EOF in one direction is passed on as a half-close, so
// request/response protocols that end a request with EOF still get their
// response. A copy error, a panic, or a side that cannot half-close closes
// both streams at once instead: the other direction would otherwise wait on
// a peer that may never write again.
func Join(a, b io.ReadWriteCloser) {
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			a.Close() //nolint:errcheck
			b.Close() //nolint:errcheck
		})
	}
	defer closeBoth()

	done := make(chan struct{})
	go func() {
		defer close(done)
		pipe(b, a, closeBoth)
	}()
	pipe(a, b, closeBoth)
	<-done
}

func pipe(dst io.WriteCloser, src io.Reader, closeBoth func()) {
	defer func() {
		// One relayed connection must not take the whole process (and,
		// in vibed, every shell session) down with it.
		if r := recover(); r != nil {
			log.Printf("netpipe: panic while relaying: %v", r)
			closeBoth()
		}
	}()
	if _, err := io.Copy(dst, src); err != nil {
		closeBoth()
		return
	}
	cw, ok := dst.(closeWriter)
	if !ok || cw.CloseWrite() != nil {
		closeBoth()
	}
}

// Accept backoff bounds, as in net/http.Server.
const (
	minAcceptDelay = 5 * time.Millisecond
	maxAcceptDelay = time.Second
)

// AcceptLoop accepts connections on l and runs handle for each in its own
// goroutine. It returns nil once l is closed. Running out of file
// descriptors or buffers, or a connection aborted before Accept returned,
// does not break the listener, so those are retried with a capped
// exponential backoff instead of giving up or spinning. Any other error is
// returned.
func AcceptLoop(l net.Listener, handle func(net.Conn)) error {
	return acceptLoop(l, handle, time.Sleep)
}

func acceptLoop(l net.Listener, handle func(net.Conn), sleep func(time.Duration)) error {
	var delay time.Duration
	for {
		conn, err := l.Accept()
		switch {
		case err == nil:
			delay = 0
			go handle(conn)
		case errors.Is(err, net.ErrClosed):
			return nil
		case retryableAcceptError(err):
			delay = min(max(2*delay, minAcceptDelay), maxAcceptDelay)
			sleep(delay)
		default:
			return err
		}
	}
}

// retryableAcceptErrnos lists the errors net/http.Server retries as
// "temporary", spelled out because net.Error.Temporary is deprecated.
var retryableAcceptErrnos = []syscall.Errno{
	syscall.EMFILE, syscall.ENFILE, syscall.ENOBUFS, syscall.ENOMEM, syscall.ECONNABORTED, syscall.ECONNRESET,
}

func retryableAcceptError(err error) bool {
	for _, errno := range retryableAcceptErrnos {
		if errors.Is(err, errno) {
			return true
		}
	}
	return false
}
