package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime/debug"
	"strconv"
)

// ControlAPI serves proxy status and configuration over HTTP.
type ControlAPI struct {
	mux           *http.ServeMux
	log           *LogBuffer
	config        any
	httpAllowlist *HTTPAllowlist
	dnsAllowlist  *DNSAllowlist
	denied        DenySet
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
	api.mux.HandleFunc("GET /stats", api.handleStats)
	api.mux.HandleFunc("GET /config", api.handleConfig)
	api.mux.HandleFunc("POST /allow-http", api.handleAllowHTTP)
	api.mux.HandleFunc("POST /allow-dns", api.handleAllowDNS)
	api.mux.HandleFunc("GET /check", api.handleCheck)
	api.mux.HandleFunc("POST /deny", api.handleDeny)
	return api
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

func (a *ControlAPI) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.log.Stats())
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
	writeJSON(w, map[string]string{"denied": t.String()})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
