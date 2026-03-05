package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

	allowlist, err := NewHTTPAllowlist([]string{"a.com:443", "b.com:443"})
	require.NoError(t, err)
	dnsAllowlist, err := NewDNSAllowlist([]string{"c.com"})
	require.NoError(t, err)
	api := NewControlAPI(log, mergedConfig, allowlist, dnsAllowlist)

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

	t.Run("GET /logs with nil URL returns all entries", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/logs", nil)
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
		req := httptest.NewRequest("GET", "/panic", nil)
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
