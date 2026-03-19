package ward

import (
	"github.com/charmbracelet/x/vt"
)

// Screen wraps a charmbracelet/x/vt SafeEmulator to provide a thread-safe
// virtual terminal screen with ANSI rendering support.
type Screen struct {
	emu *vt.SafeEmulator
}

// NewScreen creates a new Screen with the given dimensions.
// It starts a background goroutine to drain the emulator's internal response
// pipe (used for terminal query replies like DA, DSR). Without draining,
// Write() blocks when the pipe buffer fills.
//
// The drain goroutine cannot be stopped cleanly because the library's
// Close() is not safe to call concurrently with Read(). The goroutine
// is cleaned up on process exit.
func NewScreen(cols, rows int) *Screen {
	emu := vt.NewSafeEmulator(cols, rows)
	go func() {
		buf := make([]byte, 1024)
		for {
			if _, err := emu.Read(buf); err != nil {
				return
			}
		}
	}()
	return &Screen{emu: emu}
}

// Write feeds bytes into the terminal emulator.
func (s *Screen) Write(p []byte) (int, error) {
	return s.emu.Write(p)
}

// Render returns the ANSI-formatted content of the terminal screen.
func (s *Screen) Render() string {
	return s.emu.Render()
}

// Size returns the current dimensions of the screen.
func (s *Screen) Size() (cols, rows int) {
	return s.emu.Width(), s.emu.Height()
}

// Resize changes the terminal dimensions.
func (s *Screen) Resize(cols, rows int) {
	s.emu.Resize(cols, rows)
}

// CursorPosition returns the current cursor position (0-indexed col and row).
func (s *Screen) CursorPosition() (col, row int) {
	pos := s.emu.CursorPosition()
	return pos.X, pos.Y
}

// IsAltScreen returns true when the terminal is in alternate screen mode
// (used by full-screen programs like less, vim, etc.).
func (s *Screen) IsAltScreen() bool {
	return s.emu.IsAltScreen()
}
