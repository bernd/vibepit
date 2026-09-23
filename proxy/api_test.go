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
		req := httptest.NewRequest(http.MethodGet, "/logs", nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var entries []LogEntry
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &entries))
		assert.Len(t, entries, 2)
	})

	t.Run("GET /stats returns per-domain counts", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/stats", nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var stats map[string]DomainStats
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &stats))
		assert.Equal(t, 1, stats["a.com"].Allowed)
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

func TestControlAPI_LogsSince(t *testing.T) {
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

	assert.Len(t, get("after=0"), 25, "after=0 keeps its tail semantics")
	assert.Len(t, get("since=0"), 30)
	assert.Len(t, get("since=27"), 3)
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
