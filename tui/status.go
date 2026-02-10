package tui

import (
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/lipgloss"
)

var (
	stdoutRenderer = lipgloss.NewRenderer(os.Stdout)
	stderrRenderer = lipgloss.NewRenderer(os.Stderr)

	statusStyle = stdoutRenderer.NewStyle().Bold(true).Foreground(ColorCyan)
	errorStyle  = stderrRenderer.NewStyle().Bold(true).Foreground(ColorOrange)
)

func writeStatus(w io.Writer, verb string, style lipgloss.Style, format string, args ...any) {
	padded := fmt.Sprintf("%12s", verb)
	styled := style.Render(padded)
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(w, "%s %s\n", styled, msg)
}

// Status prints a right-aligned bold cyan verb followed by a message to stdout.
func Status(verb string, format string, args ...any) {
	writeStatus(os.Stdout, verb, statusStyle, format, args...)
}

// Error prints a right-aligned bold purple "error" followed by a message to stderr.
func Error(format string, args ...any) {
	writeStatus(os.Stderr, "error", errorStyle, format, args...)
}
