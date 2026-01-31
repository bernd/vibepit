// Package container provides Docker/Podman API wrappers for vibepit.
//
// # TTY session handling
//
// The runTTYSession function implements interactive terminal forwarding
// between the host and a hijacked Docker connection. It is modelled after
// the Docker CLI's hijackedIOStreamer (cli/command/container/hijack.go).
//
// The following gaps relative to the Docker CLI are known and deferred:
//
//   - Detach key support: the Docker CLI wraps stdin in an EscapeProxy that
//     intercepts ctrl-p,ctrl-q (or a custom sequence) to cleanly detach from
//     a session without stopping the container. We currently have no detach
//     support — the user must exit the shell or kill the process.
//
//   - Signal forwarding: the Docker CLI forwards all signals (except SIGCHLD,
//     SIGPIPE, SIGURG) to the container via ContainerKill when sig-proxy is
//     enabled. In TTY mode the kernel's PTY layer handles most signals, so
//     this is less critical for our use case.
//
//   - stdcopy for non-TTY: when TTY is disabled, Docker multiplexes stdout
//     and stderr over a single connection with 8-byte frame headers. The CLI
//     uses stdcopy.StdCopy to demultiplex. We always use TTY mode, so this
//     is not currently needed, but would be required to support non-TTY.
package container

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/docker/docker/api/types"
	"golang.org/x/term"
)

// ExitError is returned when a container or exec process exits with a
// non-zero status code.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit status %d", e.Code)
}

// runTTYSession puts the host terminal into raw mode, forwards stdio to/from
// the hijacked Docker connection, and handles SIGWINCH for terminal resizing.
// The resizeFn is called with (height, width) whenever the terminal changes
// size. The function blocks until the container-side stream ends, then
// returns any error.
func runTTYSession(ctx context.Context, resp types.HijackedResponse, resizeFn func(height, width uint)) error {
	fd := int(os.Stdin.Fd())

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	// Use sync.Once so whichever goroutine finishes first restores the
	// terminal immediately, rather than waiting for defer on return.
	restoreOnce := sync.OnceFunc(func() {
		term.Restore(fd, oldState)
	})
	defer restoreOnce()

	// Set initial terminal size with retry. The container/exec process may
	// not be ready to accept a resize immediately after attach.
	go func() {
		for attempt := range 5 {
			if w, h, err := term.GetSize(fd); err == nil {
				resizeFn(uint(h), uint(w))
				return
			}
			time.Sleep(time.Duration(attempt+1) * 20 * time.Millisecond)
		}
	}()

	// Forward SIGWINCH to the container.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)
	go func() {
		for range sigCh {
			if w, h, err := term.GetSize(fd); err == nil {
				resizeFn(uint(h), uint(w))
			}
		}
	}()

	outputDone := make(chan error, 1)
	inputDone := make(chan error, 1)

	// Copy container output to stdout.
	go func() {
		_, err := io.Copy(os.Stdout, resp.Reader)
		restoreOnce()
		outputDone <- err
	}()

	// Copy stdin to the container.
	go func() {
		_, err := io.Copy(resp.Conn, os.Stdin)
		resp.CloseWrite()
		inputDone <- err
	}()

	select {
	case err := <-outputDone:
		return err
	case <-inputDone:
		// Stdin finished (e.g. Ctrl-D). Wait for output to drain or
		// context to cancel.
		select {
		case err := <-outputDone:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

// terminalSize returns the current terminal dimensions, or nil if
// they cannot be determined.
func terminalSize() *[2]uint {
	w, h, err := term.GetSize(int(os.Stdin.Fd()))
	if err != nil {
		return nil
	}
	return &[2]uint{uint(h), uint(w)}
}
