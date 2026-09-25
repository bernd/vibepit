package ghostty

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// skipReconcile are DEC modes a restore must not write: DECCOLM clears the
// screen, 1048 saves the cursor, and the screen modes belong to the screen
// switch.
var skipReconcile = map[uint16]bool{3: true, 47: true, 1047: true, 1048: true, 1049: true}

// penReset clears state that the formatter only emits when it differs from
// the default, so a dirty terminal would keep it: scroll region, SGR (also
// used while drawing content), an open hyperlink, charsets and GL, and the
// kitty keyboard flags.
const penReset = "\x1b[r\x1b[0m\x1b]8;;\x1b\\\x1b(B\x1b)B\x1b*B\x1b+B\x0f\x1b[=0;1u"

// snapshotLeave restores shadow a into b, a stand-in for the real terminal,
// without assuming b was reset. The overlay's snapshot leave must emit the
// same parts in the same order: match the screen, reconcile every mode,
// reset the pen, clear, then the formatter output.
//
// Don't copy the mode loop as is: it writes every mode. On a real
// terminal the shadow's value of a mode the app never set is libghostty's
// default, not the terminal's (autorepeat 8 and cursor blinking 12 are
// off), and enabling a report mode (1004, 2031, 2033, 2048) again sends a
// report. So the overlay writes modes outside the prompt's set only when
// they changed since the cut. Here b is a shadow with the same defaults
// that sends no reports, so writing every mode is harmless and keeps the
// test simple.
func snapshotLeave(tb testing.TB, a, b *Instance) string {
	tb.Helper()
	var s strings.Builder
	isAlt := func(in *Instance) bool {
		v, err := in.GetU32(DataActiveScreen)
		require.NoError(tb, err)
		return int32(v) == ScreenAlternate
	}
	decMode := func(in *Instance, v uint16) bool {
		on, err := in.GetMode(EncodeMode(v, false))
		require.NoError(tb, err)
		return on
	}
	if isAlt(b) && !isAlt(a) {
		// Leave with the mode that entered, or its mode bit stays set.
		switch {
		case decMode(b, 1049):
			s.WriteString("\x1b[?1049l")
		case decMode(b, 1047):
			s.WriteString("\x1b[?1047l")
		default:
			s.WriteString("\x1b[?47l")
		}
	}
	for _, m := range Modes {
		if !m.ANSI && skipReconcile[m.Value] {
			continue
		}
		on, err := a.GetMode(m.Mode())
		require.NoError(tb, err)
		s.WriteString(modeSeq(m, on))
	}
	s.WriteString(penReset)
	s.WriteString("\x1b[2J\x1b[H")
	s.WriteString(snapshot(tb, a))
	return s.String()
}

// modeSeq is the SM or RM sequence that sets m: CSI n h/l for ANSI modes,
// CSI ? n h/l for DEC modes.
func modeSeq(m ModeEntry, on bool) string {
	prefix, suffix := "?", "l"
	if m.ANSI {
		prefix = ""
	}
	if on {
		suffix = "h"
	}
	return fmt.Sprintf("\x1b[%s%d%s", prefix, m.Value, suffix)
}

// dirtyState puts a terminal into random state from the mode table, the alt
// screen, kitty keyboard flags, a scroll region, charsets, SGR and an open
// hyperlink.
func dirtyState(r *rand.Rand) string {
	var s strings.Builder
	for _, m := range Modes {
		if !m.ANSI && skipReconcile[m.Value] {
			continue
		}
		if r.Intn(3) != 0 {
			continue
		}
		s.WriteString(modeSeq(m, r.Intn(2) == 0))
	}
	if r.Intn(2) == 0 {
		s.WriteString("\x1b[?1049h")
	}
	fmt.Fprintf(&s, "\x1b[>%du", r.Intn(32))
	fmt.Fprintf(&s, "\x1b[%d;%dr", 1+r.Intn(3), 5+r.Intn(5))
	s.WriteString([]string{"\x1b(0", "\x1b)0\x0e", "\x1b(A", ""}[r.Intn(4)])
	fmt.Fprintf(&s, "\x1b[%d;%dm", 1+r.Intn(8), 30+r.Intn(8))
	s.WriteString("\x1b]8;;http://dirty\x1b\\dirty text\x1b[5;5H")
	return s.String()
}

// The snapshot path must never assume a reset terminal.
func TestSnapshotRestoreIntoDirtyTerminal(t *testing.T) {
	for _, sc := range scenarios {
		for seed := int64(1); seed <= 20; seed++ {
			t.Run(fmt.Sprintf("%s/seed-%d", sc.name, seed), func(t *testing.T) {
				a := newTerm(t, scenarioCols, scenarioRows)
				b := newTerm(t, scenarioCols, scenarioRows)
				feed(t, a, sc.input)
				feed(t, b, dirtyState(rand.New(rand.NewSource(seed))))
				feed(t, b, snapshotLeave(t, a, b))
				assert.Equal(t, capture(t, a), capture(t, b))

				feed(t, a, probeSuffix)
				feed(t, b, probeSuffix)
				assert.Equal(t, capture(t, a), capture(t, b), "after probe suffix")
			})
		}
	}
}

// Without the pen reset, state that equals the default in a but not in b
// survives the restore. This pins why penReset exists.
func TestFormatterOnlyRestoreLeavesDirtyState(t *testing.T) {
	a := newTerm(t, 20, 5)
	b := newTerm(t, 20, 5)
	feed(t, a, "hi there")
	feed(t, b, "\x1b[2;4r\x1b[>5u")
	feed(t, b, "\x1b[2J\x1b[H"+snapshot(t, a))
	assert.NotEqual(t, capture(t, a), capture(t, b))
}
