package proxy

import (
	"testing"
	"time"
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
