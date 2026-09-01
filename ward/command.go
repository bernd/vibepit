package ward

import (
	"bytes"
	"context"
	"io"
	"slices"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// commandState tracks the event loop's command mode state.
type commandState int

const (
	commandNone    commandState = iota
	commandActive               // command bar shown, idle timer running
	commandPending              // action key pressed, waiting for OnKey result
)

// commandResponse is sent from the event loop to the stdin goroutine
// when entering command mode.
type commandResponse struct {
	Target      string
	VisibleKeys []byte
	Gen         uint64
	Ctx         context.Context //nolint:containedctx // channel-passed, not long-lived
}

// actionResult is sent from the stdin goroutine to the event loop
// after OnKey completes.
type actionResult struct {
	Message string
	Err     error
}

// inputHandler holds the state and callbacks for the stdin goroutine's
// command mode logic. nil means command mode is disabled.
type inputHandler struct {
	hotkey  byte
	eventCh chan<- barEvent
	onKey   func(ctx context.Context, key byte, target string) (string, error)

	// Set by enterCommandMode, cleared on exit
	target      string
	visibleKeys []byte
	gen         uint64
	cmdCtx      context.Context //nolint:containedctx // channel-passed, not long-lived
}

const onKeyTimeout = 5 * time.Second

var pasteStart = []byte(ansi.BracketedPasteStart)

// processInput scans a byte chunk and dispatches to the PTY or command mode.
// Returns true if still in command mode after processing the chunk.
//
// Whenever command mode is left, the rest of the chunk is rescanned in
// normal mode rather than forwarded blindly, so a hotkey later in the same
// read still opens the bar.
func processInput(data []byte, pty io.Writer, hotkey byte, inCommand bool, handler *inputHandler, cmdCtx context.Context) bool {
	i := 0

	for i < len(data) {
		if !inCommand {
			// Normal mode: scan for hotkey
			if handler == nil || handler.onKey == nil {
				// No command mode — pass everything through
				pty.Write(data[i:]) //nolint:errcheck
				return false
			}

			// Find next hotkey byte
			start := i
			for i < len(data) && data[i] != hotkey {
				i++
			}
			if start < i {
				pty.Write(data[start:i]) //nolint:errcheck
			}
			if i >= len(data) {
				return false
			}

			// Found hotkey — enter command mode
			i++ // consume the hotkey byte

			respCh := make(chan commandResponse, 1)
			handler.eventCh <- barEvent{
				kind:   barEventEnterCommand,
				respCh: respCh,
			}

			resp := <-respCh
			handler.target = resp.Target
			handler.visibleKeys = resp.VisibleKeys
			handler.gen = resp.Gen
			handler.cmdCtx = resp.Ctx //nolint:fatcontext // channel-passed context, not nested
			cmdCtx = resp.Ctx
			inCommand = true
			continue
		}

		// Command mode: check context first
		select {
		case <-cmdCtx.Done():
			// Timeout cancelled command mode
			inCommand = false
			continue
		default:
		}

		b := data[i]
		i++

		switch b {
		case asciiESC:
			// Command keys are single bytes, so a complete escape sequence
			// (arrow or function key, mouse or focus event, a size report
			// from a resize during command mode, a reply to a child's
			// query) is never meant for the bar, and terminal-initiated
			// traffic must not close it out from under the user. Forward
			// the sequence intact and stay in command mode. Bracketed
			// paste is the exception: its body is text that must not be
			// read as command keys, so it cancels like Alt+key below.
			seq := data[i-1:]
			if n := escSeqLen(seq); n > 0 && !bytes.HasPrefix(seq, pasteStart) {
				pty.Write(seq[:n]) //nolint:errcheck
				i += n - 1
				continue
			}

			handler.cancel()
			inCommand = false
			// A lone trailing ESC is the Esc key and is consumed here.
			// Anything following it in the same read (Alt+key, a paste, a
			// sequence cut at the read boundary) is rescanned from the ESC
			// so it reaches the child with its ESC intact, never as typed
			// text; the rewriter downstream still completes a cut size
			// report.
			if i < len(data) {
				i--
			}
			continue

		case hotkey: // Second hotkey — forward literal
			pty.Write([]byte{hotkey}) //nolint:errcheck
			handler.cancel()
			inCommand = false
			continue

		default:
			// Check if this is a visible key hint
			matched := slices.Contains(handler.visibleKeys, b)
			if !matched {
				continue // Ignore unmatched keys, stay in command mode
			}

			// Action key — begin action handshake
			ackCh := make(chan bool, 1)
			handler.eventCh <- barEvent{
				kind:  barEventBeginAction,
				gen:   handler.gen,
				ackCh: ackCh,
			}

			ack := <-ackCh
			if !ack {
				// Command mode was cancelled (e.g., timeout race)
				inCommand = false
				continue
			}

			// Call OnKey synchronously with a fresh context
			onKeyCtx, onKeyCancel := context.WithTimeout(context.Background(), onKeyTimeout)
			msg, err := handler.onKey(onKeyCtx, b, handler.target)
			onKeyCancel()

			handler.eventCh <- barEvent{
				kind:   barEventAction,
				gen:    handler.gen,
				result: actionResult{Message: msg, Err: err},
			}
			inCommand = false
			continue
		}
	}

	return inCommand
}

// cancel tells the event loop to leave command mode.
func (h *inputHandler) cancel() {
	h.eventCh <- barEvent{
		kind: barEventCancelCommand,
		gen:  h.gen,
	}
}

// escSeqLen returns the length of the complete escape sequence at the start
// of data, which must begin with ESC, or 0 if data is not one or is cut
// short. Recognised forms are the ones keyboards, mice and terminal replies
// use:
//
//   - CSI: ESC [ params intermediates final, plus two irregular shapes that
//     would otherwise be cut after a byte in the final range — the X10
//     mouse report ESC [ M Cb Cx Cy and the Linux console's ESC [ [ A..E
//     for F1–F5;
//   - SS3: ESC O final;
//   - OSC, DCS, APC, PM and SOS strings (ESC ] P _ ^ X) up to ST (ESC \),
//     or BEL for OSC.
//
// Everything else (Alt+key, a bare ESC [ or ESC O, a CSI broken off by a
// control byte) is Esc followed by ordinary bytes.
func escSeqLen(data []byte) int {
	if len(data) < 3 {
		return 0
	}
	switch data[1] {
	case 'O':
		if isCSIFinal(data[2]) {
			return 3
		}
	case '[':
		switch data[2] {
		case 'M':
			if len(data) >= 6 {
				return 6
			}
			return 0
		case '[':
			if len(data) >= 4 && data[3] >= 'A' && data[3] <= 'E' {
				return 4
			}
			return 0
		}
		i := 2
		for i < len(data) && data[i] >= csiParamMin && data[i] <= csiParamMax {
			i++
		}
		for i < len(data) && data[i] >= csiIntermediateMin && data[i] <= csiIntermediateMax {
			i++
		}
		if i < len(data) && isCSIFinal(data[i]) {
			return i + 1
		}
	case ']', 'P', '_', '^', 'X':
		for i := 2; i < len(data); i++ {
			switch data[i] {
			case asciiBEL:
				if data[1] == ']' {
					return i + 1
				}
			case asciiESC:
				if i+1 < len(data) && data[i+1] == '\\' {
					return i + 2
				}
			}
		}
	}
	return 0
}

func isCSIFinal(c byte) bool { return c >= csiFinalMin && c <= csiFinalMax }
