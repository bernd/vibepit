package ghostty

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const scenarioCols, scenarioRows = 40, 10

// scenarios feed the round-trip, chunking, golden and fuzz tests.
var scenarios = []struct{ name, input string }{
	{"primary-agent", "\x1b[>1u\x1b[?2004h\x1b[?1004h\x1b[?25lhello \x1b[1;32mgreen\x1b[0m \x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\\r\n> \x1b[2;3H"},
	{"alt-screen-mouse", "shell$ \x1b[?1049h\x1b[?1002h\x1b[?1006h\x1b[Htop line\x1b[3;4Hcontent"},
	{"scroll-region", "a\r\nb\r\nc\x1b[2;4r\x1b[4;1Hin region\r\nscrolled"},
	{"charsets-g0", "abc\x1b(0lqk"},
	{"charsets-g1", "xyz\x1b)0\x0elqk"},
	{"sgr", "\x1b[38;5;123mA\x1b[38;2;1;2;3mB\x1b[4:3mC\x1b[58;5;9mD\x1b[1;3;7mE\x1b[0mF\x1b[45m"},
	{"wide", "漢字 👍🏽 e\u0301 x"},
	{"pending-wrap", "\x1b[H0123456789012345678901234567890123456789"},
	{"insert-mode", "abcdef\x1b[1;3H\x1b[4h"},
	{"origin-mode", "\x1b[3;8r\x1b[?6h\x1b[2;2Hx"},
}

func scenarioInput(t *testing.T, name string) string {
	t.Helper()
	for _, s := range scenarios {
		if s.name == name {
			return s.input
		}
	}
	t.Fatalf("no scenario %q", name)
	return ""
}

func activeArea(t testing.TB, in *Instance) *Selection {
	t.Helper()
	cols, err := in.GetU16(DataCols)
	require.NoError(t, err)
	rows, err := in.GetU16(DataRows)
	require.NoError(t, err)
	return &Selection{
		Start: Point{Tag: PointActive},
		End:   Point{Tag: PointActive, X: cols - 1, Y: uint32(rows) - 1},
	}
}

func format(t testing.TB, in *Instance, opts FormatterOptions) string {
	t.Helper()
	out, err := in.FormatAlloc(opts)
	require.NoError(t, err)
	return string(out)
}

func plainScreen(t testing.TB, in *Instance) string {
	t.Helper()
	return format(t, in, FormatterOptions{Emit: FormatPlain, Trim: true, Selection: activeArea(t, in)})
}

// restoreExtras is every extra a restore uses. Palette, tab stops and pwd
// stay off; see known_gaps_test.go.
var restoreExtras = TerminalExtra{
	Modes: true, ScrollingRegion: true, Keyboard: true,
	Screen: ScreenExtra{Cursor: true, Style: true, Hyperlink: true, Protection: true, KittyKeyboard: true, Charsets: true},
}

// snapshot is the visible screen with every restore extra.
func snapshot(t testing.TB, in *Instance) string {
	t.Helper()
	return format(t, in, FormatterOptions{Emit: FormatVT, Unwrap: true, Extra: restoreExtras, Selection: activeArea(t, in)})
}

// state is what a restore must reproduce.
type state struct {
	Screen      string
	Alt         bool
	CursorX     uint16
	CursorY     uint16
	PendingWrap bool
	Kitty       uint8
	Modes       []bool // indexed like Modes
}

func capture(t testing.TB, in *Instance) state {
	t.Helper()
	extra := restoreExtras
	// libghostty's default designation is UTF-8, which ESC ( B (ASCII)
	// doesn't restore, while real terminals treat ESC ( B as the default.
	// probeSuffix checks charsets by printing through them instead.
	extra.Screen.Charsets = false
	s := state{Screen: format(t, in, FormatterOptions{Emit: FormatVT, Unwrap: true, Extra: extra, Selection: activeArea(t, in)})}
	screen, err := in.GetU32(DataActiveScreen)
	require.NoError(t, err)
	s.Alt = int32(screen) == ScreenAlternate
	s.CursorX, err = in.GetU16(DataCursorX)
	require.NoError(t, err)
	s.CursorY, err = in.GetU16(DataCursorY)
	require.NoError(t, err)
	s.PendingWrap, err = in.GetBool(DataCursorPendingWrap)
	require.NoError(t, err)
	s.Kitty, err = in.GetU8(DataKittyKeyboardFlags)
	require.NoError(t, err)
	for _, m := range Modes {
		v, err := in.GetMode(m.Mode())
		require.NoError(t, err)
		s.Modes = append(s.Modes, v)
	}
	return s
}

// probeSuffix shows state the capture can't: the active charset (q draws a
// line in DEC graphics), LNM, and the scroll region (DL).
const probeSuffix = "q\r\nw\x1b[Mz"

func TestFormatRoundTrip(t *testing.T) {
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			a := newTerm(t, scenarioCols, scenarioRows)
			b := newTerm(t, scenarioCols, scenarioRows)
			feed(t, a, sc.input)
			feed(t, b, snapshot(t, a))
			assert.Equal(t, capture(t, a), capture(t, b))

			feed(t, a, probeSuffix)
			feed(t, b, probeSuffix)
			assert.Equal(t, capture(t, a), capture(t, b), "after probe suffix")
		})
	}
}

// The state must not depend on how the input was chunked.
func TestChunkingInvariance(t *testing.T) {
	forEachSplit(t, func(t *testing.T, input string, k int, want state) {
		withTerm(t, scenarioCols, scenarioRows, func(in *Instance) {
			feed(t, in, input[:k])
			feed(t, in, input[k:])
			assert.Equal(t, want, capture(t, in), "split at %d", k)
		})
	})
}

// forEachSplit runs fn for every scenario and every split point k inside
// its input, with the state the whole input produces.
func forEachSplit(t *testing.T, fn func(t *testing.T, input string, k int, want state)) {
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			var want state
			withTerm(t, scenarioCols, scenarioRows, func(in *Instance) {
				feed(t, in, sc.input)
				want = capture(t, in)
			})
			for k := 1; k < len(sc.input); k++ {
				fn(t, sc.input, k, want)
			}
		})
	}
}

// Golden files make formatter output changes visible in review. Run with
// -update after reviewing a diff.
func TestFormatGolden(t *testing.T) {
	for _, name := range []string{"primary-agent", "alt-screen-mouse", "scroll-region", "origin-mode"} {
		t.Run(name, func(t *testing.T) {
			in := newTerm(t, scenarioCols, scenarioRows)
			feed(t, in, scenarioInput(t, name))
			got := snapshot(t, in)
			path := filepath.Join("testdata", "format-"+name+".golden")
			if *update {
				require.NoError(t, os.MkdirAll("testdata", 0o755))
				require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
			}
			want, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, string(want), got)
		})
	}
}

func TestFormatScrollbackOnly(t *testing.T) {
	in := newTerm(t, 10, 3)
	feed(t, in, "l1\r\nl2\r\n\x1b[31ml3\x1b[0m\r\nl4 long line wraps\r\nl5")
	rows, err := in.GetU32(DataScrollbackRows)
	require.NoError(t, err)
	require.EqualValues(t, 3, rows)

	got := format(t, in, FormatterOptions{
		Emit: FormatVT,
		Selection: &Selection{
			Start: Point{Tag: PointHistory},
			End:   Point{Tag: PointHistory, X: 9, Y: rows - 1},
		},
	})
	assert.Equal(t, "l1\r\nl2\r\n\x1b[0m\x1b[38;5;1ml3\x1b[0m", got, "content and styles only")
}

// A soft-wrapped line stays one logical line and reflows after a resize.
func TestFormatSoftWrap(t *testing.T) {
	a := newTerm(t, 10, 3)
	feed(t, a, "l4 long line wraps")
	out := snapshot(t, a)
	assert.Contains(t, out, "l4 long line wraps")

	b := newTerm(t, 10, 3)
	feed(t, b, out)
	require.NoError(t, a.TerminalResize(20, 3, 0, 0))
	require.NoError(t, b.TerminalResize(20, 3, 0, 0))
	assert.Equal(t, "l4 long line wraps", strings.Split(plainScreen(t, b), "\n")[0])
	assert.Equal(t, capture(t, a), capture(t, b))
}

func TestFormatOSC133(t *testing.T) {
	t.Run("formatter drops prompt marks", func(t *testing.T) {
		in := newTerm(t, 20, 5)
		feed(t, in, "\x1b]133;A\x07$ \x1b]133;B\x07ls\r\n\x1b]133;C\x07out\r\n\x1b]133;D;0\x07")
		assert.NotContains(t, snapshot(t, in), "\x1b]133")
	})
	t.Run("resize keeps prompt lines by default", func(t *testing.T) {
		in := newTerm(t, 20, 3)
		feed(t, in, "out\r\n\x1b]133;A\x07$ cmd")
		require.NoError(t, in.TerminalResize(10, 3, 0, 0))
		assert.Contains(t, plainScreen(t, in), "$ cmd")
	})
	t.Run("resize clears prompt lines after redraw=1", func(t *testing.T) {
		in := newTerm(t, 20, 3)
		feed(t, in, "out\r\n\x1b]133;A;redraw=1\x07$ cmd")
		require.NoError(t, in.TerminalResize(10, 3, 0, 0))
		got := plainScreen(t, in)
		assert.NotContains(t, got, "$ cmd")
		assert.Contains(t, got, "out")
	})
}

func TestFormatExtrasOnly(t *testing.T) {
	extrasOnly := func(t *testing.T, in *Instance) string {
		return format(t, in, FormatterOptions{Emit: FormatVT, Extra: restoreExtras, ContentNone: true})
	}

	t.Run("no content", func(t *testing.T) {
		in := newTerm(t, 20, 5)
		feed(t, in, "hello\x1b[2;4r\x1b[31m")
		got := extrasOnly(t, in)
		assert.NotContains(t, got, "hello")
		assert.Contains(t, got, "\x1b[2;4r")
	})

	t.Run("order", func(t *testing.T) {
		in := newTerm(t, 20, 5)
		feed(t, in, "x\x1b[2;4r\x1b[3;5H\x1b[31m\x1b(0")
		got := extrasOnly(t, in)
		region := strings.Index(got, "\x1b[2;4r")
		cup := regexp.MustCompile(`\x1b\[\d+;\d+H`).FindStringIndex(got)
		sgr := strings.Index(got, "\x1b[38;5;1m")
		charset := strings.Index(got, "\x1b(0")
		require.NotNil(t, cup)
		assert.Less(t, region, cup[0], "scroll region before the cursor")
		assert.Less(t, cup[0], sgr, "pen after the cursor")
		assert.Less(t, cup[0], charset, "charsets after the cursor")
	})

	t.Run("pending wrap is reprinted", func(t *testing.T) {
		a := newTerm(t, 10, 3)
		feed(t, a, "0123456789")
		b := newTerm(t, 10, 3)
		feed(t, b, "0123456789\x1b[H")
		feed(t, b, extrasOnly(t, a))
		wrap, err := b.GetBool(DataCursorPendingWrap)
		require.NoError(t, err)
		assert.True(t, wrap)
		assert.Equal(t, capture(t, a), capture(t, b))
	})

	t.Run("restores cursor, pen and region in a dirty terminal", func(t *testing.T) {
		a := newTerm(t, 20, 5)
		feed(t, a, "x\x1b[2;4r\x1b[3;5H\x1b[31m\x1b(0")
		b := newTerm(t, 20, 5)
		feed(t, b, "x\x1b[1;5r\x1b[32m\x1b[5;1H")
		feed(t, b, extrasOnly(t, a))
		assert.Equal(t, extrasOnly(t, a), extrasOnly(t, b))
	})
}

// An attach between "abc ESC[3" and "1mRED" must show a red RED, not
// "1mRED": the continuation restores the parser state.
func TestContinuationReplay(t *testing.T) {
	const first, rest = "abc\x1b[3", "1mRED\x1b[0m"

	whole := newTerm(t, 20, 2)
	feed(t, whole, first+rest)

	cut := newTerm(t, 20, 2)
	require.NoError(t, cut.TerminalSetSize(OptContinuationMaxBytes, 64<<10))
	feed(t, cut, first)
	cont, err := cut.ContinuationAlloc()
	require.NoError(t, err)

	replay := newTerm(t, 20, 2)
	feed(t, replay, snapshot(t, cut)+string(cont)+rest)
	assert.Equal(t, capture(t, whole), capture(t, replay))
	assert.NotContains(t, plainScreen(t, replay), "1mRED")
}

// Snapshot, then continuation, then the rest of the stream reproduces the
// uncut stream at every split point. This is vibed's attach path.
func TestContinuationReplayEverySplit(t *testing.T) {
	forEachSplit(t, func(t *testing.T, input string, k int, want state) {
		withTerm(t, scenarioCols, scenarioRows, func(cut *Instance) {
			require.NoError(t, cut.TerminalSetSize(OptContinuationMaxBytes, 64<<10))
			feed(t, cut, input[:k])
			cont, err := cut.ContinuationAlloc()
			require.NoError(t, err)
			withTerm(t, scenarioCols, scenarioRows, func(replay *Instance) {
				feed(t, replay, snapshot(t, cut)+string(cont)+input[k:])
				assert.Equal(t, want, capture(t, replay), "split at %d", k)
			})
		})
	})
}
