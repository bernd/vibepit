package overlay

import (
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// filterPendingMax bounds an unfinished sequence the filter holds back.
const filterPendingMax = 64 << 10

// csiAllowed are the finals of CSI sequences without prefix or
// intermediate that pass: cursor movement (CUP CUU CUD CUF CUB CHA VPA CNL
// CPL), erasing and editing (ED EL ECH ICH DCH IL DL), SGR, DECSTBM, and
// the sequences Bubble Tea's renderer picks by TERM, which also only move
// the cursor or edit content: REP HPA HPR VPR CHT CBT SU SD. ED passes only
// with a single parameter up to 2.
const csiAllowed = "HABCDGdEF" + "JKX@PLM" + "mr" + "b`aeIZST"

type filter struct {
	w       io.Writer
	p       *ansi.Parser
	pending []byte
	out     []byte // reused across writes; w must not keep it
	cr      bool   // the last byte passed was CR
}

// NewFilter returns a writer that passes only the output a prompt may send
// to the real terminal: text, BS, HT, LF, CR, cursor movement, erasing and
// editing, SGR, DECSTBM, and modes 25 and 2026. Everything else is
// dropped, every query included, so the prompt changes only state the
// leave restores. A bare LF gets a CR in front, as a TTY with ONLCR would
// add.
func NewFilter(w io.Writer) io.Writer {
	return &filter{w: w, p: ansi.NewParser()}
}

func (f *filter) Write(b []byte) (int, error) {
	data := b
	if len(f.pending) > 0 {
		f.pending = append(f.pending, b...)
		data, f.pending = f.pending, nil
	}
	out := f.out[:0]
	for len(data) > 0 {
		if data[0] >= utf8.RuneSelf && !utf8.FullRune(data) {
			// A character split across writes: wait for the rest.
			f.pending = append([]byte(nil), data...)
			break
		}
		seq, _, n, state := ansi.DecodeSequence(data, ansi.NormalState, f.p)
		if n <= 0 {
			break
		}
		if state != ansi.NormalState && n == len(data) {
			// A sequence split across writes: wait for the rest, within
			// reason.
			if len(data) <= filterPendingMax {
				f.pending = append([]byte(nil), data...)
			}
			break
		}
		if allowed(seq, f.p) {
			if len(seq) == 1 && seq[0] == ansi.LF && !f.cr {
				// Bubble Tea's renderer writes a bare LF for a new line
				// when its input isn't a TTY, counting on the TTY to add
				// the CR (ONLCR). The real terminal is in raw mode.
				out = append(out, ansi.CR)
			}
			out = append(out, seq...)
			f.cr = len(seq) == 1 && seq[0] == ansi.CR
		}
		data = data[n:]
	}
	f.out = out
	if len(out) > 0 {
		if _, err := f.w.Write(out); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

// allowed reports whether seq, just decoded by p, may reach the terminal.
func allowed(seq []byte, p *ansi.Parser) bool {
	switch c := seq[0]; {
	case c == ansi.ESC && len(seq) > 1 && seq[1] == '[':
		return allowedCSI(ansi.Cmd(p.Command()), p.Params())
	case c == ansi.ESC:
		// RI, IND and NEL move the cursor, scrolling at the margins.
		return len(seq) == 2 && strings.IndexByte("MDE", seq[1]) >= 0
	case c < 0x20 || c == ansi.DEL:
		return c == ansi.BS || c == ansi.HT || c == ansi.LF || c == ansi.CR
	default:
		// An invalid byte decodes as RuneError of size 1. It may be an 8-bit
		// C1 introducer (0x9b CSI, 0x9d OSC, 0x90 DCS) the parser decoded a
		// whole sequence from, which a terminal honouring 8-bit controls
		// would act on.
		r, size := utf8.DecodeRune(seq)
		if r == utf8.RuneError && size <= 1 {
			return false
		}
		return !unicode.IsControl(r) // no C1 controls
	}
}

func allowedCSI(cmd ansi.Cmd, params ansi.Params) bool {
	if cmd.Intermediate() != 0 {
		return false
	}
	switch cmd.Prefix() {
	case 0:
		if cmd.Final() == 'J' && (len(params) > 1 || (len(params) == 1 && params[0].Param(0) > 2)) {
			// ED 3 clears the scrollback, which no leave restores.
			return false
		}
		return cmd.Final() != 0 && strings.IndexByte(csiAllowed, cmd.Final()) >= 0
	case '?':
		if (cmd.Final() != 'h' && cmd.Final() != 'l') || len(params) != 1 {
			return false
		}
		mode := params[0].Param(0)
		return mode == 25 || mode == 2026
	default:
		return false
	}
}
