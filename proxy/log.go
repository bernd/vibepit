package proxy

import (
	"sync"
	"time"
)

type Action string

const (
	ActionAllow Action = "allow"
	ActionBlock Action = "block"
)

type Source string

const (
	SourceProxy Source = "proxy"
	SourceDNS   Source = "dns"
)

type LogEntry struct {
	ID     uint64    `json:"id"`
	Time   time.Time `json:"time"`
	Domain string    `json:"domain"`
	Port   string    `json:"port,omitempty"`
	Action Action    `json:"action"`
	Source Source    `json:"source"`
	Reason string    `json:"reason,omitempty"`
}

type DomainStats struct {
	Allowed int `json:"allowed"`
	Blocked int `json:"blocked"`
}

type LogBuffer struct {
	mu      sync.Mutex
	entries []LogEntry
	cap     int
	pos     int
	full    bool
	nextID  uint64
	stats   map[string]*DomainStats
}

func NewLogBuffer(capacity int) *LogBuffer {
	return &LogBuffer{
		entries: make([]LogEntry, capacity),
		cap:     capacity,
		nextID:  1,
		stats:   make(map[string]*DomainStats),
	}
}

func (b *LogBuffer) Add(entry LogEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()

	entry.ID = b.nextID
	b.nextID++
	b.entries[b.pos] = entry
	b.pos = (b.pos + 1) % b.cap
	if b.pos == 0 && !b.full {
		b.full = true
	}

	s, ok := b.stats[entry.Domain]
	if !ok {
		s = &DomainStats{}
		b.stats[entry.Domain] = s
	}
	switch entry.Action {
	case ActionAllow:
		s.Allowed++
	case ActionBlock:
		s.Blocked++
	}
}

func (b *LogBuffer) Entries() []LogEntry {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.entriesLocked()
}

// EntriesAfter returns entries with ID > afterID in chronological order.
// When afterID is 0, it returns at most the last 25 entries.
func (b *LogBuffer) EntriesAfter(afterID uint64) []LogEntry {
	b.mu.Lock()
	defer b.mu.Unlock()

	all := b.entriesLocked()

	if afterID == 0 {
		if len(all) > 25 {
			all = all[len(all)-25:]
		}
		return all
	}

	// Find the first entry with ID > afterID using linear scan.
	start := -1
	for i, e := range all {
		if e.ID > afterID {
			start = i
			break
		}
	}
	if start == -1 {
		return nil
	}
	result := make([]LogEntry, len(all)-start)
	copy(result, all[start:])
	return result
}

// entriesLocked returns all entries in chronological order. Caller must hold b.mu.
func (b *LogBuffer) entriesLocked() []LogEntry {
	if !b.full {
		result := make([]LogEntry, b.pos)
		copy(result, b.entries[:b.pos])
		return result
	}

	result := make([]LogEntry, b.cap)
	copy(result, b.entries[b.pos:])
	copy(result[b.cap-b.pos:], b.entries[:b.pos])
	return result
}

func (b *LogBuffer) Stats() map[string]DomainStats {
	b.mu.Lock()
	defer b.mu.Unlock()

	result := make(map[string]DomainStats, len(b.stats))
	for k, v := range b.stats {
		result[k] = *v
	}
	return result
}
