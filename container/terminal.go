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
	"os"
	"os/signal"
	"time"

	"github.com/bernd/vibepit/overlay"
	"github.com/docker/docker/api/types"
	"golang.org/x/term"
)

func watchResizeSignals(sigCh <-chan os.Signal, done <-chan struct{}, onResize func()) {
	for {
		select {
		case <-done:
			return
		case <-sigCh:
			onResize()
		}
	}
}

// ExitError is returned when a container or exec process exits with a
// non-zero status code.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit status %d", e.Code)
}

// AttachOption configures an interactive session started by
// AttachAndStartSession or ExecSession.
type AttachOption func(*attachOptions)

type attachOptions struct {
	onTerminal func(*overlay.Terminal)
}

// WithTerminal passes the session's overlay terminal to fn before any data
// flows, so the caller can show prompts over the session. The terminal is
// closed when the session ends.
func WithTerminal(fn func(*overlay.Terminal)) AttachOption {
	return func(o *attachOptions) { o.onTerminal = fn }
}

func buildAttachOptions(opts []AttachOption) attachOptions {
	var o attachOptions
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// runTTYSession puts the host terminal into raw mode, forwards stdio to/from
// the hijacked Docker connection, and handles SIGWINCH for terminal resizing.
// The resizeFn is called with (height, width) whenever the terminal changes
// size. The function blocks until the container-side stream ends, then
// returns any error.
func runTTYSession(ctx context.Context, resp types.HijackedResponse, resizeFn func(height, width uint), opts attachOptions) error {
	fd := int(os.Stdin.Fd())

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	defer term.Restore(fd, oldState)

	size := func() (rows, cols int, err error) {
		w, h, err := term.GetSize(fd)
		return h, w, err
	}

	// Set initial terminal size with retry. The container/exec process may
	// not be ready to accept a resize immediately after attach.
	go func() {
		for attempt := range 5 {
			if rows, cols, err := size(); err == nil {
				resizeFn(uint(rows), uint(cols))
				return
			}
			time.Sleep(time.Duration(attempt+1) * 20 * time.Millisecond)
		}
	}()

	// Forward SIGWINCH to the container.
	sigCh := make(chan os.Signal, 1)
	notifyResize(sigCh)
	defer signal.Stop(sigCh)
	// signal.Stop unregisters delivery but does not close sigCh.
	// done gives the resize watcher an explicit shutdown path.
	done := make(chan struct{})
	defer close(done)
	go func() {
		watchResizeSignals(sigCh, done, func() {
			if rows, cols, err := size(); err == nil {
				resizeFn(uint(rows), uint(cols))
			}
		})
	}()

	t := overlay.New(overlay.Config{
		Stdin:        os.Stdin,
		Stdout:       os.Stdout,
		ContainerIn:  resp.Conn,
		ContainerOut: resp.Reader,
		CloseInput:   resp.CloseWrite,
		Resize: func(rows, cols int) {
			resizeFn(uint(rows), uint(cols))
		},
		Size: size,
	})
	if opts.onTerminal != nil {
		opts.onTerminal(t)
	}
	return t.Run(ctx)
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
