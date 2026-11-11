package proxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

type EventType string

const (
	EventLog      EventType = "log"       // data: LogEntry
	EventCursor   EventType = "cursor"    // data: {"id":N}: the history boundary
	EventHoldOpen EventType = "hold-open" // data: LogEntry (a hold entry); snapshot only
	EventHoldEnd  EventType = "hold-end"  // data: HoldEnd
)

// HoldEnd tells watchers that a hold has ended and how.
type HoldEnd struct {
	ID uint64 `json:"id"` // the hold entry it ends
	HostKey
	Outcome HoldOutcome `json:"outcome"`
}

// Event is one message on the /events stream. Only the field matching Type
// is set.
type Event struct {
	Type    EventType
	Log     LogEntry // EventLog and EventHoldOpen
	HoldEnd HoldEnd
	Cursor  uint64 // EventCursor
}

// eventBuffer is how far a subscriber may lag before it is dropped.
const eventBuffer = 256

// EventHub fans events out to the stream subscribers (the watchers).
type EventHub struct {
	mu      sync.Mutex
	subs    map[chan Event]struct{}
	onEmpty func()
}

func NewEventHub() *EventHub {
	return &EventHub{subs: make(map[chan Event]struct{})}
}

// Subscribe returns a channel of published events. The hub closes it if the
// subscriber falls eventBuffer events behind; unsubscribe is idempotent.
func (h *EventHub) Subscribe() (events <-chan Event, unsubscribe func()) {
	ch := make(chan Event, eventBuffer)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()

	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.remove(ch, true)
	}
}

// remove must be called with h.mu held. notify runs the OnEmpty hook when
// ch was the last subscriber.
func (h *EventHub) remove(ch chan Event, notify bool) {
	if _, ok := h.subs[ch]; !ok {
		return
	}
	delete(h.subs, ch)
	close(ch)
	if notify && len(h.subs) == 0 && h.onEmpty != nil {
		// A goroutine because Publish runs under the log buffer's lock and
		// the hook takes other locks.
		go h.onEmpty()
	}
}

// Publish never blocks: a subscriber with a full buffer is dropped rather
// than waited for, since the caller may hold locks. A dropped subscriber
// didn't leave: it reconnects, and the stream's snapshot hands it the open
// holds again. So dropping the last one doesn't run the OnEmpty hook, which
// would end holds the watcher is about to decide; they run to their timeout
// if it never comes back.
func (h *EventHub) Publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- e:
		default:
			h.remove(ch, false)
		}
	}
}

// Watching reports whether any subscriber is connected.
func (h *EventHub) Watching() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs) > 0
}

// OnEmpty sets the hook that runs, in its own goroutine, whenever the last
// subscriber unsubscribes.
func (h *EventHub) OnEmpty(f func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onEmpty = f
}

type cursorData struct {
	ID uint64 `json:"id"`
}

// wireEntry is a log entry as the stream sends it. An open hold's deadline
// goes out as the time left: the proxy runs in its own container, often in
// a VM, whose clock can be seconds off the client's after a sleep, and an
// absolute deadline would then end holds early or late on the client. The
// reader turns it back into a deadline on its own clock, late by the
// network delay. Ended holds need no deadline and get none.
type wireEntry struct {
	LogEntry
	HoldLeftMS *int64 `json:"hold_left_ms,omitempty"`
}

func toWire(e LogEntry, now time.Time) wireEntry {
	w := wireEntry{LogEntry: e}
	w.Deadline = time.Time{}
	if e.Action == ActionHold && e.Outcome == HoldNone {
		left := max(e.Deadline.Sub(now), 0).Milliseconds()
		w.HoldLeftMS = &left
	}
	return w
}

func (w wireEntry) entry(now time.Time) LogEntry {
	e := w.LogEntry
	e.Deadline = time.Time{}
	if w.HoldLeftMS != nil {
		e.Deadline = now.Add(time.Duration(*w.HoldLeftMS) * time.Millisecond)
	}
	return e
}

// WriteEvent writes e in server-sent-events framing.
func WriteEvent(w io.Writer, e Event) error {
	return writeEvent(w, e, time.Now())
}

func writeEvent(w io.Writer, e Event, now time.Time) error {
	var payload any
	switch e.Type {
	case EventLog, EventHoldOpen:
		payload = toWire(e.Log, now)
	case EventCursor:
		payload = cursorData{ID: e.Cursor}
	case EventHoldEnd:
		payload = e.HoldEnd
	default:
		return fmt.Errorf("unknown event type %q", e.Type)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, data)
	return err
}

// ReadEvent reads the next event, skipping comments and keepalive blank
// lines. An event of an unknown type comes back with only Type set, so a
// newer proxy can add types without breaking older clients. An open hold's
// deadline is on the reader's clock, see wireEntry.
func ReadEvent(r *bufio.Reader) (Event, error) {
	return readEvent(r, time.Now)
}

func readEvent(r *bufio.Reader, now func() time.Time) (Event, error) {
	var typ EventType
	for {
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			if err == io.EOF && typ != "" {
				return Event{}, io.ErrUnexpectedEOF
			}
			return Event{}, err
		}
		line = strings.TrimRight(line, "\r\n")

		switch {
		case line == "":
			typ = ""
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event:"):
			typ = EventType(strings.TrimSpace(strings.TrimPrefix(line, "event:")))
		case strings.HasPrefix(line, "data:"):
			if typ == "" {
				return Event{}, fmt.Errorf("event data without an event type")
			}
			return decodeEvent(typ, []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), now())
		}
		// Other SSE fields (id:, retry:) are ignored.
	}
}

func decodeEvent(typ EventType, data []byte, now time.Time) (Event, error) {
	e := Event{Type: typ}
	var err error
	switch typ {
	case EventLog, EventHoldOpen:
		var w wireEntry
		err = json.Unmarshal(data, &w)
		e.Log = w.entry(now)
	case EventCursor:
		var c cursorData
		err = json.Unmarshal(data, &c)
		e.Cursor = c.ID
	case EventHoldEnd:
		err = json.Unmarshal(data, &e.HoldEnd)
	default:
		return e, nil
	}
	if err != nil {
		return Event{}, fmt.Errorf("decode %s event: %w", typ, err)
	}
	return e, nil
}
