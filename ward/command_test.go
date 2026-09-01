package ward

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockPTY collects bytes written to the "PTY" for inspection.
type mockPTY struct {
	written []byte
}

func (m *mockPTY) Write(p []byte) (int, error) {
	m.written = append(m.written, p...)
	return len(p), nil
}

func TestProcessInputNormalPassthrough(t *testing.T) {
	pty := &mockPTY{}
	input := []byte("hello world")
	inCommand := processInput(input, pty, 0x1D, false, nil, nil)
	assert.False(t, inCommand)
	assert.Equal(t, input, pty.written)
}

func TestProcessInputHotkeyNilOnKey(t *testing.T) {
	pty := &mockPTY{}
	input := []byte{0x1D, 'x'}
	// OnKey is nil via inputHandler — hotkey is passed through
	inCommand := processInput(input, pty, 0x1D, false, nil, nil)
	assert.False(t, inCommand)
	assert.Equal(t, input, pty.written)
}

func TestProcessInputHotkeyMidChunk(t *testing.T) {
	pty := &mockPTY{}
	eventCh := make(chan barEvent, 64)

	handler := &inputHandler{
		hotkey:  0x1D,
		eventCh: eventCh,
		onKey:   func(ctx context.Context, key byte, target string) (string, error) { return "", nil },
	}

	// Handle enter event in background
	go func() {
		ev := <-eventCh
		require.Equal(t, barEventEnterCommand, ev.kind)
		ev.respCh <- commandResponse{
			Target:      "example.com:443",
			VisibleKeys: []byte{'a'},
			Gen:         1,
			Ctx:         context.Background(),
		}
	}()

	input := []byte{'A', 'B', 0x1D, 'x', 'y'}
	inCommand := processInput(input, pty, 0x1D, false, handler, nil)
	// "AB" should be written to PTY, then hotkey triggers command mode
	// 'x' and 'y' are not visible keys, so discarded; we stay in command mode
	assert.True(t, inCommand)
	assert.Equal(t, []byte("AB"), pty.written)
}

func TestProcessInputDoubleHotkeyForwardsLiteral(t *testing.T) {
	pty := &mockPTY{}
	cancelCh := make(chan barEvent, 1)
	handler := &inputHandler{
		hotkey:  0x1D,
		eventCh: cancelCh,
		onKey:   func(ctx context.Context, key byte, target string) (string, error) { return "", nil },
	}

	input := []byte{0x1D}
	// commandCtx from a previous enter
	cmdCtx := t.Context()

	inCommand := processInput(input, pty, 0x1D, true, handler, cmdCtx)
	assert.False(t, inCommand)
	assert.Equal(t, []byte{0x1D}, pty.written)
	// A cancel event was sent
	require.Len(t, cancelCh, 1)
}

func TestProcessInputEscCancels(t *testing.T) {
	pty := &mockPTY{}
	cancelCh := make(chan barEvent, 1)
	handler := &inputHandler{
		hotkey:  0x1D,
		eventCh: cancelCh,
		onKey:   func(ctx context.Context, key byte, target string) (string, error) { return "", nil },
	}

	input := []byte{0x1B}
	cmdCtx := t.Context()

	inCommand := processInput(input, pty, 0x1D, true, handler, cmdCtx)
	assert.False(t, inCommand)
	// A bare Esc is consumed by the cancel.
	assert.Empty(t, pty.written)
	require.Len(t, cancelCh, 1)
}

func TestProcessInputCommandModeEscapeSequences(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		staysOpen bool
		// written overrides the expected PTY output; by default the whole
		// input is expected to be forwarded.
		written *string
	}{
		// Complete sequences (keys, mouse, focus, terminal replies) are
		// never command keys: forwarded intact, bar stays open.
		{name: "CSI arrow key", input: "\x1b[A", staysOpen: true},
		{name: "SS3 arrow key", input: "\x1bOA", staysOpen: true},
		{name: "function key", input: "\x1b[15~", staysOpen: true},
		{name: "modified arrow key", input: "\x1b[1;5C", staysOpen: true},
		{name: "CSI with intermediate", input: "\x1b[?1;2$y", staysOpen: true},
		{name: "SGR mouse press", input: "\x1b[<0;10;20M", staysOpen: true},
		{name: "SGR mouse release", input: "\x1b[<0;10;20m", staysOpen: true},
		{name: "X10 mouse report", input: "\x1b[M a1", staysOpen: true},
		{name: "linux console F1", input: "\x1b[[A", staysOpen: true},
		{name: "focus in", input: "\x1b[I", staysOpen: true},
		{name: "in-band size report", input: "\x1b[48;48;97;960;1940t", staysOpen: true},
		{name: "XTWINOPS size report", input: "\x1b[8;48;97t", staysOpen: true},
		{name: "OSC reply with ST", input: "\x1b]11;rgb:1a1a/1a1a/1a1a\x1b\\", staysOpen: true},
		{name: "OSC reply with BEL", input: "\x1b]11;rgb:1a1a/1a1a/1a1a\a", staysOpen: true},
		{name: "DCS reply", input: "\x1bP1+r524742=8/8/8\x1b\\", staysOpen: true},
		{name: "mouse motion flood", input: "\x1b[<35;10;20M\x1b[<35;11;20M\x1b[<35;12;21M", staysOpen: true},

		// A bare Esc cancels and is consumed.
		{name: "bare Esc", input: "\x1b", written: new("")},

		// Anything else — Alt+key, a sequence cut at the read boundary, or
		// a paste — cancels and is forwarded with its ESC intact. A cut
		// size report is still completed by the rewriter downstream.
		{name: "alt+key", input: "\x1bz"},
		{name: "alt+bracket", input: "\x1b["},
		{name: "alt+bracket then enter", input: "\x1b[\r"},
		{name: "cut mouse report", input: "\x1b[<0;1"},
		{name: "cut X10 mouse report", input: "\x1b[M a"},
		{name: "cut size report", input: "\x1b[8;4"},
		{name: "cut OSC reply", input: "\x1b]11;rgb:1a1a"},
		{name: "bracketed paste", input: "\x1b[200~hello\x1b[201~"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pty := &mockPTY{}
			eventCh := make(chan barEvent, 1)
			handler := &inputHandler{
				hotkey:  0x1D,
				eventCh: eventCh,
				// Bytes that occur inside the sequences and paste bodies
				// must not be consumed as command keys.
				visibleKeys: []byte("12478AIMahelou"),
				onKey: func(ctx context.Context, key byte, target string) (string, error) {
					t.Errorf("unexpected command key %q", key)
					return "", nil
				},
			}

			inCommand := processInput([]byte(tt.input), pty, 0x1D, true, handler, t.Context())
			assert.Equal(t, tt.staysOpen, inCommand)
			want := tt.input
			if tt.written != nil {
				want = *tt.written
			}
			assert.Equal(t, want, string(pty.written))
			if tt.staysOpen {
				assert.Empty(t, eventCh)
			} else {
				require.Len(t, eventCh, 1)
				assert.Equal(t, barEventCancelCommand, (<-eventCh).kind)
			}
		})
	}
}

func TestEscSeqLen(t *testing.T) {
	tests := []struct {
		name  string
		input string
		n     int
	}{
		{"too short", "\x1b[", 0},
		{"CSI", "\x1b[1;5C", 6},
		{"CSI with intermediate", "\x1b[!p", 4},
		{"CSI cut in params", "\x1b[1;", 0},
		{"CSI ended by control byte", "\x1b[\r", 0},
		{"CSI ended by ESC", "\x1b[1;\x1b[A", 0},
		{"SS3", "\x1bOP", 3},
		{"SS3 with non-final", "\x1bO\r", 0},
		{"X10 mouse", "\x1b[M a1", 6},
		{"X10 mouse cut", "\x1b[M a", 0},
		{"linux console F1", "\x1b[[A", 4},
		{"linux console prefix", "\x1b[[", 0},
		{"linux console prefix then other", "\x1b[[x", 0},
		{"OSC with BEL", "\x1b]0;x\a", 6},
		{"OSC with ST", "\x1b]0;x\x1b\\", 7},
		{"OSC cut", "\x1b]0;x", 0},
		{"DCS with BEL is not terminated", "\x1bP0;x\a", 0},
		{"DCS with ST", "\x1bP0;x\x1b\\", 7},
		{"alt+key", "\x1bzz", 0},
		{"sequence then more", "\x1b[Aab", 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.n, escSeqLen([]byte(tt.input)))
		})
	}
}

func TestProcessInputCommandModeSequenceThenCommandKey(t *testing.T) {
	pty := &mockPTY{}
	eventCh := make(chan barEvent, 2)
	var calledKey byte
	handler := &inputHandler{
		hotkey:      0x1D,
		eventCh:     eventCh,
		visibleKeys: []byte{'a'},
		onKey: func(ctx context.Context, key byte, target string) (string, error) {
			calledKey = key
			return "", nil
		},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		ev := <-eventCh
		assert.Equal(t, barEventBeginAction, ev.kind)
		ev.ackCh <- true
	}()

	inCommand := processInput([]byte("\x1b[<0;10;20Ma"), pty, 0x1D, true, handler, t.Context())
	<-done
	assert.False(t, inCommand)
	assert.Equal(t, "\x1b[<0;10;20M", string(pty.written))
	assert.Equal(t, byte('a'), calledKey)
}

func TestProcessInputCommandModeTailIsRescanned(t *testing.T) {
	// Command mode is left, then the hotkey in the same read reopens it
	// instead of reaching the child.
	tests := []struct {
		name    string
		input   string
		written string
	}{
		{"hotkey after Alt+key", "\x1bx\x1d", "\x1bx"},
		{"hotkey after second hotkey", "\x1dx\x1d", "\x1dx"},
		// Alt+Ctrl+] under metaSendsEscape: the ESC reaches the child and
		// the hotkey reopens the bar, as it would in normal mode.
		{"hotkey after Esc", "\x1b\x1d", "\x1b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pty := &mockPTY{}
			eventCh := make(chan barEvent, 2)
			handler := &inputHandler{
				hotkey:  0x1D,
				eventCh: eventCh,
				onKey:   func(ctx context.Context, key byte, target string) (string, error) { return "", nil },
			}

			done := make(chan struct{})
			go func() {
				defer close(done)
				ev := <-eventCh
				assert.Equal(t, barEventCancelCommand, ev.kind)
				ev = <-eventCh
				assert.Equal(t, barEventEnterCommand, ev.kind)
				ev.respCh <- commandResponse{Gen: 2, Ctx: context.Background()}
			}()
			inCommand := processInput([]byte(tt.input), pty, 0x1D, true, handler, t.Context())
			<-done
			assert.True(t, inCommand)
			assert.Equal(t, tt.written, string(pty.written))
		})
	}
}

// testEventLoop is a minimal event loop for testing command mode logic
// without terminal I/O.
func testEventLoop(eventCh chan barEvent, done chan struct{}, loopDone chan struct{}) {
	testEventLoopWithStateLog(eventCh, done, loopDone, nil)
}

func testEventLoopWithStateLog(eventCh chan barEvent, done chan struct{}, loopDone chan struct{}, states *[]commandState) {
	defer close(loopDone)
	var (
		activeAlert      *StatusUpdate
		alertQueue       []StatusUpdate
		alertStartedAt   time.Time
		dismissTimer     *time.Timer
		cmdState         commandState
		commandGen       uint64
		cmdIdleTimer     *time.Timer
		cmdCancel        context.CancelFunc
		savedAlertRemain time.Duration
	)

	logState := func() {
		if states != nil {
			*states = append(*states, cmdState)
		}
	}

	showAlert := func(su StatusUpdate) {
		activeAlert = &su
		timeout := su.Timeout
		if timeout <= 0 {
			timeout = DefaultTimeout
		}
		alertStartedAt = time.Now()
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
		logState()
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
		}
	}

	_ = restoreAfterCommand

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
				// tracked in real event loop but unused in test

			case barEventAlert:
				if activeAlert == nil && cmdState == commandNone {
					showAlert(ev.update)
				} else {
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
				}

			case barEventEnterCommand:
				commandGen++
				cmdState = commandActive
				logState()

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
					VisibleKeys: nil,
					Gen:         commandGen,
					Ctx:         ctx,
				}

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
				logState()
				ev.ackCh <- true

			case barEventAction:
				if cmdState != commandPending || ev.gen != commandGen {
					break
				}
				exitCommandMode()
				activeAlert = nil

			case barEventCancelCommand:
				if cmdState != commandActive || ev.gen != commandGen {
					break
				}
				exitCommandMode()
				restoreAfterCommand()
			}
		}
	}
}

func TestEventLoopEnterCommandWithAlert(t *testing.T) {
	eventCh := make(chan barEvent, 64)
	done := make(chan struct{})

	eventCh <- barEvent{
		kind:   barEventAlert,
		update: StatusUpdate{Message: "blocked", Alert: true, Timeout: time.Hour, Target: "example.com:443"},
	}

	respCh := make(chan commandResponse, 1)
	eventCh <- barEvent{
		kind:   barEventEnterCommand,
		respCh: respCh,
	}

	loopDone := make(chan struct{})
	go testEventLoop(eventCh, done, loopDone)

	resp := <-respCh
	assert.Equal(t, "example.com:443", resp.Target)
	assert.Equal(t, uint64(1), resp.Gen)
	require.NotNil(t, resp.Ctx)

	close(done)
	<-loopDone
}

func TestEventLoopCancelRestoresAlert(t *testing.T) {
	eventCh := make(chan barEvent, 64)
	done := make(chan struct{})

	eventCh <- barEvent{
		kind:   barEventAlert,
		update: StatusUpdate{Message: "blocked", Alert: true, Timeout: time.Hour, Target: "example.com:443"},
	}

	respCh := make(chan commandResponse, 1)
	eventCh <- barEvent{
		kind:   barEventEnterCommand,
		respCh: respCh,
	}

	loopDone := make(chan struct{})
	var states []commandState
	go testEventLoopWithStateLog(eventCh, done, loopDone, &states)

	resp := <-respCh

	eventCh <- barEvent{
		kind: barEventCancelCommand,
		gen:  resp.Gen,
	}

	time.Sleep(10 * time.Millisecond)

	close(done)
	<-loopDone

	require.Contains(t, states, commandActive)
}

func TestEventLoopStaleGenIgnored(t *testing.T) {
	eventCh := make(chan barEvent, 64)
	done := make(chan struct{})

	respCh1 := make(chan commandResponse, 1)
	eventCh <- barEvent{kind: barEventEnterCommand, respCh: respCh1}

	loopDone := make(chan struct{})
	go testEventLoop(eventCh, done, loopDone)

	resp1 := <-respCh1

	eventCh <- barEvent{kind: barEventCancelCommand, gen: resp1.Gen}
	time.Sleep(10 * time.Millisecond)

	respCh2 := make(chan commandResponse, 1)
	eventCh <- barEvent{kind: barEventEnterCommand, respCh: respCh2}
	resp2 := <-respCh2
	require.Equal(t, uint64(2), resp2.Gen)

	eventCh <- barEvent{kind: barEventCancelCommand, gen: resp1.Gen}
	time.Sleep(10 * time.Millisecond)

	ackCh := make(chan bool, 1)
	eventCh <- barEvent{kind: barEventBeginAction, gen: resp2.Gen, ackCh: ackCh}
	ack := <-ackCh
	assert.True(t, ack)

	close(done)
	<-loopDone
}

func TestProcessInputActionKeyTriggersOnKey(t *testing.T) {
	pty := &mockPTY{}
	eventCh := make(chan barEvent, 64)
	var calledKey byte
	var calledTarget string

	handler := &inputHandler{
		hotkey:  0x1D,
		eventCh: eventCh,
		onKey: func(ctx context.Context, key byte, target string) (string, error) {
			calledKey = key
			calledTarget = target
			return "allowed", nil
		},
		target:      "example.com:443",
		visibleKeys: []byte{'a'},
		gen:         1,
	}

	cmdCtx := t.Context()
	handler.cmdCtx = cmdCtx

	go func() {
		ev := <-eventCh // barEventBeginAction
		require.Equal(t, barEventBeginAction, ev.kind)
		ev.ackCh <- true
		<-eventCh // barEventAction
	}()

	input := []byte{'a', 'z'}
	inCommand := processInput(input, pty, 0x1D, true, handler, cmdCtx)
	assert.False(t, inCommand)
	assert.Equal(t, byte('a'), calledKey)
	assert.Equal(t, "example.com:443", calledTarget)
	assert.Equal(t, []byte("z"), pty.written)
}

func TestProcessInputActionKeyRejectedByAck(t *testing.T) {
	pty := &mockPTY{}
	eventCh := make(chan barEvent, 64)
	onKeyCalled := false

	handler := &inputHandler{
		hotkey:  0x1D,
		eventCh: eventCh,
		onKey: func(ctx context.Context, key byte, target string) (string, error) {
			onKeyCalled = true
			return "", nil
		},
		target:      "example.com:443",
		visibleKeys: []byte{'a'},
		gen:         1,
	}

	cmdCtx := t.Context()
	handler.cmdCtx = cmdCtx

	go func() {
		ev := <-eventCh
		require.Equal(t, barEventBeginAction, ev.kind)
		ev.ackCh <- false
	}()

	input := []byte{'a', 'z'}
	inCommand := processInput(input, pty, 0x1D, true, handler, cmdCtx)
	assert.False(t, inCommand)
	assert.False(t, onKeyCalled)
	assert.Equal(t, []byte("z"), pty.written)
}

func TestProcessInputContextCancelledDuringCommandMode(t *testing.T) {
	pty := &mockPTY{}
	handler := &inputHandler{
		hotkey:  0x1D,
		eventCh: make(chan barEvent, 64),
		onKey:   func(ctx context.Context, key byte, target string) (string, error) { return "", nil },
	}

	cmdCtx, cmdCancel := context.WithCancel(context.Background())
	cmdCancel() // Cancel immediately

	input := []byte{'a', 'b', 'c'}
	inCommand := processInput(input, pty, 0x1D, true, handler, cmdCtx)
	assert.False(t, inCommand)
	assert.Equal(t, []byte("abc"), pty.written)
}

func TestProcessInputHotkeyFollowedByActionInSameChunk(t *testing.T) {
	pty := &mockPTY{}
	eventCh := make(chan barEvent, 64)
	var calledKey byte

	handler := &inputHandler{
		hotkey:  0x1D,
		eventCh: eventCh,
		onKey: func(ctx context.Context, key byte, target string) (string, error) {
			calledKey = key
			return "", nil
		},
	}

	go func() {
		ev := <-eventCh // barEventEnterCommand
		require.Equal(t, barEventEnterCommand, ev.kind)
		ev.respCh <- commandResponse{
			Target:      "test.com:80",
			VisibleKeys: []byte{'a'},
			Gen:         1,
			Ctx:         context.Background(),
		}
		ev = <-eventCh // barEventBeginAction
		require.Equal(t, barEventBeginAction, ev.kind)
		ev.ackCh <- true
		<-eventCh // barEventAction
	}()

	input := []byte("pre\x1daz")
	inCommand := processInput(input, pty, 0x1D, false, handler, nil)
	assert.False(t, inCommand)
	assert.Equal(t, byte('a'), calledKey)
	assert.Equal(t, []byte("prez"), pty.written)
}
