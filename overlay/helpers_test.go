package overlay

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/bernd/vibepit/vt"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncBuffer is a bytes.Buffer the pump goroutines can share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// chunkReader hands its reader one queued chunk per Read, so tests control
// read boundaries. push returns once the reader is back for the next
// chunk, which means it has handled this one.
type chunkReader struct {
	chunks  chan []byte
	next    chan struct{}
	closed  chan struct{}
	once    sync.Once
	pending []byte
	started bool
}

func newChunkReader() *chunkReader {
	return &chunkReader{
		chunks: make(chan []byte),
		next:   make(chan struct{}),
		closed: make(chan struct{}),
	}
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.pending) == 0 {
		if r.started {
			select {
			case r.next <- struct{}{}:
			case <-r.closed:
				return 0, io.EOF
			}
		}
		select {
		case c := <-r.chunks:
			r.pending, r.started = c, true
		case <-r.closed:
			return 0, io.EOF
		}
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

// push hands chunk to the reader and waits until it has handled it. It
// reports false after a timeout or once the reader is closed, and is safe
// from any goroutine.
func (r *chunkReader) push(chunk string) bool {
	select {
	case r.chunks <- []byte(chunk):
	case <-r.closed:
		return false
	case <-time.After(5 * time.Second):
		return false
	}
	select {
	case <-r.next:
		return true
	case <-r.closed:
		return false
	case <-time.After(5 * time.Second):
		return false
	}
}

// send is push for the test goroutine.
func (r *chunkReader) send(t *testing.T, chunk string) {
	t.Helper()
	require.True(t, r.push(chunk), "reader didn't handle %q", chunk)
}

// close makes the reader's next Read return io.EOF.
func (r *chunkReader) close() { r.once.Do(func() { close(r.closed) }) }

// readN reads exactly n bytes from r, failing after a timeout.
func readN(t *testing.T, r io.Reader, n int) string {
	t.Helper()
	buf := make([]byte, n)
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(r, buf)
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		require.FailNow(t, fmt.Sprintf("reading %d bytes timed out", n))
	}
	return string(buf)
}

func newVT(t *testing.T, cols, rows int, opts ...vt.TerminalOption) *vt.Terminal {
	t.Helper()
	term, err := vt.NewTerminal(uint16(cols), uint16(rows), opts...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = term.Close() })
	return term
}

func feed(t *testing.T, term *vt.Terminal, s string) {
	t.Helper()
	_, err := term.Write([]byte(s))
	require.NoError(t, err)
}

// screenText is the visible screen as plain text, or "" on error, so it is
// safe inside require.Eventually.
func screenText(term *vt.Terminal) string {
	b, err := term.Format(vt.FormatOptions{Output: vt.OutputPlain, Trim: true, Region: vt.RegionScreen})
	if err != nil {
		return ""
	}
	return string(b)
}

// termState is what a restore must reproduce on the real terminal.
type termState struct {
	Screen string // content, modes, cursor, pen, keyboard state
	Alt    bool
	Kitty  uint8
	Modes  []vt.ModeState
	Title  string
	Pwd    string
	Cursor vt.CursorStyle
}

// stateExtras leave out charsets: libghostty's default designation is
// UTF-8, which ESC ( B doesn't restore, while real terminals treat ESC ( B
// as the default. probeSuffix checks charsets by printing through them.
var stateExtras = func() vt.Extras {
	x := vt.AllExtras
	x.Charsets = false
	return x
}()

func stateOf(t *testing.T, term *vt.Terminal) termState {
	t.Helper()
	var s termState
	screen, err := term.Format(vt.FormatOptions{Unwrap: true, Extras: stateExtras, Region: vt.RegionScreen})
	require.NoError(t, err)
	s.Screen = string(screen)
	s.Alt, err = term.AltScreen()
	require.NoError(t, err)
	s.Kitty, err = term.KittyKeyboardFlags()
	require.NoError(t, err)
	s.Modes, err = term.Modes()
	require.NoError(t, err)
	s.Title, err = term.Title()
	require.NoError(t, err)
	s.Pwd, err = term.Pwd()
	require.NoError(t, err)
	s.Cursor, err = term.CursorStyle()
	require.NoError(t, err)
	return s
}

// probeSuffix shows state the capture can't: the active charset (q draws a
// line in DEC graphics), LNM, and the scroll region (DL).
const probeSuffix = "q\r\nw\x1b[Mz"

// assertSameTerminal compares the state of want and got, then again after
// the same probe output.
func assertSameTerminal(t *testing.T, want, got *vt.Terminal) {
	t.Helper()
	assert.Equal(t, stateOf(t, want), stateOf(t, got))
	feed(t, want, probeSuffix)
	feed(t, got, probeSuffix)
	assert.Equal(t, stateOf(t, want), stateOf(t, got), "after the probe suffix")
}

// scrollbackSaves returns the erases in s that iTerm2 turns into "move the
// screen into the scrollback" instead of erasing in place, with its default
// settings, on either screen: ED 2 always, ED 0 with the cursor in the top
// left corner. A clear the leave or the prompt writes must not be one: it
// would leave a copy of the app's screen or of the prompt in the scrollback.
// term follows s, so the cursor is known at each erase.
func scrollbackSaves(t *testing.T, term *vt.Terminal, s string) []string {
	t.Helper()
	var saves []string
	p := ansi.NewParser()
	data := []byte(s)
	for len(data) > 0 {
		seq, _, n, _ := ansi.DecodeSequence(data, ansi.NormalState, p)
		require.Positive(t, n)
		if len(seq) > 2 && seq[0] == ansi.ESC && seq[1] == '[' && ansi.Cmd(p.Command()).Final() == 'J' && ansi.Cmd(p.Command()).Prefix() == 0 {
			switch mode, _, _ := p.Params().Param(0, 0); mode {
			case 2:
				saves = append(saves, string(seq))
			case 0:
				if row, col := cursorOf(t, term); row == 1 && col == 1 {
					saves = append(saves, string(seq))
				}
			}
		}
		feed(t, term, string(seq))
		data = data[n:]
	}
	return saves
}

// cursorOf asks term for its cursor position, 1-based.
func cursorOf(t *testing.T, term *vt.Terminal) (int, int) {
	t.Helper()
	b, err := term.Format(vt.FormatOptions{Region: vt.RegionNone, Extras: vt.Extras{Cursor: true}})
	require.NoError(t, err)
	var row, col int
	_, err = fmt.Sscanf(string(b), "\x1b[%d;%dH", &row, &col)
	require.NoError(t, err, "cursor extra %q", b)
	return row, col
}
