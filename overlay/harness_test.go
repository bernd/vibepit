package overlay

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bernd/vibepit/vt"
	"github.com/charmbracelet/colorprofile"
	"github.com/stretchr/testify/require"
)

// testTiming keeps budgets long where a test must not hit them. A test
// that waits for a budget shortens it with withTiming.
var testTiming = timing{
	positionWait: 5 * time.Second,
	groundWait:   5 * time.Second,
	groundBytes:  64 << 10,
	barrierWait:  5 * time.Second,
	silence:      30 * time.Millisecond,
	silenceMax:   5 * time.Second,
	lateStrip:    5 * time.Second,
	nudgeGap:     time.Millisecond,
	logMax:       4 << 20,
}

type harnessConfig struct {
	silent   bool
	noShadow bool
	timing   timing
	onResize func(cols, rows int)
	gate     chan struct{}
	host     string
}

type harnessOption func(*harnessConfig)

// silentTerminal makes the stand-in answer no queries, like a terminal
// without DSR 5n and 6n.
func silentTerminal() harnessOption {
	return func(c *harnessConfig) {
		c.silent = true
		c.timing.positionWait = time.Millisecond
	}
}

// hostOutput is on the local terminal before the session starts.
func hostOutput(s string) harnessOption { return func(c *harnessConfig) { c.host = s } }

func withoutShadow() harnessOption { return func(c *harnessConfig) { c.noShadow = true } }

func withTiming(f func(*timing)) harnessOption { return func(c *harnessConfig) { f(&c.timing) } }

// stalledSessionInput makes the session stop reading its input until
// gate is closed.
func stalledSessionInput(gate chan struct{}) harnessOption {
	return func(c *harnessConfig) { c.gate = gate }
}

// onSessionResize runs f in the session's resize, before it is logged.
func onSessionResize(f func(cols, rows int)) harnessOption {
	return func(c *harnessConfig) { c.onResize = f }
}

// harness runs a Terminal against fakes. real stands in for the user's
// terminal: it gets everything written to stdout and answers queries on
// stdin, in order, like a real terminal.
type harness struct {
	t         *testing.T
	term      *Terminal
	real      *vt.Terminal
	stdin     *bufPipe
	out       *chunkReader // the session's output
	sessionIn *syncBuffer  // the session's input
	stdout    *syncBuffer
	closedIn  atomic.Int32
	stalled   atomic.Int32  // writes waiting for a stalled session input
	done      chan struct{} // closed when Run returned
	err       error         // Run's result, after done

	mu         sync.Mutex
	cols, rows int
	resizes    []string
	onResize   func(cols, rows int)
}

func newHarness(t *testing.T, cols, rows int, opts ...harnessOption) *harness {
	t.Helper()
	hc := harnessConfig{timing: testTiming}
	for _, o := range opts {
		o(&hc)
	}
	h := &harness{
		t:         t,
		stdin:     newBufPipe(1 << 20),
		out:       newChunkReader(),
		sessionIn: &syncBuffer{},
		stdout:    &syncBuffer{},
		done:      make(chan struct{}),
		cols:      cols,
		rows:      rows,
		onResize:  hc.onResize,
	}
	var vtOpts []vt.TerminalOption
	if !hc.silent {
		vtOpts = append(vtOpts, vt.WithWritePty(func(b []byte) { _, _ = h.stdin.Write(b) }))
	}
	h.real = newVT(t, cols, rows, vtOpts...)
	feed(t, h.real, hc.host)
	h.term = New(Config{
		Stdin:      h.stdin,
		Stdout:     stdoutWriter{h},
		SessionIn:  gatedWriter{h, hc.gate},
		SessionOut: h.out,
		CloseInput: func() error { h.closedIn.Add(1); return nil },
		Resize:     h.recordResize,
		Size:       h.size,
		Environ:    []string{"TERM=xterm-256color"},
		NoShadow:   hc.noShadow,
	})
	h.term.timing = hc.timing
	// stdoutWriter isn't a TTY, so detection would find no colours.
	h.term.profile = colorprofile.ANSI256
	go func() {
		h.err = h.term.Run(context.Background())
		close(h.done)
	}()
	t.Cleanup(h.stop)
	return h
}

// stdoutWriter records what reaches the real terminal and feeds the
// stand-in.
type stdoutWriter struct{ h *harness }

func (w stdoutWriter) Write(p []byte) (int, error) {
	_, _ = w.h.stdout.Write(p)
	return w.h.real.Write(p)
}

// gatedWriter is the session's input: it takes nothing while gate is
// open.
type gatedWriter struct {
	h    *harness
	gate chan struct{}
}

func (w gatedWriter) Write(p []byte) (int, error) {
	if w.gate != nil {
		w.h.stalled.Add(1)
		<-w.gate
	}
	return w.h.sessionIn.Write(p)
}

func (h *harness) stop() {
	h.out.close()
	_ = h.stdin.Close()
	select {
	case <-h.done:
	case <-time.After(10 * time.Second):
		h.t.Error("Run didn't return")
	}
}

func (h *harness) size() (int, int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cols, h.rows, nil
}

func (h *harness) recordResize(cols, rows int) {
	if h.onResize != nil {
		h.onResize(cols, rows)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.resizes = append(h.resizes, fmt.Sprintf("%dx%d", cols, rows))
}

func (h *harness) resizeLog() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.resizes...)
}

// resize changes the local terminal's size, as SIGWINCH would.
func (h *harness) resize(cols, rows int) {
	h.t.Helper()
	h.mu.Lock()
	h.cols, h.rows = cols, rows
	h.mu.Unlock()
	require.NoError(h.t, h.real.Resize(uint16(cols), uint16(rows)))
	h.term.Resize()
}

// app writes session output and waits until the pump handled it.
func (h *harness) app(s string) {
	h.t.Helper()
	h.out.send(h.t, s)
}

// keys types on the local terminal.
func (h *harness) keys(s string) { _, _ = h.stdin.Write([]byte(s)) }

// waitSessionInput waits until the session's input is exactly want.
func (h *harness) waitSessionInput(want string) {
	h.t.Helper()
	require.Eventually(h.t, func() bool { return h.sessionIn.String() == want }, 5*time.Second, time.Millisecond,
		"the session's input never became %q", want)
}

// waitScreen waits until the real terminal's screen contains s.
func (h *harness) waitScreen(s string) {
	h.t.Helper()
	require.Eventually(h.t, func() bool { return strings.Contains(screenText(h.real), s) }, 10*time.Second, 5*time.Millisecond,
		"the screen never showed %q", s)
}

// show runs Show in the background.
func (h *harness) show(ctx context.Context, model tea.Model) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- h.term.Show(ctx, model) }()
	return ch
}

// result waits for a background call's error.
func (h *harness) result(ch <-chan error) error {
	h.t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(10 * time.Second):
		h.t.Fatal("the call didn't return")
		return nil
	}
}

// locked runs f under the terminal's lock, for reading its state.
func (h *harness) locked(f func(t *Terminal)) {
	h.term.mu.Lock()
	defer h.term.mu.Unlock()
	f(h.term)
}

func firstLine(term *vt.Terminal) string {
	return strings.SplitN(screenText(term), "\n", 2)[0]
}
