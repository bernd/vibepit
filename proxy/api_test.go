package proxy

import (
	"encoding/json"
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
		"allow":    []string{"a.com", "b.com"},
		"dns-only": []string{"c.com"},
	}

	allowlist := NewAllowlist([]string{"a.com", "b.com"})
	api := NewControlAPI(log, mergedConfig, allowlist)

	t.Run("GET /logs returns entries", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/logs", nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}

		var entries []LogEntry
		if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
			t.Fatalf("json decode: %v", err)
		}
		if len(entries) != 2 {
			t.Errorf("got %d entries, want 2", len(entries))
		}
	})

	t.Run("GET /stats returns per-domain counts", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/stats", nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}

		var stats map[string]DomainStats
		if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil {
			t.Fatalf("json decode: %v", err)
		}
		if stats["a.com"].Allowed != 1 {
			t.Errorf("a.com allowed = %d, want 1", stats["a.com"].Allowed)
		}
	})

	t.Run("GET /config returns merged config", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/config", nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
	})

	t.Run("GET /unknown returns 404", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/unknown", nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", w.Code)
		}
	})

	t.Run("POST /allow adds entries to allowlist", func(t *testing.T) {
		body := `{"entries": ["bun.sh:443", "esm.sh"]}`
		req := httptest.NewRequest("POST", "/allow", strings.NewReader(body))
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)

		var resp map[string][]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, []string{"bun.sh:443", "esm.sh"}, resp["added"])

		// Verify the allowlist was actually updated.
		assert.True(t, allowlist.Allows("bun.sh", "443"))
		assert.True(t, allowlist.Allows("esm.sh", "80"))
		assert.False(t, allowlist.Allows("bun.sh", "80"))
	})

	t.Run("POST /allow with empty entries returns 400", func(t *testing.T) {
		body := `{"entries": []}`
		req := httptest.NewRequest("POST", "/allow", strings.NewReader(body))
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("POST /allow with invalid JSON returns 400", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/allow", strings.NewReader("not json"))
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}