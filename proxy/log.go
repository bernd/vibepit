package proxy

import (
	"sync"
	"time"
)

type Action string

const (
	ActionAllow Action = "allow"
	ActionBlock Action = "block"
	// ActionHold marks a request the proxy is holding while a client decides
	// whether to allow it. Its fate is recorded in LogEntry.Outcome.
	ActionHold Action = "hold"
)

// HoldOutcome records how a held request ended. It lives here because the
// hold's log entry carries it.
type HoldOutcome string

const (
	HoldNone      HoldOutcome = ""
	HoldAllowed   HoldOutcome = "allowed"
	HoldDenied    HoldOutcome = "denied"
	HoldTimeout   HoldOutcome = "timeout"
	HoldAbandoned HoldOutcome = "abandoned"
	HoldUnwatched HoldOutcome = "unwatched"
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

// reasonNotAllowlisted is the reason logged for BlockNotAllowlisted, by
// blocks and holds alike.
const reasonNotAllowlisted = "domain not in allowlist"

type LogEntry struct {
	ID     uint64    `json:"id"`
	Time   time.Time `json:"time"`
	Domain string    `json:"domain"`
	Port   string    `json:"port,omitempty"`
	Action Action    `json:"action"`
	Source Source    `json:"source"`
	Reason string    `json:"reason,omitempty"`
	Code   BlockCode `json:"code,omitempty"`
	// Deadline and Outcome are only set on hold entries.
	Deadline time.Time   `json:"deadline,omitzero"`
	Outcome  HoldOutcome `json:"outcome,omitempty"`
}

// Allowable reports whether allowing the entry's target would unblock it.
func (e LogEntry) Allowable() bool {
	return (e.Action == ActionBlock || e.Action == ActionHold) && AllowFixes(e.HostKey(), e.Code)
}

// AllowFixes reports whether allowing k would fix a block with the given
// code. A proxy from before block codes were logged sends none; its blocks
// may be. Some targets, e.g. IPv6 literals, can't be put on the allowlist.
func AllowFixes(k HostKey, code BlockCode) bool {
	if code != BlockNotAllowlisted && code != "" {
		return false
	}
	validate := ValidateHTTPEntry
	if k.Source == SourceDNS {
		validate = ValidateDNSEntry
	}
	return validate(k.Target().String()) == nil
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
	onAdd    func(LogEntry)
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

// OnAdd registers a hook called with every added entry, ID included. It runs
// while the buffer's lock is held so hooks see entries in ID order; it must
// therefore not block and must not call back into the LogBuffer.
func (b *LogBuffer) OnAdd(f func(LogEntry)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onAdd = f
}

// Add stores the entry and returns its assigned ID.
func (b *LogBuffer) Add(entry LogEntry) uint64 {
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
	if b.onAdd != nil {
		b.onAdd(entry)
	}
	return entry.ID
}

// SetHoldOutcome records how a hold ended. It returns false when the entry has
// left the ring or isn't a hold. It doesn't call the OnAdd hook.
func (b *LogBuffer) SetHoldOutcome(id uint64, o HoldOutcome) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	lastID := b.nextID - 1
	if id == 0 || id > lastID || lastID-id >= uint64(b.cap) {
		return false
	}
	// IDs are contiguous, so the newest sits just before pos.
	idx := (b.pos - 1 - int(lastID-id) + 2*b.cap) % b.cap
	e := &b.entries[idx]
	if e.Action != ActionHold {
		return false
	}
	e.Outcome = o
	return true
}

// recordHostLocked updates the per-host stats. Caller must hold b.mu.
func (b *LogBuffer) recordHostLocked(entry LogEntry) {
	// A hold is undecided, so it counts as neither allowed nor blocked. The
	// block or allow that follows is what the stats record.
	if entry.Action == ActionHold {
		return
	}
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

	return b.lastLocked(b.cap)
}

// TailSize is how many entries a client gets when it asks for the log
// without a cursor, e.g. to fill a screen on first load.
const TailSize = 25

// Tail returns the last n entries in chronological order.
func (b *LogBuffer) Tail(n int) []LogEntry {
	entries, _ := b.TailAt(n)
	return entries
}

// EntriesAfter returns every buffered entry with ID > afterID in
// chronological order. afterID 0 means all of them. IDs are contiguous, so
// only the new entries are copied, not the whole ring.
func (b *LogBuffer) EntriesAfter(afterID uint64) []LogEntry {
	entries, _ := b.EntriesAfterAt(afterID)
	return entries
}

// TailAt is Tail plus the highest assigned ID, read under the same lock, so
// callers can tell where history ends and live events begin.
func (b *LogBuffer) TailAt(n int) ([]LogEntry, uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastLocked(n), b.nextID - 1
}

// EntriesAfterAt is EntriesAfter plus the highest assigned ID, read under the
// same lock.
func (b *LogBuffer) EntriesAfterAt(afterID uint64) ([]LogEntry, uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	lastID := b.nextID - 1
	if afterID >= lastID {
		return nil, lastID
	}
	return b.lastLocked(int(min(lastID-afterID, uint64(b.cap)))), lastID
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
