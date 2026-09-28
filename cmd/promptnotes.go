package cmd

import (
	"fmt"
	"slices"
	"sync"

	"github.com/bernd/vibepit/tui"
)

// maxPromptNotes is how many of the latest notes a session keeps.
const maxPromptNotes = 32

// promptNotes collects what prompting couldn't show while the session owns
// the terminal, to print once the session ended. It keeps the latest
// maxPromptNotes lines. The zero value is ready; a nil *promptNotes holds
// nothing.
type promptNotes struct {
	mu    sync.Mutex
	lines []string
}

// Printf sanitizes the note: it may carry sandbox-controlled text, e.g. a
// blocked domain in an error.
func (n *promptNotes) Printf(format string, args ...any) {
	line := tui.SanitizeText(fmt.Sprintf(format, args...))
	n.mu.Lock()
	defer n.mu.Unlock()
	n.lines = append(n.lines, line)
	if len(n.lines) > maxPromptNotes {
		n.lines = n.lines[1:]
	}
}

// Lines returns the notes held, oldest first.
func (n *promptNotes) Lines() []string {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.lines)
}
