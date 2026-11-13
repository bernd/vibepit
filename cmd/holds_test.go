package cmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/bernd/vibepit/proxy"
)

func TestHeldTargets(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	key := proxy.HostKey{Source: proxy.SourceProxy, Domain: "example.com", Port: "443"}

	hold := func(id uint64, deadline time.Time, o proxy.HoldOutcome) proxy.LogEntry {
		return proxy.LogEntry{
			ID: id, Domain: key.Domain, Port: key.Port, Source: key.Source,
			Action: proxy.ActionHold, Deadline: deadline, Outcome: o,
		}
	}
	logEv := func(e proxy.LogEntry) proxy.Event { return proxy.Event{Type: proxy.EventLog, Log: e} }
	openEv := func(e proxy.LogEntry) proxy.Event { return proxy.Event{Type: proxy.EventHoldOpen, Log: e} }
	endEv := func(id uint64, o proxy.HoldOutcome) proxy.Event {
		return proxy.Event{Type: proxy.EventHoldEnd, HoldEnd: proxy.HoldEnd{ID: id, HostKey: key, Outcome: o}}
	}
	dl := t0.Add(10 * time.Second)

	type outcome struct {
		id uint64
		o  proxy.HoldOutcome
		ok bool
	}
	tests := []struct {
		name      string
		events    []proxy.Event
		now       time.Time
		wantOpen  uint64 // 0 = not open
		wantCount int
		outcomes  []outcome
	}{
		{
			name:      "open",
			events:    []proxy.Event{logEv(hold(1, dl, ""))},
			now:       t0,
			wantOpen:  1,
			wantCount: 1,
			outcomes:  []outcome{{1, "", false}},
		},
		{
			name:      "open then end",
			events:    []proxy.Event{logEv(hold(1, dl, "")), endEv(1, proxy.HoldAllowed)},
			now:       t0,
			wantCount: 0,
			outcomes:  []outcome{{1, proxy.HoldAllowed, true}},
		},
		{
			name:      "end with mismatched id keeps the newer hold",
			events:    []proxy.Event{logEv(hold(2, dl, "")), endEv(1, proxy.HoldDenied)},
			now:       t0,
			wantOpen:  2,
			wantCount: 1,
			outcomes:  []outcome{{1, proxy.HoldDenied, true}, {2, "", false}},
		},
		{
			name:      "deadline expiry reads as timeout",
			events:    []proxy.Event{logEv(hold(1, dl, ""))},
			now:       dl.Add(time.Second),
			wantCount: 0,
			outcomes:  []outcome{{1, proxy.HoldTimeout, true}},
		},
		{
			name:      "history entry with outcome opens nothing",
			events:    []proxy.Event{logEv(hold(1, dl, proxy.HoldDenied))},
			now:       t0,
			wantCount: 0,
			outcomes:  []outcome{{1, proxy.HoldDenied, true}},
		},
		{
			name:      "history outcome is not undone by a later hold-open",
			events:    []proxy.Event{logEv(hold(1, dl, proxy.HoldAllowed)), openEv(hold(1, dl, ""))},
			now:       t0,
			wantCount: 0,
			outcomes:  []outcome{{1, proxy.HoldAllowed, true}},
		},
		{
			name:      "hold-open for a hold not in history",
			events:    []proxy.Event{openEv(hold(3, dl, ""))},
			now:       t0,
			wantOpen:  3,
			wantCount: 1,
		},
		{
			name:      "re-hold replaces the older one",
			events:    []proxy.Event{logEv(hold(1, dl, "")), logEv(hold(2, dl.Add(time.Second), ""))},
			now:       t0,
			wantOpen:  2,
			wantCount: 1,
		},
		{
			name:      "unknown outcome is kept as received",
			events:    []proxy.Event{logEv(hold(1, dl, "")), endEv(1, "bogus")},
			now:       t0,
			wantCount: 0,
			outcomes:  []outcome{{1, "bogus", true}},
		},
		{
			name:      "apply is idempotent",
			events:    []proxy.Event{logEv(hold(1, dl, "")), logEv(hold(1, dl, "")), endEv(1, proxy.HoldAbandoned), endEv(1, proxy.HoldAbandoned)},
			now:       t0,
			wantCount: 0,
			outcomes:  []outcome{{1, proxy.HoldAbandoned, true}},
		},
		{
			name:      "unseen id has no outcome",
			events:    nil,
			now:       t0,
			wantCount: 0,
			outcomes:  []outcome{{9, "", false}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h heldTargets // zero value must work
			for _, e := range tt.events {
				h.Apply(e)
			}
			got, ok := h.Open(key, tt.now)
			assert.Equal(t, tt.wantOpen != 0, ok)
			if ok {
				assert.Equal(t, tt.wantOpen, got.ID)
			}
			assert.Equal(t, tt.wantCount, h.Count(tt.now))
			for _, w := range tt.outcomes {
				o, ok := h.Outcome(w.id, tt.now)
				assert.Equal(t, w.ok, ok, "outcome ok for %d", w.id)
				assert.Equal(t, w.o, o, "outcome for %d", w.id)
			}
		})
	}
}

func TestHeldTargetsEndedIsBounded(t *testing.T) {
	var h heldTargets
	for id := uint64(1); id <= 2500; id++ {
		h.Apply(proxy.Event{Type: proxy.EventHoldEnd, HoldEnd: proxy.HoldEnd{ID: id, Outcome: proxy.HoldAllowed}})
	}
	assert.LessOrEqual(t, len(h.ended), maxEndedHolds)
	_, ok := h.Outcome(1, time.Now())
	assert.False(t, ok, "lowest ids are dropped")
	o, ok := h.Outcome(2500, time.Now())
	assert.True(t, ok)
	assert.Equal(t, proxy.HoldAllowed, o)
}

func TestHoldOutcomeLabel(t *testing.T) {
	tests := map[proxy.HoldOutcome]string{
		proxy.HoldAllowed:   "allowed",
		proxy.HoldDenied:    "denied",
		proxy.HoldRejected:  "rejected",
		proxy.HoldTimeout:   "timed out",
		proxy.HoldAbandoned: "request gone",
		proxy.HoldUnwatched: "nobody watching",
		"bogus":             "timed out", // from a newer proxy
	}
	for o, want := range tests {
		t.Run(string(o), func(t *testing.T) {
			assert.Equal(t, want, holdOutcomeLabel(o))
		})
	}
}
