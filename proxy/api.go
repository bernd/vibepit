package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// ControlAPI serves proxy status and configuration over HTTP.
type ControlAPI struct {
	mux           *http.ServeMux
	log           *LogBuffer
	telemetry     *TelemetryBuffer
	config        any
	httpAllowlist *HTTPAllowlist
	dnsAllowlist  *DNSAllowlist
}

func NewControlAPI(log *LogBuffer, config any, httpAllowlist *HTTPAllowlist, dnsAllowlist *DNSAllowlist, telemetry *TelemetryBuffer) *ControlAPI {
	api := &ControlAPI{
		mux:           http.NewServeMux(),
		log:           log,
		telemetry:     telemetry,
		config:        config,
		httpAllowlist: httpAllowlist,
		dnsAllowlist:  dnsAllowlist,
	}
	api.mux.HandleFunc("GET /logs", api.handleLogs)
	api.mux.HandleFunc("GET /stats", api.handleStats)
	api.mux.HandleFunc("GET /config", api.handleConfig)
	api.mux.HandleFunc("POST /allow-http", api.handleAllowHTTP)
	api.mux.HandleFunc("POST /allow-dns", api.handleAllowDNS)
	api.mux.HandleFunc("GET /telemetry/events", api.handleTelemetryEvents)
	api.mux.HandleFunc("GET /telemetry/metrics", api.handleTelemetryMetrics)
	return api
}

func (a *ControlAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mux.ServeHTTP(w, r)
}

func (a *ControlAPI) handleLogs(w http.ResponseWriter, r *http.Request) {
	var afterID uint64
	if s := r.URL.Query().Get("after"); s != "" {
		afterID, _ = strconv.ParseUint(s, 10, 64)
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
	if err := ValidateHTTPEntries(entries); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}
	a.httpAllowlist.Add(entries)
	writeJSON(w, map[string]any{"added": entries})
}

func (a *ControlAPI) handleAllowDNS(w http.ResponseWriter, r *http.Request) {
	entries, err := a.decodeAllowRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := ValidateDNSEntries(entries); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}
	a.dnsAllowlist.Add(entries)
	writeJSON(w, map[string]any{"added": entries})
}

func (a *ControlAPI) handleTelemetryEvents(w http.ResponseWriter, r *http.Request) {
	if a.telemetry == nil {
		writeJSON(w, []TelemetryEvent{})
		return
	}
	var afterID uint64
	if s := r.URL.Query().Get("after"); s != "" {
		afterID, _ = strconv.ParseUint(s, 10, 64)
	}
	events := a.telemetry.EventsAfter(afterID)
	if agent := r.URL.Query().Get("agent"); agent != "" {
		filtered := events[:0]
		for _, e := range events {
			if e.Agent == agent {
				filtered = append(filtered, e)
			}
		}
		events = filtered
	}
	writeJSON(w, events)
}

func (a *ControlAPI) handleTelemetryMetrics(w http.ResponseWriter, r *http.Request) {
	if a.telemetry == nil {
		writeJSON(w, []MetricSummary{})
		return
	}
	writeJSON(w, a.telemetry.Metrics())
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
