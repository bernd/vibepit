package cmd

import (
	"testing"
	"time"

	"github.com/bernd/vibepit/proxy"
	"github.com/stretchr/testify/assert"
)

func queueEntry(host string, action proxy.Action, id uint64, deadline time.Time) proxy.LogEntry {
	return proxy.LogEntry{ID: id, Domain: host, Port: "443", Action: action, Source: proxy.SourceProxy, Deadline: deadline}
}

func TestPromptQueue(t *testing.T) {
	deadline := time.Date(2026, 10, 26, 12, 0, 10, 0, time.UTC)
	later := deadline.Add(5 * time.Second)
	a := queueEntry("a.example.com", proxy.ActionHold, 1, deadline)
	b := queueEntry("b.example.com", proxy.ActionBlock, 0, time.Time{})
	c := queueEntry("c.example.com", proxy.ActionHold, 3, deadline)

	hosts := func(q *promptQueue) []string {
		var out []string
		for _, it := range q.Items() {
			out = append(out, it.Entry.Domain)
		}
		return out
	}

	tests := []struct {
		name      string
		run       func(q *promptQueue)
		wantHosts []string
		check     func(t *testing.T, q *promptQueue)
	}{
		{
			name: "same target is not queued twice",
			run: func(q *promptQueue) {
				assert.True(t, q.Add(a))
				assert.False(t, q.Add(a))
				assert.True(t, q.Add(b))
			},
			wantHosts: []string{"a.example.com", "b.example.com"},
		},
		{
			name: "hold refresh keeps its position",
			run: func(q *promptQueue) {
				q.Add(a)
				q.Add(b)
				refreshed := queueEntry("a.example.com", proxy.ActionHold, 9, later)
				assert.False(t, q.Add(refreshed))
			},
			wantHosts: []string{"a.example.com", "b.example.com"},
			check: func(t *testing.T, q *promptQueue) {
				it := q.Items()[0]
				assert.Equal(t, uint64(9), it.Entry.ID)
				assert.Equal(t, later, it.Deadline)
			},
		},
		{
			name: "block entry leaves a queued hold alone",
			run: func(q *promptQueue) {
				q.Add(a)
				q.Add(queueEntry("a.example.com", proxy.ActionBlock, 0, time.Time{}))
			},
			wantHosts: []string{"a.example.com"},
			check: func(t *testing.T, q *promptQueue) {
				assert.Equal(t, deadline, q.Items()[0].Deadline)
			},
		},
		{
			name: "hold entry with an outcome is a plain block",
			run: func(q *promptQueue) {
				ended := a
				ended.Outcome = proxy.HoldTimeout
				q.Add(ended)
			},
			wantHosts: []string{"a.example.com"},
			check: func(t *testing.T, q *promptQueue) {
				assert.True(t, q.Items()[0].Deadline.IsZero())
				assert.False(t, q.Items()[0].Held(deadline.Add(-time.Second)))
			},
		},
		{
			name: "allowed removes the target",
			run: func(q *promptQueue) {
				q.Add(a)
				q.Add(b)
				q.EndHold(proxy.HoldEnd{ID: 1, HostKey: a.HostKey(), Outcome: proxy.HoldAllowed})
			},
			wantHosts: []string{"b.example.com"},
		},
		{
			name: "denied removes the target even with another ID",
			run: func(q *promptQueue) {
				q.Add(a)
				q.EndHold(proxy.HoldEnd{ID: 42, HostKey: a.HostKey(), Outcome: proxy.HoldDenied})
			},
			wantHosts: nil,
		},
		{
			name: "timeout clears the deadline",
			run: func(q *promptQueue) {
				q.Add(a)
				q.EndHold(proxy.HoldEnd{ID: 1, HostKey: a.HostKey(), Outcome: proxy.HoldTimeout})
			},
			wantHosts: []string{"a.example.com"},
			check: func(t *testing.T, q *promptQueue) {
				assert.True(t, q.Items()[0].Deadline.IsZero())
			},
		},
		{
			name: "timeout with a stale ID changes nothing",
			run: func(q *promptQueue) {
				q.Add(queueEntry("a.example.com", proxy.ActionHold, 9, later))
				q.EndHold(proxy.HoldEnd{ID: 1, HostKey: a.HostKey(), Outcome: proxy.HoldTimeout})
			},
			wantHosts: []string{"a.example.com"},
			check: func(t *testing.T, q *promptQueue) {
				assert.Equal(t, later, q.Items()[0].Deadline)
			},
		},
		{
			name: "remove and has",
			run: func(q *promptQueue) {
				q.Add(a)
				q.Add(c)
				assert.True(t, q.Has(a.Target()))
				q.Remove(a.Target())
				assert.False(t, q.Has(a.Target()))
			},
			wantHosts: []string{"c.example.com"},
		},
		{
			name: "clear returns the items and empties the queue",
			run: func(q *promptQueue) {
				q.Add(a)
				q.Add(b)
				got := q.Clear()
				assert.Len(t, got, 2)
				assert.Equal(t, "a.example.com", got[0].Entry.Domain)
			},
			wantHosts: nil,
			check: func(t *testing.T, q *promptQueue) {
				assert.Equal(t, 0, q.Len())
			},
		},
		{
			name: "dropping holds keeps the targets as blocks",
			run: func(q *promptQueue) {
				q.Add(a)
				q.Add(b)
				q.DropHolds()
				// The new proxy's IDs start over: its end of hold 1 is
				// not the end of the old proxy's hold 1.
				q.EndHold(proxy.HoldEnd{ID: 1, HostKey: a.HostKey(), Outcome: proxy.HoldTimeout})
				q.Add(c)
			},
			wantHosts: []string{"a.example.com", "b.example.com", "c.example.com"},
			check: func(t *testing.T, q *promptQueue) {
				items := q.Items()
				for _, it := range items[:2] {
					assert.Zero(t, it.Deadline, it.Entry.Domain)
					assert.Zero(t, it.Entry.ID, it.Entry.Domain)
				}
				assert.Equal(t, deadline, items[2].Deadline, "holds after the drop count")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := newPromptQueue()
			tt.run(q)
			assert.Equal(t, tt.wantHosts, hosts(q))
			assert.Equal(t, len(tt.wantHosts), q.Len())
			if tt.check != nil {
				tt.check(t, q)
			}
		})
	}
}

func TestPromptItem_Held(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	assert.False(t, promptItem{}.Held(now))
	assert.True(t, promptItem{Deadline: now.Add(time.Second)}.Held(now))
	assert.False(t, promptItem{Deadline: now}.Held(now))
}
