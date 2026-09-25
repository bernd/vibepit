package tui

import (
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
