package overlay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// ErrClosed is returned by Show once the session has ended.
var ErrClosed = errors.New("overlay: terminal closed")

const (
	// bounceDelay separates the two resizes of the redraw bounce, so apps
	// that coalesce SIGWINCH still see a size change.
	bounceDelay = 100 * time.Millisecond
	// shutdownGrace bounds how long Run waits for an open overlay to
	// restore the screen after the session ended.
	shutdownGrace = 2 * time.Second
)

// Config wires a Terminal to the user's terminal and the session.
type Config struct {
	Stdin        io.Reader // the user's terminal, already in raw mode
	Stdout       io.Writer // the user's terminal, raw mode (no ONLCR)
	ContainerIn  io.Writer // session input
	ContainerOut io.Reader // session output
	// CloseInput, if set, is called once Stdin ends, e.g. to half-close
	// the session connection.
	CloseInput func() error
	// Resize sets the session's terminal size. Size reads the user's.
	// Both are optional; without them alt-screen apps are not asked to
	// redraw after an overlay.
	Resize func(rows, cols int)
	Size   func() (rows, cols int, err error)
	// ProgramOptions are appended to the options of every overlay
	// program, e.g. to pin the environment in tests.
	ProgramOptions []tea.ProgramOption
}

// Terminal passes a session through to the user's terminal and can pause it
// to show a Bubble Tea program on top.
type Terminal struct {
	cfg     Config
	tracker *modeTracker
	in      *inputMux
	out     *outputMux

	// show is a one-slot semaphore serializing overlays.
	show chan struct{}
	// life ends when the session does and cancels any open overlay.
	life context.Context
	end  context.CancelFunc

	bounceDelay time.Duration

	logMu sync.Mutex
	logFn func(format string, args ...any)
}

// New returns a Terminal for cfg. Call Run to start passing data through.
func New(cfg Config) *Terminal {
	tracker := newModeTracker()
	life, end := context.WithCancel(context.Background())
	in := newInputMux(cfg.Stdin, cfg.ContainerIn)
	in.cprReply = tracker.cprReply
	return &Terminal{
		cfg:         cfg,
		tracker:     tracker,
		in:          in,
		out:         newOutputMux(cfg.ContainerOut, cfg.Stdout, tracker),
		show:        make(chan struct{}, 1),
		life:        life,
		end:         end,
		bounceDelay: bounceDelay,
	}
}

// SetLogf sets where diagnostics go. Nothing is written to Stdout outside
// an overlay, so without it they are dropped.
func (t *Terminal) SetLogf(logf func(format string, args ...any)) {
	t.logMu.Lock()
	defer t.logMu.Unlock()
	t.logFn = logf
}

// Run passes both streams through until the session output ends or ctx is
// done. When Stdin ends first, the session input is closed and Run keeps
// draining output. An overlay open at the end is cancelled, and Run waits
// briefly for it to restore the screen.
func (t *Terminal) Run(ctx context.Context) error {
	defer t.shutdown()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// A read blocked on the source cannot be interrupted, so on ctx a pump
	// is left behind until its next read returns.
	outDone := make(chan error, 1)
	inDone := make(chan error, 1)
	go func() { outDone <- t.out.pump() }()
	go func() { inDone <- t.in.pump() }()

	select {
	case err := <-outDone:
		return err
	case <-inDone:
		// Without input an overlay cannot be answered.
		t.end()
		// Half-close once the queued input reached the session. A session
		// that does not read it must not hold up draining its output.
		go func() {
			_ = t.in.closeSession()
			if t.cfg.CloseInput != nil {
				_ = t.cfg.CloseInput()
			}
		}()
		select {
		case err := <-outDone:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *Terminal) shutdown() {
	t.end()
	select {
	case t.show <- struct{}{}:
	case <-time.After(shutdownGrace):
	}
}

// Show pauses the session, runs model over it, and restores the session's
// screen and input. Concurrent calls wait for their turn. The program is
// cancelled with ctx or when the session ends. Restoring the screen does
// not depend on how the program ended.
func (t *Terminal) Show(ctx context.Context, model tea.Model) error {
	select {
	case t.show <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-t.life.Done():
		return ErrClosed
	}
	defer func() { <-t.show }()
	if t.life.Err() != nil {
		return ErrClosed
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(t.life, cancel)()

	cut, resume, err := t.out.Pause(ctx)
	if err != nil {
		return err
	}
	if cut.Aborted || cut.SyncOutput {
		t.logf("overlay: output stayed inside an escape sequence or sync batch for %v, cut anyway", t.out.pauseTimeout)
	}

	in, release, err := t.in.Acquire(ctx)
	if err != nil {
		t.logResume(resume)
		return err
	}

	runErr := t.draw(ctx, in, cut, model)
	release()
	t.logResume(resume)
	if cut.AltScreen {
		t.bounce()
	}
	return runErr
}

func (t *Terminal) logResume(resume func() error) {
	if err := resume(); err != nil {
		t.logf("overlay: replay output: %v", err)
	}
}

// draw runs the program between the enter and leave sequences. Leave is
// deferred, so a panic or a failed program still restores the screen.
func (t *Terminal) draw(ctx context.Context, in *overlayInput, cut cutState, model tea.Model) (err error) {
	t.write(enterSequence(cut))
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("overlay: panic: %v", r)
		}
		t.write(leaveSequence(cut.termModes))
	}()

	opts := []tea.ProgramOption{
		tea.WithInput(in),
		tea.WithOutput(programOutput(t.cfg.Stdout)),
		tea.WithContext(ctx),
		// The process's signals belong to the session owner. ctx covers
		// shutdown.
		tea.WithoutSignalHandler(),
	}
	if t.cfg.Size != nil {
		if rows, cols, err := t.cfg.Size(); err == nil {
			opts = append(opts, tea.WithWindowSize(cols, rows))
		}
	}
	opts = append(opts, t.cfg.ProgramOptions...)

	_, err = tea.NewProgram(quitWatch{Model: model, stop: in.stop}, opts...).Run()
	in.stop()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		err = ctx.Err()
	}
	return err
}

// quitWatch stops the program's input once the program quits, rather than
// when Run returns: Bubble Tea shuts down after quitting, waiting for
// commands still running, and drops the input it reads meanwhile. A quit
// inside a tea.Sequence is not seen; Run returning covers it.
type quitWatch struct {
	tea.Model
	stop func()
}

func (q quitWatch) Init() tea.Cmd { return q.wrap(q.Model.Init()) }

func (q quitWatch) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := q.Model.Update(msg)
	q.Model = next
	return q, q.wrap(cmd)
}

func (q quitWatch) wrap(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		msg := cmd()
		switch msg := msg.(type) {
		case tea.QuitMsg:
			q.stop()
		case tea.BatchMsg:
			for i, c := range msg {
				msg[i] = q.wrap(c)
			}
		}
		return msg
	}
}

// bounce makes an alt-screen app redraw by shrinking and restoring its
// terminal size. The overlay drew over the app's screen, and only the app
// knows what belongs there.
func (t *Terminal) bounce() {
	if t.cfg.Size == nil || t.cfg.Resize == nil {
		return
	}
	rows, cols, err := t.cfg.Size()
	if err != nil || rows < 2 {
		return
	}
	t.cfg.Resize(rows-1, cols)
	select {
	case <-time.After(t.bounceDelay):
	case <-t.life.Done():
	}
	// Read the size again: the user may have resized in the meantime.
	if r, c, err := t.cfg.Size(); err == nil {
		rows, cols = r, c
	}
	t.cfg.Resize(rows, cols)
}

func (t *Terminal) write(p []byte) {
	if _, err := t.cfg.Stdout.Write(p); err != nil {
		t.logf("overlay: write: %v", err)
	}
}

func (t *Terminal) logf(format string, args ...any) {
	t.logMu.Lock()
	logFn := t.logFn
	t.logMu.Unlock()
	if logFn != nil {
		logFn(format, args...)
	}
}

// enterSequence prepares the screen for an overlay at cut. An escape
// sequence the app was stuck in is aborted, and an open synchronized-output
// batch ended, so the overlay's output takes effect. On the main screen the
// overlay gets a fresh alternate screen, so leaving it restores the app's
// screen and cursor exactly. On the alternate screen it draws over the
// app, saving the cursor to put it back.
//
// Either way the overlay starts with the full screen as scroll region,
// origin mode off, replace mode, autowrap, the ASCII character set, no open
// hyperlink, default attributes, and a visible cursor. The cursor save and
// the screen switch also save origin mode, character sets, and attributes,
// so leaving restores those. Keyboard and mouse reporting stay as the app
// set them: Bubble Tea reads any of the encodings.
func enterSequence(c cutState) []byte {
	var b strings.Builder
	if c.Aborted {
		b.WriteByte(byteCAN)
	}
	if c.SyncOutput {
		b.WriteString(ansi.ResetModeSynchronizedOutput)
	}
	b.WriteString(pick(c.AltScreen, ansi.SaveCursor, ansi.SetModeAltScreenSaveCursor))
	for _, seq := range []string{
		resetMargins,
		ansi.ResetModeOrigin,
		ansi.ResetModeInsertReplace,
		ansi.SetModeAutoWrap,
		asciiCharset,
		closeHyperlink,
		ansi.ResetStyle,
		ansi.EraseEntireScreen,
		ansi.CursorHomePosition,
		ansi.ShowCursor,
	} {
		b.WriteString(seq)
	}
	return []byte(b.String())
}

// leaveSequence undoes enterSequence and whatever the overlay program left
// behind, and puts back the app's scroll region, insert mode, autowrap, and
// cursor visibility. On the alternate screen the overlay's drawing is
// cleared: the app redraws after the resize bounce. Setting the scroll
// region moves the cursor, so it comes before the cursor is restored. On
// the main screen it comes before the switch back, which reaches terminals
// that share one region between screens and is harmless on the others.
// An open hyperlink or synchronized-output batch is not reopened.
func leaveSequence(m termModes) []byte {
	var b strings.Builder
	if m.AltScreen {
		b.WriteString(ansi.ResetStyle + ansi.EraseEntireScreen)
		b.Write(m.MarginSequence())
		b.WriteString(ansi.RestoreCursor)
	} else {
		b.Write(m.MarginSequence())
		b.WriteString(ansi.ResetModeAltScreenSaveCursor)
	}
	b.WriteString(pick(m.InsertMode, ansi.SetModeInsertReplace, ansi.ResetModeInsertReplace))
	b.WriteString(pick(m.AutoWrap, ansi.SetModeAutoWrap, ansi.ResetModeAutoWrap))
	b.WriteString(pick(m.CursorVisible, ansi.ShowCursor, ansi.HideCursor))
	return []byte(b.String())
}

// Sequences ansi has no exact constant for: its DECSTBM helper emits
// CSI ; r, and its hyperlink reset ends with BEL rather than ST.
const (
	resetMargins   = "\x1b[r"
	asciiCharset   = "\x1b(B\x0f" // G0 = ASCII, shift in G0
	closeHyperlink = "\x1b]8;;\x1b\\"
)

func pick(cond bool, yes, no string) string {
	if cond {
		return yes
	}
	return no
}
