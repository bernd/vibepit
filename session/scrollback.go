package session

import (
	"bytes"
	"sync"
)

// Scrollback is a line-oriented ring buffer that stores the last N lines of
// terminal output. It is safe for concurrent use.
type Scrollback struct {
	mu       sync.Mutex
	lines    [][]byte
	maxLines int
	partial  []byte
	paused   bool
}

func NewScrollback(maxLines int) *Scrollback {
	return &Scrollback{
		lines:    make([][]byte, 0, maxLines),
		maxLines: maxLines,
	}
}

func (s *Scrollback) Write(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.paused {
		return
	}

	for len(p) > 0 {
		idx := bytes.IndexByte(p, '\n')
		if idx == -1 {
			s.partial = append(s.partial, p...)
			return
		}
		line := make([]byte, 0, len(s.partial)+idx+1)
		line = append(line, s.partial...)
		line = append(line, p[:idx+1]...)
		s.partial = s.partial[:0]
		p = p[idx+1:]
		s.appendLine(line)
	}
}

func (s *Scrollback) appendLine(line []byte) {
	if len(s.lines) >= s.maxLines {
		copy(s.lines, s.lines[1:])
		s.lines[len(s.lines)-1] = line
	} else {
		s.lines = append(s.lines, line)
	}
}

func (s *Scrollback) Snapshot() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()

	size := 0
	for _, l := range s.lines {
		size += len(l)
	}
	buf := make([]byte, 0, size)
	for _, l := range s.lines {
		buf = append(buf, l...)
	}
	return buf
}

func (s *Scrollback) SetPaused(paused bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = paused
}
