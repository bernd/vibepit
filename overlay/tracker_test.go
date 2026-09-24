package overlay

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestModeTracker_Modes(t *testing.T) {
	def := termModes{CursorVisible: true, AutoWrap: true}
	with := func(f func(*termModes)) termModes {
		m := def
		f(&m)
		return m
	}

	tests := []struct {
		name  string
		input string
		want  termModes
	}{
		{name: "power-on defaults", input: "", want: def},
		{name: "plain text", input: "hello\r\nworld", want: def},
		{name: "alt screen 1049", input: "\x1b[?1049h", want: with(func(m *termModes) { m.AltScreen = true })},
		{name: "alt screen 1047", input: "\x1b[?1047h", want: with(func(m *termModes) { m.AltScreen = true })},
		{name: "alt screen 47", input: "\x1b[?47h", want: with(func(m *termModes) { m.AltScreen = true })},
		{name: "alt screen left", input: "\x1b[?1049h\x1b[?1049l", want: def},
		{name: "hidden cursor", input: "\x1b[?25l", want: with(func(m *termModes) { m.CursorVisible = false })},
		{name: "multiple params in one sequence", input: "\x1b[?1000;25;7l", want: with(func(m *termModes) {
			m.CursorVisible = false
			m.AutoWrap = false
		})},
		{name: "insert mode", input: "\x1b[4h", want: with(func(m *termModes) { m.InsertMode = true })},
		{name: "insert mode off", input: "\x1b[4h\x1b[4l", want: def},
		{name: "private 4 is not insert mode", input: "\x1b[?4h", want: def},
		{name: "autowrap off", input: "\x1b[?7l", want: with(func(m *termModes) { m.AutoWrap = false })},
		{name: "sync output open", input: "\x1b[?2026h", want: with(func(m *termModes) { m.SyncOutput = true })},
		{name: "sync output closed", input: "\x1b[?2026h frame \x1b[?2026l", want: def},
		{name: "ANSI 25 is not a private mode", input: "\x1b[25l", want: def},
		{name: "scroll region", input: "\x1b[3;20r", want: with(func(m *termModes) { m.ScrollTop, m.ScrollBottom = 3, 20 })},
		{name: "scroll region top only", input: "\x1b[2r", want: with(func(m *termModes) { m.ScrollTop = 2 })},
		{name: "scroll region reset", input: "\x1b[3;20r\x1b[r", want: def},
		{name: "scroll region reset explicit", input: "\x1b[3;20r\x1b[1r", want: def},
		{name: "xterm restore private mode is not DECSTBM", input: "\x1b[?1r", want: def},
		{name: "margins are per screen", input: "\x1b[?1049h\x1b[1;2r\x1b[?1049l", want: def},
		{name: "alternate screen starts without margins", input: "\x1b[3;20r\x1b[?1049h", want: with(func(m *termModes) { m.AltScreen = true })},
		{name: "main margins survive the alternate screen", input: "\x1b[3;20r\x1b[?1049h\x1b[1;2r\x1b[?1049l", want: with(func(m *termModes) { m.ScrollTop, m.ScrollBottom = 3, 20 })},
		{name: "DECSTR resets margins, insert mode, cursor", input: "\x1b[3;20r\x1b[4h\x1b[?25l\x1b[!p", want: def},
		{name: "RIS resets everything", input: "\x1b[?1049h\x1b[?25l\x1b[4h\x1b[?7l\x1b[3;20r\x1bc", want: def},
		{name: "OSC with BEL is skipped", input: "\x1b]0;[?25l\a", want: def},
		{name: "OSC with ST is skipped", input: "\x1b]0;title\x1b\\\x1b[?25l", want: with(func(m *termModes) { m.CursorVisible = false })},
		{name: "ESC inside string starts a new sequence", input: "\x1b]0;x\x1b[?25l", want: with(func(m *termModes) { m.CursorVisible = false })},
		{name: "DCS is skipped", input: "\x1bP+q544e\x1b\\", want: def},
		{name: "CSI cut short by ESC", input: "\x1b[?10\x1b[?25l", want: with(func(m *termModes) { m.CursorVisible = false })},
		{name: "CAN aborts CSI", input: "\x1b[?25\x18l", want: def},
		{name: "charset designation", input: "\x1b(B\x1b[?25l", want: with(func(m *termModes) { m.CursorVisible = false })},
		{name: "C0 inside CSI executes, sequence continues", input: "\x1b[?2\n5l", want: with(func(m *termModes) { m.CursorVisible = false })},
		{name: "C0 after ESC executes, sequence continues", input: "\x1b\r[?25l", want: with(func(m *termModes) { m.CursorVisible = false })},
		{name: "C0 after intermediate executes, sequence continues", input: "\x1b(\nB\x1b[?25l", want: with(func(m *termModes) { m.CursorVisible = false })},
		{name: "DEL inside CSI is ignored", input: "\x1b[?2\x7f5l", want: with(func(m *termModes) { m.CursorVisible = false })},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := newModeTracker()
			tr.Feed([]byte(tt.input))
			assert.Equal(t, tt.want, tr.Snapshot())
			assert.True(t, tr.scan.inGround())

			// Byte-at-a-time feeding must give the same result.
			tr = newModeTracker()
			for i := range len(tt.input) {
				tr.Feed([]byte{tt.input[i]})
			}
			assert.Equal(t, tt.want, tr.Snapshot(), "split feed")
		})
	}
}

func TestModeTracker_CanCut(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		ground bool
		cut    bool
	}{
		{name: "empty", input: "", ground: true, cut: true},
		{name: "text", input: "abc", ground: true, cut: true},
		{name: "lone ESC", input: "\x1b", ground: false},
		{name: "ESC with intermediate", input: "\x1b(", ground: false},
		{name: "partial CSI", input: "\x1b[?104", ground: false},
		{name: "partial OSC", input: "\x1b]0;tit", ground: false},
		{name: "OSC waiting for ST", input: "\x1b]0;t\x1b", ground: false},
		{name: "complete CSI", input: "\x1b[1m", ground: true, cut: true},
		{name: "partial UTF-8", input: "\xe2\x94", ground: false},
		{name: "complete UTF-8", input: "\xe2\x94\x80", ground: true, cut: true},
		{name: "open sync batch", input: "\x1b[?2026hframe", ground: true, cut: false},
		{name: "closed sync batch", input: "\x1b[?2026hframe\x1b[?2026l", ground: true, cut: true},
		{name: "control inside CSI keeps the sequence open", input: "\x1b[31\n", ground: false},
		{name: "control after ESC keeps the sequence open", input: "\x1b\r", ground: false},
		{name: "CAN ends the sequence", input: "\x1b[31\x18", ground: true, cut: true},
		{name: "fresh cursor save", input: "\x1b7progress", ground: true, cut: false},
		{name: "cursor save restored", input: "\x1b7progress\x1b8", ground: true, cut: true},
		{name: "DECALN is not a cursor save", input: "\x1b#8", ground: true, cut: true},
		{name: "ESC after an intermediate starts fresh", input: "\x1b(\x1b7progress", ground: true, cut: false},
		{name: "fresh SCOSC", input: "\x1b[sprogress", ground: true, cut: false},
		{name: "SCOSC restored", input: "\x1b[sprogress\x1b[u", ground: true, cut: true},
		{name: "fresh DECSET 1048", input: "\x1b[?1048hprogress", ground: true, cut: false},
		{name: "DECSET 1048 restored", input: "\x1b[?1048hprogress\x1b[?1048l", ground: true, cut: true},
		{name: "DECSLRM is not a save", input: "\x1b[2;40s", ground: true, cut: true},
		{name: "kitty keyboard is not a restore", input: "\x1b7\x1b[>1u", ground: true, cut: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := newModeTracker()
			tr.Feed([]byte(tt.input))
			assert.Equal(t, tt.ground, tr.scan.inGround())
			assert.Equal(t, tt.cut, tr.canCut())
		})
	}
}

func TestModeTracker_CursorSaveHold(t *testing.T) {
	now := time.Unix(1000, 0)
	tr := newModeTracker()
	tr.now = func() time.Time { return now }

	tr.Feed([]byte("\x1b7"))
	assert.False(t, tr.canCut(), "no cut right after a cursor save")
	now = now.Add(cursorSaveHold)
	assert.True(t, tr.canCut(), "an app that never restores only delays the cut")
}

func TestModeTracker_CPRReply(t *testing.T) {
	now := time.Unix(1000, 0)
	tr := newModeTracker()
	tr.now = func() time.Time { return now }

	assert.False(t, tr.cprReply(), "no query yet")
	tr.Feed([]byte("\x1b[5n\x1b[?15n\x1b[?6n"))
	assert.False(t, tr.cprReply(), "other status queries and DECXCPR")

	tr.Feed([]byte("\x1b[6n\x1b[6n"))
	now = now.Add(5 * time.Second)
	assert.True(t, tr.cprReply(), "a slow reply still counts")
	assert.True(t, tr.cprReply(), "one reply per query")
	assert.False(t, tr.cprReply(), "all queries answered: F3 is a key")

	tr.Feed([]byte("\x1b[6n"))
	now = now.Add(cprExpiry)
	assert.False(t, tr.cprReply(), "an unanswered query expires")

	for range maxCPRPending + 5 {
		tr.Feed([]byte("\x1b[6n"))
	}
	for range maxCPRPending {
		assert.True(t, tr.cprReply())
	}
	assert.False(t, tr.cprReply(), "pending queries are bounded")
}

func TestModeTracker_ForceCut(t *testing.T) {
	tr := newModeTracker()
	tr.Feed([]byte("\x1b[?2026h\x1b]0;never ends"))
	m, aborted := tr.forceCut()
	assert.True(t, aborted)
	assert.True(t, m.SyncOutput, "modes as they were at the cut")

	// The overlay sent CAN and ended the batch: the tracker follows.
	assert.True(t, tr.canCut())
	tr.Feed([]byte("\x1b[?25l"))
	assert.False(t, tr.Snapshot().CursorVisible, "parsing resumes from ground")

	_, aborted = tr.forceCut()
	assert.False(t, aborted, "nothing to abort in ground")
}

func TestModes_MarginSequence(t *testing.T) {
	assert.Equal(t, "\x1b[r", string(termModes{}.MarginSequence()))
	assert.Equal(t, "\x1b[3;20r", string(termModes{ScrollTop: 3, ScrollBottom: 20}.MarginSequence()))
	assert.Equal(t, "\x1b[1;2r", string(termModes{ScrollBottom: 2}.MarginSequence()))
	assert.Equal(t, "\x1b[4r", string(termModes{ScrollTop: 4}.MarginSequence()))
}
