package proxy

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"
)

// Limits on holding, so a flood of unique hosts can't pile up waiting
// requests. Past either one, requests are blocked at once as if nobody were
// watching.
const (
	maxHeldTargets  = 32
	maxHeldRequests = 64
)

// hold is one open hold on a target, shared by every request waiting on it.
type hold struct {
	entry   LogEntry // the hold's log entry, ID included
	waiters int
	timer   *time.Timer
	// done is closed when the hold ends; outcome is set before that.
	done    chan struct{}
	outcome HoldOutcome
}

// Holds makes requests blocked only because their target isn't on the
// allowlist wait while a watcher decides whether to allow them.
//
// Lock order is Holds → LogBuffer → EventHub: the registry logs and
// publishes while holding its lock, so nothing may call into it while
// holding the log's or the hub's lock.
type Holds struct {
	timeout time.Duration
	log     *LogBuffer
	hub     *EventHub
	denied  *DenySet
	// decide is the policy's Decide; a field so tests can stall it.
	decide func(Target) Decision

	mu      sync.Mutex
	held    map[Target]*hold
	waiting int // requests waiting across all holds
}

// WireEvents connects log to a new event hub and hold registry. The hook
// publishes under the log's lock, which keeps live log events in ID order.
func WireEvents(log *LogBuffer, timeout time.Duration, denied *DenySet, policy *Policy) (*EventHub, *Holds) {
	hub := NewEventHub()
	log.OnAdd(func(e LogEntry) { hub.Publish(Event{Type: EventLog, Log: e}) })
	return hub, NewHolds(timeout, log, hub, denied, policy)
}

// NewHolds creates the hold registry. policy decides which targets an
// allow releases; a denied target is never held or released as allowed.
func NewHolds(timeout time.Duration, log *LogBuffer, hub *EventHub, denied *DenySet, policy *Policy) *Holds {
	h := &Holds{
		timeout: timeout,
		log:     log,
		hub:     hub,
		denied:  denied,
		decide:  policy.Decide,
		held:    make(map[Target]*hold),
	}
	hub.OnEmpty(h.endUnwatched)
	return h
}

// Wait holds a request for k until the hold ends and returns how it ended,
// and the ID of the hold's entry, zero when the request wasn't held.
// HoldNone: not held (timeout 0, nobody watching, denied by a rule,
// AllowFixes false, or over a limit). HoldRejected without waiting: a user
// rejected the target this session, so it is never held again.
// HoldAllowed without waiting: the allowlist admits the target by the time
// the registry lock is taken. HoldAbandoned: this request's ctx ended first.
func (h *Holds) Wait(ctx context.Context, k HostKey) (HoldOutcome, uint64) {
	// Only proxy requests are held; DNS queries are always answered at once.
	if k.Source != SourceProxy || !AllowFixes(k, BlockNotAllowlisted) {
		return HoldNone, 0
	}
	t := k.Target()

	h.mu.Lock()
	// Allows and denies update their lists before taking this lock to end
	// holds, so re-checking here means a decision made since the caller's
	// filter ran is never missed by a hold registered after it.
	decision := h.decide(t)
	if decision == Allowed {
		h.mu.Unlock()
		return HoldAllowed, 0
	}
	// Checked before the timeout and watchers, so the block says why even
	// with nobody left to ask.
	if decision != Denied && h.denied.Denied(t) {
		h.mu.Unlock()
		return HoldRejected, 0
	}
	if h.timeout <= 0 || decision == Denied || !h.hub.Watching() || h.waiting >= maxHeldRequests {
		h.mu.Unlock()
		return HoldNone, 0
	}
	hd := h.held[t]
	if hd == nil {
		if len(h.held) >= maxHeldTargets {
			h.mu.Unlock()
			return HoldNone, 0
		}
		hd = h.openLocked(t, k)
	}
	hd.waiters++
	h.waiting++
	h.mu.Unlock()

	select {
	case <-hd.done:
		return hd.outcome, hd.entry.ID
	case <-ctx.Done():
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	select {
	case <-hd.done:
		// The hold ended while this request was leaving and its waiters
		// were already released, so report the ending.
		return hd.outcome, hd.entry.ID
	default:
	}
	hd.waiters--
	h.waiting--
	if hd.waiters == 0 {
		h.endLocked(t, hd, HoldAbandoned)
	}
	return HoldAbandoned, hd.entry.ID
}

// openLocked logs a new hold on t and arms its timeout. Caller must hold h.mu.
func (h *Holds) openLocked(t Target, k HostKey) *hold {
	now := time.Now()
	hd := &hold{
		entry: LogEntry{
			Time:     now,
			Domain:   k.Domain,
			Port:     k.Port,
			Action:   ActionHold,
			Source:   SourceProxy,
			Reason:   reasonNotAllowlisted,
			Code:     BlockNotAllowlisted,
			Deadline: now.Add(h.timeout),
		},
		done: make(chan struct{}),
	}
	hd.entry.ID = h.log.Add(hd.entry)
	hd.timer = time.AfterFunc(h.timeout, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		// A later hold on the same target is not this timer's to end.
		if h.held[t] == hd {
			h.endLocked(t, hd, HoldTimeout)
		}
	})
	h.held[t] = hd
	return hd
}

// End ends the open hold on t, if any, with outcome o. Callers deciding a
// target must record the decision (allowlist, DenySet) before calling End.
func (h *Holds) End(t Target, o HoldOutcome) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if hd := h.held[t]; hd != nil {
		h.endLocked(t, hd, o)
	}
}

// EndAllowed ends each open hold whose target the allowlist now admits, as
// HoldAllowed. Callers must add the allowlist entries before calling it.
func (h *Holds) EndAllowed() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for t, hd := range h.held {
		if h.decide(t) == Allowed {
			h.endLocked(t, hd, HoldAllowed)
		}
	}
}

// Open returns the hold entries of all open holds in ID order, for the
// stream's snapshot.
func (h *Holds) Open() []LogEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	entries := make([]LogEntry, 0, len(h.held))
	for _, hd := range h.held {
		entries = append(entries, hd.entry)
	}
	slices.SortFunc(entries, func(a, b LogEntry) int { return cmp.Compare(a.ID, b.ID) })
	return entries
}

// endUnwatched is the hub's OnEmpty hook. It runs after the hub's lock is
// released, so a new watcher may have arrived since; holds only end if
// nobody is watching now.
func (h *Holds) endUnwatched() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hub.Watching() {
		return
	}
	for t, hd := range h.held {
		h.endLocked(t, hd, HoldUnwatched)
	}
}

// endLocked ends hd once. The outcome is stored on the log entry before the
// event goes out, so a stream's history read never lags the live event.
// Caller must hold h.mu and hd must still be open.
func (h *Holds) endLocked(t Target, hd *hold, o HoldOutcome) {
	hd.timer.Stop()
	delete(h.held, t)
	h.waiting -= hd.waiters
	h.log.SetHoldOutcome(hd.entry.ID, o)
	hd.outcome = o
	close(hd.done)
	h.hub.Publish(Event{Type: EventHoldEnd, HoldEnd: HoldEnd{
		ID:      hd.entry.ID,
		HostKey: hd.entry.HostKey(),
		Outcome: o,
	}})
}

// EndDenied settles holds after enforced rules have been published.
func (h *Holds) EndDenied() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for t, hd := range h.held {
		if h.decide(t) == Denied {
			h.endLocked(t, hd, HoldDenied)
		}
	}
}
