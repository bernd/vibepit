package proxy

import (
	"fmt"
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
		if len(entries) != 3 {
			t.Fatalf("got %d entries, want 3", len(entries))
		}
		if entries[0].Domain != "a.com" {
			t.Errorf("first entry domain = %q, want %q", entries[0].Domain, "a.com")
		}
	})

	t.Run("overwrites oldest when full", func(t *testing.T) {
		buf := NewLogBuffer(2)
		buf.Add(LogEntry{Domain: "a.com"})
		buf.Add(LogEntry{Domain: "b.com"})
		buf.Add(LogEntry{Domain: "c.com"})

		entries := buf.Entries()
		if len(entries) != 2 {
			t.Fatalf("got %d entries, want 2", len(entries))
		}
		if entries[0].Domain != "b.com" {
			t.Errorf("first entry = %q, want %q", entries[0].Domain, "b.com")
		}
		if entries[1].Domain != "c.com" {
			t.Errorf("second entry = %q, want %q", entries[1].Domain, "c.com")
		}
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

	t.Run("stats counts per domain", func(t *testing.T) {
		buf := NewLogBuffer(100)
		buf.Add(LogEntry{Domain: "a.com", Action: ActionAllow})
		buf.Add(LogEntry{Domain: "a.com", Action: ActionAllow})
		buf.Add(LogEntry{Domain: "a.com", Action: ActionBlock})
		buf.Add(LogEntry{Domain: "b.com", Action: ActionBlock})

		stats := buf.Stats()
		if stats["a.com"].Allowed != 2 {
			t.Errorf("a.com allowed = %d, want 2", stats["a.com"].Allowed)
		}
		if stats["a.com"].Blocked != 1 {
			t.Errorf("a.com blocked = %d, want 1", stats["a.com"].Blocked)
		}
		if stats["b.com"].Blocked != 1 {
			t.Errorf("b.com blocked = %d, want 1", stats["b.com"].Blocked)
		}
	})
}

func TestEntriesAfter(t *testing.T) {
	t.Run("zero afterID returns last 25 entries", func(t *testing.T) {
		buf := NewLogBuffer(100)
		for range 30 {
			buf.Add(LogEntry{Domain: "a.com"})
		}

		entries := buf.EntriesAfter(0)
		require.Len(t, entries, 25)
		assert.Equal(t, uint64(6), entries[0].ID)
		assert.Equal(t, uint64(30), entries[24].ID)
	})

	t.Run("zero afterID with fewer than 25 entries returns all", func(t *testing.T) {
		buf := NewLogBuffer(100)
		for range 5 {
			buf.Add(LogEntry{Domain: "a.com"})
		}

		entries := buf.EntriesAfter(0)
		require.Len(t, entries, 5)
		assert.Equal(t, uint64(1), entries[0].ID)
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
