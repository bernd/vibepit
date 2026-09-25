// Package container provides Docker/Podman API wrappers for vibepit.
//
// # TTY session handling
//
// The runTTYSession function implements interactive terminal forwarding
// between the host and a hijacked Docker connection. It is modelled after
// the Docker CLI's hijackedIOStreamer (cli/command/container/hijack.go).
// It forwards through an overlay.Terminal, which can show prompts over
// the session (see WithTerminal).
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
	"time"

	"github.com/bernd/vibepit/overlay"
	"github.com/docker/docker/api/types"
	"golang.org/x/term"
)

// WatchResizeSignals calls onResize for each signal on sigCh until done is
// closed.
func WatchResizeSignals(sigCh <-chan os.Signal, done <-chan struct{}, onResize func()) {
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
	logf       func(format string, args ...any)
}

// WithTerminal passes the session's terminal to fn before any data flows,
// so the caller can show prompts over the session. Without it the session
// runs without a shadow terminal and can't prompt.
func WithTerminal(fn func(*overlay.Terminal)) AttachOption {
	return func(o *attachOptions) { o.onTerminal = fn }
}

// WithLogf receives the session terminal's diagnostics. The session owns
// the screen, so they can't go to stderr.
func WithLogf(fn func(format string, args ...any)) AttachOption {
	return func(o *attachOptions) { o.logf = fn }
}

func buildAttachOptions(opts []AttachOption) attachOptions {
	var o attachOptions
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// newSessionTerminal connects a hijacked session to the local terminal.
func newSessionTerminal(resp types.HijackedResponse, stdin io.Reader, stdout io.Writer, size func() (int, int, error), resizeFn func(height, width uint), o attachOptions) *overlay.Terminal {
	return overlay.New(overlay.Config{
		Stdin:        stdin,
		Stdout:       stdout,
		ContainerIn:  resp.Conn,
		ContainerOut: resp.Reader,
		CloseInput:   resp.CloseWrite,
		Resize:       func(cols, rows int) { resizeFn(uint(rows), uint(cols)) },
		Size:         size,
		Logf:         o.logf,
		NoShadow:     o.onTerminal == nil,
	})
}

// runTTYSession puts the host terminal into raw mode, forwards stdio to and
// from the hijacked Docker connection through an overlay.Terminal, and
// handles SIGWINCH for terminal resizing. The resizeFn is called with
// (height, width) whenever the terminal changes size. The function blocks
// until the container-side stream ends, then returns any error.
func runTTYSession(ctx context.Context, resp types.HijackedResponse, resizeFn func(height, width uint), opts attachOptions) error {
	fd := int(os.Stdin.Fd())

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	defer term.Restore(fd, oldState)

	size := func() (int, int, error) { return term.GetSize(fd) }
	t := newSessionTerminal(resp, os.Stdin, os.Stdout, size, resizeFn, opts)
	if opts.onTerminal != nil {
		opts.onTerminal(t)
	}

	// Set initial terminal size with retry. The container/exec process may
	// not be ready to accept a resize immediately after attach.
	go func() {
		for attempt := range 5 {
			if _, _, err := size(); err == nil {
				t.Resize()
				return
			}
			time.Sleep(time.Duration(attempt+1) * 20 * time.Millisecond)
		}
	}()

	// Forward SIGWINCH to the container.
	sigCh := make(chan os.Signal, 1)
	NotifyResize(sigCh)
	defer signal.Stop(sigCh)
	// signal.Stop unregisters delivery but does not close sigCh.
	// done gives the resize watcher an explicit shutdown path.
	done := make(chan struct{})
	defer close(done)
	go WatchResizeSignals(sigCh, done, t.Resize)

	// Run returns when the output ends, so the deferred restore runs as
	// soon as the container is done, as before.
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
