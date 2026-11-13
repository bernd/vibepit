package proxy

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogBuffer(t *testing.T) {
	t.Run("stores entries up to capacity", func(t *testing.T) {
		buf := NewLogBuffer(3)
		buf.Add(LogEntry{Time: time.Now(), Domain: "a.com", Action: ActionAllow, Source: SourceProxy})
		buf.Add(LogEntry{Time: time.Now(), Domain: "b.com", Action: ActionBlock, Source: SourceProxy})
		buf.Add(LogEntry{Time: time.Now(), Domain: "c.com", Action: ActionAllow, Source: SourceDNS})

		entries := buf.Entries()
		require.Len(t, entries, 3)
		assert.Equal(t, "a.com", entries[0].Domain)
	})

	t.Run("overwrites oldest when full", func(t *testing.T) {
		buf := NewLogBuffer(2)
		buf.Add(LogEntry{Domain: "a.com"})
		buf.Add(LogEntry{Domain: "b.com"})
		buf.Add(LogEntry{Domain: "c.com"})

		entries := buf.Entries()
		require.Len(t, entries, 2)
		assert.Equal(t, "b.com", entries[0].Domain)
		assert.Equal(t, "c.com", entries[1].Domain)
	})

	t.Run("assigns sequential IDs", func(t *testing.T) {
		buf := NewLogBuffer(10)
		buf.Add(LogEntry{Domain: "a.com"})
		buf.Add(LogEntry{Domain: "b.com"})
		buf.Add(LogEntry{Domain: "c.com"})

		entries := buf.Entries()
		require.Len(t, entries, 3)
		assert.Equal(t, uint64(1), entries[0].ID)
		assert.Equal(t, uint64(2), entries[1].ID)
		assert.Equal(t, uint64(3), entries[2].ID)
	})
}

func hostByDomain(t *testing.T, hosts []HostStats, domain string) HostStats {
	t.Helper()
	for _, h := range hosts {
		if h.Domain == domain {
			return h
		}
	}
	t.Fatalf("host %q not found", domain)
	return HostStats{}
}

func hostDomains(hosts []HostStats) []string {
	var domains []string
	for _, h := range hosts {
		domains = append(domains, h.Domain)
	}
	return domains
}

func TestLogBufferHosts(t *testing.T) {
	t0 := time.Date(2026, 10, 16, 20, 0, 0, 0, time.UTC)

	t.Run("separates source and port", func(t *testing.T) {
		buf := NewLogBuffer(100)
		buf.Add(LogEntry{Time: t0, Domain: "a.com", Port: "443", Source: SourceProxy, Action: ActionAllow})
		buf.Add(LogEntry{Time: t0, Domain: "a.com", Port: "80", Source: SourceProxy, Action: ActionAllow})
		buf.Add(LogEntry{Time: t0, Domain: "a.com", Source: SourceDNS, Action: ActionAllow})

		assert.Len(t, buf.Hosts(), 3)
	})

	t.Run("counts and first/last", func(t *testing.T) {
		buf := NewLogBuffer(100)
		e := LogEntry{Domain: "a.com", Port: "443", Source: SourceProxy}
		e.Time, e.Action = t0, ActionBlock
		buf.Add(e)
		e.Time, e.Action = t0.Add(time.Second), ActionAllow
		buf.Add(e)
		e.Time, e.Action = t0.Add(2*time.Second), ActionAllow
		buf.Add(e)

		hosts := buf.Hosts()
		require.Len(t, hosts, 1)
		h := hosts[0]
		assert.Equal(t, HostKey{Source: SourceProxy, Domain: "a.com", Port: "443"}, h.HostKey)
		assert.Equal(t, 2, h.Allowed)
		assert.Equal(t, 1, h.Blocked)
		assert.Equal(t, t0, h.FirstSeen)
		assert.Equal(t, ActionBlock, h.FirstAction)
		assert.Equal(t, t0.Add(2*time.Second), h.LastSeen)
		assert.Equal(t, ActionAllow, h.LastAction)
	})

	t.Run("last block details", func(t *testing.T) {
		buf := NewLogBuffer(100)
		cidrReason := "resolved IP 10.0.0.1 is in blocked CIDR range"
		buf.Add(LogEntry{Time: t0, Domain: "a.com", Port: "443", Source: SourceProxy, Action: ActionBlock,
			Reason: "domain not in allowlist", Code: BlockNotAllowlisted})
		buf.Add(LogEntry{Time: t0.Add(time.Second), Domain: "a.com", Port: "443", Source: SourceProxy, Action: ActionBlock,
			Reason: cidrReason, Code: BlockCIDR})
		buf.Add(LogEntry{Time: t0.Add(2 * time.Second), Domain: "a.com", Port: "443", Source: SourceProxy, Action: ActionAllow})

		h := buf.Hosts()[0]
		assert.Equal(t, BlockCIDR, h.LastBlockCode)
		assert.Equal(t, cidrReason, h.LastBlockReason)
	})

	t.Run("survives ring overwrite", func(t *testing.T) {
		buf := NewLogBuffer(2)
		for i := range 5 {
			buf.Add(LogEntry{Time: t0, Domain: fmt.Sprintf("%d.com", i), Source: SourceDNS, Action: ActionBlock})
		}

		assert.Len(t, buf.Hosts(), 5)
	})

	t.Run("evicts least recently seen at cap", func(t *testing.T) {
		buf := NewLogBuffer(100)
		buf.maxHosts = 3
		buf.Add(LogEntry{Time: t0.Add(3 * time.Second), Domain: "a.com", Source: SourceDNS})
		buf.Add(LogEntry{Time: t0.Add(1 * time.Second), Domain: "b.com", Source: SourceDNS})
		buf.Add(LogEntry{Time: t0.Add(2 * time.Second), Domain: "c.com", Source: SourceDNS})
		buf.Add(LogEntry{Time: t0.Add(4 * time.Second), Domain: "d.com", Source: SourceDNS})

		assert.ElementsMatch(t, []string{"a.com", "c.com", "d.com"}, hostDomains(buf.Hosts()))
	})

	t.Run("existing host at cap is updated, not evicted", func(t *testing.T) {
		buf := NewLogBuffer(100)
		buf.maxHosts = 2
		buf.Add(LogEntry{Time: t0, Domain: "a.com", Source: SourceDNS, Action: ActionBlock})
		buf.Add(LogEntry{Time: t0.Add(time.Second), Domain: "b.com", Source: SourceDNS, Action: ActionBlock})
		buf.Add(LogEntry{Time: t0.Add(2 * time.Second), Domain: "a.com", Source: SourceDNS, Action: ActionBlock})

		hosts := buf.Hosts()
		assert.Len(t, hosts, 2)
		a := hostByDomain(t, hosts, "a.com")
		assert.Equal(t, 2, a.Allowed+a.Blocked)
	})

	t.Run("Hosts returns copies", func(t *testing.T) {
		buf := NewLogBuffer(100)
		buf.Add(LogEntry{Time: t0, Domain: "a.com", Source: SourceDNS, Action: ActionBlock})

		hosts := buf.Hosts()
		hosts[0].Blocked = 99
		assert.Equal(t, 1, buf.Hosts()[0].Blocked)
	})
}

func TestTail(t *testing.T) {
	t.Run("returns last n entries", func(t *testing.T) {
		buf := NewLogBuffer(100)
		for range 30 {
			buf.Add(LogEntry{Domain: "a.com"})
		}

		entries := buf.Tail(25)
		require.Len(t, entries, 25)
		assert.Equal(t, uint64(6), entries[0].ID)
		assert.Equal(t, uint64(30), entries[24].ID)
	})

	t.Run("fewer entries than n returns all", func(t *testing.T) {
		buf := NewLogBuffer(100)
		for range 5 {
			buf.Add(LogEntry{Domain: "a.com"})
		}

		entries := buf.Tail(25)
		require.Len(t, entries, 5)
		assert.Equal(t, uint64(1), entries[0].ID)
	})

	t.Run("empty buffer returns nil", func(t *testing.T) {
		assert.Nil(t, NewLogBuffer(10).Tail(25))
	})

	t.Run("works after buffer wraps", func(t *testing.T) {
		buf := NewLogBuffer(3)
		for i := range 5 {
			buf.Add(LogEntry{Domain: fmt.Sprintf("%d.com", i+1)})
		}
		entries := buf.Tail(2)
		require.Len(t, entries, 2)
		assert.Equal(t, "4.com", entries[0].Domain)
		assert.Equal(t, "5.com", entries[1].Domain)
	})
}

func TestEntriesAfter(t *testing.T) {
	t.Run("zero afterID returns every buffered entry", func(t *testing.T) {
		buf := NewLogBuffer(100)
		for range 30 {
			buf.Add(LogEntry{Domain: "a.com"})
		}

		entries := buf.EntriesAfter(0)
		require.Len(t, entries, 30)
		assert.Equal(t, uint64(1), entries[0].ID)
	})

	t.Run("zero afterID on empty buffer returns nil", func(t *testing.T) {
		assert.Nil(t, NewLogBuffer(10).EntriesAfter(0))
	})

	t.Run("returns entries after given ID", func(t *testing.T) {
		buf := NewLogBuffer(100)
		for range 10 {
			buf.Add(LogEntry{Domain: "a.com"})
		}

		entries := buf.EntriesAfter(7)
		require.Len(t, entries, 3)
		assert.Equal(t, uint64(8), entries[0].ID)
		assert.Equal(t, uint64(9), entries[1].ID)
		assert.Equal(t, uint64(10), entries[2].ID)
	})

	t.Run("returns nil when no new entries", func(t *testing.T) {
		buf := NewLogBuffer(100)
		for range 5 {
			buf.Add(LogEntry{Domain: "a.com"})
		}

		entries := buf.EntriesAfter(5)
		assert.Nil(t, entries)
	})

	t.Run("works after buffer wraps", func(t *testing.T) {
		buf := NewLogBuffer(3)
		// Add 5 entries to a buffer of capacity 3; entries 1 and 2 are overwritten
		for i := range 5 {
			buf.Add(LogEntry{Domain: fmt.Sprintf("%d.com", i+1)})
		}

		// Ask for entries after ID 2 (which has been evicted)
		entries := buf.EntriesAfter(2)
		require.Len(t, entries, 3)
		assert.Equal(t, uint64(3), entries[0].ID)
		assert.Equal(t, "3.com", entries[0].Domain)
		assert.Equal(t, uint64(5), entries[2].ID)
		assert.Equal(t, "5.com", entries[2].Domain)
	})

	t.Run("works after wrap with recent cursor", func(t *testing.T) {
		buf := NewLogBuffer(3)
		for i := range 5 {
			buf.Add(LogEntry{Domain: fmt.Sprintf("%d.com", i+1)})
		}

		entries := buf.EntriesAfter(4)
		require.Len(t, entries, 1)
		assert.Equal(t, uint64(5), entries[0].ID)
		assert.Equal(t, "5.com", entries[0].Domain)
	})
}

func TestLogEntryAllowable(t *testing.T) {
	tests := []struct {
		name  string
		entry LogEntry
		want  bool
	}{
		{"allowlist miss", LogEntry{Domain: "a.com", Port: "443", Source: SourceProxy, Action: ActionBlock, Code: BlockNotAllowlisted}, true},
		{"block without code", LogEntry{Domain: "a.com", Port: "443", Source: SourceProxy, Action: ActionBlock}, true},
		{"dns allowlist miss", LogEntry{Domain: "a.com", Source: SourceDNS, Action: ActionBlock, Code: BlockNotAllowlisted}, true},
		{"IPv4 literal", LogEntry{Domain: "192.0.2.1", Port: "443", Source: SourceProxy, Action: ActionBlock, Code: BlockNotAllowlisted}, true},
		// The allowlist can't hold an IPv6 literal, so allowing would fail.
		{"IPv6 literal", LogEntry{Domain: "2001:db8::1", Port: "443", Source: SourceProxy, Action: ActionBlock, Code: BlockNotAllowlisted}, false},
		// The sandbox names the host; allowing a '*' in it would add a pattern.
		{"wildcard host", LogEntry{Domain: "**.*", Port: "443", Source: SourceProxy, Action: ActionBlock, Code: BlockNotAllowlisted}, false},
		{"wildcard label", LogEntry{Domain: "*.com", Port: "443", Source: SourceProxy, Action: ActionBlock, Code: BlockNotAllowlisted}, false},
		{"wildcard port", LogEntry{Domain: "a.com", Port: "*", Source: SourceProxy, Action: ActionBlock, Code: BlockNotAllowlisted}, false},
		{"dns wildcard", LogEntry{Domain: "*.*", Source: SourceDNS, Action: ActionBlock, Code: BlockNotAllowlisted}, false},
		{"blocked IP", LogEntry{Domain: "a.com", Port: "443", Source: SourceProxy, Action: ActionBlock, Code: BlockCIDR}, false},
		{"resolve failed", LogEntry{Domain: "a.com", Port: "443", Source: SourceProxy, Action: ActionBlock, Code: BlockResolutionFailed}, false},
		{"allowed", LogEntry{Domain: "a.com", Port: "443", Source: SourceProxy, Action: ActionAllow}, false},
		{"hold", LogEntry{Domain: "a.com", Port: "443", Source: SourceProxy, Action: ActionHold, Code: BlockNotAllowlisted}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.entry.Allowable())
		})
	}
}

func TestLogBuffer_HoldEntriesListButDontCount(t *testing.T) {
	buf := NewLogBuffer(10)
	t0 := time.Now()
	hold := LogEntry{Time: t0, Domain: "docs.example.com", Port: "443", Action: ActionHold, Source: SourceProxy,
		Reason: reasonNotAllowlisted, Code: BlockNotAllowlisted}

	buf.Add(hold)
	hosts, lastID := buf.HostsAt()
	require.Len(t, hosts, 1, "a held host is listed before the request is decided")
	assert.Equal(t, uint64(1), lastID)
	assert.Zero(t, hosts[0].Allowed+hosts[0].Blocked)
	assert.Equal(t, ActionHold, hosts[0].LastAction)
	assert.Equal(t, BlockNotAllowlisted, hosts[0].LastBlockCode)

	block := hold
	block.Time, block.Action = t0.Add(time.Second), ActionBlock
	buf.Add(block)

	hosts, lastID = buf.HostsAt()
	require.Len(t, hosts, 1)
	assert.Equal(t, uint64(2), lastID)
	assert.Equal(t, 1, hosts[0].Blocked)
	assert.Equal(t, 0, hosts[0].Allowed)
	assert.Equal(t, ActionBlock, hosts[0].LastAction)
	assert.Equal(t, ActionHold, hosts[0].FirstAction)
	assert.Equal(t, t0, hosts[0].FirstSeen)
}

func TestLogBuffer_AddReturnsID(t *testing.T) {
	buf := NewLogBuffer(10)
	assert.Equal(t, uint64(1), buf.Add(LogEntry{Domain: "a.com"}))
	assert.Equal(t, uint64(2), buf.Add(LogEntry{Domain: "b.com"}))
}

func TestLogBuffer_OnAdd(t *testing.T) {
	buf := NewLogBuffer(10)
	var got []LogEntry
	buf.OnAdd(func(e LogEntry) { got = append(got, e) })

	buf.Add(LogEntry{Domain: "a.com"})
	buf.Add(LogEntry{Domain: "b.com"})

	require.Len(t, got, 2)
	assert.Equal(t, uint64(1), got[0].ID)
	assert.Equal(t, "a.com", got[0].Domain)
	assert.Equal(t, uint64(2), got[1].ID)
}

func TestLogBuffer_OnAddInIDOrder(t *testing.T) {
	t.Run("slow hook keeps ID order", func(t *testing.T) {
		buf := NewLogBuffer(10)
		entered := make(chan struct{})
		var ids []uint64
		buf.OnAdd(func(e LogEntry) {
			ids = append(ids, e.ID)
			if e.ID == 1 {
				close(entered)
				time.Sleep(50 * time.Millisecond)
			}
		})

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); buf.Add(LogEntry{Domain: "a.com"}) }()
		go func() { defer wg.Done(); <-entered; buf.Add(LogEntry{Domain: "b.com"}) }()
		wg.Wait()

		assert.Equal(t, []uint64{1, 2}, ids)
	})

	t.Run("concurrent adds", func(t *testing.T) {
		buf := NewLogBuffer(10)
		var ids []uint64
		buf.OnAdd(func(e LogEntry) { ids = append(ids, e.ID) })

		var wg sync.WaitGroup
		for range 50 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 20 {
					buf.Add(LogEntry{Domain: "a.com"})
				}
			}()
		}
		wg.Wait()

		require.Len(t, ids, 1000)
		for i := 1; i < len(ids); i++ {
			require.Greater(t, ids[i], ids[i-1])
		}
	})
}

func TestLogEntry_HoldJSON(t *testing.T) {
	t.Run("zero values omitted", func(t *testing.T) {
		b, err := json.Marshal(LogEntry{Domain: "a.com", Action: ActionHold})
		require.NoError(t, err)
		assert.NotContains(t, string(b), "deadline")
		assert.NotContains(t, string(b), "outcome")
	})

	t.Run("set values round-trip", func(t *testing.T) {
		deadline := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		b, err := json.Marshal(LogEntry{Domain: "a.com", Action: ActionHold, Deadline: deadline, Outcome: HoldAllowed})
		require.NoError(t, err)
		assert.Contains(t, string(b), `"deadline"`)
		assert.Contains(t, string(b), `"outcome":"allowed"`)

		var got LogEntry
		require.NoError(t, json.Unmarshal(b, &got))
		assert.True(t, deadline.Equal(got.Deadline))
		assert.Equal(t, HoldAllowed, got.Outcome)
	})
}

func TestLogBuffer_HistoryBoundary(t *testing.T) {
	t.Run("empty buffer", func(t *testing.T) {
		buf := NewLogBuffer(10)
		e, last := buf.TailAt(5)
		assert.Empty(t, e)
		assert.Equal(t, uint64(0), last)
		e, last = buf.EntriesAfterAt(0)
		assert.Empty(t, e)
		assert.Equal(t, uint64(0), last)
	})

	buf := NewLogBuffer(200)
	for range 100 {
		buf.Add(LogEntry{Domain: "a.com"})
	}

	t.Run("TailAt", func(t *testing.T) {
		e, last := buf.TailAt(0)
		assert.Empty(t, e)
		assert.Equal(t, uint64(100), last)

		e, last = buf.TailAt(2)
		require.Len(t, e, 2)
		assert.Equal(t, uint64(99), e[0].ID)
		assert.Equal(t, uint64(100), e[1].ID)
		assert.Equal(t, uint64(100), last)
	})

	t.Run("EntriesAfterAt", func(t *testing.T) {
		e, last := buf.EntriesAfterAt(98)
		require.Len(t, e, 2)
		assert.Equal(t, uint64(99), e[0].ID)
		assert.Equal(t, uint64(100), e[1].ID)
		assert.Equal(t, uint64(100), last)
	})
}

func TestLogBuffer_SetHoldOutcome(t *testing.T) {
	t.Run("sets outcome on hold entry", func(t *testing.T) {
		buf := NewLogBuffer(10)
		hookCalls := 0
		buf.OnAdd(func(LogEntry) { hookCalls++ })
		id := buf.Add(LogEntry{Domain: "a.com", Action: ActionHold})

		assert.True(t, buf.SetHoldOutcome(id, HoldAllowed))
		assert.Equal(t, HoldAllowed, buf.EntriesAfter(0)[0].Outcome)
		assert.Equal(t, HoldAllowed, buf.Tail(1)[0].Outcome)
		assert.Equal(t, 1, hookCalls)
	})

	t.Run("false for block entry", func(t *testing.T) {
		buf := NewLogBuffer(10)
		id := buf.Add(LogEntry{Domain: "a.com", Action: ActionBlock})
		assert.False(t, buf.SetHoldOutcome(id, HoldDenied))
		assert.Equal(t, HoldNone, buf.Tail(1)[0].Outcome)
	})

	t.Run("false for unknown ID", func(t *testing.T) {
		buf := NewLogBuffer(10)
		buf.Add(LogEntry{Domain: "a.com", Action: ActionHold})
		assert.False(t, buf.SetHoldOutcome(0, HoldDenied))
		assert.False(t, buf.SetHoldOutcome(2, HoldDenied))
	})

	t.Run("false for overwritten entry", func(t *testing.T) {
		buf := NewLogBuffer(3)
		first := buf.Add(LogEntry{Domain: "a.com", Action: ActionHold})
		for range 3 {
			buf.Add(LogEntry{Domain: "b.com", Action: ActionHold})
		}
		assert.False(t, buf.SetHoldOutcome(first, HoldTimeout))
		assert.True(t, buf.SetHoldOutcome(first+3, HoldTimeout))
	})
}
