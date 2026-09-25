package overlay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bernd/vibepit/vt"
	"github.com/charmbracelet/colorprofile"
)

// barrierTimeoutsBeforeDegraded consecutive barrier timeouts switch T2 to
// waiting for input silence.
const barrierTimeoutsBeforeDegraded = 2

// Config connects a Terminal to the local terminal and the container.
type Config struct {
	Stdin        io.Reader // the local terminal, already in raw mode
	Stdout       io.Writer
	ContainerIn  io.Writer
	ContainerOut io.Reader
	// CloseInput half-closes the container's input after stdin ends.
	CloseInput func() error
	// Resize resizes the container's PTY.
	Resize func(cols, rows int)
	// Size reports the local terminal's size.
	Size func() (cols, rows int, err error)
	// Environ is the prompt program's environment; nil means os.Environ.
	// Its TERM picks the sequences Bubble Tea's renderer uses.
	Environ []string
	// Logf receives diagnostics; nil discards them.
	Logf func(format string, args ...any)
	// NoShadow skips the emulator for sessions that never prompt. Output
	// passes through, and Show returns ErrUnavailable.
	NoShadow bool
}

// timing holds the spec's budgets; tests shorten them.
type timing struct {
	positionWait time.Duration // Run: wait for the cursor position reply
	groundWait   time.Duration // T1: longest wait for ground before a forced cut
	groundBytes  int           // T1: most bytes forwarded while waiting for ground
	barrierWait  time.Duration // T2: wait for the barrier reply
	silence      time.Duration // T2 without barrier support: input quiet time
	silenceMax   time.Duration // T2 without barrier support: longest wait for quiet
	lateStrip    time.Duration // after a barrier or position timeout: drop a late reply
	nudgeGap     time.Duration // between the two resizes of a repaint nudge
	logMax       int           // raw log cap
}

var defaultTiming = timing{
	positionWait: 500 * time.Millisecond,
	groundWait:   250 * time.Millisecond,
	groundBytes:  64 << 10,
	barrierWait:  500 * time.Millisecond,
	silence:      100 * time.Millisecond,
	silenceMax:   time.Second,
	lateStrip:    10 * time.Second,
	nudgeGap:     100 * time.Millisecond,
	logMax:       4 << 20,
}

var errNoShadow = errors.New("no shadow terminal")

// Terminal forwards a container session to the local terminal and shows
// prompts over it. Create it with New and run it with Run.
type Terminal struct {
	cfg     Config
	timing  timing
	in      *inputMux
	toCont  *containerInput
	environ []string
	profile colorprofile.Profile
	logf    func(format string, args ...any)
	done    chan struct{} // closed when Run returns

	showMu sync.Mutex // one Show at a time; finish takes it to wait for one
	// resizeMu orders whole resizes and repaint nudges, so the shadow and
	// the container end at the same, latest size. The container's resize
	// runs outside mu.
	resizeMu sync.Mutex

	mu              sync.Mutex // guards everything below
	shadow          *vt.Terminal
	shadowErr       error // set: prompts are unavailable
	cols, rows      int
	attached        bool
	detach          *detachReq // a T1 waiting for ground
	cut             *cutState  // from T1 to T3
	log             rawLog     // output since the cut
	answers         []byte     // the shadow's answers during the current call
	answered        bool       // the shadow answered a query while detached
	resized         bool       // resized while detached
	resyncing       bool       // drop output up to the next ground
	misaligned      bool       // the shadow's cursor may not be the real one's
	prog            *tea.Program
	closed          bool
	barrierTimeouts int
	// snapshot is snapshotLeave; tests replace it to inject failures.
	snapshot func(*vt.Terminal, *cutState, entered) ([]byte, bool, error)
}

// detachReq is a pending T1: the pump cuts at the next byte where the
// shadow is at ground.
type detachReq struct {
	done      chan struct{}
	forwarded int   // bytes forwarded while waiting for ground
	err       error // why no cut happened
}

// rawLog holds the container output since the cut, for raw replay.
type rawLog struct {
	buf      []byte
	overflow bool
}

// append keeps p unless the log would pass max. Past it the log is dropped,
// which rules out raw replay; the pump never waits.
func (l *rawLog) append(p []byte, max int) {
	if l.overflow {
		return
	}
	if len(l.buf)+len(p) > max {
		l.buf, l.overflow = nil, true
		return
	}
	l.buf = append(l.buf, p...)
}

// New sets up a Terminal. A shadow that can't be created only turns
// prompts off.
func New(cfg Config) *Terminal {
	t := &Terminal{
		cfg:      cfg,
		timing:   defaultTiming,
		done:     make(chan struct{}),
		environ:  cfg.Environ,
		logf:     cfg.Logf,
		attached: true,
		snapshot: snapshotLeave,
	}
	if t.environ == nil {
		t.environ = os.Environ()
	}
	if t.logf == nil {
		t.logf = func(string, ...any) {}
	}
	t.profile = colorprofile.Detect(cfg.Stdout, t.environ)
	t.toCont = newContainerInput(cfg.ContainerIn)
	t.in = newInputMux(cfg.Stdin, t.toCont)
	t.cols, t.rows = t.size()
	if cfg.NoShadow {
		t.shadowErr = errNoShadow
		return t
	}
	// vt's defaults fit: the snapshot uses only the visible screen, and
	// 64 KiB of continuation covers OSC 52 clipboard writes and kitty
	// graphics.
	sh, err := vt.NewTerminal(uint16(t.cols), uint16(t.rows),
		// Runs inside shadow calls, which all hold t.mu.
		vt.WithWritePty(func(b []byte) { t.answers = append(t.answers, b...) }),
	)
	if err != nil {
		t.shadowErr = err
		t.logf("overlay: no shadow terminal, prompts disabled: %v", err)
		return t
	}
	t.shadow = sh
	return t
}

// Run forwards stdin and the container's output until the output ends or
// ctx is done. When stdin ends it half-closes the container's input and
// keeps forwarding output.
func (t *Terminal) Run(ctx context.Context) error {
	defer t.finish()
	go t.toCont.run()
	inDone := make(chan struct{})
	go func() {
		defer close(inDone)
		if err := t.in.Run(); err != nil {
			t.logf("overlay: stdin: %v", err)
		}
		t.toCont.flush()
		if t.cfg.CloseInput != nil {
			_ = t.cfg.CloseInput()
		}
	}()
	outDone := make(chan error, 1)
	go func() {
		t.align(ctx)
		outDone <- t.pump()
	}()
	select {
	case err := <-outDone:
		return err
	case <-inDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-outDone:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// finish ends the session for Show. A prompt that is showing is cancelled
// and restores the screen before Run returns.
func (t *Terminal) finish() {
	close(t.done)
	t.toCont.close()
	t.showMu.Lock()
	defer t.showMu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	if t.shadow != nil {
		_ = t.shadow.Close()
	}
}

// align moves the shadow's cursor to the real terminal's before any
// output: the session starts wherever the host left the cursor, the shadow
// at its top left, and the raw leave restores the cursor by absolute
// position. Lines above the session stay blank in the shadow. Without a
// reply the leave restores from the shadow instead. It holds t.mu while
// waiting, so a Show can't cut before the shadow is aligned.
func (t *Terminal) align(ctx context.Context) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.shadowErr != nil {
		return
	}
	t.in.ExpectPosition()
	_, _ = io.WriteString(t.cfg.Stdout, positionQuery)
	row, col, err := t.in.AwaitPosition(ctx, t.timing.positionWait, t.timing.lateStrip)
	if err != nil {
		t.misaligned = true
		t.logf("overlay: no cursor position from the terminal, prompts restore the screen from the shadow: %v", err)
		return
	}
	t.shadowWriteLocked(fmt.Appendf(nil, "\x1b[%d;%dH", row, col))
}

func (t *Terminal) pump() error {
	buf := make([]byte, 32<<10)
	for {
		n, err := t.cfg.ContainerOut.Read(buf)
		if n > 0 {
			t.output(buf[:n])
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// output handles one chunk of container output. Resync runs first: while
// it drops a sequence tail the shadow isn't at ground, so a pending detach
// can only cut after it.
func (t *Terminal) output(p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.resyncing && t.shadowErr == nil {
		p = t.resyncLocked(p)
	}
	if t.detach != nil {
		p = t.advanceDetachLocked(p)
	}
	t.shadowWriteLocked(p)
	switch {
	case len(p) == 0:
	case t.attached:
		_, _ = t.cfg.Stdout.Write(p)
	default:
		t.log.append(p, t.timing.logMax)
	}
}

func (t *Terminal) shadowWriteLocked(p []byte) {
	if t.shadowErr != nil || len(p) == 0 {
		return
	}
	if _, err := t.shadow.Write(p); err != nil {
		t.failLocked(err)
	}
	t.flushAnswersLocked()
}

// failLocked turns prompts off after the shadow failed. Output keeps
// passing through.
func (t *Terminal) failLocked(err error) {
	if t.shadowErr != nil {
		return
	}
	t.shadowErr = err
	if !t.closed {
		t.logf("overlay: shadow terminal failed, prompts disabled: %v", err)
	}
}

// flushAnswersLocked sends the shadow's answers to the container while
// detached. While attached the real terminal answers the same queries, so
// the shadow's answers are dropped.
func (t *Terminal) flushAnswersLocked() {
	if len(t.answers) == 0 {
		return
	}
	if !t.attached {
		_, _ = t.toCont.Write(t.answers)
		t.answered = true
	}
	t.answers = t.answers[:0]
}

// Resize applies the local terminal's size: to the shadow first, so its
// layout matches what the app redraws for, then to the container, then to
// a prompt that is showing.
func (t *Terminal) Resize() {
	t.resizeMu.Lock()
	defer t.resizeMu.Unlock()
	cols, rows := t.size()
	t.mu.Lock()
	changed := cols != t.cols || rows != t.rows
	t.cols, t.rows = cols, rows
	if changed && t.shadowErr == nil {
		if err := t.shadow.Resize(uint16(cols), uint16(rows)); err != nil {
			t.failLocked(err)
		}
		t.flushAnswersLocked()
	}
	if changed && !t.attached {
		// The log was written for the old geometry.
		t.resized = true
	}
	prog := t.prog
	t.mu.Unlock()
	if t.cfg.Resize != nil {
		t.cfg.Resize(cols, rows)
	}
	if prog != nil {
		// Bubble Tea can't see SIGWINCH: its output isn't a TTY.
		go prog.Send(tea.WindowSizeMsg{Width: cols, Height: rows})
	}
}

// size is the local terminal's size, or 80x24 when unknown.
func (t *Terminal) size() (int, int) {
	cols, rows := 80, 24
	if t.cfg.Size != nil {
		if c, r, err := t.cfg.Size(); err == nil && c > 0 && r > 0 {
			cols, rows = c, r
		}
	}
	return min(cols, math.MaxUint16), min(rows, math.MaxUint16)
}

func (t *Terminal) isDone() bool { return isClosed(t.done) }

// barrierUnsupportedLocked tells whether T2 waits for input silence
// instead of the barrier reply. The count stops once it is reached.
func (t *Terminal) barrierUnsupportedLocked() bool {
	return t.barrierTimeouts >= barrierTimeoutsBeforeDegraded
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
