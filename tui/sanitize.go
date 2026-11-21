package tui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SanitizeText strips C0 (except tab), C1, DEL, and invalid UTF-8 from text
// that did not originate with the user, e.g. domains and reasons logged from
// sandbox requests, so it cannot inject terminal escape sequences.
func SanitizeText(s string) string {
	return strings.Map(func(r rune) rune {
		if r != '\t' && (unicode.IsControl(r) || r == utf8.RuneError) {
			return -1
		}
		return r
	}, s)
}

// EscapeText makes multi-line text that did not originate with the user
// safe to print for review. Control characters, invisible format characters
// such as bidi overrides, line separators, and invalid UTF-8 come out as Go
// escapes rather than vanishing, since they could hide lines or rewrite
// what the terminal shows. Newlines and tabs stay.
func EscapeText(s string) string {
	var b strings.Builder
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[0])
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case unicode.IsControl(r):
			fmt.Fprintf(&b, `\x%02x`, r)
		case unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
		s = s[size:]
	}
	return b.String()
}
