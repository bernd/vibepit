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

// BlockCode classifies why a request was blocked, so clients can tell blocks
// that an allowlist entry would fix from ones it would not.
type BlockCode string

const (
	BlockNotAllowlisted   BlockCode = "not_allowlisted"
	BlockCIDR             BlockCode = "blocked_cidr"
	BlockResolutionFailed BlockCode = "resolution_failed"
)

type LogEntry struct {
	ID     uint64    `json:"id"`
	Time   time.Time `json:"time"`
	Domain string    `json:"domain"`
	Port   string    `json:"port,omitempty"`
	Action Action    `json:"action"`
	Source Source    `json:"source"`
	Reason string    `json:"reason,omitempty"`
	Code   BlockCode `json:"code,omitempty"`
}

// MaxHosts caps the per-host stats table. The sandbox can query an unbounded
// number of unique names, so the table must not grow without limit.
const MaxHosts = 2000

// HostKey identifies a host in the stats table. DNS and proxy traffic for the
// same domain are tracked separately because they need different allow rules.
type HostKey struct {
	Source Source `json:"source"`
	Domain string `json:"domain"`
	Port   string `json:"port,omitempty"`
}

func (e LogEntry) HostKey() HostKey {
	return HostKey{Source: e.Source, Domain: e.Domain, Port: e.Port}
}

type HostStats struct {
	HostKey
	Allowed         int       `json:"allowed"`
	Blocked         int       `json:"blocked"`
	FirstSeen       time.Time `json:"first_seen"`
	FirstAction     Action    `json:"first_action"`
	LastSeen        time.Time `json:"last_seen"`
	LastAction      Action    `json:"last_action"`
	LastBlockReason string    `json:"last_block_reason,omitempty"`
	LastBlockCode   BlockCode `json:"last_block_code,omitempty"`
	// Allowlisted reports whether the allowlist admits the host at snapshot
	// time. It covers runtime allows from any client, so it can be true
	// while LastAction is still a block from before the allow.
	Allowlisted bool `json:"allowlisted"`
}

type LogBuffer struct {
	mu       sync.Mutex
	entries  []LogEntry
	cap      int
	pos      int
	full     bool
	nextID   uint64
	hosts    map[HostKey]*HostStats
	maxHosts int
}

func NewLogBuffer(capacity int) *LogBuffer {
	return &LogBuffer{
		entries:  make([]LogEntry, capacity),
		cap:      capacity,
		nextID:   1,
		hosts:    make(map[HostKey]*HostStats),
		maxHosts: MaxHosts,
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

	b.recordHostLocked(entry)
}

// recordHostLocked updates the per-host stats. Caller must hold b.mu.
func (b *LogBuffer) recordHostLocked(entry LogEntry) {
	key := entry.HostKey()
	h, ok := b.hosts[key]
	if !ok {
		if len(b.hosts) >= b.maxHosts {
			b.evictOldestHostLocked()
		}
		h = &HostStats{HostKey: key, FirstSeen: entry.Time, FirstAction: entry.Action}
		b.hosts[key] = h
	}
	h.LastSeen = entry.Time
	h.LastAction = entry.Action
	switch entry.Action {
	case ActionAllow:
		h.Allowed++
	case ActionBlock:
		h.Blocked++
		h.LastBlockReason = entry.Reason
		h.LastBlockCode = entry.Code
	}
}

// evictOldestHostLocked removes the least recently seen host. A linear scan is
// fine: it only runs when a new host arrives while the table is full.
func (b *LogBuffer) evictOldestHostLocked() {
	var oldest *HostStats
	for _, h := range b.hosts {
		if oldest == nil || h.LastSeen.Before(oldest.LastSeen) {
			oldest = h
		}
	}
	if oldest != nil {
		delete(b.hosts, oldest.HostKey)
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

// Hosts returns a snapshot of the per-host stats in no particular order.
func (b *LogBuffer) Hosts() []HostStats {
	b.mu.Lock()
	defer b.mu.Unlock()

	result := make([]HostStats, 0, len(b.hosts))
	for _, h := range b.hosts {
		result = append(result, *h)
	}
	return result
}
