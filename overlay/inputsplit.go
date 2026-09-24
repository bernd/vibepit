package overlay

import "bytes"

// unitKind classifies a unit of terminal input for routing.
type unitKind int

const (
	unitUser   unitKind = iota // typed, pasted, or clicked by the user
	unitReport                 // sent by the terminal for the session: a query reply or focus report
)

// splitInput cuts terminal input into units: a character, a control, or an
// escape sequence, and emits each complete one. It returns what is left
// unfinished at the end of p, for the caller to complete with the next
// read, and whether that is already recognizably a reply. cpr tells whether
// a CSI … R is a cursor position reply, and counts it; it may be nil.
func splitInput(p []byte, cpr func() bool, emit func(unit []byte, kind unitKind)) (rest []byte, restReport bool) {
	var s scanner
	start := 0
	kind := unitUser
	str := stringOpen
	for i, b := range p {
		switch s.step(b) {
		case evCSI:
			kind = classifyCSI(&s, b, cpr)
		case evString:
			if str >= stringMaybe {
				kind = unitReport
			}
		}
		if s.state == stString && str < stringReply {
			// Users type ESC ] and the like as Alt+]. Once what follows
			// cannot start a reply, it is typing: cut the string short.
			if str = classifyString(p[start : i+1]); str == stringNot {
				s.abort()
			}
		}
		if s.inGround() {
			emit(p[start:i+1], kind)
			start, kind, str = i+1, unitUser, stringOpen
		}
	}
	rest = p[start:]
	switch s.state {
	case stString, stStringEsc:
		restReport = str >= stringMaybe
	case stCsi:
		restReport = s.prefix == '?' || s.prefix == '>'
	}
	return rest, restReport
}

// classifyCSI tells the terminal's replies and focus reports from user
// input. Replies: status (n), mode (y), and window (t) reports, device
// attributes (c with a prefix), the kitty keyboard flags (? u), and cursor
// position (R). Focus: bare I and O. Some terminals encode F3 with
// modifiers as CSI 1;m R, like a cursor position reply, so a bare-prefix R
// is a reply only while cpr says the session is waiting for one. F3 pressed
// just then still goes to the session.
func classifyCSI(s *scanner, final byte, cpr func() bool) unitKind {
	bare := len(s.params) == 1 && s.params[0] < 0
	switch {
	case final == 'n', final == 't':
		return unitReport
	case final == 'R' && s.prefix == '?':
		return unitReport
	case final == 'R' && s.prefix == 0 && s.inter == 0 && cpr != nil && cpr():
		return unitReport
	case final == 'y' && s.inter == '$':
		return unitReport
	case final == 'c' && s.prefix != 0:
		return unitReport
	case final == 'u' && s.prefix == '?':
		return unitReport
	case s.prefix == 0 && bare && (final == 'I' || final == 'O'):
		return unitReport
	}
	return unitUser
}

// stringClass is how far the start of a control string matches a reply.
type stringClass int

const (
	stringNot   stringClass = iota // no reply starts like this
	stringOpen                     // nothing after the introducer yet
	stringMaybe                    // the start of a reply, or of typing
	stringReply                    // a reply
)

// dcsReplies are the starts of DCS replies: DECRQSS, XTGETTCAP (some
// terminals leave out its digit), XTVERSION, DECRPTUI, DECTABSR, and DECCIR.
var dcsReplies = [][]byte{[]byte("1$r"), []byte("0$r"), []byte("1+r"), []byte("0+r"), []byte("+r"), []byte(">|"), []byte("!|"), []byte("2$u"), []byte("1$u")}

// classifyString tells from unit, ESC, an introducer, and what followed so
// far, whether it is a control string a terminal sends as a reply: OSC with
// a number and a semicolon, DCS with one of dcsReplies, or APC G, the kitty
// graphics response. Terminals send no SOS or PM.
func classifyString(unit []byte) stringClass {
	if len(unit) < 2 || unit[0] != byteESC {
		return stringNot
	}
	body := unit[2:]
	if len(body) == 0 && unit[1] != 'X' && unit[1] != '^' {
		return stringOpen
	}
	switch unit[1] {
	case ']':
		i := 0
		for i < len(body) && body[i] >= '0' && body[i] <= '9' {
			i++
		}
		switch {
		case i == len(body):
			return stringMaybe
		case i > 0 && body[i] == ';':
			return stringReply
		}
	case 'P':
		for _, r := range dcsReplies {
			if bytes.HasPrefix(body, r) {
				return stringReply
			}
			if bytes.HasPrefix(r, body) {
				return stringMaybe
			}
		}
	case '_':
		if body[0] == 'G' {
			return stringReply
		}
	}
	return stringNot
}
