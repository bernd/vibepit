package overlay

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	vt "github.com/unixshells/vt-go"
)

// boxModel renders a multi-line prompt and quits on the first key.
type boxModel struct{ keyModel }

func (m boxModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.keyModel.Update(msg)
	return boxModel{next.(keyModel)}, cmd
}

func (m boxModel) View() tea.View {
	return tea.NewView("\x1b[1;31mblocked\x1b[m evil.example:443\n\nAllow this connection?\n[a] allow  [n] deny")
}

// lockedEmu serializes writes and screen reads: cells are read through
// pointers, which the emulator's own lock does not cover.
type lockedEmu struct {
	mu  sync.Mutex
	emu *vt.SafeEmulator
}

func (l *lockedEmu) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.emu.Write(p)
}

func (l *lockedEmu) Read(p []byte) (int, error) { return l.emu.Read(p) }

func (l *lockedEmu) CursorPosition() (x, y int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	p := l.emu.CursorPosition()
	return p.X, p.Y
}

func (l *lockedEmu) IsAltScreen() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.emu.IsAltScreen()
}

// emuHarness runs a Terminal whose Stdout is a terminal emulator, and whose
// Stdin receives the emulator's replies to queries, like a real terminal.
type emuHarness struct {
	*termHarness
	emu *lockedEmu

	mu      sync.Mutex
	resizes []string
}

func newEmuHarness(t *testing.T, cols, rows int) *emuHarness {
	t.Helper()
	h := &emuHarness{emu: &lockedEmu{emu: vt.NewSafeEmulator(cols, rows)}}
	resize := func(r, c int) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.resizes = append(h.resizes, fmt.Sprintf("%dx%d", r, c))
	}
	h.termHarness = startTerm(t, h.emu, rows, cols, resize)
	// No emulator Close on cleanup: vt-go races Close against a blocked Read.
	go func() { _, _ = io.Copy(h.stdinW, h.emu) }()
	return h
}

func (h *emuHarness) screen() string {
	h.emu.mu.Lock()
	defer h.emu.mu.Unlock()
	e := h.emu.emu
	var lines []string
	for y := range e.Height() {
		var b strings.Builder
		for x := range e.Width() {
			if c := e.CellAt(x, y); c != nil && c.Content != "" {
				b.WriteString(c.Content)
			} else {
				b.WriteByte(' ')
			}
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// prompt shows a prompt, waits for it to be drawn, presses key, and waits
// for the prompt to close.
func (h *emuHarness) prompt(t *testing.T, key string, whileShown func()) {
	t.Helper()
	var got string
	done := make(chan error, 1)
	go func() { done <- h.term.Show(context.Background(), boxModel{keyModel{got: &got}}) }()
	waitFor(t, func() bool { return strings.Contains(h.screen(), "Allow this connection?") }, "prompt drawn")
	if whileShown != nil {
		whileShown()
	}
	// Let the prompt's own queries settle before typing.
	time.Sleep(20 * time.Millisecond)
	_, _ = h.stdinW.Write([]byte(key))
	require.NoError(t, waitErr(t, done))
	assert.Equal(t, key, got)
}

func TestEmulator_MainScreenRestoredExactly(t *testing.T) {
	h := newEmuHarness(t, 60, 12)
	h.output(t, "$ make test\r\n\x1b[32mok\x1b[m  github.com/x/y\r\n$ ")
	waitFor(t, func() bool { return strings.HasSuffix(h.screen(), "$") }, "shell drawn")
	before := h.screen()
	cx, cy := h.emu.CursorPosition()

	h.prompt(t, "a", func() {
		assert.NotContains(t, h.screen(), "make test", "prompt has the screen to itself")
		assert.Contains(t, h.screen(), "\nAllow this connection?\n[a] allow", "lines start at the left edge")
		h.output(t, "echo")
	})

	waitFor(t, func() bool { return strings.Contains(h.screen(), "$ echo") }, "held output replayed")
	after := h.screen()
	assert.Equal(t, before+" echo", after, "screen is exactly what it was, plus the replayed output")
	assert.False(t, h.emu.IsAltScreen())
	x, y := h.emu.CursorPosition()
	assert.Equal(t, cy, y, "cursor back on the prompt line")
	assert.Equal(t, cx+4, x, "cursor after the replayed output")
	assert.NotContains(t, after, "Allow", "no prompt remnants")
	assert.Empty(t, h.session.String(), "neither the key nor query replies reach the session")

	h.mu.Lock()
	assert.Empty(t, h.resizes, "no redraw bounce on the main screen")
	h.mu.Unlock()
}

func TestEmulator_AltScreenApp(t *testing.T) {
	h := newEmuHarness(t, 60, 12)
	h.output(t, "$ vim\r\n\x1b[?1049h\x1b[H\x1b[2J~\r\n~\r\n\x1b[5;10Hcursor")
	waitFor(t, func() bool { return strings.Contains(h.screen(), "cursor") }, "app drawn")
	cx, cy := h.emu.CursorPosition()

	h.prompt(t, "n", nil)

	assert.True(t, h.emu.IsAltScreen(), "app stays on the alternate screen")
	assert.NotContains(t, h.screen(), "Allow", "prompt cleared")
	x, y := h.emu.CursorPosition()
	assert.Equal(t, [2]int{cx, cy}, [2]int{x, y}, "cursor where the app left it")
	waitFor(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return strings.Join(h.resizes, ",") == "11x60,12x60"
	}, "app asked to redraw")

	// Leaving the app shows the untouched main screen.
	h.output(t, "\x1b[?1049l")
	waitFor(t, func() bool { return !h.emu.IsAltScreen() }, "app left the alternate screen")
	assert.Equal(t, "$ vim", h.screen())
}

func TestEmulator_ScreenSwitchRightAfterCut(t *testing.T) {
	// The app switches to the alternate screen right after the cut. The
	// overlay must still treat the terminal as on the main screen, or it
	// would draw over and clear the main screen.
	h := newEmuHarness(t, 60, 12)
	h.output(t, "$ history\r\n$ vim\r\n\x1b[?25")
	waitFor(t, func() bool { return strings.Contains(h.screen(), "$ vim") }, "shell drawn")

	var got string
	done := make(chan error, 1)
	go func() { done <- h.term.Show(context.Background(), boxModel{keyModel{got: &got}}) }()
	time.Sleep(10 * time.Millisecond)
	h.output(t, "l")
	h.output(t, "\x1b[?1049h\x1b[H\x1b[2JAPP")
	waitFor(t, func() bool { return strings.Contains(h.screen(), "Allow this connection?") }, "prompt drawn")
	time.Sleep(20 * time.Millisecond)
	_, _ = h.stdinW.Write([]byte("a"))
	require.NoError(t, waitErr(t, done))

	waitFor(t, func() bool { return strings.Contains(h.screen(), "APP") }, "app drawn after replay")
	assert.True(t, h.emu.IsAltScreen())
	h.output(t, "\x1b[?1049l")
	waitFor(t, func() bool { return !h.emu.IsAltScreen() }, "app left")
	assert.Equal(t, "$ history\n$ vim", h.screen(), "main screen intact")
}

func TestEmulator_AltScreenScrollRegion(t *testing.T) {
	h := newEmuHarness(t, 60, 12)
	h.output(t, "\x1b[?1049h\x1b[H\x1b[2Jtop\r\nsecond\x1b[1;2r\x1b[2;1H")
	waitFor(t, func() bool { return strings.Contains(h.screen(), "second") }, "app drawn")

	h.prompt(t, "a", func() {
		scr := h.screen()
		assert.Contains(t, scr, "evil.example:443", "first prompt line visible despite the app's margins")
		assert.Contains(t, scr, "Allow this connection?")
	})

	_, y := h.emu.CursorPosition()
	assert.Equal(t, 1, y, "cursor back on the app's row")
	// Line feeds at the bottom margin scroll the region instead of moving
	// the cursor down: the app's margins are back.
	h.output(t, "\n\n\nX")
	waitFor(t, func() bool { return strings.Contains(h.screen(), "X") }, "written")
	_, y = h.emu.CursorPosition()
	assert.Equal(t, 1, y, "scroll region restored")
}

func TestEmulator_PromptWaitsForSequence(t *testing.T) {
	// The app stops in the middle of an SGR sequence, after a line feed
	// that already took effect. The prompt waits for the sequence to end
	// instead of drawing into it.
	h := newEmuHarness(t, 60, 12)
	h.output(t, "shell\x1b[31\n")
	waitFor(t, func() bool {
		_, y := h.emu.CursorPosition()
		return y == 1
	}, "line feed executed")

	var got string
	done := make(chan error, 1)
	go func() { done <- h.term.Show(context.Background(), boxModel{keyModel{got: &got}}) }()
	time.Sleep(300 * time.Millisecond)
	assert.NotContains(t, h.screen(), "Allow", "no prompt inside the sequence")

	h.output(t, "mRED\x1b[m")
	waitFor(t, func() bool { return strings.Contains(h.screen(), "Allow this connection?") }, "prompt drawn")
	time.Sleep(20 * time.Millisecond)
	_, _ = h.stdinW.Write([]byte("a"))
	require.NoError(t, waitErr(t, done))

	// vt-go drops the SGR when a control interrupts the CSI, so only the
	// text is checked: one line feed, and no "m" printed.
	assert.Equal(t, "shell\n     RED", h.screen(), "one line feed, sequence completed")
}

func TestEmulator_MarginsLeftOnAltScreen(t *testing.T) {
	// An app sets margins on the alternate screen and exits without
	// resetting them. The overlay must not restore them onto the shell.
	h := newEmuHarness(t, 60, 12)
	h.output(t, "$ app\r\n\x1b[?1049h\x1b[1;2r\x1b[?1049l$ ")
	waitFor(t, func() bool { return strings.Contains(h.screen(), "$ app\n$") }, "shell drawn")

	h.prompt(t, "a", nil)

	h.output(t, "\r\n1\r\n2\r\n3\r\n4")
	waitFor(t, func() bool { return strings.Contains(h.screen(), "4") }, "written")
	_, y := h.emu.CursorPosition()
	assert.Equal(t, 5, y, "line feeds move down the whole screen")
	assert.Equal(t, "$ app\n$\n1\n2\n3\n4", h.screen())
}

func TestEmulator_LineDrawingCharset(t *testing.T) {
	// An ncurses app switched to DEC line drawing. The prompt must still
	// be text. Restoring the app's charset afterwards is left to the
	// terminal's cursor save, which covers character sets per the VT spec
	// and in xterm, but not in vt-go, so it is not checked here.
	h := newEmuHarness(t, 60, 12)
	h.output(t, "\x1b(0lqk")
	waitFor(t, func() bool { return strings.Contains(h.screen(), "┌─┐") }, "box drawn")

	h.prompt(t, "a", func() {
		assert.Contains(t, h.screen(), "Allow this connection?", "prompt text is not line drawing")
	})
	assert.Equal(t, "┌─┐", h.screen(), "main screen intact")
}

func TestEmulator_StuckSyncBatch(t *testing.T) {
	// The app died inside a synchronized-output batch. The prompt still
	// appears, after the pause timeout, and the session works afterwards.
	h := newEmuHarness(t, 60, 12)
	h.term.out.pauseTimeout = 200 * time.Millisecond
	h.output(t, "$ tui\r\n\x1b[?2026hframe")
	waitFor(t, func() bool { return strings.Contains(h.screen(), "frame") }, "app drawn")

	h.prompt(t, "a", nil)

	assert.Equal(t, "$ tui\nframe", h.screen(), "main screen intact")
	h.output(t, "\r\n$ ")
	waitFor(t, func() bool { return strings.HasSuffix(h.screen(), "$") }, "shell continues")
	assert.False(t, h.term.tracker.Snapshot().SyncOutput, "the overlay ended the batch")
}
