package ghostty

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The restore sequences depend on each of these values.
func TestStateGetters(t *testing.T) {
	type check func(t *testing.T, in *Instance)
	screen := func(want int32) check {
		return func(t *testing.T, in *Instance) {
			v, err := in.GetU32(DataActiveScreen)
			require.NoError(t, err)
			assert.Equal(t, want, int32(v))
		}
	}
	mode := func(value uint16, want bool) check {
		return func(t *testing.T, in *Instance) {
			v, err := in.GetMode(EncodeMode(value, false))
			require.NoError(t, err)
			assert.Equal(t, want, v)
		}
	}
	kitty := func(want uint8) check {
		return func(t *testing.T, in *Instance) {
			v, err := in.GetU8(DataKittyKeyboardFlags)
			require.NoError(t, err)
			assert.Equal(t, want, v)
		}
	}
	boolean := func(d TerminalData, want bool) check {
		return func(t *testing.T, in *Instance) {
			v, err := in.GetBool(d)
			require.NoError(t, err)
			assert.Equal(t, want, v)
		}
	}
	str := func(d TerminalData, want string) check {
		return func(t *testing.T, in *Instance) {
			v, err := in.GetString(d)
			require.NoError(t, err)
			assert.Equal(t, want, v)
		}
	}
	cursorAt := func(x, y uint16) check {
		return func(t *testing.T, in *Instance) {
			gx, err := in.GetU16(DataCursorX)
			require.NoError(t, err)
			gy, err := in.GetU16(DataCursorY)
			require.NoError(t, err)
			assert.Equal(t, [2]uint16{x, y}, [2]uint16{gx, gy})
		}
	}
	cursorStyle := func(style CursorVisualStyle, blinking bool) check {
		return func(t *testing.T, in *Instance) {
			s, b, err := in.RenderStateCursor()
			require.NoError(t, err)
			assert.Equal(t, style, s, "style")
			assert.Equal(t, blinking, b, "blinking")
		}
	}

	tests := []struct {
		name  string
		input string
		check check
	}{
		{"primary screen", "", screen(ScreenPrimary)},
		{"alt screen via 1049", "\x1b[?1049h", screen(ScreenAlternate)},
		{"alt screen via 1047", "\x1b[?1047h", screen(ScreenAlternate)},
		{"alt screen via 47", "\x1b[?47h", screen(ScreenAlternate)},
		{"back to primary", "\x1b[?1049h\x1b[?1049l", screen(ScreenPrimary)},
		{"mode 25 off", "\x1b[?25l", mode(25, false)},
		{"mode 1000", "\x1b[?1000h", mode(1000, true)},
		{"mode 1002", "\x1b[?1002h", mode(1002, true)},
		{"mode 1003", "\x1b[?1003h", mode(1003, true)},
		{"mode 1006", "\x1b[?1006h", mode(1006, true)},
		{"mode 1004", "\x1b[?1004h", mode(1004, true)},
		{"mode 2004", "\x1b[?2004h", mode(2004, true)},
		{"mode 2026", "\x1b[?2026h", mode(2026, true)},
		{"kitty push", "\x1b[>5u", kitty(5)},
		{"kitty push push pop", "\x1b[>5u\x1b[>1u\x1b[<u", kitty(5)},
		{"kitty set", "\x1b[=3;1u", kitty(3)},
		{"cursor fresh", "", cursorStyle(CursorVisualBlock, false)},
		{"DECSCUSR 0", "\x1b[0 q", cursorStyle(CursorVisualBlock, false)},
		{"DECSCUSR 1", "\x1b[1 q", cursorStyle(CursorVisualBlock, true)},
		{"DECSCUSR 2", "\x1b[2 q", cursorStyle(CursorVisualBlock, false)},
		{"DECSCUSR 3", "\x1b[3 q", cursorStyle(CursorVisualUnderline, true)},
		{"DECSCUSR 4", "\x1b[4 q", cursorStyle(CursorVisualUnderline, false)},
		{"DECSCUSR 5", "\x1b[5 q", cursorStyle(CursorVisualBar, true)},
		{"DECSCUSR 6", "\x1b[6 q", cursorStyle(CursorVisualBar, false)},
		{"blinking off by mode 12", "\x1b[5 q\x1b[?12l", cursorStyle(CursorVisualBar, false)},
		{"cursor visible", "", boolean(DataCursorVisible, true)},
		{"cursor hidden", "\x1b[?25l", boolean(DataCursorVisible, false)},
		{"no mouse tracking", "", boolean(DataMouseTracking, false)},
		{"mouse tracking", "\x1b[?1000h", boolean(DataMouseTracking, true)},
		{"title via OSC 0", "\x1b]0;zero\x07", str(DataTitle, "zero")},
		{"title via OSC 2", "\x1b]2;two\x1b\\", str(DataTitle, "two")},
		{"pwd via OSC 7 has no trailing NUL", "\x1b]7;file://host/tmp\x07", str(DataPwd, "file://host/tmp")},
		{"cursor position", "\x1b[3;7H", cursorAt(6, 2)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := newTerm(t, 80, 24)
			feed(t, in, tt.input)
			tt.check(t, in)
		})
	}
}

// RenderStateCursor creates its render state once and keeps it, so every
// call must update it: a second read on the same instance sees changes
// made after the first.
func TestRenderStateCursorRefreshes(t *testing.T) {
	in := newTerm(t, 80, 24)
	steps := []struct {
		input    string
		style    CursorVisualStyle
		blinking bool
	}{
		{"", CursorVisualBlock, false},
		{"\x1b[5 q", CursorVisualBar, true},
		{"\x1b[?12l", CursorVisualBar, false},
		{"\x1b[4 q", CursorVisualUnderline, false},
		{"\x1b[?12h", CursorVisualUnderline, true},
	}
	for _, s := range steps {
		feed(t, in, s.input)
		style, blinking, err := in.RenderStateCursor()
		require.NoError(t, err)
		assert.Equal(t, s.style, style, "style after %q", s.input)
		assert.Equal(t, s.blinking, blinking, "blinking after %q", s.input)
	}
}
