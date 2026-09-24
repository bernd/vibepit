package overlay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// eventLog records terminal writes and resizes in one ordered stream.
type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) Write(p []byte) (int, error) {
	l.add(string(p))
	return len(p), nil
}

func (l *eventLog) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, s)
}

func (l *eventLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.events, "")
}

// keyModel quits on the first key press and remembers it.
type keyModel struct {
	got   *string
	panic bool
}

func (m keyModel) Init() tea.Cmd { return nil }

func (m keyModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		if m.panic {
			panic("boom")
		}
		*m.got = k.String()
		return m, tea.Quit
	}
	return m, nil
}

func (m keyModel) View() tea.View {
	v := tea.NewView("PROMPT")
	v.AltScreen = true // the filter must keep this from switching screens
	return v
}

// termHarness is a Terminal between fake stdio pipes and a fake session.
type termHarness struct {
	term    *Terminal
	stdinW  *io.PipeWriter
	outW    *io.PipeWriter
	session *syncBuffer
	runDone chan error
}

func startTerm(t *testing.T, stdout io.Writer, rows, cols int, resize func(rows, cols int), opts ...tea.ProgramOption) *termHarness {
	t.Helper()
	stdin, stdinW := io.Pipe()
	out, outW := io.Pipe()
	h := &termHarness{
		stdinW:  stdinW,
		outW:    outW,
		session: &syncBuffer{},
		runDone: make(chan error, 1),
	}
	h.term = New(Config{
		Stdin:          stdin,
		Stdout:         stdout,
		ContainerIn:    h.session,
		ContainerOut:   out,
		Size:           func() (int, int, error) { return rows, cols, nil },
		Resize:         resize,
		ProgramOptions: append([]tea.ProgramOption{tea.WithEnvironment([]string{"TERM=xterm-256color"})}, opts...),
	})
	h.term.bounceDelay = time.Millisecond
	go func() { h.runDone <- h.term.Run(context.Background()) }()
	t.Cleanup(func() {
		stdinW.Close()
		outW.Close()
	})
	return h
}

func (h *termHarness) output(t *testing.T, s string) {
	t.Helper()
	_, err := h.outW.Write([]byte(s))
	require.NoError(t, err)
}

func (h *termHarness) show(ctx context.Context, m tea.Model) chan error {
	done := make(chan error, 1)
	go func() { done <- h.term.Show(ctx, m) }()
	return done
}

// harness records terminal writes and resizes in one ordered log.
type harness struct {
	*termHarness
	stdout *eventLog
}

func newHarness(t *testing.T, opts ...tea.ProgramOption) *harness {
	t.Helper()
	h := &harness{stdout: &eventLog{}}
	resize := func(rows, cols int) { h.stdout.add(fmt.Sprintf("<resize %dx%d>", rows, cols)) }
	opts = append([]tea.ProgramOption{tea.WithColorProfile(colorprofile.NoTTY)}, opts...)
	h.termHarness = startTerm(t, h.stdout, 24, 80, resize, opts...)
	return h
}

func waitErr(t *testing.T, ch chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("timed out")
		return nil
	}
}

// assertOrder checks that each part occurs in s after the previous one.
func assertOrder(t *testing.T, s string, parts ...string) {
	t.Helper()
	pos := 0
	for _, p := range parts {
		i := strings.Index(s[pos:], p)
		if !assert.GreaterOrEqual(t, i, 0, "%q not found after offset %d in %q", p, pos, s) {
			return
		}
		pos += i + len(p)
	}
}

func TestTerminal_Show(t *testing.T) {
	tests := []struct {
		name    string
		setup   string // session output before the overlay
		enter   string
		leave   string
		bounced bool
	}{
		{
			name:  "main screen",
			setup: "$ ",
			enter: "\x1b[?1049h\x1b[r\x1b[?6l\x1b[4l\x1b[?7h\x1b(B\x0f\x1b]8;;\x1b\\\x1b[m\x1b[2J\x1b[H\x1b[?25h",
			leave: "\x1b[r\x1b[?1049l\x1b[4l\x1b[?7h\x1b[?25h",
		},
		{
			name:    "alt screen app",
			setup:   "\x1b[?1049h\x1b[?1000h\x1b[?2004h\x1b[?25l\x1b[4h\x1b[?7l\x1b[2;20rvim",
			enter:   "\x1b7\x1b[r\x1b[?6l\x1b[4l\x1b[?7h\x1b(B\x0f\x1b]8;;\x1b\\\x1b[m\x1b[2J\x1b[H\x1b[?25h",
			leave:   "\x1b[m\x1b[2J\x1b[2;20r\x1b8\x1b[4h\x1b[?7l\x1b[?25l",
			bounced: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.output(t, tt.setup)
			waitFor(t, func() bool { return h.stdout.String() == tt.setup }, "session output passes through")

			var got string
			done := h.show(context.Background(), keyModel{got: &got})
			waitFor(t, func() bool { return strings.Contains(h.stdout.String(), "PROMPT") }, "prompt drawn")

			h.output(t, "during")
			assert.NotContains(t, h.stdout.String(), "during", "session output is held while the prompt shows")

			_, _ = h.stdinW.Write([]byte("y"))
			require.NoError(t, waitErr(t, done))
			assert.Equal(t, "y", got, "prompt gets the key")
			assert.Empty(t, h.session.String(), "session does not get the prompt's key")

			out := h.stdout.String()
			parts := []string{tt.setup, tt.enter, "PROMPT", tt.leave, "during"}
			if tt.bounced {
				parts = append(parts, "<resize 23x80>", "<resize 24x80>")
			} else {
				assert.NotContains(t, out, "<resize", "no redraw needed on the main screen")
			}
			assertOrder(t, out, parts...)

			program := out[strings.Index(out, tt.enter)+len(tt.enter) : strings.LastIndex(out, tt.leave)]
			assert.NotContains(t, program, "\x1b[?1049", "program must not switch screens")
			for _, seq := range []string{"\x1b[>", "\x1b[<", "\x1b[=", "\x1b[?2004", "\x1b[?2027", "$p", "\x1b]", "\x1bP"} {
				assert.NotContains(t, program, seq, "keyboard, mode, OSC, and query sequences are filtered")
			}

			_, _ = h.stdinW.Write([]byte("ls"))
			waitFor(t, func() bool { return h.session.String() == "ls" }, "input goes back to the session")
			h.output(t, " after")
			waitFor(t, func() bool { return strings.HasSuffix(h.stdout.String(), " after") }, "output passes through again")
		})
	}
}

func TestTerminal_ShowRestoresOnError(t *testing.T) {
	leave := "\x1b[r\x1b[?1049l\x1b[4l\x1b[?7h\x1b[?25h"

	t.Run("panic", func(t *testing.T) {
		// Let the panic reach Show instead of Bubble Tea's own handler.
		h := newHarness(t, tea.WithoutCatchPanics())
		var got string
		done := h.show(context.Background(), keyModel{got: &got, panic: true})
		waitFor(t, func() bool { return strings.Contains(h.stdout.String(), "PROMPT") }, "prompt drawn")
		_, _ = h.stdinW.Write([]byte("y"))
		assert.ErrorContains(t, waitErr(t, done), "panic: boom")
		assert.True(t, strings.HasSuffix(h.stdout.String(), leave), "screen restored after panic")

		_, _ = h.stdinW.Write([]byte("x"))
		waitFor(t, func() bool { return h.session.String() == "x" }, "input goes back to the session")
	})

	t.Run("cancelled", func(t *testing.T) {
		h := newHarness(t)
		ctx, cancel := context.WithCancel(context.Background())
		var got string
		done := h.show(ctx, keyModel{got: &got})
		waitFor(t, func() bool { return strings.Contains(h.stdout.String(), "PROMPT") }, "prompt drawn")
		cancel()
		assert.ErrorIs(t, waitErr(t, done), context.Canceled)
		assert.True(t, strings.HasSuffix(h.stdout.String(), leave), "screen restored after cancel")

		_, _ = h.stdinW.Write([]byte("x"))
		waitFor(t, func() bool { return h.session.String() == "x" }, "input goes back to the session")
	})

	t.Run("session ends", func(t *testing.T) {
		h := newHarness(t)
		var got string
		done := h.show(context.Background(), keyModel{got: &got})
		waitFor(t, func() bool { return strings.Contains(h.stdout.String(), "PROMPT") }, "prompt drawn")
		h.output(t, "bye")
		h.outW.Close()
		err := waitErr(t, done)
		assert.True(t, errors.Is(err, context.Canceled), "got %v", err)
		require.NoError(t, waitErr(t, h.runDone))
		assertOrder(t, h.stdout.String(), leave, "bye")

		assert.ErrorIs(t, h.term.Show(context.Background(), keyModel{got: &got}), ErrClosed)
	})
}

func TestTerminal_RepliesReachSessionDuringPrompt(t *testing.T) {
	// The app queried the terminal just before the prompt opened. The reply
	// arrives while the prompt holds the input and must still reach the app.
	h := newHarness(t)
	h.output(t, "\x1b[6n")
	waitFor(t, func() bool { return h.stdout.String() == "\x1b[6n" }, "query sent")
	var got string
	done := h.show(context.Background(), keyModel{got: &got})
	waitFor(t, func() bool { return strings.Contains(h.stdout.String(), "PROMPT") }, "prompt drawn")

	_, _ = h.stdinW.Write([]byte("\x1b[12;40R"))
	waitFor(t, func() bool { return h.session.String() == "\x1b[12;40R" }, "reply reaches the session")
	select {
	case <-done:
		t.Fatal("a reply must not answer the prompt")
	case <-time.After(50 * time.Millisecond):
	}

	_, _ = h.stdinW.Write([]byte("n"))
	require.NoError(t, waitErr(t, done))
	assert.Equal(t, "n", got)
}

func TestTerminal_StdinEndsWhileSessionStuck(t *testing.T) {
	// The session stopped reading its input, and then the user's terminal
	// went away. Overlays end, and output keeps flowing until it ends,
	// though the queued input never reaches the session.
	stdin, stdinW := io.Pipe()
	out, outW := io.Pipe()
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	writing := make(chan struct{}, 1)
	closed := make(chan struct{})
	var stdout syncBuffer
	term := New(Config{
		Stdin:  stdin,
		Stdout: &stdout,
		ContainerIn: writerFunc(func(p []byte) (int, error) {
			writing <- struct{}{}
			<-stuck
			return len(p), nil
		}),
		ContainerOut: out,
		CloseInput:   func() error { close(closed); return nil },
	})
	runDone := make(chan error, 1)
	go func() { runDone <- term.Run(context.Background()) }()

	_, _ = stdinW.Write([]byte("typed"))
	<-writing
	stdinW.Close()
	waitFor(t, func() bool { return term.life.Err() != nil }, "input end noticed")
	var got string
	assert.ErrorIs(t, term.Show(context.Background(), keyModel{got: &got}), ErrClosed)
	select {
	case <-closed:
		t.Fatal("input half-closed before it reached the session")
	default:
	}

	_, _ = outW.Write([]byte("still here"))
	waitFor(t, func() bool { return stdout.String() == "still here" }, "output passes")
	outW.Close()
	require.NoError(t, waitErr(t, runDone))
}

func TestQuitWatch(t *testing.T) {
	stops := 0
	q := quitWatch{stop: func() { stops++ }}
	msg := q.wrap(tea.Quit)()
	assert.IsType(t, tea.QuitMsg{}, msg)
	assert.Equal(t, 1, stops)

	batch := q.wrap(tea.Batch(func() tea.Msg { return nil }, tea.Quit))()
	require.IsType(t, tea.BatchMsg{}, batch)
	for _, c := range batch.(tea.BatchMsg) {
		c()
	}
	assert.Equal(t, 2, stops, "a quit inside a batch counts")

	assert.Nil(t, q.wrap(nil))
	q.wrap(func() tea.Msg { return tea.KeyPressMsg{} })()
	assert.Equal(t, 2, stops)
}

func TestTerminal_BounceUsesCurrentSize(t *testing.T) {
	// A resize during the bounce wins over the size read before it.
	h := newHarness(t)
	rows := 24
	var mu sync.Mutex
	h.term.cfg.Size = func() (int, int, error) {
		mu.Lock()
		defer mu.Unlock()
		return rows, 80, nil
	}
	h.term.bounceDelay = 50 * time.Millisecond
	go func() {
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		rows = 30
		mu.Unlock()
	}()
	h.term.bounce()
	assertOrder(t, h.stdout.String(), "<resize 23x80>", "<resize 30x80>")
}

func TestTerminal_ShowSerializes(t *testing.T) {
	h := newHarness(t)
	var first, second string
	done1 := h.show(context.Background(), keyModel{got: &first})
	waitFor(t, func() bool { return strings.Count(h.stdout.String(), "PROMPT") == 1 }, "first prompt drawn")
	done2 := h.show(context.Background(), keyModel{got: &second})

	_, _ = h.stdinW.Write([]byte("a"))
	require.NoError(t, waitErr(t, done1))
	waitFor(t, func() bool { return strings.Count(h.stdout.String(), "\x1b[?1049h") == 2 }, "second prompt entered")
	// Let the second prompt set up before typing.
	time.Sleep(50 * time.Millisecond)
	_, _ = h.stdinW.Write([]byte("b"))
	require.NoError(t, waitErr(t, done2))
	assert.Equal(t, "a", first)
	assert.Equal(t, "b", second)
}

func TestOutputFilter(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "text and UTF-8", in: "héllo ─", want: "héllo ─"},
		{name: "LF becomes CRLF", in: "a\nb\r\nc", want: "a\r\nb\r\r\nc"},
		{name: "BS, HT, CR pass", in: "a\b\tb\r", want: "a\b\tb\r"},
		{name: "BEL, SO, SI dropped", in: "a\a\x0e\x0fb", want: "ab"},
		{name: "SGR passes", in: "\x1b[1;38:2::255:0:0mX\x1b[m", want: "\x1b[1;38:2::255:0:0mX\x1b[m"},
		{name: "cursor movement and erase pass", in: "\x1b[3;4H\x1b[2A\x1b[5G\x1b[K\x1b[J\x1b[2X\x1b[3d\x1b[4`", want: "\x1b[3;4H\x1b[2A\x1b[5G\x1b[K\x1b[J\x1b[2X\x1b[3d\x1b[4`"},
		{name: "insert, delete, scroll, repeat, margins pass", in: "\x1b[2@\x1b[P\x1b[L\x1b[M\x1b[S\x1b[T\x1b[3b\x1b[1;5r", want: "\x1b[2@\x1b[P\x1b[L\x1b[M\x1b[S\x1b[T\x1b[3b\x1b[1;5r"},
		{name: "reverse index passes", in: "\x1bM", want: "\x1bM"},
		{name: "cursor visibility and sync pass", in: "\x1b[?25l\x1b[?2026h\x1b[?2026l\x1b[?25h", want: "\x1b[?25l\x1b[?2026h\x1b[?2026l\x1b[?25h"},
		{name: "modifyOtherKeys dropped", in: "\x1b[>4;2mX\x1b[>4m", want: "X"},
		{name: "kitty keyboard dropped", in: "\x1b[>1u\x1b[<u\x1b[=0;1u\x1b[?u", want: ""},
		{name: "other modes dropped", in: "\x1b[?2004h\x1b[?2027h\x1b[?1000h\x1b[?1049h\x1b[?7l\x1b[4h\x1b[?5W", want: ""},
		{name: "mixed private modes dropped whole", in: "\x1b[?25;2004h", want: ""},
		{name: "queries dropped", in: "\x1b[?2026$p\x1b[c\x1b[>c\x1b[6n\x1b[14t", want: ""},
		{name: "cursor shape dropped", in: "\x1b[5 q", want: ""},
		{name: "cursor save and restore dropped", in: "\x1b7X\x1b8\x1b[s\x1b[u", want: "X"},
		{name: "OSC dropped", in: "\x1b]0;title\aA\x1b]8;;http://x\x1b\\B\x1b]11;?\x1b\\", want: "AB"},
		{name: "DCS and APC dropped", in: "\x1bP+q544e\x1b\\\x1b_Gi=1\x1b\\X", want: "X"},
		{name: "charset changes dropped", in: "\x1b(0q\x1b(B", want: "q"},
		{name: "LF inside OSC is not rewritten", in: "\x1b]2;a\nb\a", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			n, err := programOutput(&buf).Write([]byte(tt.in))
			require.NoError(t, err)
			assert.Equal(t, len(tt.in), n, "reports the caller's byte count")
			assert.Equal(t, tt.want, buf.String())

			// Byte-at-a-time writing must give the same result.
			buf.Reset()
			w := programOutput(&buf)
			for i := range len(tt.in) {
				_, _ = w.Write([]byte{tt.in[i]})
			}
			assert.Equal(t, tt.want, buf.String(), "split writes")
		})
	}

	_, isFile := programOutput(os.Stdout).(interface{ Fd() uintptr })
	assert.True(t, isFile, "a terminal stays detectable as one")
}

func TestEnterLeaveSequence(t *testing.T) {
	enter := "\x1b[r\x1b[?6l\x1b[4l\x1b[?7h\x1b(B\x0f\x1b]8;;\x1b\\\x1b[m\x1b[2J\x1b[H\x1b[?25h"
	tests := []struct {
		name  string
		cut   cutState
		enter string
		leave string
	}{
		{
			name:  "main screen with margins",
			cut:   cutState{termModes: termModes{CursorVisible: true, AutoWrap: true, ScrollTop: 1, ScrollBottom: 23}},
			enter: "\x1b[?1049h" + enter,
			leave: "\x1b[1;23r\x1b[?1049l\x1b[4l\x1b[?7h\x1b[?25h",
		},
		{
			name:  "forced cut in a sequence and a batch",
			cut:   cutState{termModes: termModes{CursorVisible: true, AutoWrap: true, SyncOutput: true}, Aborted: true},
			enter: "\x18\x1b[?2026l\x1b[?1049h" + enter,
			leave: "\x1b[r\x1b[?1049l\x1b[4l\x1b[?7h\x1b[?25h",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.enter, string(enterSequence(tt.cut)))
			assert.Equal(t, tt.leave, string(leaveSequence(tt.cut.termModes)))
		})
	}
}
