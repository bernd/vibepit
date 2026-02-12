package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestControlAPI(t *testing.T) {
	log := NewLogBuffer(100)
	log.Add(LogEntry{Domain: "a.com", Action: ActionAllow, Source: SourceProxy})
	log.Add(LogEntry{Domain: "b.com", Action: ActionBlock, Source: SourceDNS})

	mergedConfig := map[string]any{
		"allow-http": []string{"a.com:443", "b.com:443"},
		"allow-dns":  []string{"c.com"},
	}

	allowlist := NewHTTPAllowlist([]string{"a.com:443", "b.com:443"})
	dnsAllowlist := NewDNSAllowlist([]string{"c.com"})
	telBuf := NewTelemetryBuffer(100)
	api := NewControlAPI(log, mergedConfig, allowlist, dnsAllowlist, telBuf)

	t.Run("GET /logs returns entries", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/logs", nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var entries []LogEntry
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &entries))
		assert.Len(t, entries, 2)
	})

	t.Run("GET /stats returns per-domain counts", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/stats", nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var stats map[string]DomainStats
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &stats))
		assert.Equal(t, 1, stats["a.com"].Allowed)
	})

	t.Run("GET /config returns merged config", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/config", nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("GET /unknown returns 404", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/unknown", nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("POST /allow-http adds entries to allowlist", func(t *testing.T) {
		body := `{"entries": ["bun.sh:443", "esm.sh:*"]}`
		req := httptest.NewRequest("POST", "/allow-http", strings.NewReader(body))
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
		req := httptest.NewRequest("POST", "/allow-http", strings.NewReader(body))
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("POST /allow-http with invalid JSON returns 400", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/allow-http", strings.NewReader("not json"))
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("POST /allow-http with malformed entry returns 400", func(t *testing.T) {
		body := `{"entries": ["github.com"]}`
		req := httptest.NewRequest("POST", "/allow-http", strings.NewReader(body))
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.False(t, allowlist.Allows("github.com", "443"))
	})

	t.Run("POST /allow-dns adds entries to DNS allowlist", func(t *testing.T) {
		body := `{"entries": ["internal.example.com", "*.svc.local"]}`
		req := httptest.NewRequest("POST", "/allow-dns", strings.NewReader(body))
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
		req := httptest.NewRequest("POST", "/allow-dns", strings.NewReader(body))
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.False(t, dnsAllowlist.Allows("github.com"))
	})

	t.Run("GET /telemetry/events returns events", func(t *testing.T) {
		telBuf.AddEvent(TelemetryEvent{Time: time.Now(), Agent: "claude-code", EventName: "tool_result"})
		telBuf.AddEvent(TelemetryEvent{Time: time.Now(), Agent: "codex", EventName: "api_request"})

		req := httptest.NewRequest("GET", "/telemetry/events", nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var events []TelemetryEvent
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &events))
		assert.Len(t, events, 2)
	})

	t.Run("GET /telemetry/events with after param", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/telemetry/events?after=1", nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var events []TelemetryEvent
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &events))
		assert.Len(t, events, 1)
		assert.Equal(t, "codex", events[0].Agent)
	})

	t.Run("GET /telemetry/events with agent filter", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/telemetry/events?agent=claude-code", nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var events []TelemetryEvent
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &events))
		assert.Len(t, events, 1)
		assert.Equal(t, "claude-code", events[0].Agent)
	})

	t.Run("GET /telemetry/metrics returns metrics", func(t *testing.T) {
		telBuf.UpdateMetric(MetricSummary{Name: "tokens", Agent: "claude-code", Value: 42})

		req := httptest.NewRequest("GET", "/telemetry/metrics", nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var metrics []MetricSummary
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &metrics))
		assert.Len(t, metrics, 1)
	})

	t.Run("GET /telemetry/events with nil buffer returns empty array", func(t *testing.T) {
		nilAPI := NewControlAPI(log, mergedConfig, allowlist, dnsAllowlist, nil)
		req := httptest.NewRequest("GET", "/telemetry/events", nil)
		w := httptest.NewRecorder()
		nilAPI.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "[]\n", w.Body.String())
	})

	t.Run("GET /telemetry/metrics with nil buffer returns empty array", func(t *testing.T) {
		nilAPI := NewControlAPI(log, mergedConfig, allowlist, dnsAllowlist, nil)
		req := httptest.NewRequest("GET", "/telemetry/metrics", nil)
		w := httptest.NewRecorder()
		nilAPI.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "[]\n", w.Body.String())
	})
}
