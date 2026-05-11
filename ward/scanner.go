package ward

// escScanner recognizes the small set of terminal escape sequences that
// affect ward's protected bar row. It is not a full terminal emulator —
// its only job is to detect sequences that invalidate the scroll region
// or erase the bar, and to track whether the byte stream is currently
// inside an escape sequence (so ward can avoid injecting its own
// sequences mid-stream).
type escScanner struct {
	state        int
	csiHasParams bool
	csiParamByte byte
	csiNumParam  int
}

// scanResult reports what the scanner found in a chunk of PTY output.
type scanResult struct {
	ScrollReset bool // ESC c, parameterless CSI r, or alternate screen toggle
	BarErased   bool // CSI J, CSI 0J, CSI 2J, or CSI 3J
}

const (
	esGround    = iota
	esEsc       // saw ESC, waiting for command byte
	esCsi       // inside CSI sequence, waiting for final byte
	esString    // inside OSC/DCS/PM/APC string
	esStringEsc // saw ESC inside string (likely ST)
)

const (
	asciiBEL byte = 0x07
	asciiESC byte = 0x1B

	csiParamMin byte = 0x30
	csiParamMax byte = 0x3F
	csiFinalMin byte = 0x40
	csiFinalMax byte = 0x7E
)

// Scan processes a chunk of PTY output and returns which ward-relevant
// sequences were detected. The scanner state is preserved across calls
// to handle sequences split across read boundaries. A result may report
// ScrollReset or BarErased while InGround() is still false (e.g. an
// erase followed by an incomplete OSC in the same chunk). Callers must
// defer injection until InGround() returns true.
func (s *escScanner) Scan(data []byte) scanResult {
	var r scanResult
	for _, b := range data {
		switch s.state {
		case esGround:
			if b == asciiESC {
				s.state = esEsc
			}
		case esEsc:
			switch b {
			case 'c':
				r.ScrollReset = true
				s.state = esGround
			case '[':
				s.state = esCsi
				s.csiHasParams = false
				s.csiParamByte = 0
				s.csiNumParam = 0
			case ']', 'P', '^', '_':
				s.state = esString
			default:
				s.state = esGround
			}
		case esCsi:
			if b >= csiParamMin && b <= csiParamMax {
				if !s.csiHasParams {
					s.csiParamByte = b
				}
				s.csiHasParams = true
				if s.csiParamByte == '?' && b >= '0' && b <= '9' {
					s.csiNumParam = s.csiNumParam*10 + int(b-'0')
				}
			} else if b >= csiFinalMin && b <= csiFinalMax {
				switch {
				case b == 'r' && !s.csiHasParams:
					r.ScrollReset = true
				case b == 'J' && (!s.csiHasParams || s.csiParamByte == '0' || s.csiParamByte == '2' || s.csiParamByte == '3'):
					r.BarErased = true
				case (b == 'h' || b == 'l') && s.csiParamByte == '?' && isAltScreenMode(s.csiNumParam):
					r.ScrollReset = true
				}
				s.state = esGround
			}
		case esString:
			switch b {
			case asciiBEL:
				s.state = esGround
			case asciiESC:
				s.state = esStringEsc
			}
		case esStringEsc:
			if b == 'c' {
				r.ScrollReset = true
			}
			s.state = esGround
		}
	}
	return r
}

// InGround reports whether the scanner is in the ground state (not
// inside any escape sequence). Ward only injects its own sequences
// when this returns true to avoid corrupting the child's output.
func (s *escScanner) InGround() bool {
	return s.state == esGround
}

func isAltScreenMode(param int) bool {
	return param == 1049 || param == 1047 || param == 47
}
