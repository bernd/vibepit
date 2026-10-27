package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime/debug"
	"strconv"
	"time"
)

// eventsKeepalive is how often an idle /events stream writes a comment. It
// bounds how long a dead client goes unnoticed and lets clients detect a
// dead proxy. A var so tests can shorten it.
var eventsKeepalive = 15 * time.Second

// eventsWriteTimeout drops a stream client that stops reading.
const eventsWriteTimeout = 10 * time.Second

// ControlAPI serves proxy status and configuration over HTTP.
type ControlAPI struct {
	mux           *http.ServeMux
	log           *LogBuffer
	config        any
	httpAllowlist *HTTPAllowlist
	dnsAllowlist  *DNSAllowlist
	denied        DenySet
	hub           *EventHub
	holds         *Holds
}

func NewControlAPI(log *LogBuffer, config any, httpAllowlist *HTTPAllowlist, dnsAllowlist *DNSAllowlist) *ControlAPI {
	api := &ControlAPI{
		mux:           http.NewServeMux(),
		log:           log,
		config:        config,
		httpAllowlist: httpAllowlist,
		dnsAllowlist:  dnsAllowlist,
	}
	api.mux.HandleFunc("GET /logs", api.handleLogs)
	api.mux.HandleFunc("GET /hosts", api.handleHosts)
	api.mux.HandleFunc("GET /config", api.handleConfig)
	api.mux.HandleFunc("POST /allow-http", api.handleAllowHTTP)
	api.mux.HandleFunc("POST /allow-dns", api.handleAllowDNS)
	api.mux.HandleFunc("GET /check", api.handleCheck)
	api.mux.HandleFunc("POST /deny", api.handleDeny)
	api.mux.HandleFunc("GET /events", api.handleEvents)
	return api
}

// SetEvents enables the /events stream and lets allow and deny decisions end
// held requests. Without it, /events answers 404, as from a proxy that
// predates the stream.
func (a *ControlAPI) SetEvents(hub *EventHub, holds *Holds) {
	a.hub = hub
	a.holds = holds
}

// Denied returns the set of targets users denied, which the hold registry
// consults so a denied target is never held.
func (a *ControlAPI) Denied() *DenySet {
	return &a.denied
}

func (a *ControlAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rw := &responseState{ResponseWriter: w}
	defer func() {
		if p := recover(); p != nil {
			fmt.Fprintf(os.Stderr, "control API panic: %v\n%s\n", p, debug.Stack())
			if !rw.written {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprintln(w, `{"error":"internal server error"}`)
			}
		}
	}()
	a.mux.ServeHTTP(rw, r)
}

// responseState wraps http.ResponseWriter to track whether headers/body
// have been sent, so panic recovery can avoid corrupting in-flight responses.
type responseState struct {
	http.ResponseWriter
	written bool
}

func (rw *responseState) WriteHeader(code int) {
	rw.written = true
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseState) Write(b []byte) (int, error) {
	rw.written = true
	return rw.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the connection for deadlines.
func (rw *responseState) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// Flush sends the headers, so it counts as written.
func (rw *responseState) Flush() {
	rw.FlushError() //nolint:errcheck
}

// FlushError is what http.ResponseController prefers over Flush, so stream
// handlers see failed flushes.
func (rw *responseState) FlushError() error {
	rw.written = true
	return http.NewResponseController(rw.ResponseWriter).Flush()
}

// handleLogs returns a recent tail when called without a cursor, and every
// entry with a larger ID for "after=N", including N=0.
func (a *ControlAPI) handleLogs(w http.ResponseWriter, r *http.Request) {
	var q url.Values
	if r.URL != nil {
		q = r.URL.Query()
	}
	if !q.Has("after") {
		writeJSON(w, a.log.Tail(TailSize))
		return
	}
	afterID, _ := strconv.ParseUint(q.Get("after"), 10, 64)
	writeJSON(w, a.log.EntriesAfter(afterID))
}

// handleEvents streams log entries and hold changes. It replays history
// first: "after=N" sends every entry above N (reconnects), "tail=N" the newest
// N (at most TailSize), and neither sends none. A cursor event marks where
// history ends, a snapshot of open holds follows, then live events.
func (a *ControlAPI) handleEvents(w http.ResponseWriter, r *http.Request) {
	if a.hub == nil {
		http.Error(w, `{"error":"events not supported"}`, http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	var after, tail uint64
	var err error
	if q.Has("after") {
		after, err = strconv.ParseUint(q.Get("after"), 10, 64)
	} else if q.Has("tail") {
		tail, err = strconv.ParseUint(q.Get("tail"), 10, 64)
	}
	if err != nil {
		http.Error(w, `{"error":"after and tail must be non-negative integers"}`, http.StatusBadRequest)
		return
	}

	// Subscribe before reading history so nothing logged in between is lost;
	// entries that arrive both ways are skipped by ID below.
	events, unsubscribe := a.hub.Subscribe()
	defer unsubscribe()

	rc := http.NewResponseController(w)
	// The server's ReadTimeout and WriteTimeout would end the stream, so
	// lift the read deadline and give each write its own. Errors only mean
	// the writer can't do deadlines (test recorders). Flushing waits for
	// the end of a batch, so history and bursts go out in few writes.
	rc.SetReadDeadline(time.Time{}) //nolint:errcheck
	send := func(e Event) error {
		rc.SetWriteDeadline(time.Now().Add(eventsWriteTimeout)) //nolint:errcheck
		return WriteEvent(w, e)
	}
	flush := func() error {
		rc.SetWriteDeadline(time.Now().Add(eventsWriteTimeout)) //nolint:errcheck
		return rc.Flush()
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if flush() != nil {
		return
	}

	var history []LogEntry
	var sent uint64
	if q.Has("after") {
		history, sent = a.log.EntriesAfterAt(after)
	} else {
		history, sent = a.log.TailAt(int(min(tail, TailSize)))
	}
	for _, e := range history {
		if send(Event{Type: EventLog, Log: e}) != nil {
			return
		}
	}
	if send(Event{Type: EventCursor, Cursor: sent}) != nil {
		return
	}
	// Open holds are sent whatever the tail size, so a late watcher can
	// still decide them.
	for _, e := range a.holds.Open() {
		if send(Event{Type: EventHoldOpen, Log: e}) != nil {
			return
		}
	}
	if flush() != nil {
		return
	}

	keepalive := time.NewTicker(eventsKeepalive)
	defer keepalive.Stop()
	for {
		select {
		case e, ok := <-events:
			// Send this event and whatever else is already queued.
			for ok {
				if e.Type != EventLog || e.Log.ID > sent {
					if e.Type == EventLog {
						sent = e.Log.ID
					}
					if send(e) != nil {
						return
					}
				}
				if len(events) == 0 {
					break
				}
				e, ok = <-events
			}
			if !ok {
				// Dropped for falling behind; the client reconnects with after.
				return
			}
		case <-keepalive.C:
			rc.SetWriteDeadline(time.Now().Add(eventsWriteTimeout)) //nolint:errcheck
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
		case <-r.Context().Done():
			return
		}
		if flush() != nil {
			return
		}
	}
}

func (a *ControlAPI) handleHosts(w http.ResponseWriter, r *http.Request) {
	hosts := a.log.Hosts()
	for i := range hosts {
		hosts[i].Allowlisted = a.allowlisted(hosts[i].HostKey)
	}
	writeJSON(w, hosts)
}

func (a *ControlAPI) allowlisted(k HostKey) bool {
	if k.Source == SourceDNS {
		return a.dnsAllowlist.Allows(k.Domain)
	}
	return a.httpAllowlist.Allows(k.Domain, k.Port)
}

func (a *ControlAPI) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.config)
}

func (a *ControlAPI) decodeAllowRequest(r *http.Request) ([]string, error) {
	var req struct {
		Entries []string `json:"entries"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, fmt.Errorf(`{"error":"invalid JSON"}`)
	}
	if len(req.Entries) == 0 {
		return nil, fmt.Errorf(`{"error":"entries required"}`)
	}
	return req.Entries, nil
}

func (a *ControlAPI) handleAllowHTTP(w http.ResponseWriter, r *http.Request) {
	entries, err := a.decodeAllowRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.httpAllowlist.Add(entries); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}
	// Only after Add: a hold opening concurrently then either sees the new
	// entries in its own allowlist check or is ended here.
	if a.holds != nil {
		a.holds.EndAllowed()
	}
	writeJSON(w, map[string]any{"added": entries})
}

func (a *ControlAPI) handleAllowDNS(w http.ResponseWriter, r *http.Request) {
	entries, err := a.decodeAllowRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.dnsAllowlist.Add(entries); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"added": entries})
}

// handleCheck reports whether the live allowlist permits a target and whether
// a user denied it. Unlike /config it reflects runtime changes, which lets one
// client notice that another client already decided on a blocked target.
func (a *ControlAPI) handleCheck(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	t, err := ParseTarget(q.Get("source"), q.Get("target"))
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}
	var allowed bool
	switch t.Source {
	case SourceProxy:
		allowed = a.httpAllowlist.Allows(t.Host, t.Port)
	case SourceDNS:
		allowed = a.dnsAllowlist.Allows(t.Host)
	}
	writeJSON(w, map[string]bool{"allowed": allowed, "denied": a.denied.Denied(t)})
}

func (a *ControlAPI) handleDeny(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source string `json:"source"`
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}
	t, err := ParseTarget(req.Source, req.Target)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}
	a.denied.Add(t)
	if a.holds != nil {
		a.holds.End(t, HoldDenied)
	}
	writeJSON(w, map[string]string{"denied": t.String()})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
