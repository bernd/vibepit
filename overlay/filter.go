package overlay

import (
	"io"
	"strings"
)

// outputFilter passes only the overlay program output needed to draw text:
// printable text, SGR styles, cursor movement, erasing, inserting and
// deleting, scrolling, and cursor visibility and synchronized output. It
// drops everything else Bubble Tea sends: keyboard protocol changes
// (modifyOtherKeys, kitty flags), other modes (screen switches, which Show
// owns, bracketed paste, mouse, unicode core, tab stops), OSC strings (titles, colors, hyperlinks,
// clipboard), and all queries, whose replies would otherwise reach the app
// after the prompt closed. The overlay then only has to put back what its
// own enter and leave sequences change.
//
// It also turns LF into CRLF, like a terminal's ONLCR output processing,
// which raw mode switches off. Bubble Tea assumes ONLCR whenever its input
// is not a terminal, as it is for an overlay reading from the input mux,
// and would otherwise draw each line one column further right.
type outputFilter struct {
	w    io.Writer
	scan scanner
	seq  []byte // the escape sequence in progress
	out  []byte
}

// csiFinals are the final bytes of the allowed CSI sequences without a
// prefix or intermediate: cursor movement (A–G, H, I, Z, `, a, d, e, f),
// erase (J, K, X), insert and delete (@, L, M, P), scroll (S, T), repeat
// (b), SGR (m), and scroll margins (r). Margins are restored on leave.
const csiFinals = "@ABCDEFGHIJKLMPSTXZ`abdefmr"

func (f *outputFilter) Write(p []byte) (int, error) {
	f.out = f.out[:0]
	for _, b := range p {
		wasGround := f.scan.state == stGround
		ev := f.scan.step(b)
		switch ev {
		case evText:
			f.out = append(f.out, b)
			continue
		case evControl:
			switch b {
			case '\b', '\t', '\r':
				f.out = append(f.out, b)
			case '\n':
				f.out = append(f.out, '\r', '\n')
			}
			continue
		}
		if b == byteESC || wasGround {
			f.seq = f.seq[:0]
		}
		if f.scan.state == stString || ev == evString {
			continue // strings are dropped whole, no need to keep them
		}
		f.seq = append(f.seq, b)
		switch ev {
		case evEsc:
			if f.scan.escInter == 0 && (b == 'M' || b == 'D' || b == 'E') { // RI, IND, NEL
				f.out = append(f.out, f.seq...)
			}
		case evCSI:
			if f.allowedCSI(b) {
				f.out = append(f.out, f.seq...)
			}
		}
	}
	if len(f.out) > 0 {
		if _, err := f.w.Write(f.out); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (f *outputFilter) allowedCSI(final byte) bool {
	s := &f.scan
	if s.inter != 0 {
		return false
	}
	switch s.prefix {
	case 0:
		return strings.IndexByte(csiFinals, final) >= 0
	case '?':
		if final != 'h' && final != 'l' {
			return false
		}
		for _, p := range s.params {
			if p != 25 && p != 2026 {
				return false
			}
		}
		return true
	}
	return false
}

// fdFile is a terminal file, as Bubble Tea detects it.
type fdFile interface {
	io.Writer
	Fd() uintptr
}

// filterFile keeps the file descriptor visible, so Bubble Tea still reads
// the terminal size and follows resizes. It never closes the terminal.
type filterFile struct {
	*outputFilter
	f fdFile
}

func (c filterFile) Fd() uintptr                { return c.f.Fd() }
func (c filterFile) Read(p []byte) (int, error) { return 0, io.EOF }
func (c filterFile) Close() error               { return nil }

// programOutput wraps the terminal for an overlay program.
func programOutput(w io.Writer) io.Writer {
	f := &outputFilter{w: w}
	if file, ok := w.(fdFile); ok {
		return filterFile{f, file}
	}
	return f
}
