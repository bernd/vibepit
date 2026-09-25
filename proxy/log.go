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

// Cause is why a request was blocked. Reason stays the text for people;
// code decides on Cause.
type Cause string

const (
	// CauseAllowlist: the target isn't on the allowlist. Allowing it
	// unblocks it.
	CauseAllowlist Cause = "allowlist"
	// CauseBlockedIP: the target resolves to a blocked CIDR range, which
	// the allowlist can't override.
	CauseBlockedIP Cause = "blocked-ip"
	// CauseResolveFailed: the CIDR check couldn't resolve the target, so
	// the proxy failed closed.
	CauseResolveFailed Cause = "resolve-failed"
)

type LogEntry struct {
	ID     uint64    `json:"id"`
	Time   time.Time `json:"time"`
	Domain string    `json:"domain"`
	Port   string    `json:"port,omitempty"`
	Action Action    `json:"action"`
	Source Source    `json:"source"`
	Reason string    `json:"reason,omitempty"`
	Cause  Cause     `json:"cause,omitempty"`
}

// Allowable reports whether allowing the entry's target would unblock it.
// A proxy from before causes were logged sends none; its blocks may be.
func (e LogEntry) Allowable() bool {
	return e.Action == ActionBlock && (e.Cause == CauseAllowlist || e.Cause == "")
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

	return b.lastLocked(b.cap)
}

// TailSize is how many entries a client gets when it asks for the log
// without a cursor, e.g. to fill a screen on first load.
const TailSize = 25

// Tail returns the last n entries in chronological order.
func (b *LogBuffer) Tail(n int) []LogEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastLocked(n)
}

// EntriesAfter returns every buffered entry with ID > afterID in
// chronological order. afterID 0 means all of them. IDs are contiguous, so
// only the new entries are copied, not the whole ring.
func (b *LogBuffer) EntriesAfter(afterID uint64) []LogEntry {
	b.mu.Lock()
	defer b.mu.Unlock()

	lastID := b.nextID - 1
	if afterID >= lastID {
		return nil
	}
	return b.lastLocked(int(min(lastID-afterID, uint64(b.cap))))
}

// lastLocked copies the newest n buffered entries. Caller must hold b.mu.
func (b *LogBuffer) lastLocked(n int) []LogEntry {
	count := b.pos
	if b.full {
		count = b.cap
	}
	n = min(n, count)
	if n <= 0 {
		return nil
	}
	result := make([]LogEntry, n)
	start := (b.pos - n + b.cap) % b.cap
	copied := copy(result, b.entries[start:min(start+n, b.cap)])
	copy(result[copied:], b.entries[:n-copied])
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
