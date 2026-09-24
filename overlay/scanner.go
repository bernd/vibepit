package overlay

const (
	stGround    = iota
	stEsc       // saw ESC
	stEscInter  // ESC followed by intermediate bytes, e.g. ESC ( B
	stCsi       // inside CSI, waiting for the final byte
	stString    // inside OSC, DCS, SOS, PM, or APC
	stStringEsc // saw ESC inside a string, likely ST
)

const (
	byteBEL = 0x07
	byteCAN = 0x18
	byteSUB = 0x1a
	byteESC = 0x1b
	byteDEL = 0x7f
)

// maxParams bounds how many CSI parameters are recorded. Longer sequences
// are still parsed, the excess parameters are dropped.
const maxParams = 16

// event is what a byte completed, as seen by scanner.step.
type event int

const (
	evNone    event = iota // inside a sequence, or ignored
	evText                 // a printable byte, including UTF-8 bytes
	evControl              // a C0 control the terminal executes
	evEsc                  // an ESC sequence ended, final byte in hand
	evCSI                  // a CSI sequence ended, final byte in hand
	evString               // an OSC, DCS, SOS, PM, or APC string ended
)

// scanner is the escape-sequence grammar of a VT500-style terminal parser:
// ground, ESC, CSI, and control strings. It tells where sequences begin and
// end, and records CSI parameters. It does not interpret anything.
type scanner struct {
	state int
	// utf8Rest counts the continuation bytes still missing from a UTF-8
	// sequence in ground state.
	utf8Rest int

	escInter byte // last intermediate byte of an ESC sequence, 0 if none

	prefix byte // first CSI parameter byte if it is one of < = > ?
	inter  byte // last CSI intermediate byte, 0 if none
	params []int
	cur    int
	hasCur bool
}

// inGround reports whether the stream is outside any escape sequence and
// multibyte character, so bytes of another writer can be inserted safely.
func (s *scanner) inGround() bool {
	return s.state == stGround && s.utf8Rest == 0
}

// abort puts the scanner in ground state, as a CAN sent to the terminal does.
func (s *scanner) abort() {
	s.state = stGround
	s.utf8Rest = 0
}

// isC0 reports whether b is a C0 control that executes inside a sequence.
// ESC, CAN, and SUB instead restart or abort the sequence.
func isC0(b byte) bool {
	return b < 0x20 && b != byteESC && b != byteCAN && b != byteSUB
}

func (s *scanner) step(b byte) event {
	// CAN and SUB abort any sequence in progress.
	if b == byteCAN || b == byteSUB {
		s.abort()
		return evControl
	}
	switch s.state {
	case stGround:
		return s.ground(b)
	case stEsc:
		return s.esc(b)
	case stEscInter:
		switch {
		case b >= 0x20 && b <= 0x2f:
			s.escInter = b
		case isC0(b):
			return evControl
		case b == byteESC:
			s.enterEsc()
		case b == byteDEL:
		default:
			s.state = stGround
			return evEsc
		}
	case stCsi:
		return s.csi(b)
	case stString:
		switch b {
		case byteBEL:
			s.state = stGround
			return evString
		case byteESC:
			s.state = stStringEsc
		}
	case stStringEsc:
		if b == '\\' {
			s.state = stGround
			return evString
		}
		// Not ST: the ESC aborted the string and starts a new sequence.
		s.enterEsc()
		return s.esc(b)
	}
	return evNone
}

// enterEsc starts a new escape sequence, dropping any unfinished one.
func (s *scanner) enterEsc() {
	s.state = stEsc
	s.escInter = 0
}

func (s *scanner) ground(b byte) event {
	switch {
	case b == byteESC:
		s.utf8Rest = 0
		s.enterEsc()
		return evNone
	case b < 0x20:
		s.utf8Rest = 0
		return evControl
	case b == byteDEL:
		return evNone
	case b&0xc0 == 0x80:
		if s.utf8Rest > 0 {
			s.utf8Rest--
		}
	case b&0xe0 == 0xc0:
		s.utf8Rest = 1
	case b&0xf0 == 0xe0:
		s.utf8Rest = 2
	case b&0xf8 == 0xf0:
		s.utf8Rest = 3
	default:
		s.utf8Rest = 0
	}
	return evText
}

func (s *scanner) esc(b byte) event {
	switch {
	case b == '[':
		s.state = stCsi
		s.prefix = 0
		s.inter = 0
		s.params = s.params[:0]
		s.cur = 0
		s.hasCur = false
	case b == ']' || b == 'P' || b == 'X' || b == '^' || b == '_':
		s.state = stString
	case b == byteESC:
		s.enterEsc()
	case b >= 0x20 && b <= 0x2f:
		s.state = stEscInter
		s.escInter = b
	case isC0(b):
		s.state = stEsc
		return evControl
	case b == byteDEL:
		s.state = stEsc
	default:
		s.state = stGround
		return evEsc
	}
	return evNone
}

func (s *scanner) csi(b byte) event {
	switch {
	case b >= '0' && b <= '9':
		if s.cur < 1_000_000 {
			s.cur = s.cur*10 + int(b-'0')
		}
		s.hasCur = true
	case b == ';' || b == ':':
		s.pushParam()
	case b >= '<' && b <= '?':
		if s.prefix == 0 && len(s.params) == 0 && !s.hasCur {
			s.prefix = b
		}
	case b >= 0x20 && b <= 0x2f:
		s.inter = b
	case b >= 0x40 && b <= 0x7e:
		s.pushParam()
		s.state = stGround
		return evCSI
	case b == byteESC:
		// A CSI cut short by a new ESC: the terminal restarts parsing there.
		s.enterEsc()
	case isC0(b):
		return evControl
	}
	return evNone
}

func (s *scanner) pushParam() {
	if len(s.params) < maxParams {
		v := -1 // omitted
		if s.hasCur {
			v = s.cur
		}
		s.params = append(s.params, v)
	}
	s.cur = 0
	s.hasCur = false
}

// param returns CSI parameter i, or def when it is missing or omitted.
func (s *scanner) param(i, def int) int {
	if i < len(s.params) && s.params[i] >= 0 {
		return s.params[i]
	}
	return def
}
