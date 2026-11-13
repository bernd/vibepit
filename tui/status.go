package tui

import (
	"fmt"
	"io"
	"os"

	"charm.land/lipgloss/v2"
)

var (
	statusStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorCyan)
	errorStyle  = lipgloss.NewStyle().Bold(true).Foreground(ColorOrange)
	warnStyle   = lipgloss.NewStyle().Bold(true).Foreground(ColorYellow)
	debugStyle  = lipgloss.NewStyle().Bold(true).Foreground(ColorPurple)
)

func writeStatus(w io.Writer, verb string, style lipgloss.Style, format string, args ...any) {
	fmt.Fprintln(w, formatStatus(verb, style, format, args...))
}

func formatStatus(verb string, style lipgloss.Style, format string, args ...any) string {
	return style.Render(fmt.Sprintf("%12s", verb)) + " " + fmt.Sprintf(format, args...)
}

// StatusLine returns what Status prints, without the newline, for output
// that doesn't go to stdout, such as vibed's session banner.
func StatusLine(verb string, format string, args ...any) string {
	return formatStatus(verb, statusStyle, format, args...)
}

// Status prints a right-aligned bold cyan verb followed by a message to stdout.
func Status(verb string, format string, args ...any) {
	writeStatus(os.Stdout, verb, statusStyle, format, args...)
}

// Error prints a right-aligned bold orange "error" followed by a message to stderr.
func Error(format string, args ...any) {
	writeStatus(os.Stderr, "error", errorStyle, format, args...)
}

// Warn prints a right-aligned bold yellow "warning" followed by a message to stderr.
func Warn(format string, args ...any) {
	writeStatus(os.Stderr, "warning", warnStyle, format, args...)
}

// Debug prints a right-aligned bold purple "debug" followed by a message to stdout.
func Debug(format string, args ...any) {
	writeStatus(os.Stdout, "debug", debugStyle, format, args...)
}
