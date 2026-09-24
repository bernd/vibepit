package ghostty

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRISResetsModesAndKittyFlags(t *testing.T) {
	in := newTerm(t, 80, 24)
	feed(t, in, "\x1b[?2004h\x1b[>5u\x1b[?1049hX\x1bc")

	paste, err := in.GetMode(EncodeMode(2004, false))
	require.NoError(t, err)
	assert.False(t, paste)
	flags, err := in.GetU8(DataKittyKeyboardFlags)
	require.NoError(t, err)
	assert.Zero(t, flags)
	screen, err := in.GetU32(DataActiveScreen)
	require.NoError(t, err)
	assert.Equal(t, ScreenPrimary, int32(screen))
}

func TestVTGround(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		ground bool
	}{
		{"at rest", "", true},
		{"after text", "hello", true},
		{"after a complete CSI", "\x1b[31m", true},
		{"lone ESC", "\x1b", false},
		{"inside CSI", "\x1b[3", false},
		{"inside OSC", "\x1b]2;tit", false},
		{"inside DCS", "\x1bP+q54", false},
		{"inside UTF-8", "\xe2\x82", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := newTerm(t, 80, 24)
			feed(t, in, tt.input)
			got, err := in.GetBool(DataVTGround)
			require.NoError(t, err)
			assert.Equal(t, tt.ground, got)
		})
	}
}

func TestWriteUntilGround(t *testing.T) {
	big := strings.Repeat("x", inputBufSize+10)
	tests := []struct {
		name         string
		before       string
		input        string
		wantConsumed int
		wantGround   bool
		wantTitle    string
	}{
		{name: "already at ground consumes nothing", input: "abc", wantConsumed: 0, wantGround: true},
		{name: "rest of a UTF-8 character", before: "abc\xe2", input: "\x82\xacX", wantConsumed: 2, wantGround: true},
		{name: "unfinished OSC stays unfinished", before: "\x1b]2;tit", input: "le", wantConsumed: 2, wantGround: false},
		{name: "OSC terminator", before: "\x1b]2;tit", input: "\x07after", wantConsumed: 1, wantGround: true, wantTitle: "tit"},
		// libghostty drops a title this long, so only the counts are checked.
		{
			name: "ground after the first chunk", before: "\x1b]2;", input: big + "\x07tail",
			wantConsumed: len(big) + 1, wantGround: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := newTerm(t, 80, 24)
			feed(t, in, tt.before)
			n, ground, err := in.VTWriteUntilGround([]byte(tt.input))
			require.NoError(t, err)
			assert.Equal(t, tt.wantConsumed, n)
			assert.Equal(t, tt.wantGround, ground)
			if tt.wantTitle != "" {
				title, err := in.GetString(DataTitle)
				require.NoError(t, err)
				assert.Equal(t, tt.wantTitle, title)
			}
		})
	}
}

// A cut at ground keeps the real terminal and the shadow in the same
// parser state: "abc€X" stays intact.
func TestWriteUntilGroundThenRest(t *testing.T) {
	in := newTerm(t, 20, 2)
	feed(t, in, "abc\xe2")
	rest := []byte("\x82\xacX")
	n, ground, err := in.VTWriteUntilGround(rest)
	require.NoError(t, err)
	require.True(t, ground)
	require.NoError(t, in.VTWrite(rest[n:]))

	out, err := in.GetU16(DataCursorX)
	require.NoError(t, err)
	assert.EqualValues(t, 5, out, "a, b, c, €, X")
}

// TestModeTableComplete scans every mode number. It fails when upstream
// adds or removes a mode, or changes a default, so Modes stays exact. The
// defaults must also hold after a reset (RIS), since a restore may rely on
// either.
func TestModeTableComplete(t *testing.T) {
	known := map[uint16]ModeEntry{}
	for _, m := range Modes {
		known[m.Mode()] = m
	}
	scan := func(t *testing.T, in *Instance) {
		found := map[uint16]bool{}
		for _, ansi := range []bool{true, false} {
			for v := uint16(0); v < 10000; v++ {
				mode := EncodeMode(v, ansi)
				on, err := in.GetMode(mode)
				if err != nil {
					require.ErrorIs(t, err, InvalidValue)
					continue
				}
				found[mode] = true
				entry, ok := known[mode]
				if assert.True(t, ok, "mode %d (ansi=%v) is not in Modes", v, ansi) {
					assert.Equal(t, entry.Default, on, "default of mode %d (ansi=%v)", v, ansi)
				}
			}
		}
		for mode, m := range known {
			assert.True(t, found[mode], "mode %d (ansi=%v) in Modes is unknown to libghostty", m.Value, m.ANSI)
		}
	}

	t.Run("fresh", func(t *testing.T) {
		scan(t, newTerm(t, 80, 24))
	})
	t.Run("after reset", func(t *testing.T) {
		in := newTerm(t, 80, 24)
		for _, m := range Modes {
			require.NoError(t, in.TerminalSetMode(m.Mode(), !m.Default))
			on, err := in.GetMode(m.Mode())
			require.NoError(t, err)
			require.Equal(t, !m.Default, on, "mode %d (ansi=%v) is dirty", m.Value, m.ANSI)
		}
		require.NoError(t, in.TerminalReset())
		scan(t, in)
	})
}
