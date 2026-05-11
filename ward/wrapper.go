package ward

import (
	"context"
	"errors"
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
	Command []string
	Hotkey  byte                // default 0x1D = Ctrl+]
	Env     []string            // extra KEY=VALUE pairs for the child process
	Status  <-chan StatusUpdate // nil-safe; bar stays hidden until first event
	OnKey   func(ctx context.Context, key byte, target string) (string, error)
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

// scrollRegionSeq returns the DEC-save-wrapped scroll region escape for
// rows 1..maxRow.
//
// The sequence is:
//   - ESC 7: save the current cursor position and attributes.
//   - CSI 1;<maxRow> r: set the DEC scrolling region (DECSTBM) to all rows
//     except the row reserved for ward's bar.
//   - ESC 8: restore the saved cursor position and attributes.
//
// Saving/restoring keeps this maintenance sequence from visibly moving the
// user's cursor while ward repairs or resizes the protected region.
func scrollRegionSeq(maxRow int) string {
	return fmt.Sprintf("\x1b7\x1b[1;%dr\x1b8", maxRow)
}

// resizeScrollRegionSeq returns the scroll-region repair used after a
// terminal resize. VTE can leave the real cursor on the newly protected
// bottom row while a command is streaming output; restoring that cursor
// outside the scroll region makes later output collapse onto the bar row.
// Moving up one row after restore mirrors the startup repair and keeps
// the cursor inside rows 1..maxRow.
func resizeScrollRegionSeq(maxRow int) string {
	return fmt.Sprintf("\x1b7\x1b[1;%dr\x1b8\x1b[A", maxRow)
}

const activeOutputResizeWindow = 500 * time.Millisecond

func resizeRepairSeq(maxRow int, lastOutputAt time.Time, now time.Time) string {
	if !lastOutputAt.IsZero() && now.Sub(lastOutputAt) < activeOutputResizeWindow {
		return resizeScrollRegionSeq(maxRow)
	}
	return scrollRegionSeq(maxRow)
}

// Run starts the child process in a PTY and manages I/O until it exits.
// Returns the child's exit code and any error.
func (w *Wrapper) Run(ctx context.Context) (int, error) {
	cmd := exec.CommandContext(ctx, w.opts.Command[0], w.opts.Command[1:]...)
	cmd.Env = append(append([]string{}, os.Environ()...), w.opts.Env...)

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
		//
		// The sequence is intentionally emitted as one write:
		//   - ESC 7: save cursor/attributes.
		//   - CSI 1 S: scroll the full screen up one line. This makes room
		//     before the bottom row becomes protected.
		//   - CSI 1;<rows-1> r: reserve rows 1..rows-1 as the scroll region.
		//   - ESC 8: restore cursor/attributes.
		//   - CSI A: move the restored cursor up one row so it is inside the
		//     newly constrained scroll region.
		fmt.Fprintf(os.Stdout, "\x1b7\x1b[1S\x1b[1;%dr\x1b8\x1b[A", rows-1) //nolint:errcheck
	}

	// Internal event channel and done signal
	eventCh := make(chan barEvent, 64)
	done := make(chan struct{})

	// Enter raw mode if stdin is a terminal
	if isTTY {
		oldState, err := term.MakeRaw(stdinFd)
		if err == nil {
			defer term.Restore(stdinFd, oldState) //nolint:errcheck
		}
	}

	// Mutex protecting: stdout writes, screen access, bar state, term dimensions
	var outputMu sync.Mutex
	barScrollSeq := scrollRegionSeq(rows - 1)
	termRows := rows
	termCols := cols
	var lastOutputAt time.Time

	var cache barCache // shared with output goroutine and SIGWINCH; guarded by outputMu

	renderBarEsc := func(message string, alert bool) string {
		barWidth := max(termCols-1, 1)
		bar := RenderStatusBar(message, barWidth, alert)
		// Avoid writing into the final column. Many terminals set a
		// pending autowrap state when the last column is filled; after a
		// resize that state can reflow the bar into normal scrollback.
		return fmt.Sprintf("\x1b7\x1b[%d;1H\x1b[K%s\x1b8", termRows, bar)
	}

	renderCommandBarEsc := func(target string, hints []KeyHint) string {
		barWidth := max(termCols-1, 1)
		hasAlert := target != ""
		bar := RenderCommandBar(target, hints, barWidth, hasAlert)
		return fmt.Sprintf("\x1b7\x1b[%d;1H\x1b[K%s\x1b8", termRows, bar)
	}

	clearBarRowEsc := func(row int) string {
		return fmt.Sprintf("\x1b7\x1b[%d;1H\x1b[K\x1b8", row)
	}

	clearBarEsc := func() string {
		return clearBarRowEsc(termRows)
	}

	clearResizeBarRows := func(oldRows, newRows int) {
		if cache.mode == barHidden {
			return
		}
		rowsToClear := []int{
			oldRows - 1,
			oldRows,
			newRows - 1,
			newRows,
		}
		cleared := map[int]bool{}
		for _, row := range rowsToClear {
			if row < 1 || row > newRows || cleared[row] {
				continue
			}
			cleared[row] = true
			os.Stdout.WriteString(clearBarRowEsc(row)) //nolint:errcheck
		}
	}

	// Merge goroutine: forward opts.Status events into eventCh (only when non-nil)
	if w.opts.Status != nil {
		go func() {
			for {
				select {
				case <-done:
					return
				case su, ok := <-w.opts.Status:
					if !ok {
						return
					}
					kind := barEventStatus
					if su.Alert {
						kind = barEventAlert
					}
					select {
					case <-done:
						return
					case eventCh <- barEvent{kind: kind, update: su}:
					}
				}
			}
		}()
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)

	go func() {
		for range sigCh {
			if isTTY {
				if w, h, err := term.GetSize(stdoutFd); err == nil {
					outputMu.Lock()
					oldRows := termRows
					clearResizeBarRows(oldRows, h)
					termCols, termRows = w, h
					os.Stdout.WriteString(resizeRepairSeq(termRows-1, lastOutputAt, time.Now())) //nolint:errcheck
					_ = pty.Setsize(ptmx, &pty.Winsize{
						Rows: uint16(termRows - 1),
						Cols: uint16(termCols),
					})
					barScrollSeq = scrollRegionSeq(termRows - 1)
					switch cache.mode {
					case barStatus, barAlert:
						cache.rendered = renderBarEsc(cache.message, cache.mode == barAlert)
						os.Stdout.WriteString(cache.rendered) //nolint:errcheck
					case barCommand:
						cache.rendered = renderCommandBarEsc(cache.target, cache.keyHints)
						os.Stdout.WriteString(cache.rendered) //nolint:errcheck
					case barCleared:
						cache.rendered = clearBarEsc()
						os.Stdout.WriteString(cache.rendered) //nolint:errcheck
					case barHidden:
						// Nothing to do
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

	// Event loop goroutine — sole owner of bar state; updates shared cache under outputMu.
	const maxAlertQueue = 64
	loopDone := make(chan struct{})

	go func() {
		defer close(loopDone)
		var (
			lastStatus     string
			alertQueue     []StatusUpdate
			activeAlert    *StatusUpdate
			dismissTimer   *time.Timer
			alertStartedAt time.Time

			// Command mode state
			cmdState         commandState
			commandGen       uint64
			cmdIdleTimer     *time.Timer
			cmdCancel        context.CancelFunc
			savedAlertRemain time.Duration
		)

		showAlert := func(su StatusUpdate) {
			activeAlert = &su
			timeout := su.Timeout
			if timeout <= 0 {
				timeout = DefaultTimeout
			}
			alertStartedAt = time.Now()

			outputMu.Lock()
			cache = barCache{
				rendered: renderBarEsc(su.Message, true),
				message:  su.Message,
				mode:     barAlert,
			}
			if stdoutIsTTY {
				os.Stdout.WriteString(cache.rendered) //nolint:errcheck
			}
			outputMu.Unlock()

			if dismissTimer != nil {
				dismissTimer.Stop()
			}
			dismissTimer = time.AfterFunc(timeout, func() {
				select {
				case <-done:
				case eventCh <- barEvent{kind: barEventDismiss}:
				}
			})
		}

		showStatus := func(msg string) {
			outputMu.Lock()
			cache = barCache{
				rendered: renderBarEsc(msg, false),
				message:  msg,
				mode:     barStatus,
			}
			if stdoutIsTTY {
				os.Stdout.WriteString(cache.rendered) //nolint:errcheck
			}
			outputMu.Unlock()
		}

		showCleared := func() {
			outputMu.Lock()
			cache = barCache{
				rendered: clearBarEsc(),
				message:  "",
				mode:     barCleared,
			}
			if stdoutIsTTY {
				os.Stdout.WriteString(cache.rendered) //nolint:errcheck
			}
			outputMu.Unlock()
		}

		visibleKeys := func() []byte {
			if activeAlert == nil {
				return nil
			}
			hasAlert := activeAlert.Target != ""
			var keys []byte
			for _, h := range activeAlert.KeyHints {
				if h.RequireAlert && !hasAlert {
					continue
				}
				keys = append(keys, h.Key)
			}
			return keys
		}

		exitCommandMode := func() {
			if cmdIdleTimer != nil {
				cmdIdleTimer.Stop()
				cmdIdleTimer = nil
			}
			if cmdCancel != nil {
				cmdCancel()
				cmdCancel = nil
			}
			cmdState = commandNone
		}

		restoreAfterCommand := func() {
			if activeAlert != nil {
				showAlert(*activeAlert)
				if savedAlertRemain > 0 {
					if dismissTimer != nil {
						dismissTimer.Stop()
					}
					dismissTimer = time.AfterFunc(savedAlertRemain, func() {
						select {
						case <-done:
						case eventCh <- barEvent{kind: barEventDismiss}:
						}
					})
				}
			} else if len(alertQueue) > 0 {
				next := alertQueue[0]
				alertQueue = alertQueue[1:]
				showAlert(next)
			} else if lastStatus != "" {
				showStatus(lastStatus)
			} else {
				showCleared()
			}
		}

		dismissAfterAction := func() {
			activeAlert = nil
			if len(alertQueue) > 0 {
				next := alertQueue[0]
				alertQueue = alertQueue[1:]
				showAlert(next)
			} else if lastStatus != "" {
				showStatus(lastStatus)
			} else {
				showCleared()
			}
		}

		for {
			select {
			case <-done:
				if dismissTimer != nil {
					dismissTimer.Stop()
				}
				if cmdIdleTimer != nil {
					cmdIdleTimer.Stop()
				}
				if cmdCancel != nil {
					cmdCancel()
				}
				return
			case ev := <-eventCh:
				switch ev.kind {
				case barEventStatus:
					lastStatus = ev.update.Message
					if activeAlert == nil && cmdState == commandNone {
						showStatus(lastStatus)
					}

				case barEventAlert:
					if activeAlert == nil && cmdState == commandNone {
						showAlert(ev.update)
					} else {
						if len(alertQueue) >= maxAlertQueue {
							alertQueue = alertQueue[1:]
						}
						alertQueue = append(alertQueue, ev.update)
					}

				case barEventDismiss:
					if cmdState != commandNone {
						break
					}
					activeAlert = nil
					if len(alertQueue) > 0 {
						next := alertQueue[0]
						alertQueue = alertQueue[1:]
						showAlert(next)
					} else if lastStatus != "" {
						showStatus(lastStatus)
					} else {
						showCleared()
					}

				case barEventEnterCommand:
					commandGen++
					cmdState = commandActive

					if dismissTimer != nil {
						dismissTimer.Stop()
						elapsed := time.Since(alertStartedAt)
						timeout := DefaultTimeout
						if activeAlert != nil && activeAlert.Timeout > 0 {
							timeout = activeAlert.Timeout
						}
						savedAlertRemain = max(timeout-elapsed, 0)
					}

					idleGen := commandGen
					cmdIdleTimer = time.AfterFunc(5*time.Second, func() {
						select {
						case <-done:
						case eventCh <- barEvent{kind: barEventCancelCommand, gen: idleGen}:
						}
					})

					ctx, cancel := context.WithCancel(context.Background())
					cmdCancel = cancel

					target := ""
					if activeAlert != nil {
						target = activeAlert.Target
					}

					ev.respCh <- commandResponse{
						Target:      target,
						VisibleKeys: visibleKeys(),
						Gen:         commandGen,
						Ctx:         ctx,
					}

					hasAlert := activeAlert != nil && activeAlert.Target != ""
					var hints []KeyHint
					if activeAlert != nil {
						hints = activeAlert.KeyHints
					}
					outputMu.Lock()
					barWidth := max(termCols-1, 1)
					bar := RenderCommandBar(target, hints, barWidth, hasAlert)
					cache = barCache{
						rendered: fmt.Sprintf("\x1b7\x1b[%d;1H\x1b[K%s\x1b8", termRows, bar),
						message:  target,
						mode:     barCommand,
						target:   target,
						keyHints: hints,
					}
					if stdoutIsTTY {
						os.Stdout.WriteString(cache.rendered) //nolint:errcheck
					}
					outputMu.Unlock()

				case barEventBeginAction:
					if cmdState != commandActive || ev.gen != commandGen {
						ev.ackCh <- false
						break
					}
					if cmdIdleTimer != nil {
						cmdIdleTimer.Stop()
						cmdIdleTimer = nil
					}
					cmdState = commandPending
					ev.ackCh <- true

				case barEventAction:
					if cmdState != commandPending || ev.gen != commandGen {
						break
					}
					exitCommandMode()

					if ev.result.Err != nil {
						msg := ev.result.Err.Error()
						showAlert(StatusUpdate{Message: msg, Alert: true, Timeout: 2 * time.Second})
						activeAlert = nil
					} else if ev.result.Message != "" {
						activeAlert = nil
						showStatus(ev.result.Message)
						if dismissTimer != nil {
							dismissTimer.Stop()
						}
						dismissTimer = time.AfterFunc(2*time.Second, func() {
							select {
							case <-done:
							case eventCh <- barEvent{kind: barEventDismiss}:
							}
						})
					} else {
						dismissAfterAction()
					}

				case barEventCancelCommand:
					if cmdState != commandActive || ev.gen != commandGen {
						break
					}
					exitCommandMode()
					restoreAfterCommand()
				}
			}
		}
	}()

	// PTY -> stdout goroutine
	//
	// This path is normally a byte-for-byte passthrough from child PTY to
	// the real terminal, but ward owns one piece of real-terminal state:
	// the scroll region that protects the last row for notifications.
	//
	// Child applications are allowed to emit terminal-control sequences.
	// Some of those sequences affect ward's protected bar row:
	//   - ESC c, RIS ("Reset to Initial State"), resets scroll margins and
	//     clears the screen.
	//   - CSI r with no parameters, DECSTBM, resets top/bottom margins to
	//     the terminal's full height.
	//   - CSI 2J / CSI 3J / CSI J ("Erase in Display"), clears cells
	//     including the bar row without changing scroll margins.
	//
	// The scanner below recognizes just enough ECMA-48/DEC grammar to
	// detect these sequences and re-apply ward's scroll region and/or
	// repaint the bar after writing the child's bytes.
	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		buf := make([]byte, 32*1024)
		var scanner escScanner
		pendingScrollReset := false
		pendingBarErased := false

		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				data := buf[:n]

				outputMu.Lock()

				result := scanner.Scan(data)
				pendingScrollReset = pendingScrollReset || result.ScrollReset
				pendingBarErased = pendingBarErased || result.BarErased
				lastOutputAt = time.Now()

				os.Stdout.Write(data) //nolint:errcheck

				// The bar is NOT repainted after every read. The scroll
				// region keeps it in place during normal output. Repainting
				// per-chunk would leak bar escape sequences into the
				// terminal's scrollback buffer, making the status bar
				// visible when scrolling back through history. The bar is
				// repainted only on scroll/erase reset (here), content
				// change (event loop), and resize (SIGWINCH handler).
				//
				// Pending flags preserve detected repairs across chunk
				// boundaries. Injection is deferred until the scanner
				// returns to ground state to avoid corrupting an
				// in-progress escape sequence.
				if stdoutIsTTY && scanner.InGround() {
					if pendingScrollReset {
						os.Stdout.WriteString(barScrollSeq) //nolint:errcheck
					}
					if (pendingScrollReset || pendingBarErased) && cache.mode != barHidden {
						os.Stdout.WriteString(cache.rendered) //nolint:errcheck
					}
					pendingScrollReset = false
					pendingBarErased = false
				}

				outputMu.Unlock()
			}
			if err != nil {
				break
			}
		}
	}()

	// stdin -> PTY goroutine with command mode input parsing.
	go func() {
		buf := make([]byte, 32*1024)
		inCommand := false
		var handler *inputHandler
		var cmdCtx context.Context

		if w.opts.OnKey != nil && isTTY {
			handler = &inputHandler{
				hotkey:  w.opts.Hotkey,
				eventCh: eventCh,
				onKey:   w.opts.OnKey,
			}
		}

		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if handler == nil {
					if _, werr := ptmx.Write(buf[:n]); werr != nil {
						break
					}
				} else {
					// Check if command mode was cancelled while we were blocked in Read
					if inCommand && cmdCtx != nil {
						select {
						case <-cmdCtx.Done():
							inCommand = false
						default:
						}
					}
					inCommand = processInput(buf[:n], ptmx, w.opts.Hotkey, inCommand, handler, cmdCtx)
					if inCommand {
						cmdCtx = handler.cmdCtx
					} else {
						cmdCtx = nil
					}
				}
			}
			if err != nil {
				break
			}
		}
	}()

	cmdErr := cmd.Wait()

	// Wait for PTY output to drain before touching the screen.
	<-outputDone

	// Signal event loop shutdown and wait for it to exit before the deferred
	// terminal restore runs. Without this join, the event loop could process
	// a buffered event and write a bar after the scroll region is restored.
	close(done)
	<-loopDone

	if cmdErr != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](cmdErr); ok {
			return exitErr.ExitCode(), nil
		}
		return 0, cmdErr
	}
	return 0, nil
}
