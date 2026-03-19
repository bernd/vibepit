package ward

import (
	"unicode/utf8"

	lipgloss "charm.land/lipgloss/v2"
)

// barStyle is the lipgloss style for the notification bar.
var barStyle = lipgloss.NewStyle().
	Bold(true).
	Foreground(lipgloss.Color("15")). // white
	Background(lipgloss.Color("1"))   // red background

// RenderBar renders a notification message as a full-width bottom bar.
func RenderBar(message string, cols int) string {
	msg := truncateRunes(message, cols-2) // leave room for padding
	return barStyle.Width(cols).Render(" " + msg)
}

// truncateRunes truncates s to at most maxRunes runes, appending "..." if truncated.
func truncateRunes(s string, maxRunes int) string {
	if maxRunes < 1 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	if maxRunes <= 3 {
		runes := []rune(s)
		return string(runes[:maxRunes])
	}
	runes := []rune(s)
	return string(runes[:maxRunes-3]) + "..."
}
