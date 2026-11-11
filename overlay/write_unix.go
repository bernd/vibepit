//go:build unix

package overlay

import (
	"errors"
	"io"
	"syscall"

	"golang.org/x/sys/unix"
)

// blockingWriter writes all of p to a file that may be non-blocking. Go
// waits out EAGAIN only for files that were non-blocking when it took
// them. When another process on the same terminal switches stdout to
// non-blocking later, Go returns EAGAIN instead, and the real terminal
// would miss bytes the shadow has. Waiting passes the terminal's
// backpressure on to the session, like a blocking write.
type blockingWriter struct {
	w  io.Writer
	rc syscall.RawConn
}

// newBlockingWriter wraps w when it is a file.
func newBlockingWriter(w io.Writer) io.Writer {
	sc, ok := w.(syscall.Conn)
	if !ok {
		return w
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		return w
	}
	return &blockingWriter{w: w, rc: rc}
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	var n int
	for {
		m, err := b.w.Write(p[n:])
		n += m
		if !errors.Is(err, syscall.EAGAIN) {
			return n, err
		}
		if err := b.waitWritable(); err != nil {
			return n, err
		}
	}
}

// waitWritable blocks until the file takes more bytes, or reports an
// error or hangup, which the next write returns.
func (b *blockingWriter) waitWritable() error {
	var perr error
	err := b.rc.Control(func(fd uintptr) {
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
		for {
			if _, perr = unix.Poll(fds, -1); !errors.Is(perr, unix.EINTR) {
				return
			}
		}
	})
	if err != nil {
		return err
	}
	return perr
}
