package tui

import tea "github.com/charmbracelet/bubbletea"

// FooterKey describes a single keybinding hint shown in the footer.
type FooterKey struct {
	Key  string // display text for the key, e.g. "a"
	Desc string // description, e.g. "allow"
}

// Screen is implemented by each TUI screen (monitor, session selector, etc.).
type Screen interface {
	// Update handles input and custom messages. Returning a different Screen
	// switches the active screen. The Window pointer provides access to shared
	// state (dimensions, flash, error).
	Update(msg tea.Msg, w *Window) (Screen, tea.Cmd)

	// View renders the content area between header and footer.
	View(w *Window) string

	// FooterKeys returns context-sensitive keybinding hints for the right
	// side of the footer. These are prepended before the base keys.
	FooterKeys(w *Window) []FooterKey

	// FooterStatus returns an optional left-side indicator for the footer
	// (e.g. tailing animation, progress). Return "" for no indicator.
	FooterStatus(w *Window) string
}
