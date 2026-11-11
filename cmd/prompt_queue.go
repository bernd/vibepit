package cmd

import (
	"slices"
	"sync"
	"time"

	"github.com/bernd/vibepit/proxy"
)

// promptItem is one target waiting for a decision.
type promptItem struct {
	Entry    proxy.LogEntry // the hold entry when held; its ID matches HoldEnd.ID
	Deadline time.Time      // zero: plain block
	// Ended is how the last hold on the target ended without a decision;
	// empty while held and for a target that was never held.
	Ended proxy.HoldOutcome
}

// Held reports whether the proxy is still holding the request at now.
func (it promptItem) Held(now time.Time) bool {
	return !it.Deadline.IsZero() && now.Before(it.Deadline)
}

// HoldEnded reports whether the target was held and no longer is at now, and
// how the hold ended. A passed deadline whose end event hasn't arrived yet
// reports HoldNone, which reads as a timeout.
func (it promptItem) HoldEnded(now time.Time) (proxy.HoldOutcome, bool) {
	if it.Ended != proxy.HoldNone {
		return it.Ended, true
	}
	return proxy.HoldNone, !it.Deadline.IsZero() && !it.Held(now)
}

// promptQueue is the ordered set of targets the approve screen works through.
// The prompter feeds it from the event stream while the screen reads it, so it
// is safe for concurrent use.
type promptQueue struct {
	mu    sync.Mutex
	items []promptItem
}

func newPromptQueue() *promptQueue { return &promptQueue{} }

func newPromptItem(e proxy.LogEntry) promptItem {
	it := promptItem{Entry: e}
	if e.Action == proxy.ActionHold {
		// A hold that already ended gets no countdown; it would lie.
		if e.Outcome == proxy.HoldNone {
			it.Deadline = e.Deadline
		} else {
			it.Ended = e.Outcome
		}
	}
	return it
}

func (q *promptQueue) index(t proxy.Target) int {
	return slices.IndexFunc(q.items, func(it promptItem) bool { return it.Entry.Target() == t })
}

// Add queues e. It returns false when the target was already queued: a hold
// entry then refreshes the item in place, a block entry changes nothing.
func (q *promptQueue) Add(e proxy.LogEntry) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	i := q.index(e.Target())
	if i < 0 {
		q.items = append(q.items, newPromptItem(e))
		return true
	}
	if e.Action == proxy.ActionHold {
		q.items[i] = newPromptItem(e)
	}
	return false
}

func (q *promptQueue) Has(t proxy.Target) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.index(t) >= 0
}

// EndHold applies the end of a hold. An allow or deny settles the target; any
// other outcome only stops the countdown and records why, and only for the
// hold it names, so a late event can't cancel a newer hold on the same target.
func (q *promptQueue) EndHold(end proxy.HoldEnd) {
	q.mu.Lock()
	defer q.mu.Unlock()
	i := q.index(end.Target())
	if i < 0 {
		return
	}
	switch end.Outcome {
	case proxy.HoldAllowed, proxy.HoldDenied:
		q.items = slices.Delete(q.items, i, i+1)
	default:
		if q.items[i].Entry.ID == end.ID {
			q.items[i].Deadline = time.Time{}
			q.items[i].Ended = end.Outcome
		}
	}
}

// DropHolds turns every item into a plain block, for a proxy that restarted:
// the requests it held are gone, and the IDs of its holds start over, so an
// old ID must not match the end of a new hold.
func (q *promptQueue) DropHolds() {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.items {
		q.items[i].Entry.ID = 0
		q.items[i].Deadline = time.Time{}
	}
}

func (q *promptQueue) Remove(t proxy.Target) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if i := q.index(t); i >= 0 {
		q.items = slices.Delete(q.items, i, i+1)
	}
}

// Items returns a copy of the queue, first target first.
func (q *promptQueue) Items() []promptItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Clone(q.items)
}

func (q *promptQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// Clear empties the queue and returns what was left in it.
func (q *promptQueue) Clear() []promptItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	items := q.items
	q.items = nil
	return items
}
