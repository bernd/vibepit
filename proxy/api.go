package proxy

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// ControlAPI serves proxy status and configuration over HTTP.
type ControlAPI struct {
	mux       *http.ServeMux
	log       *LogBuffer
	config    any
	allowlist *Allowlist
}

func NewControlAPI(log *LogBuffer, config any, allowlist *Allowlist) *ControlAPI {
	api := &ControlAPI{
		mux:       http.NewServeMux(),
		log:       log,
		config:    config,
		allowlist: allowlist,
	}
	api.mux.HandleFunc("GET /logs", api.handleLogs)
	api.mux.HandleFunc("GET /stats", api.handleStats)
	api.mux.HandleFunc("GET /config", api.handleConfig)
	api.mux.HandleFunc("POST /allow", api.handleAllow)
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

func (a *ControlAPI) handleAllow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Entries []string `json:"entries"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}
	if len(req.Entries) == 0 {
		http.Error(w, `{"error":"entries required"}`, http.StatusBadRequest)
		return
	}
	a.allowlist.Add(req.Entries)
	writeJSON(w, map[string]any{"added": req.Entries})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
