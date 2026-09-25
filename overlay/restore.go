package overlay

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/bernd/vibepit/vt"
)

// modeKey names a mode: a DEC mode (CSI ? n h) unless ansi.
type modeKey struct {
	mode uint16
	ansi bool
}

var (
	modeIRM  = modeKey{4, true}
	modeSync = modeKey{2026, false}
	// drawModes are the set-D modes except IRM, which the raw leave writes
	// after the cursor, because the pending-wrap reprint must not insert.
	drawModes = []modeKey{{20, true}, {5, false}, {6, false}, {7, false}, {25, false}, modeSync}
)

// skipReconcile are DEC modes a restore must not write: DECCOLM clears the
// screen, 1048 saves the cursor, and the screen modes belong to the screen
// switch.
var skipReconcile = map[uint16]bool{3: true, 47: true, 1047: true, 1048: true, 1049: true}

// reportModes make some terminals send a report as soon as they are
// enabled: focus (1004), colour scheme (2031), visibility (2033) and
// in-band size (2048). A snapshot writes them only when they changed, so
// the app gets each report once, from the real terminal.
var reportModes = map[uint16]bool{1004: true, 2031: true, 2033: true, 2048: true}

const (
	// penReset clears what the formatter emits only when it differs from
	// the default: the scroll region, SGR, an open hyperlink, the charsets
	// and GL.
	penReset = "\x1b[r\x1b[0m\x1b]8;;\x1b\\\x1b(B\x1b)B\x1b*B\x1b+B\x0f"
	// keyboardReset is the snapshot's addition: the kitty keyboard flags
	// and modifyOtherKeys, which the formatter also emits only when set.
	// The raw leave must not write it, because its pop already restored
	// the app's flags.
	keyboardReset = "\x1b[=0;1u\x1b[>4;0m"
	clearScreen   = "\x1b[2J\x1b[H"
	ris           = "\x1bc"
)

// cutState is the real terminal's state at the cut (T1), read from the
// shadow. The prompt changes only set D, so everything else stays the
// real terminal's known state until the leave.
type cutState struct {
	forced      bool // no ground within the budget; CAN was written
	barrierSent bool
	alt         bool
	modes       map[modeKey]bool
	extras      []byte // scroll region, cursor, pen, hyperlink, protection, charsets
	title, pwd  string
	cursor      vt.CursorStyle
}

// cutExtras restore the set-D state the modes don't cover. The Modes
// extra is left out: the leave writes set-D modes itself.
var cutExtras = vt.Extras{ScrollRegion: true, Cursor: true, Style: true, Hyperlink: true, Protection: true, Charsets: true}

func captureCut(sh *vt.Terminal) (*cutState, error) {
	c := &cutState{modes: make(map[modeKey]bool)}
	var err error
	if c.alt, err = sh.AltScreen(); err != nil {
		return nil, err
	}
	modes, err := sh.Modes()
	if err != nil {
		return nil, err
	}
	for _, m := range modes {
		c.modes[modeKey{m.Mode, m.ANSI}] = m.Value
	}
	if c.extras, err = sh.Format(vt.FormatOptions{Region: vt.RegionNone, Extras: cutExtras}); err != nil {
		return nil, err
	}
	if c.title, err = sh.Title(); err != nil {
		return nil, err
	}
	if c.pwd, err = sh.Pwd(); err != nil {
		return nil, err
	}
	if c.cursor, err = sh.CursorStyle(); err != nil {
		return nil, err
	}
	return c, nil
}

// entered records what the enter sequence changed, so the leave undoes
// exactly that.
type entered struct {
	kitty        bool // pushed kitty keyboard flags 0
	promptScreen bool // switched to the alternate screen with 1047
}

func enterFor(c *cutState) entered { return entered{kitty: true, promptScreen: !c.alt} }

// enterSeq changes only set D: the prompt's screen when the app is on the
// primary one, kitty keyboard, the drawing modes, the scroll region and
// the pen. It clears the screen the prompt draws on in both cases:
// Bubble Tea's renderer assumes a clear screen.
func enterSeq(e entered) string {
	var s strings.Builder
	if e.promptScreen {
		// 1047, not 1049: 1049 saves the cursor into the app's DECSC slot.
		s.WriteString("\x1b[?1047h")
	}
	if e.kitty {
		s.WriteString("\x1b[>0u")
	}
	s.WriteString("\x1b[4l\x1b[20l\x1b[?5l\x1b[?6l\x1b[?7h\x1b[?25h\x1b[?2026l")
	s.WriteString(penReset)
	s.WriteString(clearScreen)
	return s.String()
}

// leaveScreen pops the prompt's kitty flags and leaves its screen.
func leaveScreen(e entered) string {
	var s string
	if e.kitty {
		s += "\x1b[<u"
	}
	if e.promptScreen {
		s += "\x1b[?1047l"
	}
	return s
}

// rawLeave restores the real terminal to the cut and replays the output
// logged since: leave the prompt's screen, set-D modes except IRM, the pen
// reset and the cut extras, IRM, the log. DECOM and DECSTBM come before
// the cursor, because both home it; IRM comes after it, because the
// pending-wrap reprint must not insert. With nothing entered it undoes
// T1's writes, which is the leave after a barrier timeout.
func rawLeave(c *cutState, e entered, log []byte) []byte {
	var b bytes.Buffer
	b.WriteString(leaveScreen(e))
	for _, k := range drawModes {
		b.WriteString(modeSeq(k, c.modes[k]))
	}
	b.WriteString(penReset)
	b.Write(c.extras)
	b.WriteString(modeSeq(modeIRM, c.modes[modeIRM]))
	b.Write(log)
	return b.Bytes()
}

// snapshotExtras are every extra except modes: the snapshot writes each
// mode itself, and the formatter's would enable report modes again.
var snapshotExtras = func() vt.Extras {
	x := vt.AllExtras
	x.Modes = false
	return x
}()

// snapshotLeave rebuilds the real terminal from the shadow without
// assuming anything about it beyond the cut. The order follows
// snapshotLeave in vt/internal/ghostty/restore_test.go: leave the prompt's
// screen, match the screen, reconcile every mode, reset the pen, clear,
// the formatter output. Then come the title, working directory and cursor
// style, which the formatter doesn't emit, and the continuation of an
// unfinished sequence. resync reports that the continuation was
// unavailable, so the caller must drop output up to the next ground.
func snapshotLeave(sh *vt.Terminal, c *cutState, e entered) (out []byte, resync bool, err error) {
	// Synchronized output off, so the real terminal draws the snapshot.
	if err := sh.SetMode(2026, false, false); err != nil {
		return nil, false, err
	}
	alt, err := sh.AltScreen()
	if err != nil {
		return nil, false, err
	}
	modes, err := sh.Modes()
	if err != nil {
		return nil, false, err
	}
	screen, err := sh.Format(vt.FormatOptions{Unwrap: true, Extras: snapshotExtras, Region: vt.RegionScreen})
	if err != nil {
		return nil, false, err
	}
	title, err := sh.Title()
	if err != nil {
		return nil, false, err
	}
	pwd, err := sh.Pwd()
	if err != nil {
		return nil, false, err
	}
	cursor, err := sh.CursorStyle()
	if err != nil {
		return nil, false, err
	}
	cont, err := sh.Continuation()
	switch {
	case errors.Is(err, vt.ErrContinuationUnavailable):
		resync = true
	case err != nil:
		return nil, false, err
	}
	current := make(map[modeKey]bool, len(modes))
	for _, m := range modes {
		current[modeKey{m.Mode, m.ANSI}] = m.Value
	}

	var b bytes.Buffer
	b.WriteString(leaveScreen(e))
	// The real terminal is on the cut's screen now.
	switch {
	case c.alt && !alt:
		b.WriteString(screenSeq(c.modes, false))
	case !c.alt && alt:
		// Enter before the clear, so the primary screen keeps its content.
		// 1049 saves the cursor and pen: put back the cut's first, not the
		// prompt's.
		b.Write(rawLeave(c, entered{}, nil))
		b.WriteString(screenSeq(current, true))
	}
	for _, m := range modes {
		k := modeKey{m.Mode, m.ANSI}
		if !m.ANSI && skipReconcile[m.Mode] {
			continue
		}
		if !m.ANSI && reportModes[m.Mode] && m.Value == c.modes[k] {
			continue
		}
		b.WriteString(modeSeq(k, m.Value))
	}
	b.WriteString(penReset)
	b.WriteString(keyboardReset)
	b.WriteString(clearScreen)
	b.Write(screen)
	// After a forced cut the real terminal's title and directory are
	// unknown: some terminals, ghostty among them, apply an OSC that CAN
	// ends instead of dropping it.
	if title != c.title || c.forced {
		fmt.Fprintf(&b, "\x1b]2;%s\x1b\\", oscText(title))
	}
	if pwd != c.pwd || c.forced {
		fmt.Fprintf(&b, "\x1b]7;%s\x1b\\", oscText(pwd))
	}
	if cursor != c.cursor {
		b.WriteString(cursor.DECSCUSR())
	}
	b.Write(cont)
	return b.Bytes(), resync, nil
}

// resetLeave is the last resort when neither replay nor snapshot is
// possible: RIS, after which the caller nudges the app to repaint.
func resetLeave(e entered) []byte { return []byte(leaveScreen(e) + ris) }

// screenSeq switches the alternate screen on or off with the mode that is
// set in modes, preferring 1049, so no mode bit stays set.
func screenSeq(modes map[modeKey]bool, on bool) string {
	n := uint16(47)
	switch {
	case modes[modeKey{1049, false}]:
		n = 1049
	case modes[modeKey{1047, false}]:
		n = 1047
	}
	return modeSeq(modeKey{n, false}, on)
}

// modeSeq is the SM or RM sequence that sets k to on.
func modeSeq(k modeKey, on bool) string {
	prefix, final := "?", "l"
	if k.ansi {
		prefix = ""
	}
	if on {
		final = "h"
	}
	return fmt.Sprintf("\x1b[%s%d%s", prefix, k.mode, final)
}

// oscText drops controls from an OSC payload, so it can't end the sequence
// early.
func oscText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
}
