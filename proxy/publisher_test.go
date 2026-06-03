package proxy

import "sync"

// fakePublisher is a test double for LogPublisher that captures entries.
type fakePublisher struct {
	mu      sync.Mutex
	entries []LogEntry
}

func (f *fakePublisher) PublishLog(e LogEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, e)
}

func (f *fakePublisher) all() []LogEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]LogEntry(nil), f.entries...)
}
