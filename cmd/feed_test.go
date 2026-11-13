package cmd

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bernd/vibepit/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// eventsProxy is a real control API with the event stream enabled.
type eventsProxy struct {
	log    *proxy.LogBuffer
	hub    *proxy.EventHub
	holds  *proxy.Holds
	api    *proxy.ControlAPI
	client *ControlClient
}

func newEventsProxy(t *testing.T) *eventsProxy {
	t.Helper()
	tp := newTestProxy(t)
	ep := &eventsProxy{log: tp.log, api: tp.api, client: tp.client}
	ep.hub, ep.holds = proxy.WireEvents(ep.log, 10*time.Second, ep.api.Denied(), proxy.NewPolicy(nil, nil, nil))
	ep.api.SetEvents(ep.hub, ep.holds)
	return ep
}

func (ep *eventsProxy) addEntries(n int) {
	for range n {
		ep.log.Add(proxy.LogEntry{Domain: "a.com", Port: "443", Action: proxy.ActionAllow, Source: proxy.SourceProxy})
	}
}

// addHold logs a hold entry directly, without a waiting request, so the
// test decides when it ends.
func (ep *eventsProxy) addHold() uint64 {
	return ep.log.Add(proxy.LogEntry{
		Domain:   "held.com",
		Port:     "443",
		Action:   proxy.ActionHold,
		Source:   proxy.SourceProxy,
		Code:     proxy.BlockNotAllowlisted,
		Deadline: time.Now().Add(time.Minute),
	})
}

// cutStream serves the real API, records each /events query, and ends a
// stream as soon as drop reports true for an event it sent. before runs
// ahead of every stream, so a test can keep the feed disconnected.
type cutStream struct {
	api    http.Handler
	drop   func(conn int, e proxy.Event) bool
	before func(conn int)

	mu      sync.Mutex
	queries []string
}

func (h *cutStream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/events" {
		h.api.ServeHTTP(w, r)
		return
	}
	h.mu.Lock()
	h.queries = append(h.queries, r.URL.RawQuery)
	conn := len(h.queries)
	h.mu.Unlock()
	if h.before != nil {
		h.before(conn)
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	cw := &cutWriter{ResponseWriter: w}
	if h.drop != nil {
		cw.drop = func(e proxy.Event) bool {
			if h.drop(conn, e) {
				cancel()
				return true
			}
			return false
		}
	}
	h.api.ServeHTTP(cw, r.WithContext(ctx))
}

func (h *cutStream) recorded() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.queries...)
}

// cutWriter passes on what the handler flushes one event at a time and
// stops right after the first event drop selects, however many events the
// handler batched into that flush.
type cutWriter struct {
	http.ResponseWriter
	buf  bytes.Buffer
	drop func(proxy.Event) bool // nil never cuts
	cut  bool
}

func (w *cutWriter) Write(p []byte) (int, error) {
	if !w.cut {
		w.buf.Write(p)
	}
	return len(p), nil
}

func (w *cutWriter) Flush() {
	for !w.cut {
		i := bytes.Index(w.buf.Bytes(), []byte("\n\n"))
		if i < 0 {
			break
		}
		frame := bytes.Clone(w.buf.Next(i + 2))
		w.ResponseWriter.Write(frame) //nolint:errcheck
		if w.drop == nil {
			continue
		}
		if e, err := proxy.ReadEvent(bufio.NewReader(bytes.NewReader(frame))); err == nil && w.drop(e) {
			w.cut = true
		}
	}
	w.ResponseWriter.(http.Flusher).Flush()
}

func (w *cutWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// describe flattens a batch into comparable lines.
func describe(b feedBatch) []string {
	if b.Err != nil {
		return []string{"err"}
	}
	if b.Reset {
		return []string{"reset"}
	}
	if len(b.Events) == 0 {
		return []string{"connect"}
	}
	var lines []string
	for _, e := range b.Events {
		switch e.Type {
		case proxy.EventLog, proxy.EventHoldOpen:
			lines = append(lines, fmt.Sprintf("%s %d", e.Type, e.Log.ID))
		case proxy.EventHoldEnd:
			lines = append(lines, fmt.Sprintf("%s %d %s", e.Type, e.HoldEnd.ID, e.HoldEnd.Outcome))
		default:
			lines = append(lines, string(e.Type))
		}
	}
	return lines
}

// readFeed reads batches until it has at least n lines.
func readFeed(t *testing.T, ch <-chan feedBatch, n int) []string {
	t.Helper()
	var lines []string
	timeout := time.After(5 * time.Second)
	for len(lines) < n {
		select {
		case b, ok := <-ch:
			require.True(t, ok, "feed closed early after %v", lines)
			lines = append(lines, describe(b)...)
		case <-timeout:
			require.FailNow(t, "timed out", "got %v", lines)
		}
	}
	return lines
}

const testRetry = 10 * time.Millisecond

func TestStartFeed_ReconnectsFromLastID(t *testing.T) {
	ep := newEventsProxy(t)
	ep.addEntries(2)
	gate := make(chan struct{})
	h := &cutStream{
		api:  ep.api,
		drop: func(conn int, e proxy.Event) bool { return conn == 1 && e.Type == proxy.EventCursor },
		before: func(conn int) {
			if conn == 2 {
				<-gate
			}
		},
	}
	ch := startFeed(t.Context(), testControlClient(t, h), 25, testRetry)

	assert.Equal(t, []string{"connect", "log 1", "log 2", "err"}, readFeed(t, ch, 4))
	ep.addEntries(2)
	close(gate)
	assert.Equal(t, []string{"connect", "log 3", "log 4"}, readFeed(t, ch, 3))
	assert.Equal(t, []string{"tail=25", "after=2"}, h.recorded())
}

func TestStartFeed_ReplaysOutcomeOfHoldEndedWhileDisconnected(t *testing.T) {
	ep := newEventsProxy(t)
	ep.addEntries(4)
	holdID := ep.addHold()
	require.Equal(t, uint64(5), holdID)
	ep.addEntries(2)
	gate := make(chan struct{})
	h := &cutStream{
		api:  ep.api,
		drop: func(conn int, e proxy.Event) bool { return conn == 1 && e.Type == proxy.EventCursor },
		before: func(conn int) {
			if conn == 2 {
				<-gate
			}
		},
	}
	ch := startFeed(t.Context(), testControlClient(t, h), 25, testRetry)

	assert.Equal(t, []string{"connect", "log 1", "log 2", "log 3", "log 4", "log 5", "log 6", "log 7", "err"}, readFeed(t, ch, 9))
	require.True(t, ep.log.SetHoldOutcome(holdID, proxy.HoldDenied))
	ep.addEntries(1)
	close(gate)
	assert.Equal(t, []string{"connect", "hold-end 5 denied", "log 8"}, readFeed(t, ch, 3))
	assert.Equal(t, []string{"tail=25", "after=4"}, h.recorded())
}

func TestStartFeed_TailZeroReconnectBeforeFirstLog(t *testing.T) {
	ep := newEventsProxy(t)
	ep.addEntries(100)
	h := &cutStream{
		api:  ep.api,
		drop: func(conn int, e proxy.Event) bool { return conn == 1 && e.Type == proxy.EventCursor },
	}
	ch := startFeed(t.Context(), testControlClient(t, h), 0, testRetry)

	assert.Equal(t, []string{"connect", "err", "connect"}, readFeed(t, ch, 3))
	ep.addEntries(1)
	assert.Equal(t, []string{"log 101"}, readFeed(t, ch, 1))
	assert.Equal(t, []string{"tail=0", "after=100"}, h.recorded())
}

func TestStartFeed_ReconnectsWhenIdle(t *testing.T) {
	old := feedIdleTimeout
	feedIdleTimeout = 100 * time.Millisecond
	t.Cleanup(func() { feedIdleTimeout = old })

	var mu sync.Mutex
	var conns int
	client := testControlClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		conns++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	ch := startFeed(t.Context(), client, 0, testRetry)

	assert.Equal(t, []string{"connect", "err", "connect"}, readFeed(t, ch, 3))
	mu.Lock()
	defer mu.Unlock()
	assert.GreaterOrEqual(t, conns, 2)
}

func TestStartFeed_ReportsProxyWithoutStream(t *testing.T) {
	tp := newTestProxy(t)
	var requests atomic.Int32
	client := testControlClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		tp.api.ServeHTTP(w, r)
	}))
	ch := startFeed(t.Context(), client, 25, testRetry)

	b := <-ch
	require.ErrorIs(t, b.Err, ErrUnsupportedEndpoint)
	assert.Empty(t, b.Events)
	// The feed gives up: no retries, no polling.
	time.Sleep(10 * testRetry)
	assert.Equal(t, int32(1), requests.Load())
	select {
	case b := <-ch:
		require.FailNow(t, "unexpected batch after the unsupported report", "%+v", b)
	default:
	}
}

func TestStartFeed_GroupsEventsWhileConsumerBusy(t *testing.T) {
	ep := newEventsProxy(t)
	ep.addEntries(10)
	ch := startFeed(t.Context(), ep.client, 25, testRetry)

	require.Equal(t, []string{"connect"}, readFeed(t, ch, 1))
	// Stay busy long enough for the feed to read the whole history.
	time.Sleep(200 * time.Millisecond)
	var batches int
	var lines []string
	for len(lines) < 10 {
		lines = append(lines, readFeed(t, ch, 1)...)
		batches++
	}
	assert.LessOrEqual(t, batches, 2, "history arrived in %d batches", batches)
	assert.Len(t, lines, 10)
}

func TestStartFeed_StopsOnCancel(t *testing.T) {
	ep := newEventsProxy(t)
	ctx, cancel := context.WithCancel(t.Context())
	ch := startFeed(ctx, ep.client, 0, testRetry)
	require.Equal(t, []string{"connect"}, readFeed(t, ch, 1))

	cancel()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-timeout:
			require.FailNow(t, "feed did not close after cancel")
		}
	}
}

func TestStartFeed_HoldOpenAndEnd(t *testing.T) {
	ep := newEventsProxy(t)
	ep.addEntries(3)
	// Holds only open while someone watches.
	watcher, err := ep.client.EventsTail(t.Context(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { watcher.Close() })
	require.Eventually(t, ep.hub.Watching, 5*time.Second, time.Millisecond)

	key := proxy.HostKey{Source: proxy.SourceProxy, Domain: "held.com", Port: "443"}
	waited := make(chan proxy.HoldOutcome, 1)
	go func() { o, _ := ep.holds.Wait(context.Background(), key); waited <- o }()
	require.Eventually(t, func() bool { return len(ep.holds.Open()) == 1 }, 5*time.Second, time.Millisecond)

	ch := startFeed(t.Context(), ep.client, 0, testRetry)
	assert.Equal(t, []string{"connect", "hold-open 4"}, readFeed(t, ch, 2))

	ep.holds.End(key.Target(), proxy.HoldAllowed)
	assert.Equal(t, proxy.HoldAllowed, <-waited)
	ep.addEntries(1)
	assert.Equal(t, []string{"hold-end 4 allowed", "log 5"}, readFeed(t, ch, 2))
}

// restartProxy serves one control API until restart swaps in another, like
// a proxy container that restarted on the same port: its log IDs start
// over. A restart ends the old API's open streams.
type restartProxy struct {
	mu      sync.Mutex
	api     http.Handler
	cancels []context.CancelFunc
}

func (p *restartProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	p.mu.Lock()
	api := p.api
	p.cancels = append(p.cancels, cancel)
	p.mu.Unlock()
	api.ServeHTTP(w, r.WithContext(ctx))
}

func (p *restartProxy) restart(api http.Handler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.api = api
	for _, cancel := range p.cancels {
		cancel()
	}
	p.cancels = nil
}

func TestStartFeed_ProxyRestart(t *testing.T) {
	t.Run("delivers the new proxy's entries and holds", func(t *testing.T) {
		old := newEventsProxy(t)
		old.addEntries(5)
		rp := &restartProxy{api: old.api}
		h := &cutStream{api: rp}
		ch := startFeed(t.Context(), testControlClient(t, h), 25, testRetry)
		assert.Equal(t, []string{"connect", "log 1", "log 2", "log 3", "log 4", "log 5"}, readFeed(t, ch, 6))

		fresh := newEventsProxy(t)
		fresh.addEntries(2)
		rp.restart(fresh.api)
		assert.Equal(t, []string{"err", "connect", "reset"}, readFeed(t, ch, 3))

		fresh.addEntries(1)
		key := proxy.HostKey{Source: proxy.SourceProxy, Domain: "held.com", Port: "443"}
		waited := make(chan proxy.HoldOutcome, 1)
		go func() { o, _ := fresh.holds.Wait(t.Context(), key); waited <- o }()
		assert.Equal(t, []string{"log 3", "log 4"}, readFeed(t, ch, 2))
		fresh.holds.End(key.Target(), proxy.HoldAllowed)
		assert.Equal(t, proxy.HoldAllowed, <-waited)
		assert.Equal(t, []string{"hold-end 4 allowed"}, readFeed(t, ch, 1))
		assert.Equal(t, []string{"tail=25", "after=5"}, h.recorded())
	})

	t.Run("drops the new proxy's history before the reset", func(t *testing.T) {
		old := newEventsProxy(t)
		old.addEntries(2)
		require.Equal(t, uint64(3), old.addHold())
		old.addEntries(7)
		rp := &restartProxy{api: old.api}
		h := &cutStream{api: rp}
		ch := startFeed(t.Context(), testControlClient(t, h), 25, testRetry)
		readFeed(t, ch, 11)

		// The new proxy's hold 3 ended, and it has nothing to do with the
		// old proxy's hold 3 the feed still thinks is open.
		fresh := newEventsProxy(t)
		fresh.addEntries(2)
		require.Equal(t, uint64(3), fresh.addHold())
		require.True(t, fresh.log.SetHoldOutcome(3, proxy.HoldDenied))
		fresh.addEntries(1)
		rp.restart(fresh.api)
		assert.Equal(t, []string{"err", "connect", "reset"}, readFeed(t, ch, 3))

		fresh.addEntries(1)
		assert.Equal(t, []string{"log 5"}, readFeed(t, ch, 1))
		assert.Equal(t, []string{"tail=25", "after=2"}, h.recorded())
	})
}

// scriptedEvents serves /events from a script: stream i sends script[i]
// and ends, streams past the script stay open and silent. It returns a
// client and the queries the streams were opened with.
func scriptedEvents(t *testing.T, script ...[]proxy.Event) (*ControlClient, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var queries []string
	client := testControlClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.RawQuery)
		conn := len(queries) - 1
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if conn >= len(script) {
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		for _, e := range script[conn] {
			require.NoError(t, proxy.WriteEvent(w, e))
		}
		w.(http.Flusher).Flush()
	}))
	return client, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), queries...)
	}
}

func TestStartFeed_OpenHoldsDontPinReconnects(t *testing.T) {
	logEvent := func(id uint64, action proxy.Action, deadline time.Time, outcome proxy.HoldOutcome) proxy.Event {
		return proxy.Event{Type: proxy.EventLog, Log: proxy.LogEntry{ID: id, Domain: fmt.Sprintf("d%d.com", id), Port: "443",
			Action: action, Source: proxy.SourceProxy, Deadline: deadline, Outcome: outcome}}
	}
	cursor := func(id uint64) proxy.Event { return proxy.Event{Type: proxy.EventCursor, Cursor: id} }
	future := time.Now().Add(time.Hour)

	tests := []struct {
		name   string
		script [][]proxy.Event
		want   []string
	}{
		{
			// Its entry may have left the log ring, so replaying from it
			// could mean the whole ring on every reconnect.
			name: "a hold past its deadline",
			script: [][]proxy.Event{{
				logEvent(1, proxy.ActionAllow, time.Time{}, proxy.HoldNone),
				logEvent(2, proxy.ActionHold, time.Now().Add(-time.Second), proxy.HoldNone),
				logEvent(3, proxy.ActionAllow, time.Time{}, proxy.HoldNone),
				cursor(3),
			}},
			want: []string{"tail=25", "after=3"},
		},
		{
			name: "a hold whose new entry came with its outcome",
			script: [][]proxy.Event{
				{cursor(5), {Type: proxy.EventHoldOpen, Log: logEvent(6, proxy.ActionHold, future, proxy.HoldNone).Log}},
				{logEvent(6, proxy.ActionHold, future, proxy.HoldDenied), cursor(6)},
			},
			want: []string{"tail=25", "after=5", "after=6"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, queries := scriptedEvents(t, tt.script...)
			startFeed(t.Context(), client, 25, testRetry)
			require.Eventually(t, func() bool { return len(queries()) >= len(tt.want) }, 5*time.Second, time.Millisecond)
			assert.Equal(t, tt.want, queries())
		})
	}
}
