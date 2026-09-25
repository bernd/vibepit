package vt_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/bernd/vibepit/vt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTerminal(t *testing.T, cols, rows uint16, opts ...vt.TerminalOption) *vt.Terminal {
	t.Helper()
	term, err := vt.NewTerminal(cols, rows, opts...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = term.Close() })
	return term
}

func write(t *testing.T, term *vt.Terminal, s string) {
	t.Helper()
	n, err := term.Write([]byte(s))
	require.NoError(t, err)
	require.Equal(t, len(s), n)
}

func TestFormatRegions(t *testing.T) {
	const input = "l1\r\nl2\r\n\x1b[31ml3\x1b[0m\r\nl4 long line wraps\r\nl5\x1b[?2004h"
	tests := []struct {
		name     string
		input    string
		opts     vt.FormatOptions
		contains []string
		excludes []string
		empty    bool
	}{
		{
			name:     "screen",
			input:    input,
			opts:     vt.FormatOptions{Unwrap: true, Extras: vt.AllExtras},
			contains: []string{"\x1b[?2004h", "l4 long line wraps", "l5"},
			excludes: []string{"l1"},
		},
		{
			name:     "scrollback",
			input:    input,
			opts:     vt.FormatOptions{Region: vt.RegionScrollback},
			contains: []string{"l1\r\nl2", "l3"},
			excludes: []string{"l5", "\x1b[?2004h"},
		},
		{
			name:     "none",
			input:    input,
			opts:     vt.FormatOptions{Region: vt.RegionNone, Extras: vt.AllExtras},
			contains: []string{"\x1b[?2004h"},
			excludes: []string{"l1", "l5"},
		},
		{
			name:  "scrollback empty",
			input: "only screen",
			opts:  vt.FormatOptions{Region: vt.RegionScrollback},
			empty: true,
		},
		{
			name:     "plain",
			input:    "\x1b[31mred\x1b[0m",
			opts:     vt.FormatOptions{Output: vt.OutputPlain, Trim: true},
			contains: []string{"red"},
			excludes: []string{"\x1b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			term := newTerminal(t, 10, 3)
			write(t, term, tt.input)
			got, err := term.Format(tt.opts)
			require.NoError(t, err)
			if tt.empty {
				assert.Empty(t, got)
			}
			for _, s := range tt.contains {
				assert.Contains(t, string(got), s)
			}
			for _, s := range tt.excludes {
				assert.NotContains(t, string(got), s)
			}
		})
	}
}

func TestCursorStyle(t *testing.T) {
	tests := []struct {
		input string
		want  vt.CursorStyle
	}{
		{"", vt.CursorStyle{Shape: vt.CursorBlock}},
		{"\x1b[0 q", vt.CursorStyle{Shape: vt.CursorBlock}},
		{"\x1b[1 q", vt.CursorStyle{Shape: vt.CursorBlock, Blinking: true}},
		{"\x1b[2 q", vt.CursorStyle{Shape: vt.CursorBlock}},
		{"\x1b[3 q", vt.CursorStyle{Shape: vt.CursorUnderline, Blinking: true}},
		{"\x1b[4 q", vt.CursorStyle{Shape: vt.CursorUnderline}},
		{"\x1b[5 q", vt.CursorStyle{Shape: vt.CursorBar, Blinking: true}},
		{"\x1b[6 q", vt.CursorStyle{Shape: vt.CursorBar}},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.input), func(t *testing.T) {
			term := newTerminal(t, 80, 24)
			write(t, term, tt.input)
			got, err := term.CursorStyle()
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)

			again := newTerminal(t, 80, 24)
			write(t, again, got.DECSCUSR())
			round, err := again.CursorStyle()
			require.NoError(t, err)
			assert.Equal(t, got, round, "DECSCUSR round trip")
		})
	}
}

func TestMouseTracking(t *testing.T) {
	tests := []struct {
		input string
		want  vt.MouseTracking
	}{
		{"", vt.MouseNone},
		{"\x1b[?9h", vt.MouseX10},
		{"\x1b[?1000h", vt.MouseNormal},
		{"\x1b[?1002h", vt.MouseButton},
		{"\x1b[?1003h", vt.MouseAny},
		{"\x1b[?1000h\x1b[?1003h", vt.MouseAny},
		{"\x1b[?1003h\x1b[?1003l\x1b[?1000h", vt.MouseNormal},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.input), func(t *testing.T) {
			term := newTerminal(t, 80, 24)
			write(t, term, tt.input)
			got, err := term.MouseTracking()
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestModes(t *testing.T) {
	term := newTerminal(t, 80, 24)
	write(t, term, "\x1b[?2004h\x1b[4h")
	modes, err := term.Modes()
	require.NoError(t, err)
	require.Len(t, modes, 43)
	assert.Contains(t, modes, vt.ModeState{Mode: 2004, Value: true})
	assert.Contains(t, modes, vt.ModeState{Mode: 4, ANSI: true, Value: true})
	assert.Contains(t, modes, vt.ModeState{Mode: 7, Value: true})

	on, err := term.Mode(2004, false)
	require.NoError(t, err)
	assert.True(t, on)
	require.NoError(t, term.SetMode(2004, false, false))
	on, err = term.Mode(2004, false)
	require.NoError(t, err)
	assert.False(t, on)
}

func TestScreenGetters(t *testing.T) {
	term := newTerminal(t, 80, 24)
	write(t, term, "\x1b[?1049h\x1b[>5u\x1b[?25l\x1b]2;title\x07\x1b]7;file://h/tmp\x07")

	alt, err := term.AltScreen()
	require.NoError(t, err)
	assert.True(t, alt)
	flags, err := term.KittyKeyboardFlags()
	require.NoError(t, err)
	assert.EqualValues(t, 5, flags)
	visible, err := term.CursorVisible()
	require.NoError(t, err)
	assert.False(t, visible)
	title, err := term.Title()
	require.NoError(t, err)
	assert.Equal(t, "title", title)
	pwd, err := term.Pwd()
	require.NoError(t, err)
	assert.Equal(t, "file://h/tmp", pwd)
}

func TestWriteUntilGround(t *testing.T) {
	term := newTerminal(t, 80, 24)
	n, ground, err := term.WriteUntilGround([]byte("abc"))
	require.NoError(t, err)
	assert.Equal(t, 0, n, "already at ground")
	assert.True(t, ground)

	write(t, term, "abc\xe2")
	atGround, err := term.AtGround()
	require.NoError(t, err)
	assert.False(t, atGround)
	n, ground, err = term.WriteUntilGround([]byte("\x82\xacX"))
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.True(t, ground)
}

func TestContinuation(t *testing.T) {
	term := newTerminal(t, 80, 24)
	write(t, term, "abc\x1b[3")
	got, err := term.Continuation()
	require.NoError(t, err)
	assert.Equal(t, "\x1b[3", string(got), "tracking is on by default")

	small := newTerminal(t, 80, 24, vt.WithContinuationMaxBytes(1024))
	write(t, small, "\x1b]2;"+strings.Repeat("x", 1100))
	_, err = small.Continuation()
	assert.ErrorIs(t, err, vt.ErrContinuationUnavailable)
}

func TestWritePty(t *testing.T) {
	var answers []byte
	term := newTerminal(t, 80, 24, vt.WithWritePty(func(p []byte) { answers = append(answers, p...) }))
	write(t, term, "\x1b[6n")
	assert.Equal(t, "\x1b[1;1R", string(answers))
}

// A panic in the callback runs inside the emulator's Write, so it can't
// be told apart from a fault in the emulator: it fails the terminal.
func TestWritePtyPanicFailsTerminal(t *testing.T) {
	term := newTerminal(t, 80, 24, vt.WithWritePty(func([]byte) { panic("callback bug") }))
	n, err := term.Write([]byte("\x1b[6n"))
	require.ErrorIs(t, err, vt.ErrFailed)
	assert.Zero(t, n)

	_, err = term.Write([]byte("x"))
	assert.ErrorIs(t, err, vt.ErrFailed, "the failure sticks")
	_, err = term.Format(vt.FormatOptions{})
	assert.ErrorIs(t, err, vt.ErrFailed)
}

func TestResize(t *testing.T) {
	term := newTerminal(t, 10, 3)
	write(t, term, "l4 long line wraps")
	require.NoError(t, term.Resize(20, 3))
	got, err := term.Format(vt.FormatOptions{Output: vt.OutputPlain, Trim: true})
	require.NoError(t, err)
	assert.Equal(t, "l4 long line wraps", strings.Split(string(got), "\n")[0])
}

func TestZeroSize(t *testing.T) {
	_, err := vt.NewTerminal(0, 24)
	assert.Error(t, err)

	term := newTerminal(t, 80, 24)
	assert.Error(t, term.Resize(80, 0))
	write(t, term, "still usable")
}

func TestClose(t *testing.T) {
	term, err := vt.NewTerminal(80, 24)
	require.NoError(t, err)
	require.NoError(t, term.Close())
	require.NoError(t, term.Close(), "idempotent")
	_, err = term.Write([]byte("x"))
	assert.ErrorIs(t, err, vt.ErrClosed)
	_, err = term.Format(vt.FormatOptions{})
	assert.ErrorIs(t, err, vt.ErrClosed)
}

func TestConcurrentWriteAndFormat(t *testing.T) {
	term := newTerminal(t, 80, 24)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 200 {
				_, err := term.Write([]byte("\x1b[31mline\x1b[0m\r\n"))
				assert.NoError(t, err)
			}
		}()
		go func() {
			defer wg.Done()
			for range 200 {
				_, err := term.Format(vt.FormatOptions{Extras: vt.AllExtras})
				assert.NoError(t, err)
			}
		}()
	}
	wg.Wait()
}

// With generous scrollback limits, the memory limit is what stops growth.
// Hitting it is not an error for Write. A large Format then fails with
// ErrOutOfMemory, which must not fail the terminal: a small Format still
// works. vibed's scrollback replay (phase 2) relies on this to fall back.
func TestMemoryLimitOption(t *testing.T) {
	term := newTerminal(t, 200, 24,
		vt.WithMemoryLimit(4<<20), vt.WithScrollbackLines(1<<20), vt.WithScrollbackBytes(1<<30))
	line := strings.Repeat("x", 190) + "\r\n"
	_, err := term.Write([]byte(strings.Repeat(line, 50000)))
	require.NoError(t, err, "hitting the memory limit is not an error for Write")

	_, err = term.Format(vt.FormatOptions{Region: vt.RegionScrollback})
	require.ErrorIs(t, err, vt.ErrOutOfMemory, "about 1,600 rows of history don't fit")
	assert.NotErrorIs(t, err, vt.ErrFailed)

	write(t, term, "\x1b[2J\x1b[Hstill here")
	got, err := term.Format(vt.FormatOptions{Output: vt.OutputPlain, Trim: true})
	require.NoError(t, err)
	assert.Contains(t, string(got), "still here")
}
