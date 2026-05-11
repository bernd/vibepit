package ward

import (
	"context"
	"io"
	"slices"
	"time"
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

// processInput scans a byte chunk and dispatches to the PTY or command mode.
// Returns true if still in command mode after processing the chunk.
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
			// Timeout cancelled command mode — forward remaining to PTY
			if i < len(data) {
				pty.Write(data[i:]) //nolint:errcheck
			}
			return false
		default:
		}

		b := data[i]
		i++

		switch b {
		case 0x1B: // Esc — cancel
			handler.eventCh <- barEvent{
				kind: barEventCancelCommand,
				gen:  handler.gen,
			}
			// Forward remaining bytes to PTY
			if i < len(data) {
				pty.Write(data[i:]) //nolint:errcheck
			}
			return false

		case hotkey: // Second hotkey — forward literal
			pty.Write([]byte{hotkey}) //nolint:errcheck
			handler.eventCh <- barEvent{
				kind: barEventCancelCommand,
				gen:  handler.gen,
			}
			// Forward remaining bytes to PTY
			if i < len(data) {
				pty.Write(data[i:]) //nolint:errcheck
			}
			return false

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
				if i < len(data) {
					pty.Write(data[i:]) //nolint:errcheck
				}
				return false
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
			// Forward remaining bytes to PTY
			if i < len(data) {
				pty.Write(data[i:]) //nolint:errcheck
			}
			return false
		}
	}

	return inCommand
}
