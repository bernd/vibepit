package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bernd/vibepit/proxy"
)

// feedBatch is what the feed delivers: events, or a connection error.
// An empty batch with Err == nil and Reset unset reports a (re)connect.
type feedBatch struct {
	Events []proxy.Event
	Err    error
	// Reset reports that the feed reconnected to a new proxy, e.g. after
	// its container restarted: log and hold IDs started over, so whatever
	// the consumer keeps by ID from before is stale. It comes alone, after
	// every event from the old proxy and before any from the new one.
	Reset bool
}

// feedIdleTimeout is how long the feed waits without reading anything,
// keepalives included, before it gives up on a stream and reconnects. The
// proxy writes a keepalive every 15 seconds.
var feedIdleTimeout = 30 * time.Second

// startFeed keeps an event stream to the proxy open and delivers its events
// until ctx ends, then closes the channel. The first stream starts with the
// newest tail entries; reconnects resume where the last stream ended, so
// every log entry is delivered once. A proxy without the stream is polled
// every retry instead.
func startFeed(ctx context.Context, c *ControlClient, tail int, retry time.Duration) <-chan feedBatch {
	f := &feed{
		client: c,
		tail:   max(tail, 0),
		retry:  retry,
		idle:   feedIdleTimeout,
		open:   make(map[uint64]time.Time),
		queue:  newFeedQueue(),
	}
	out := make(chan feedBatch)
	go f.run(ctx)
	go f.queue.deliver(ctx, out)
	return out
}

type feed struct {
	client *ControlClient
	tail   int
	retry  time.Duration
	idle   time.Duration
	queue  *feedQueue

	// positioned is set once lastID means something: after the first
	// cursor or log entry. Until then a connect asks for the tail.
	positioned bool
	// lastID is the highest cursor or log ID seen. Log entries up to it
	// were delivered or deliberately skipped by the tail.
	lastID uint64
	// open maps the IDs of hold entries the consumer was told about and
	// hasn't seen end yet to their deadlines.
	open map[uint64]time.Time
	// down is set once a failure was reported, so a proxy that stays away
	// is reported once rather than on every retry.
	down bool
}

func (f *feed) run(ctx context.Context) {
	for {
		err := f.stream(ctx)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, ErrUnsupportedEndpoint) {
			f.poll(ctx)
			return
		}
		f.fail(err)
		if !sleepCtx(ctx, f.retry) {
			return
		}
	}
}

// stream runs one stream until it fails, goes idle or ctx ends.
func (f *feed) stream(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var idled atomic.Bool
	idle := time.AfterFunc(f.idle, func() {
		idled.Store(true)
		cancel()
	})
	defer idle.Stop()
	idleErr := fmt.Errorf("event stream: nothing received for %s", f.idle)

	var s *EventStream
	var err error
	if f.positioned {
		s, err = f.client.EventsAfter(ctx, f.resumeAfter())
	} else {
		s, err = f.client.EventsTail(ctx, f.tail)
	}
	if err != nil {
		if idled.Load() {
			return idleErr
		}
		return err
	}
	defer s.Close()
	idle.Reset(f.idle)
	s.onRead(func() { idle.Reset(f.idle) })
	f.down = false
	f.queue.push(feedBatch{})

	// The history waits for the cursor that ends it: only the cursor tells
	// whether the proxy is still the one the feed's IDs came from.
	var history []proxy.Event
	cursorSeen := false
	for {
		e, err := s.Next()
		if err != nil {
			if idled.Load() {
				return idleErr
			}
			if errors.Is(err, io.EOF) {
				return errors.New("event stream: closed by proxy")
			}
			return fmt.Errorf("event stream: %w", err)
		}
		if !cursorSeen {
			if e.Type != proxy.EventCursor {
				history = append(history, e)
				continue
			}
			cursorSeen = true
			// A proxy never hands out an ID above its cursor, so a lower
			// cursor means a new proxy whose IDs started over.
			if f.positioned && e.Cursor < f.lastID {
				f.reset(e.Cursor)
			}
			for _, h := range history {
				f.deliver(h)
			}
			history = nil
		}
		f.deliver(e)
	}
}

func (f *feed) deliver(e proxy.Event) {
	if e, ok := f.apply(e); ok {
		f.queue.push(feedBatch{Events: []proxy.Event{e}})
	}
}

// reset forgets the old proxy's IDs and tells the consumer. The new
// proxy's entries up to its cursor are skipped like a tail would skip them:
// the history above the old position is only a part of them.
func (f *feed) reset(cursor uint64) {
	f.lastID = cursor
	clear(f.open)
	f.queue.push(feedBatch{Reset: true})
}

// resumeAfter reaches back far enough to replay every hold entry the
// consumer still thinks is open, so holds that ended while disconnected
// come back with their outcome. A hold past its deadline has ended, and
// its entry may have left the log ring: reaching back for it could replay
// the whole ring on every reconnect, so it is no longer tracked.
func (f *feed) resumeAfter() uint64 {
	now := time.Now()
	after := f.lastID
	for id, deadline := range f.open {
		if !now.Before(deadline) {
			delete(f.open, id)
			continue
		}
		after = min(after, id-1)
	}
	return after
}

// apply updates the feed's position and open holds for e and returns the
// event to deliver, if any. Replayed entries become hold-end events or are
// dropped, so the consumer never sees an entry or an ending twice.
func (f *feed) apply(e proxy.Event) (proxy.Event, bool) {
	switch e.Type {
	case proxy.EventCursor:
		f.positioned = true
		f.lastID = max(f.lastID, e.Cursor)
		return e, false
	case proxy.EventLog:
		id := e.Log.ID
		if id <= f.lastID {
			if _, ok := f.open[id]; !ok || e.Log.Outcome == proxy.HoldNone {
				return e, false
			}
			delete(f.open, id)
			return proxy.Event{Type: proxy.EventHoldEnd, HoldEnd: proxy.HoldEnd{
				ID:      id,
				HostKey: e.Log.HostKey(),
				Outcome: e.Log.Outcome,
			}}, true
		}
		f.positioned = true
		f.lastID = id
		if e.Log.Action == proxy.ActionHold {
			if e.Log.Outcome == proxy.HoldNone {
				f.open[id] = e.Log.Deadline
			} else {
				// Its hold-open may have come first; the outcome ends it.
				delete(f.open, id)
			}
		}
		return e, true
	case proxy.EventHoldOpen:
		f.open[e.Log.ID] = e.Log.Deadline
		return e, true
	case proxy.EventHoldEnd:
		// An ending the consumer already got from a replayed outcome, or
		// of a hold it never saw open, means nothing to it.
		if _, ok := f.open[e.HoldEnd.ID]; !ok {
			return e, false
		}
		delete(f.open, e.HoldEnd.ID)
		return e, true
	default:
		return e, false
	}
}

// poll replaces the stream for a proxy that predates it. Such a proxy
// never holds requests, so log entries are all there is.
func (f *feed) poll(ctx context.Context) {
	up := false
	for {
		entries, err := f.fetch()
		if err != nil {
			f.fail(err)
			up = false
		} else {
			if !up {
				f.down = false
				up = true
				f.queue.push(feedBatch{})
			}
			if len(entries) > 0 {
				events := make([]proxy.Event, len(entries))
				for i, e := range entries {
					events[i] = proxy.Event{Type: proxy.EventLog, Log: e}
				}
				f.queue.push(feedBatch{Events: events})
			}
		}
		if !sleepCtx(ctx, f.retry) {
			return
		}
	}
}

// fetch returns the entries to deliver from one poll.
func (f *feed) fetch() ([]proxy.LogEntry, error) {
	if f.positioned {
		entries, err := f.client.LogsAfter(f.lastID)
		if err == nil && len(entries) > 0 {
			f.lastID = entries[len(entries)-1].ID
		}
		return entries, err
	}
	entries, err := f.client.Logs()
	if err != nil {
		return nil, err
	}
	f.positioned = true
	if len(entries) > 0 {
		f.lastID = entries[len(entries)-1].ID
	}
	return entries[max(0, len(entries)-f.tail):], nil
}

func (f *feed) fail(err error) {
	if !f.down {
		f.down = true
		f.queue.push(feedBatch{Err: err})
	}
}

// sleepCtx waits d and reports false if ctx ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// feedQueue sits between the feed and its consumer so the feed never waits
// on a slow consumer and never drops events. Events that pile up while the
// consumer is busy are merged into one batch.
type feedQueue struct {
	mu      sync.Mutex
	pending []feedBatch
	wake    chan struct{}
}

func newFeedQueue() *feedQueue {
	return &feedQueue{wake: make(chan struct{}, 1)}
}

func (q *feedQueue) push(b feedBatch) {
	q.mu.Lock()
	// Errors, connects and resets stay batches of their own, so they are
	// still recognisable and in order, and no batch mixes events from
	// before a reset with events from after it.
	if n := len(q.pending); n > 0 && len(b.Events) > 0 && len(q.pending[n-1].Events) > 0 {
		q.pending[n-1].Events = append(q.pending[n-1].Events, b.Events...)
	} else {
		q.pending = append(q.pending, b)
	}
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *feedQueue) pop() (feedBatch, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 {
		return feedBatch{}, false
	}
	b := q.pending[0]
	q.pending = q.pending[1:]
	return b, true
}

// deliver hands batches to out until ctx ends, then closes out.
func (q *feedQueue) deliver(ctx context.Context, out chan<- feedBatch) {
	defer close(out)
	for {
		b, ok := q.pop()
		if !ok {
			select {
			case <-q.wake:
				continue
			case <-ctx.Done():
				return
			}
		}
		select {
		case out <- b:
		case <-ctx.Done():
			return
		}
	}
}
