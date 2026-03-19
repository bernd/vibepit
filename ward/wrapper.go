package ward

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
)

// Options configures the PTY wrapper.
type Options struct {
	Command    []string
	SocketPath string
	Hotkey     byte // default 0x1D = Ctrl+]
}

// Wrapper is the core PTY wrapper that runs a child process in a pseudo-terminal,
// manages toast notifications, and handles terminal resizing.
type Wrapper struct {
	opts Options
}

// NewWrapper creates a new Wrapper with the given options.
func NewWrapper(opts Options) *Wrapper {
	if opts.Hotkey == 0 {
		opts.Hotkey = 0x1D // Ctrl+]
	}
	return &Wrapper{opts: opts}
}

// scrollRegionSeq returns the DEC-save-wrapped scroll region escape for 1..maxRow.
func scrollRegionSeq(maxRow int) string {
	return fmt.Sprintf("\x1b7\x1b[1;%dr\x1b8", maxRow)
}

// setScrollRegionAndPTY sets the real terminal's scroll region to 1..(rows-1),
// sizes the PTY to (cols x rows-1), and resizes the screen emulator.
// Must be called with outputMu held.
func setScrollRegionAndPTY(ptmx *os.File, screen *Screen, cols, rows int) {
	os.Stdout.WriteString(scrollRegionSeq(rows - 1)) //nolint:errcheck
	_ = pty.Setsize(ptmx, &pty.Winsize{
		Rows: uint16(rows - 1),
		Cols: uint16(cols),
	})
	screen.Resize(cols, rows-1)
}

// Run starts the child process in a PTY and manages I/O until it exits.
// Returns the child's exit code and any error.
func (w *Wrapper) Run(ctx context.Context) (int, error) {
	// Determine socket path before starting child
	sockPath := w.opts.SocketPath
	if sockPath == "" {
		sockPath = SocketPath(os.Getpid())
	}

	// Create the command
	cmd := exec.CommandContext(ctx, w.opts.Command[0], w.opts.Command[1:]...)
	cmd.Env = append(os.Environ(), "WARD_SOCKET="+sockPath)

	// Get terminal size (default 80x24 if not a terminal).
	// Check both stdin and stdout — they may differ (e.g., piped stdin).
	cols, rows := 80, 24
	stdinFd := os.Stdin.Fd()
	stdoutFd := os.Stdout.Fd()
	stdinIsTTY := term.IsTerminal(stdinFd)
	stdoutIsTTY := term.IsTerminal(stdoutFd)
	isTTY := stdinIsTTY && stdoutIsTTY
	if stdoutIsTTY {
		if w, h, err := term.GetSize(stdoutFd); err == nil {
			cols, rows = w, h
		}
	} else if stdinIsTTY {
		if w, h, err := term.GetSize(stdinFd); err == nil {
			cols, rows = w, h
		}
	}

	// Create virtual screen — sized to rows-1 (last row reserved for bar)
	screen := NewScreen(cols, rows-1)

	// Start the command in a PTY
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return 0, err
	}
	defer ptmx.Close() //nolint:errcheck

	// Reserve the last row for the notification bar.
	// Only emit terminal escapes when stdout is a TTY.
	_ = pty.Setsize(ptmx, &pty.Winsize{
		Rows: uint16(rows - 1),
		Cols: uint16(cols),
	})
	if stdoutIsTTY {
		// Scroll up first to ensure the cursor ends up inside the scroll
		// region even if it was on the very last row when ward started.
		fmt.Fprintf(os.Stdout, "\x1b7\x1b[1S\x1b[1;%dr\x1b8\x1b[A", rows-1) //nolint:errcheck
	}

	// Toast notification channel and shutdown signal
	toastCh := make(chan Notification, 64)
	toastDone := make(chan struct{})

	// Start notification socket listener.
	// The callback uses a select on toastDone to avoid sending after shutdown.
	var sl *SocketListener
	sl, err = ListenSocket(sockPath, func(n Notification) {
		select {
		case <-toastDone:
			// shutting down, discard
		case toastCh <- n:
		default:
			// channel full, discard
		}
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ward: socket: %v\n", err)
	}

	// Enter raw mode if stdin is a terminal
	if isTTY {
		oldState, err := term.MakeRaw(stdinFd)
		if err == nil {
			defer term.Restore(stdinFd, oldState) //nolint:errcheck
		}
	}

	// Mutex protecting: stdout writes, screen access, bar state, term dimensions
	var outputMu sync.Mutex
	barVisible := false
	barRenderedMsg := "" // current bar message text
	barRendered := ""    // cached rendered bar output (invalidated on new message or resize)
	barScrollSeq := ""   // cached scroll region escape (invalidated on resize)
	termRows := rows
	termCols := cols

	// updateBarCache rebuilds the cached bar render and scroll region sequence.
	// Must be called with outputMu held.
	updateBarCache := func(message string) {
		bar := RenderBar(message, termCols)
		barRendered = fmt.Sprintf("\x1b7\x1b[%d;1H%s\x1b8", termRows, bar)
		barScrollSeq = scrollRegionSeq(termRows - 1)
	}

	// Handle SIGWINCH
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)

	go func() {
		for range sigCh {
			if isTTY {
				if w, h, err := term.GetSize(stdoutFd); err == nil {
					outputMu.Lock()
					termCols, termRows = w, h
					setScrollRegionAndPTY(ptmx, screen, termCols, termRows)
					if barVisible {
						updateBarCache(barRenderedMsg)
						os.Stdout.WriteString(barRendered) //nolint:errcheck
					}
					outputMu.Unlock()
				}
			}
		}
	}()

	// Restore full scroll region on exit
	defer func() {
		if stdoutIsTTY {
			outputMu.Lock()
			os.Stdout.WriteString(scrollRegionSeq(termRows))               //nolint:errcheck
			fmt.Fprintf(os.Stdout, "\x1b7\x1b[%d;1H\x1b[K\x1b8", termRows) //nolint:errcheck
			outputMu.Unlock()
		}
	}()

	// Toast dismiss timer
	var toastTimer *time.Timer

	// Toast receiver goroutine — only renders when stdout is a TTY
	go func() {
		for {
			select {
			case <-toastDone:
				return
			case n := <-toastCh:
				if !stdoutIsTTY {
					continue
				}
				outputMu.Lock()

				if toastTimer != nil {
					toastTimer.Stop()
				}

				barRenderedMsg = n.Message
				barVisible = true
				updateBarCache(n.Message)
				os.Stdout.WriteString(barRendered) //nolint:errcheck

				timeout := n.Timeout
				toastTimer = time.AfterFunc(timeout, func() {
					outputMu.Lock()
					if barVisible {
						barVisible = false
						barRenderedMsg = ""
						barRendered = ""
						fmt.Fprintf(os.Stdout, "\x1b7\x1b[%d;1H\x1b[K\x1b8", termRows) //nolint:errcheck
					}
					outputMu.Unlock()
				})

				outputMu.Unlock()
			}
		}
	}()

	// PTY -> stdout goroutine (tracked by WaitGroup)
	var outputDone sync.WaitGroup
	outputDone.Go(func() {
		buf := make([]byte, 32*1024)

		// Minimal escape state tracker to avoid injecting scroll region
		// re-apply in the middle of an ANSI sequence (which corrupts it).
		const (
			esGround    = iota
			esEsc       // saw ESC
			esCsi       // ESC [ — CSI, waiting for terminator 0x40-0x7E
			esString    // ESC ] (OSC), ESC P (DCS), ESC ^ (PM), ESC _ (APC)
			esStringEsc // inside string sequence, saw ESC — next byte decides
		)
		escState := esGround

		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				data := buf[:n]

				outputMu.Lock()

				// Only scan escape state when bar is visible (the only
				// time we inject sequences between reads).
				if barVisible {
					for _, b := range data {
						switch escState {
						case esGround:
							if b == 0x1B {
								escState = esEsc
							}
						case esEsc:
							switch b {
							case '[':
								escState = esCsi
							case ']', 'P', '^', '_': // OSC, DCS, PM, APC
								escState = esString
							default:
								escState = esGround
							}
						case esCsi:
							if b >= 0x40 && b <= 0x7E {
								escState = esGround
							}
						case esString:
							switch b {
							case 0x07:
								escState = esGround
							case 0x1B:
								escState = esStringEsc
							}
						case esStringEsc:
							escState = esGround
						}
					}
				}

				screen.Write(data)    //nolint:errcheck
				os.Stdout.Write(data) //nolint:errcheck

				// Re-apply scroll region and bar when visible and
				// the stream is not mid-escape-sequence.
				if barVisible && escState == esGround {
					os.Stdout.WriteString(barScrollSeq) //nolint:errcheck
					os.Stdout.WriteString(barRendered)  //nolint:errcheck
				}

				outputMu.Unlock()
			}
			if err != nil {
				break
			}
		}
	})

	// stdin -> PTY goroutine.
	// Not tracked by WaitGroup because os.Stdin.Read cannot be unblocked
	// portably. The goroutine exits on the next read after ptmx is closed
	// (the write will fail) or when stdin itself returns an error/EOF.
	// For a one-shot CLI this is cleaned up by process exit.
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				// TODO: hotkey interception
				if _, werr := ptmx.Write(buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
	}()

	// Wait for command to finish
	cmdErr := cmd.Wait()

	// Wait for PTY output to drain
	outputDone.Wait()

	// Signal toast system shutdown. The done channel stops both the receiver
	// goroutine and prevents the socket callback from sending to toastCh.
	// Stop any active dismiss timer to prevent post-teardown terminal writes.
	close(toastDone)
	outputMu.Lock()
	if toastTimer != nil {
		toastTimer.Stop()
	}
	barVisible = false
	outputMu.Unlock()
	if sl != nil {
		sl.Close() //nolint:errcheck
	}

	// Determine exit code
	if cmdErr != nil {
		if exitErr, ok := cmdErr.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return 0, cmdErr
	}
	return 0, nil
}
