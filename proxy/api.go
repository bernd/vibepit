package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
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

func (a *ControlAPI) handleLogs(w http.ResponseWriter, r *http.Request) {
	var afterID uint64
	if r.URL != nil {
		if s := r.URL.Query().Get("after"); s != "" {
			afterID, _ = strconv.ParseUint(s, 10, 64)
		}
	}
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
