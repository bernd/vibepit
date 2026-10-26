package cmd

import (
	"slices"
	"time"

	"github.com/bernd/vibepit/proxy"
)

// maxEndedHolds bounds the outcome table: a long session sees an unbounded
// number of holds, and only recent ones are ever looked up.
const maxEndedHolds = 1000

// heldTargets mirrors the proxy's open holds as seen on the event stream. It
// does no locking; use it from one goroutine. The zero value is ready to use.
type heldTargets struct {
	open  map[proxy.HostKey]openHold
	ended map[uint64]proxy.HoldOutcome
}

type openHold struct {
	ID       uint64
	Deadline time.Time
	Entry    proxy.LogEntry
}

// Apply folds one stream event into the table. It is idempotent.
func (h *heldTargets) Apply(e proxy.Event) {
	switch e.Type {
	case proxy.EventLog:
		if e.Log.Action != proxy.ActionHold {
			return
		}
		if e.Log.Outcome != proxy.HoldNone {
			// Replayed history: the hold is already over.
			h.record(e.Log.ID, e.Log.Outcome)
			h.closeID(e.Log.HostKey(), e.Log.ID)
			return
		}
		h.openHold(e.Log)
	case proxy.EventHoldOpen:
		if _, done := h.ended[e.Log.ID]; done {
			return
		}
		h.openHold(e.Log)
	case proxy.EventHoldEnd:
		h.record(e.HoldEnd.ID, e.HoldEnd.Outcome)
		h.closeID(e.HoldEnd.HostKey, e.HoldEnd.ID)
	}
}

func (h *heldTargets) openHold(e proxy.LogEntry) {
	if h.open == nil {
		h.open = make(map[proxy.HostKey]openHold)
	}
	h.open[e.HostKey()] = openHold{ID: e.ID, Deadline: e.Deadline, Entry: e}
}

// closeID only closes the hold it names: a newer hold for the same key must
// survive the end of the one it replaced.
func (h *heldTargets) closeID(k proxy.HostKey, id uint64) {
	if cur, ok := h.open[k]; ok && cur.ID == id {
		delete(h.open, k)
	}
}

func (h *heldTargets) record(id uint64, o proxy.HoldOutcome) {
	if h.ended == nil {
		h.ended = make(map[uint64]proxy.HoldOutcome)
	}
	if len(h.ended) >= maxEndedHolds {
		// IDs grow with time, so the lowest are the oldest. Drop half to
		// amortize the sort.
		ids := make([]uint64, 0, len(h.ended))
		for id := range h.ended {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids[:len(ids)/2] {
			delete(h.ended, id)
		}
	}
	switch o {
	case proxy.HoldAllowed, proxy.HoldDenied, proxy.HoldTimeout, proxy.HoldAbandoned, proxy.HoldUnwatched:
	default:
		o = proxy.HoldTimeout
	}
	h.ended[id] = o
}

// Open returns the hold for k while it is open and its deadline hasn't passed.
func (h *heldTargets) Open(k proxy.HostKey, now time.Time) (openHold, bool) {
	oh, ok := h.open[k]
	if !ok || !now.Before(oh.Deadline) {
		return openHold{}, false
	}
	return oh, true
}

// Outcome reports how hold id ended. A hold whose deadline has passed reads as
// timed out even before the proxy's end event arrives.
func (h *heldTargets) Outcome(id uint64, now time.Time) (proxy.HoldOutcome, bool) {
	if o, ok := h.ended[id]; ok {
		return o, true
	}
	for _, oh := range h.open {
		if oh.ID == id {
			if !now.Before(oh.Deadline) {
				return proxy.HoldTimeout, true
			}
			return "", false
		}
	}
	return "", false
}

// Count returns the number of holds still waiting for a decision.
func (h *heldTargets) Count(now time.Time) int {
	n := 0
	for _, oh := range h.open {
		if now.Before(oh.Deadline) {
			n++
		}
	}
	return n
}

func holdOutcomeLabel(o proxy.HoldOutcome) string {
	switch o {
	case proxy.HoldAllowed:
		return "allowed"
	case proxy.HoldDenied:
		return "denied"
	case proxy.HoldAbandoned:
		return "request gone"
	case proxy.HoldUnwatched:
		return "nobody watching"
	default:
		return "timed out"
	}
}
