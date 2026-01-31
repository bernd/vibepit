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
	stats   map[string]*DomainStats
}

func NewLogBuffer(capacity int) *LogBuffer {
	return &LogBuffer{
		entries: make([]LogEntry, capacity),
		cap:     capacity,
		stats:   make(map[string]*DomainStats),
	}
}

func (b *LogBuffer) Add(entry LogEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()

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
