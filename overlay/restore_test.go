package overlay

import (
	"strings"
	"testing"

	"github.com/bernd/vibepit/vt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// promptDraw is output the filter lets through. It leaves the pen, the
// scroll region, the cursor and its visibility dirty, as a prompt might.
const promptDraw = "\x1b[1;1H\x1b[1;35mPROMPT\x1b[3;4r\x1b[?25l\x1b[2;2Hx"

// throughPrompt feeds real what the real terminal gets from T1 until the
// program exits: the T1 writes, the enter sequence and prompt output.
func throughPrompt(t *testing.T, real *vt.Terminal, c *cutState) entered {
	t.Helper()
	if c.modes[modeSync] {
		feed(t, real, "\x1b[?2026l")
	}
	e := enterFor(c)
	feed(t, real, enterSeq(e))
	feed(t, real, promptDraw)
	return e
}

func mustCut(t *testing.T, sh *vt.Terminal) *cutState {
	t.Helper()
	c, err := captureCut(sh)
	require.NoError(t, err)
	return c
}

func TestCaptureCut(t *testing.T) {
	sh := newVT(t, 20, 5)
	feed(t, sh, "\x1b]2;title\x07\x1b]7;file://h/p\x07\x1b[5 q\x1b[?1049h\x1b[4h\x1b[2;4r\x1b[31mab")
	c := mustCut(t, sh)
	assert.True(t, c.alt)
	assert.True(t, c.modes[modeIRM])
	assert.True(t, c.modes[modeKey{1049, false}])
	assert.False(t, c.modes[modeSync])
	assert.Equal(t, "title", c.title)
	assert.Equal(t, "file://h/p", c.pwd)
	assert.Equal(t, vt.CursorStyle{Shape: vt.CursorBar, Blinking: true}, c.cursor)
	assert.Contains(t, string(c.extras), "\x1b[2;4r", "the scroll region")
	assert.NotContains(t, string(c.extras), "ab", "no content")
}

func TestEnterSeq(t *testing.T) {
	const setD = "\x1b[4l\x1b[20l\x1b[?5l\x1b[?6l\x1b[?7h\x1b[?25h\x1b[?2026l"
	assert.Equal(t, "\x1b[?1047h\x1b[>0u"+setD+penReset+clearScreen, enterSeq(entered{kitty: true, promptScreen: true}))
	assert.Equal(t, "\x1b[>0u"+setD+penReset+clearScreen, enterSeq(entered{kitty: true}), "on the app's alternate screen")
	assert.Equal(t, entered{kitty: true, promptScreen: true}, enterFor(&cutState{}))
	assert.Equal(t, entered{kitty: true}, enterFor(&cutState{alt: true}))
}

func TestRawLeaveOrder(t *testing.T) {
	c := &cutState{
		modes:  map[modeKey]bool{modeIRM: true, {7, false}: false, {25, false}: true, modeSync: true},
		extras: []byte("<extras>"),
	}
	want := "\x1b[<u\x1b[?1047l" +
		"\x1b[20l\x1b[?5l\x1b[?6l\x1b[?7l\x1b[?25h\x1b[?2026h" +
		penReset + "<extras>" +
		"\x1b[4h" +
		"<log>"
	assert.Equal(t, want, string(rawLeave(c, entered{kitty: true, promptScreen: true}, []byte("<log>"))))
}

func TestRawLeaveRestoresTheCut(t *testing.T) {
	tests := []struct{ name, before, detached string }{
		{"scroll region, insert, DEC graphics, red, pending wrap", "\x1b[2;4r\x1b[4h\x1b(0\x1b[31m\x1b[3;1H" + strings.Repeat("x", 20), "q\x1b[0mtail"},
		{"origin mode", "\x1b[2;5r\x1b[?6h\x1b[2;3Hab", "cd"},
		{"line feed mode, reverse video, no wrap", "\x1b[20h\x1b[?5h\x1b[?7lline", "\nnext"},
		{"hidden cursor, bracketed paste, kitty flags", "\x1b[?25l\x1b[?2004h\x1b[>5uab", "\x1b[<ucd"},
		{"synchronized output open", "\x1b[?2026hframe", "\x1b[?2026l"},
		{"hyperlink and protection", "\x1b]8;;http://x\x1b\\link\x1b[1\"q", "\x1b]8;;\x1b\\"},
		{"saved cursor", "ab\x1b7cd", "\x1b8Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shadow, real := newVT(t, 20, 6), newVT(t, 20, 6)
			feed(t, shadow, tt.before)
			feed(t, real, tt.before)
			c := mustCut(t, shadow)
			e := throughPrompt(t, real, c)
			feed(t, shadow, tt.detached)
			feed(t, real, string(rawLeave(c, e, []byte(tt.detached))))
			assertSameTerminal(t, shadow, real)
		})
	}
}

func TestSnapshotLeaveRestoresChangesWhileDetached(t *testing.T) {
	tests := []struct {
		name, before, detached string
		later                  string // app output after the leave
	}{
		{name: "app leaves the alternate screen, resets insert and wrap", before: "shell$ \x1b[?1049h\x1b[4h\x1b[?7lvim", detached: "\x1b[4l\x1b[?7h\x1b[?1049lback"},
		{name: "app enters the alternate screen", before: "shell$ ls", detached: "\x1b[?1049hvim", later: "\x1b[?1049l"},
		{name: "title, working directory, cursor style", before: "a", detached: "\x1b]2;new\x07\x1b]7;file://h/tmp\x07\x1b[5 q"},
		{name: "scroll region, origin mode, pen, charset", before: "a", detached: "\x1b[3;6r\x1b[?6h\x1b[1;32mgreen\x1b(0"},
		{name: "kitty flags set", before: "a", detached: "\x1b[=5;1u"},
		{name: "modifyOtherKeys reset", before: "\x1b[>4;2ma", detached: "\x1b[>4;0m"},
		{name: "synchronized output open", before: "a", detached: "\x1b[?2026hframe"},
		{name: "mouse modes on the alternate screen", before: "\x1b[?1049h\x1b[?1002h\x1b[?1006hmenu", detached: "\x1b[?1002l\x1b[?1003h"},
		{name: "lines scrolled off", before: "a", detached: strings.Repeat("line\r\n", 12) + "end"},
		{name: "pending wrap", before: "a", detached: "\x1b[6;1H" + strings.Repeat("w", 20), later: "Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shadow, real := newVT(t, 20, 6), newVT(t, 20, 6)
			feed(t, shadow, tt.before)
			feed(t, real, tt.before)
			c := mustCut(t, shadow)
			e := throughPrompt(t, real, c)
			feed(t, shadow, tt.detached)
			out, resync, err := snapshotLeave(shadow, c, e)
			require.NoError(t, err)
			assert.False(t, resync)
			feed(t, real, string(out))
			feed(t, shadow, tt.later)
			feed(t, real, tt.later)
			assertSameTerminal(t, shadow, real)
		})
	}
}

func TestSnapshotLeaveOrder(t *testing.T) {
	shadow := newVT(t, 20, 6)
	feed(t, shadow, "\x1b[?1049hvim")
	c := mustCut(t, shadow)
	feed(t, shadow, "\x1b[?1049l\x1b]2;t\x07shell\x1b[3")
	out, resync, err := snapshotLeave(shadow, c, entered{kitty: true})
	require.NoError(t, err)
	assert.False(t, resync)
	s := string(out)
	last := 0
	for _, part := range []string{"\x1b[<u", "\x1b[?1049l", "\x1b[4l", penReset + keyboardReset, clearScreen, "shell", "\x1b]2;t\x1b\\"} {
		i := strings.Index(s[last:], part)
		require.GreaterOrEqual(t, i, 0, "%q missing after byte %d of %q", part, last, s)
		last += i + len(part)
	}
	assert.True(t, strings.HasSuffix(s, "\x1b[3"), "the continuation comes last")
}

func TestSnapshotLeaveWritesModesOutsideDOnlyWhenChanged(t *testing.T) {
	shadow := newVT(t, 20, 6)
	feed(t, shadow, "\x1b[?1004h\x1b[?2048h\x1b[?2004h")
	c := mustCut(t, shadow)
	feed(t, shadow, "\x1b[?2048l\x1b[?2031h\x1b[?1000h")
	out, _, err := snapshotLeave(shadow, c, entered{})
	require.NoError(t, err)
	s := string(out)
	for _, unchanged := range []string{
		"\x1b[?1004", // enabling it again would send a focus report
		"\x1b[?2033",
		"\x1b[?2004",
		"\x1b[?8",    // the shadow's default is off, a real terminal's on: keys would stop repeating
		"\x1b[?12",   // would stop a blinking cursor
		"\x1b[?1036", // would override the terminal's configured Alt key
	} {
		assert.NotContains(t, s, unchanged)
	}
	assert.Contains(t, s, "\x1b[?2048l")
	assert.Contains(t, s, "\x1b[?2031h")
	assert.Contains(t, s, "\x1b[?1000h")
	for _, k := range append(drawModes, modeIRM) {
		assert.Contains(t, s, modeSeq(k, c.modes[k]), "the prompt changed set D")
	}
}

func TestSnapshotLeaveTurnsSynchronizedOutputOff(t *testing.T) {
	shadow := newVT(t, 20, 6)
	c := mustCut(t, shadow)
	feed(t, shadow, "\x1b[?2026h")
	out, _, err := snapshotLeave(shadow, c, entered{})
	require.NoError(t, err)
	assert.Contains(t, string(out), "\x1b[?2026l")
	on, err := shadow.Mode(2026, false)
	require.NoError(t, err)
	assert.False(t, on, "the shadow matches what the real terminal gets")
}

func TestSnapshotLeaveResyncsPastTheContinuationLimit(t *testing.T) {
	shadow := newVT(t, 20, 6, vt.WithContinuationMaxBytes(16))
	c := mustCut(t, shadow)
	feed(t, shadow, "\x1b]2;"+strings.Repeat("y", 100))
	out, resync, err := snapshotLeave(shadow, c, entered{})
	require.NoError(t, err)
	assert.True(t, resync)
	assert.NotContains(t, string(out), "yyyy")
}

func TestLeaveKeepsTheKittyStack(t *testing.T) {
	for _, alt := range []bool{false, true} {
		name := "primary screen, raw replay"
		if alt {
			name = "alternate screen, snapshot"
		}
		t.Run(name, func(t *testing.T) {
			before := "\x1b[>1u\x1b[>3u"
			if alt {
				before = "\x1b[?1049h" + before
			}
			shadow, real := newVT(t, 20, 6), newVT(t, 20, 6)
			feed(t, shadow, before)
			feed(t, real, before)
			c := mustCut(t, shadow)
			e := throughPrompt(t, real, c)
			out := rawLeave(c, e, nil)
			if alt {
				var err error
				out, _, err = snapshotLeave(shadow, c, e)
				require.NoError(t, err)
			}
			feed(t, real, string(out))
			for range 2 {
				feed(t, shadow, "\x1b[<u")
				feed(t, real, "\x1b[<u")
				want, err := shadow.KittyKeyboardFlags()
				require.NoError(t, err)
				got, err := real.KittyKeyboardFlags()
				require.NoError(t, err)
				assert.Equal(t, want, got)
			}
		})
	}
}

func TestResetLeave(t *testing.T) {
	assert.Equal(t, "\x1b[<u\x1b[?1047l\x1bc", string(resetLeave(entered{kitty: true, promptScreen: true})))
	assert.Equal(t, "\x1bc", string(resetLeave(entered{})))
}

func TestOSCText(t *testing.T) {
	assert.Equal(t, "evil[2Jtitle", oscText("evil\x1b[2J\x07title\u009c"))
}
