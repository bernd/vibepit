// Package overlay draws a Bubble Tea program over a running terminal session
// and puts the session's screen back afterwards.
//
// The session's two byte streams are owned by an inputMux and an outputMux,
// which hand each stream to exactly one consumer at a time. A modeTracker
// passively parses the session output so the overlay knows where it can cut
// the stream and which terminal settings to put back. Terminal ties them
// together and implements the pause, draw, restore sequence in Show.
//
// Screen contents are never copied: on the main screen the terminal saves
// and restores them itself, and an app on the alternate screen redraws. The
// overlay limits what it changes instead of tracking everything the app
// might set: its program's output passes an allowlist filter, and the
// tracker covers exactly the settings the enter and leave sequences touch.
package overlay

import (
	"fmt"
	"sync"
	"time"
)

// termModes is the terminal state the overlay changes and puts back.
type termModes struct {
	AltScreen     bool // DECSET 1049, 1047, 47
	CursorVisible bool // DECSET 25, default on
	SyncOutput    bool // DECSET 2026, open batch
	InsertMode    bool // IRM, CSI 4 h
	AutoWrap      bool // DECAWM, DECSET 7, default on
	// ScrollTop and ScrollBottom are the DECSTBM margins as the app set
	// them, 1-based. 0 means unset: top of screen, bottom of screen.
	ScrollTop    int
	ScrollBottom int
}

// HasMargins reports whether the app set a scroll region.
func (m termModes) HasMargins() bool { return m.ScrollTop > 1 || m.ScrollBottom > 0 }

// MarginSequence returns the DECSTBM sequence setting the app's scroll
// region. Like any DECSTBM it moves the cursor.
func (m termModes) MarginSequence() []byte {
	if !m.HasMargins() {
		return []byte(resetMargins)
	}
	if m.ScrollBottom == 0 {
		return fmt.Appendf(nil, "\x1b[%dr", max(m.ScrollTop, 1))
	}
	return fmt.Appendf(nil, "\x1b[%d;%dr", max(m.ScrollTop, 1), m.ScrollBottom)
}

// cursorSaveHold is how long after an unmatched cursor save (ESC 7, CSI s,
// or DECSET 1048) the stream is not cut. The overlay's own save shares the terminal's single
// slot, so cutting between an app's ESC 7 and ESC 8 would move the app's
// cursor. Apps that save and never restore only delay the prompt by this.
const cursorSaveHold = 50 * time.Millisecond

// A CSI … R on the input is a cursor position reply, rather than F3 with
// modifiers that some terminals encode the same way, while a cursor
// position query (DSR 6) the session sent is unanswered. A query older than
// cprExpiry is taken as lost, as terminals that do not answer leave it, so
// F3 becomes a key again. At most maxCPRPending queries are remembered.
const (
	cprExpiry     = 10 * time.Second
	maxCPRPending = 16
)

// modeTracker is a passive parser over session output. It is not a terminal
// emulator: it does not track cursor position or cell contents, only the
// escape-sequence state and the modes the overlay touches. All methods are
// safe for concurrent use.
type modeTracker struct {
	mu  sync.Mutex
	now func() time.Time

	scan  scanner
	modes termModes
	// margins holds the DECSTBM top and bottom per screen, main at 0 and
	// alternate at 1. Terminals differ here: some keep one scroll region
	// for both screens. Tracking per screen, and starting the alternate
	// screen without one, never restores margins onto a screen that was not
	// given them, which would trap a shell's output in a few rows. The
	// overlay resets the region on entry anyway, so a region the tracker
	// misses only costs that region, not the prompt.
	margins [2][2]int
	// savedAt is when the app saved the cursor without restoring it yet.
	savedAt time.Time
	// cprPending holds when the app asked for the cursor position without
	// a reply yet, oldest first.
	cprPending []time.Time
}

// newModeTracker returns a tracker in the terminal's power-on state.
func newModeTracker() *modeTracker {
	t := &modeTracker{now: time.Now}
	t.reset()
	return t
}

func (t *modeTracker) reset() {
	t.modes = termModes{CursorVisible: true, AutoWrap: true}
	t.margins = [2][2]int{}
	t.savedAt = time.Time{}
}

// Feed advances the tracker over p. Sequences may be split across calls.
func (t *modeTracker) Feed(p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, b := range p {
		switch t.scan.step(b) {
		case evEsc:
			t.escape(b)
		case evCSI:
			t.dispatch(b)
		}
	}
}

// canCut reports whether the stream can be cut here cleanly: outside any
// sequence, synchronized-output batch, and fresh cursor save.
func (t *modeTracker) canCut() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	saved := !t.savedAt.IsZero() && t.now().Sub(t.savedAt) < cursorSaveHold
	return t.scan.inGround() && !t.modes.SyncOutput && !saved
}

// cprReply reports whether the app waits for a cursor position reply, and
// if so counts one as received.
func (t *modeTracker) cprReply() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	for len(t.cprPending) > 0 && now.Sub(t.cprPending[0]) >= cprExpiry {
		t.cprPending = t.cprPending[1:]
	}
	if len(t.cprPending) == 0 {
		return false
	}
	t.cprPending = t.cprPending[1:]
	return true
}

// forceCut records what the overlay does when it cuts the stream anyway:
// it aborts the sequence in progress and ends the batch. The tracker then
// matches the terminal again. It returns the modes before, and whether a
// sequence was aborted.
func (t *modeTracker) forceCut() (termModes, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	m := t.snapshot()
	aborted := !t.scan.inGround()
	t.scan.abort()
	t.modes.SyncOutput = false
	return m, aborted
}

// Snapshot returns the current modes.
func (t *modeTracker) Snapshot() termModes {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snapshot()
}

func (t *modeTracker) snapshot() termModes {
	m := t.modes
	m.ScrollTop, m.ScrollBottom = t.margins[t.screen()][0], t.margins[t.screen()][1]
	return m
}

func (t *modeTracker) screen() int {
	if t.modes.AltScreen {
		return 1
	}
	return 0
}

func (t *modeTracker) escape(final byte) {
	if t.scan.escInter != 0 {
		return
	}
	switch final {
	case 'c': // RIS
		t.reset()
	case '7': // DECSC
		t.cursorSave(true)
	case '8': // DECRC
		t.cursorSave(false)
	}
}

// cursorSave notes a save or restore of the terminal's saved cursor.
func (t *modeTracker) cursorSave(save bool) {
	if save {
		t.savedAt = t.now()
	} else {
		t.savedAt = time.Time{}
	}
}

func (t *modeTracker) dispatch(final byte) {
	s := &t.scan
	switch {
	case s.prefix == '?' && s.inter == 0 && (final == 'h' || final == 'l'):
		for _, p := range s.params {
			t.setPrivateMode(p, final == 'h')
		}
	case s.prefix == 0 && s.inter == 0 && (final == 'h' || final == 'l'):
		for _, p := range s.params {
			if p == 4 {
				t.modes.InsertMode = final == 'h'
			}
		}
	case s.prefix == 0 && s.inter == 0 && final == 'r':
		m := termModes{ScrollTop: s.param(0, 0), ScrollBottom: s.param(1, 0)}
		if m.HasMargins() {
			t.margins[t.screen()] = [2]int{m.ScrollTop, m.ScrollBottom}
		} else {
			t.margins[t.screen()] = [2]int{}
		}
	case s.prefix == 0 && s.inter == 0 && (final == 's' || final == 'u') && s.param(0, -1) < 0 && len(s.params) <= 1:
		// SCOSC and SCORC. With parameters, CSI s sets left and right
		// margins; treating a bare one as a save at most delays a cut.
		t.cursorSave(final == 's')
	case s.prefix == 0 && s.inter == 0 && final == 'n' && s.param(0, 0) == 6:
		// DECXCPR, CSI ? 6 n, is answered with a ? and needs no tracking.
		if len(t.cprPending) == maxCPRPending {
			t.cprPending = t.cprPending[1:]
		}
		t.cprPending = append(t.cprPending, t.now())
	case s.prefix == 0 && s.inter == '!' && final == 'p':
		// DECSTR soft reset: the parts of it terminals agree on.
		t.modes.CursorVisible = true
		t.modes.InsertMode = false
		t.margins[t.screen()] = [2]int{}
	}
}

func (t *modeTracker) setPrivateMode(mode int, on bool) {
	switch mode {
	case 1049, 1047, 47:
		if on && !t.modes.AltScreen {
			t.margins[1] = [2]int{}
		}
		t.modes.AltScreen = on
	case 25:
		t.modes.CursorVisible = on
	case 7:
		t.modes.AutoWrap = on
	case 1048:
		t.cursorSave(on)
	case 2026:
		t.modes.SyncOutput = on
	}
}
