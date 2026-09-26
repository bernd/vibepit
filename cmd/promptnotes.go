package cmd

import (
	"fmt"
	"sync"
)

// maxPromptNotes is how many of the latest notes a session keeps.
const maxPromptNotes = 32

// promptNotes collects what prompting couldn't show while the session owns
// the terminal, to print once the session ended. It keeps the latest
// maxPromptNotes lines in a ring. The zero value is ready; a nil
// *promptNotes holds nothing.
type promptNotes struct {
	mu    sync.Mutex
	lines [maxPromptNotes]string
	next  int // where the next line goes
	count int // lines held, up to maxPromptNotes
}

func (n *promptNotes) Printf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	n.mu.Lock()
	defer n.mu.Unlock()
	n.lines[n.next] = line
	n.next = (n.next + 1) % maxPromptNotes
	n.count = min(n.count+1, maxPromptNotes)
}

// Lines returns the notes held, oldest first.
func (n *promptNotes) Lines() []string {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	var lines []string
	start := (n.next - n.count + maxPromptNotes) % maxPromptNotes
	for i := range n.count {
		lines = append(lines, n.lines[(start+i)%maxPromptNotes])
	}
	return lines
}
