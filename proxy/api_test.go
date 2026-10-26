package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestControlAPI(t *testing.T) {
	log := NewLogBuffer(100)
	log.Add(LogEntry{Domain: "a.com", Port: "443", Action: ActionAllow, Source: SourceProxy})
	log.Add(LogEntry{Domain: "b.com", Action: ActionBlock, Source: SourceDNS})

	mergedConfig := map[string]any{
		"allow-http": []string{"a.com:443", "b.com:443"},
		"allow-dns":  []string{"c.com"},
	}

	allowlist, err := NewHTTPAllowlist([]string{"a.com:443", "b.com:443"})
	require.NoError(t, err)
	dnsAllowlist, err := NewDNSAllowlist([]string{"c.com"})
	require.NoError(t, err)
	api := NewControlAPI(log, mergedConfig, allowlist, dnsAllowlist)

	t.Run("GET /logs returns entries", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/logs", nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var entries []LogEntry
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &entries))
		assert.Len(t, entries, 2)
	})

	t.Run("GET /hosts returns per-host stats", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/hosts", nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var hosts []HostStats
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &hosts))
		require.Len(t, hosts, 2)
		byDomain := map[string]HostStats{}
		for _, h := range hosts {
			byDomain[h.Domain] = h
		}
		require.Contains(t, byDomain, "b.com")
		assert.Equal(t, SourceDNS, byDomain["b.com"].Source)
		assert.Equal(t, 1, byDomain["b.com"].Blocked)
		assert.True(t, byDomain["a.com"].Allowlisted)
		assert.False(t, byDomain["b.com"].Allowlisted, "b.com is only in the HTTP allowlist")
	})

	t.Run("GET /hosts reflects runtime allows", func(t *testing.T) {
		require.NoError(t, dnsAllowlist.Add([]string{"b.com"}))

		req := httptest.NewRequest(http.MethodGet, "/hosts", nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)

		var hosts []HostStats
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &hosts))
		for _, h := range hosts {
			if h.Domain == "b.com" {
				assert.True(t, h.Allowlisted)
				assert.Equal(t, ActionBlock, h.LastAction, "the block predates the allow")
				return
			}
		}
		t.Fatal("b.com not found")
	})

	t.Run("GET /config returns merged config", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/config", nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("GET /unknown returns 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/unknown", nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("POST /allow-http adds entries to allowlist", func(t *testing.T) {
		body := `{"entries": ["bun.sh:443", "esm.sh:*"]}`
		req := httptest.NewRequest(http.MethodPost, "/allow-http", strings.NewReader(body))
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)

		var resp map[string][]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, []string{"bun.sh:443", "esm.sh:*"}, resp["added"])

		// Verify the allowlist was actually updated.
		assert.True(t, allowlist.Allows("bun.sh", "443"))
		assert.True(t, allowlist.Allows("esm.sh", "80"))
		assert.False(t, allowlist.Allows("bun.sh", "80"))
	})

	t.Run("POST /allow-http with empty entries returns 400", func(t *testing.T) {
		body := `{"entries": []}`
		req := httptest.NewRequest(http.MethodPost, "/allow-http", strings.NewReader(body))
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("POST /allow-http with invalid JSON returns 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/allow-http", strings.NewReader("not json"))
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("POST /allow-http with malformed entry returns 400", func(t *testing.T) {
		body := `{"entries": ["github.com"]}`
		req := httptest.NewRequest(http.MethodPost, "/allow-http", strings.NewReader(body))
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.False(t, allowlist.Allows("github.com", "443"))
	})

	t.Run("POST /allow-dns adds entries to DNS allowlist", func(t *testing.T) {
		body := `{"entries": ["internal.example.com", "*.svc.local"]}`
		req := httptest.NewRequest(http.MethodPost, "/allow-dns", strings.NewReader(body))
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)

		var resp map[string][]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, []string{"internal.example.com", "*.svc.local"}, resp["added"])
		assert.True(t, dnsAllowlist.Allows("internal.example.com"))
		assert.True(t, dnsAllowlist.Allows("db.svc.local"))
		assert.False(t, dnsAllowlist.Allows("svc.local"))
	})

	t.Run("POST /allow-dns with malformed entry returns 400", func(t *testing.T) {
		body := `{"entries": ["github.com:443"]}`
		req := httptest.NewRequest(http.MethodPost, "/allow-dns", strings.NewReader(body))
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.False(t, dnsAllowlist.Allows("github.com"))
	})

	t.Run("GET /logs with nil URL returns all entries", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/logs", nil)
		req.URL = nil
		w := httptest.NewRecorder()
		api.handleLogs(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var entries []LogEntry
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &entries))
		assert.Len(t, entries, 2)
	})
}

func TestControlAPIPanicRecovery(t *testing.T) {
	log := NewLogBuffer(100)
	allowlist, err := NewHTTPAllowlist(nil)
	require.NoError(t, err)
	dnsAllowlist, err := NewDNSAllowlist(nil)
	require.NoError(t, err)
	api := NewControlAPI(log, nil, allowlist, dnsAllowlist)

	// Register a handler that panics.
	api.mux.HandleFunc("GET /panic", func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	})

	t.Run("httptest recorder", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/panic", nil)
		w := httptest.NewRecorder()

		assert.NotPanics(t, func() {
			api.ServeHTTP(w, req)
		})

		assert.Equal(t, http.StatusInternalServerError, w.Code)
		assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
		assert.Contains(t, w.Body.String(), "internal server error")
	})

	t.Run("live server", func(t *testing.T) {
		srv := httptest.NewServer(api)
		defer srv.Close()

		resp, err := http.Get(srv.URL + "/panic")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
		assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	})

	t.Run("live server panic after partial write", func(t *testing.T) {
		// Register a handler that writes headers then panics.
		api.mux.HandleFunc("GET /partial-panic", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("partial"))
			panic("mid-write panic")
		})

		srv := httptest.NewServer(api)
		defer srv.Close()

		resp, err := http.Get(srv.URL + "/partial-panic")
		require.NoError(t, err)
		defer resp.Body.Close()

		// Headers already sent before panic — recovery skips writing the
		// error response to avoid corrupting the in-flight body.
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, "partial", string(body), "body should not have error JSON appended")
	})
}

func TestControlAPI_Check(t *testing.T) {
	httpAL, err := NewHTTPAllowlist([]string{"a.com:443"})
	require.NoError(t, err)
	dnsAL, err := NewDNSAllowlist([]string{"c.com"})
	require.NoError(t, err)
	api := NewControlAPI(NewLogBuffer(10), nil, httpAL, dnsAL)

	tests := []struct {
		name        string
		query       string
		wantCode    int
		wantAllowed bool
	}{
		{name: "proxy allowed", query: "source=proxy&target=a.com:443", wantCode: http.StatusOK, wantAllowed: true},
		{name: "proxy other port", query: "source=proxy&target=a.com:80", wantCode: http.StatusOK},
		{name: "proxy unknown host", query: "source=proxy&target=b.com:443", wantCode: http.StatusOK},
		{name: "dns allowed", query: "source=dns&target=c.com", wantCode: http.StatusOK, wantAllowed: true},
		{name: "dns not allowed", query: "source=dns&target=a.com", wantCode: http.StatusOK},
		{name: "proxy target without port", query: "source=proxy&target=a.com", wantCode: http.StatusBadRequest},
		{name: "unknown source", query: "source=smtp&target=a.com:25", wantCode: http.StatusBadRequest},
		{name: "missing target", query: "source=dns", wantCode: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/check?"+tt.query, nil)
			w := httptest.NewRecorder()
			api.ServeHTTP(w, req)
			require.Equal(t, tt.wantCode, w.Code)
			if tt.wantCode != http.StatusOK {
				return
			}
			var res struct {
				Allowed bool `json:"allowed"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))
			assert.Equal(t, tt.wantAllowed, res.Allowed)
		})
	}

	t.Run("reflects runtime additions", func(t *testing.T) {
		require.NoError(t, httpAL.Add([]string{"late.com:443"}))
		req := httptest.NewRequest(http.MethodGet, "/check?source=proxy&target=late.com:443", nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		assert.JSONEq(t, `{"allowed":true,"denied":false}`, w.Body.String())
	})
}

func TestControlAPI_LogsCursor(t *testing.T) {
	log := NewLogBuffer(100)
	for range 30 {
		log.Add(LogEntry{Domain: "x.com"})
	}
	api := NewControlAPI(log, nil, nil, nil)

	get := func(q string) []LogEntry {
		req := httptest.NewRequest(http.MethodGet, "/logs?"+q, nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)
		var entries []LogEntry
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &entries))
		return entries
	}

	assert.Len(t, get(""), 25, "no cursor returns a recent tail")
	assert.Len(t, get("after=0"), 30, "after=0 is strict")
	assert.Len(t, get("after=27"), 3)
}

func TestControlAPI_Deny(t *testing.T) {
	httpAL, err := NewHTTPAllowlist(nil)
	require.NoError(t, err)
	dnsAL, err := NewDNSAllowlist(nil)
	require.NoError(t, err)
	api := NewControlAPI(NewLogBuffer(10), nil, httpAL, dnsAL)

	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		return w
	}

	w := do(http.MethodGet, "/check?source=proxy&target=a.com:443", "")
	assert.JSONEq(t, `{"allowed":false,"denied":false}`, w.Body.String())

	w = do(http.MethodPost, "/deny", `{"source":"proxy","target":"a.com:443"}`)
	require.Equal(t, http.StatusOK, w.Code)

	w = do(http.MethodGet, "/check?source=proxy&target=a.com:443", "")
	assert.JSONEq(t, `{"allowed":false,"denied":true}`, w.Body.String())

	w = do(http.MethodGet, "/check?source=proxy&target=a.com:80", "")
	assert.JSONEq(t, `{"allowed":false,"denied":false}`, w.Body.String())

	w = do(http.MethodGet, "/check?source=proxy&target=A.COM:443", "")
	assert.JSONEq(t, `{"allowed":false,"denied":true}`, w.Body.String(), "equivalent spelling matches")

	for name, body := range map[string]string{
		"invalid json":       `{`,
		"unknown source":     `{"source":"smtp","target":"a.com:25"}`,
		"proxy without port": `{"source":"proxy","target":"a.com"}`,
		"missing target":     `{"source":"dns"}`,
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, http.StatusBadRequest, do(http.MethodPost, "/deny", body).Code)
		})
	}
}

// eventsFixture is a ControlAPI with an event hub and hold registry, served
// by a live server because streaming needs flushing and deadlines.
type eventsFixture struct {
	api       *ControlAPI
	log       *LogBuffer
	hub       *EventHub
	holds     *Holds
	allowlist *HTTPAllowlist
	srv       *httptest.Server
}

func newEventsFixture(t *testing.T, holdTimeout time.Duration, configure func(*http.Server)) *eventsFixture {
	t.Helper()
	httpAL, err := NewHTTPAllowlist(nil)
	require.NoError(t, err)
	dnsAL, err := NewDNSAllowlist(nil)
	require.NoError(t, err)
	f := &eventsFixture{
		log:       NewLogBuffer(1000),
		hub:       NewEventHub(),
		allowlist: httpAL,
	}
	f.api = NewControlAPI(f.log, nil, httpAL, dnsAL)
	// Server.Run wires these in production.
	f.log.OnAdd(func(e LogEntry) { f.hub.Publish(Event{Type: EventLog, Log: e}) })
	f.holds = NewHolds(holdTimeout, f.log, f.hub, f.api.Denied(), func(t Target) bool {
		return httpAL.Allows(t.Host, t.Port)
	})
	f.api.SetEvents(f.hub, f.holds)

	f.srv = httptest.NewUnstartedServer(f.api)
	if configure != nil {
		configure(f.srv.Config)
	}
	f.srv.Start()
	t.Cleanup(f.srv.Close)
	return f
}

// watch subscribes a watcher that drains its events, so requests get held.
// It stays until the test ends, when leaving ends every open hold.
func (f *eventsFixture) watch(t *testing.T) {
	events, unsubscribe := f.hub.Subscribe()
	go func() {
		for range events {
		}
	}()
	t.Cleanup(unsubscribe)
}

func (f *eventsFixture) addEntries(n int) {
	for range n {
		f.log.Add(LogEntry{Domain: "x.com", Port: "443", Action: ActionBlock, Source: SourceProxy})
	}
}

// startHold makes a request wait on k and returns once its hold is open.
func (f *eventsFixture) startHold(t *testing.T, k HostKey) <-chan HoldOutcome {
	t.Helper()
	ch := make(chan HoldOutcome, 1)
	go func() { ch <- f.holds.Wait(context.Background(), k) }()
	require.Eventually(t, func() bool {
		for _, e := range f.holds.Open() {
			if e.Domain == k.Domain {
				return true
			}
		}
		return false
	}, time.Second, time.Millisecond, "hold on %v never opened", k)
	return ch
}

func (f *eventsFixture) post(t *testing.T, path, body string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	f.api.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

type eventStream struct {
	body   io.Closer
	events chan Event
}

func (f *eventsFixture) open(t *testing.T, query string) *eventStream {
	t.Helper()
	resp, err := f.srv.Client().Get(f.srv.URL + "/events" + query)
	require.NoError(t, err)
	// Registered after the server's Close, so it runs first: Close waits
	// for the streaming handler, which only returns once the client leaves.
	t.Cleanup(func() { resp.Body.Close() })
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	assert.Equal(t, "no-cache", resp.Header.Get("Cache-Control"))

	s := &eventStream{body: resp.Body, events: make(chan Event, 1024)}
	go func() {
		defer close(s.events)
		r := bufio.NewReader(resp.Body)
		for {
			e, err := ReadEvent(r)
			if err != nil {
				return
			}
			s.events <- e
		}
	}()
	return s
}

func (s *eventStream) next(t *testing.T) Event {
	t.Helper()
	select {
	case e, ok := <-s.events:
		require.True(t, ok, "stream ended")
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for an event")
		return Event{}
	}
}

// history reads the replayed log entries up to and including the cursor.
func (s *eventStream) history(t *testing.T) ([]uint64, uint64) {
	t.Helper()
	var ids []uint64
	for {
		e := s.next(t)
		switch e.Type {
		case EventLog:
			ids = append(ids, e.Log.ID)
		case EventCursor:
			return ids, e.Cursor
		default:
			t.Fatalf("unexpected %q event before the cursor", e.Type)
		}
	}
}

func TestControlAPI_EventsUnsupportedWithoutHub(t *testing.T) {
	api := NewControlAPI(NewLogBuffer(10), nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	w := httptest.NewRecorder()
	api.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestControlAPI_EventsBadParams(t *testing.T) {
	f := newEventsFixture(t, 0, nil)
	for _, q := range []string{"?tail=x", "?tail=-1", "?after=x", "?after=-1", "?tail=1&after=x"} {
		t.Run(q, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/events"+q, nil)
			w := httptest.NewRecorder()
			f.api.ServeHTTP(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

func TestControlAPI_EventsHistoryThenLive(t *testing.T) {
	tests := []struct {
		name    string
		entries int
		query   string
		want    []uint64
	}{
		{name: "after", entries: 3, query: "?after=1", want: []uint64{2, 3}},
		{name: "tail", entries: 3, query: "?tail=2", want: []uint64{2, 3}},
		{name: "no parameter", entries: 3, query: ""},
		{name: "after wins over tail", entries: 3, query: "?tail=1&after=0", want: []uint64{1, 2, 3}},
		{name: "tail capped", entries: 30, query: "?tail=100", want: seq(6, 30)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newEventsFixture(t, 0, nil)
			f.addEntries(tt.entries)

			s := f.open(t, tt.query)
			ids, cursor := s.history(t)
			assert.Equal(t, tt.want, ids)
			assert.Equal(t, uint64(tt.entries), cursor)

			f.addEntries(1)
			e := s.next(t)
			require.Equal(t, EventLog, e.Type)
			assert.Equal(t, uint64(tt.entries+1), e.Log.ID)
			assert.NotContains(t, ids, e.Log.ID, "no duplicate IDs")
		})
	}
}

func seq(from, to uint64) []uint64 {
	var ids []uint64
	for id := from; id <= to; id++ {
		ids = append(ids, id)
	}
	return ids
}

func TestControlAPI_EventsNoGapUnderLoad(t *testing.T) {
	const total = 500
	f := newEventsFixture(t, 0, nil)

	go func() {
		f.addEntries(100)
		// Keep adding while the stream subscribes and reads its history,
		// which is the window a gap or a duplicate would come from.
		for !f.hub.Watching() {
			runtime.Gosched()
		}
		for range total - 100 {
			f.addEntries(1)
			time.Sleep(20 * time.Microsecond)
		}
	}()

	s := f.open(t, "?after=0")
	var ids []uint64
	var cursor uint64
	for len(ids) == 0 || ids[len(ids)-1] < total {
		e := s.next(t)
		switch e.Type {
		case EventLog:
			ids = append(ids, e.Log.ID)
		case EventCursor:
			cursor = e.Cursor
		}
	}
	assert.Equal(t, seq(1, total), ids)
	assert.GreaterOrEqual(t, cursor, uint64(100))
	t.Logf("history boundary at %d", cursor)
}

func TestControlAPI_EventsCursor(t *testing.T) {
	f := newEventsFixture(t, 0, nil)
	f.addEntries(100)

	s := f.open(t, "")
	ids, cursor := s.history(t)
	assert.Empty(t, ids)
	assert.Equal(t, uint64(100), cursor)
	f.addEntries(1)
	e := s.next(t)
	require.Equal(t, EventLog, e.Type)
	assert.Equal(t, uint64(101), e.Log.ID)

	ids, cursor = f.open(t, "?after=98").history(t)
	assert.Equal(t, []uint64{99, 100, 101}, ids)
	assert.Equal(t, uint64(101), cursor)
}

func TestControlAPI_EventsSnapshotsOpenHolds(t *testing.T) {
	f := newEventsFixture(t, 10*time.Second, nil)
	f.watch(t)

	ended := proxyKey("ended.com")
	f.startHold(t, ended)
	f.post(t, "/deny", `{"source":"proxy","target":"ended.com:443"}`)
	f.startHold(t, proxyKey("open.com"))
	f.addEntries(30)

	s := f.open(t, "")
	ids, _ := s.history(t)
	assert.Empty(t, ids)

	e := s.next(t)
	require.Equal(t, EventHoldOpen, e.Type)
	assert.Equal(t, "open.com", e.Log.Domain)
	assert.Equal(t, ActionHold, e.Log.Action)
	assert.False(t, e.Log.Deadline.IsZero())

	// The next event is live: the ended hold isn't in the snapshot.
	f.addEntries(1)
	e = s.next(t)
	require.Equal(t, EventLog, e.Type)
	assert.Equal(t, ActionBlock, e.Log.Action)
}

func TestControlAPI_EventsReplayHoldOutcome(t *testing.T) {
	f := newEventsFixture(t, 10*time.Second, nil)
	f.watch(t)
	ch := f.startHold(t, proxyKey("a.com"))
	f.post(t, "/deny", `{"source":"proxy","target":"a.com:443"}`)
	require.Equal(t, HoldDenied, recvOutcome(t, ch, time.Second))

	s := f.open(t, "?after=0")
	e := s.next(t)
	require.Equal(t, EventLog, e.Type)
	assert.Equal(t, ActionHold, e.Log.Action)
	assert.Equal(t, HoldDenied, e.Log.Outcome)
}

func TestResponseState_SupportsResponseController(t *testing.T) {
	api := NewControlAPI(NewLogBuffer(10), nil, nil, nil)
	results := make(chan error, 2)
	api.mux.HandleFunc("GET /flush", func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		results <- rc.SetWriteDeadline(time.Now().Add(time.Second))
		w.Write([]byte("flushed"))
		results <- rc.Flush()
		// Flushing sent the response, so recovery must not append to it.
		panic("after flush")
	})
	srv := httptest.NewServer(api)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/flush")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.NoError(t, <-results, "SetWriteDeadline")
	assert.NoError(t, <-results, "Flush")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, "flushed", string(body))
}

func TestControlAPI_EventsOutlivesServerTimeouts(t *testing.T) {
	saved := eventsKeepalive
	eventsKeepalive = 50 * time.Millisecond
	t.Cleanup(func() { eventsKeepalive = saved })

	f := newEventsFixture(t, 0, func(s *http.Server) {
		s.ReadTimeout = 200 * time.Millisecond
		s.WriteTimeout = 200 * time.Millisecond
	})
	s := f.open(t, "")
	s.history(t)

	time.Sleep(time.Second)
	f.addEntries(1)
	e := s.next(t)
	require.Equal(t, EventLog, e.Type)
	assert.Equal(t, uint64(1), e.Log.ID)
}

func TestControlAPI_AllowHTTPEndsMatchingHolds(t *testing.T) {
	f := newEventsFixture(t, 10*time.Second, nil)
	f.watch(t)
	ch := f.startHold(t, proxyKey("api.example.com"))
	other := f.startHold(t, proxyKey("other.org"))

	f.post(t, "/allow-http", `{"entries":["*.example.com:443"]}`)
	assert.Equal(t, HoldAllowed, recvOutcome(t, ch, time.Second))
	assertStillWaiting(t, other)
}

func TestControlAPI_DenyEndsHold(t *testing.T) {
	f := newEventsFixture(t, 10*time.Second, nil)
	f.watch(t)
	ch := f.startHold(t, proxyKey("a.com"))

	f.post(t, "/deny", `{"source":"proxy","target":"a.com:443"}`)
	assert.Equal(t, HoldDenied, recvOutcome(t, ch, time.Second))
}

func TestControlAPI_ClosingStreamEndsHolds(t *testing.T) {
	f := newEventsFixture(t, 10*time.Second, nil)
	s := f.open(t, "")
	s.history(t)
	require.True(t, f.hub.Watching(), "the stream is the watcher")
	ch := f.startHold(t, proxyKey("a.com"))

	require.NoError(t, s.body.Close())
	assert.Equal(t, HoldUnwatched, recvOutcome(t, ch, time.Second))
}
