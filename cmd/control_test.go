package cmd

import (
	"net/http/httptest"
	"testing"

	"github.com/bernd/vibepit/config"
	"github.com/bernd/vibepit/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testControlClient(t *testing.T, api *proxy.ControlAPI) *ControlClient {
	t.Helper()
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	return &ControlClient{http: srv.Client(), baseURL: srv.URL}
}

func TestControlClient_Logs(t *testing.T) {
	log := proxy.NewLogBuffer(100)
	log.Add(proxy.LogEntry{Domain: "a.com", Action: proxy.ActionAllow, Source: proxy.SourceProxy})
	log.Add(proxy.LogEntry{Domain: "b.com", Action: proxy.ActionBlock, Source: proxy.SourceDNS})

	api := proxy.NewControlAPI(log, nil, proxy.NewAllowlist(nil))
	client := testControlClient(t, api)

	t.Run("returns all entries", func(t *testing.T) {
		entries, err := client.Logs()
		require.NoError(t, err)
		require.Len(t, entries, 2)
		assert.Equal(t, "a.com", entries[0].Domain)
		assert.Equal(t, proxy.ActionAllow, entries[0].Action)
		assert.Equal(t, "b.com", entries[1].Domain)
		assert.Equal(t, proxy.ActionBlock, entries[1].Action)
	})

	t.Run("returns empty slice when no logs", func(t *testing.T) {
		emptyAPI := proxy.NewControlAPI(proxy.NewLogBuffer(100), nil, proxy.NewAllowlist(nil))
		c := testControlClient(t, emptyAPI)

		entries, err := c.Logs()
		require.NoError(t, err)
		assert.Empty(t, entries)
	})
}

func TestControlClient_Stats(t *testing.T) {
	log := proxy.NewLogBuffer(100)
	log.Add(proxy.LogEntry{Domain: "a.com", Action: proxy.ActionAllow})
	log.Add(proxy.LogEntry{Domain: "a.com", Action: proxy.ActionAllow})
	log.Add(proxy.LogEntry{Domain: "a.com", Action: proxy.ActionBlock})
	log.Add(proxy.LogEntry{Domain: "b.com", Action: proxy.ActionBlock})

	api := proxy.NewControlAPI(log, nil, proxy.NewAllowlist(nil))
	client := testControlClient(t, api)

	stats, err := client.Stats()
	require.NoError(t, err)
	assert.Equal(t, proxy.DomainStats{Allowed: 2, Blocked: 1}, stats["a.com"])
	assert.Equal(t, proxy.DomainStats{Allowed: 0, Blocked: 1}, stats["b.com"])
}

func TestControlClient_Config(t *testing.T) {
	merged := config.MergedConfig{
		Allow:     []string{"a.com", "b.com"},
		DNSOnly:   []string{"c.com"},
		BlockCIDR: []string{"10.0.0.0/8"},
		AllowHTTP: true,
	}

	api := proxy.NewControlAPI(proxy.NewLogBuffer(100), merged, proxy.NewAllowlist(nil))
	client := testControlClient(t, api)

	cfg, err := client.Config()
	require.NoError(t, err)
	assert.Equal(t, []string{"a.com", "b.com"}, cfg.Allow)
	assert.Equal(t, []string{"c.com"}, cfg.DNSOnly)
	assert.Equal(t, []string{"10.0.0.0/8"}, cfg.BlockCIDR)
	assert.True(t, cfg.AllowHTTP)
}

func TestControlClient_Allow(t *testing.T) {
	allowlist := proxy.NewAllowlist([]string{"existing.com"})
	api := proxy.NewControlAPI(proxy.NewLogBuffer(100), nil, allowlist)
	client := testControlClient(t, api)

	t.Run("adds entries and returns them", func(t *testing.T) {
		added, err := client.Allow([]string{"new.com", "other.com:8080"})
		require.NoError(t, err)
		assert.Equal(t, []string{"new.com", "other.com:8080"}, added)
	})

	t.Run("allowlist is updated on the server", func(t *testing.T) {
		assert.True(t, allowlist.Allows("new.com", "443"))
		assert.True(t, allowlist.Allows("other.com", "8080"))
	})
}

func TestControlClient_ServerError(t *testing.T) {
	log := proxy.NewLogBuffer(100)
	allowlist := proxy.NewAllowlist(nil)
	api := proxy.NewControlAPI(log, nil, allowlist)
	client := testControlClient(t, api)

	t.Run("GET non-existent path returns error", func(t *testing.T) {
		err := client.get("/nonexistent", &struct{}{})
		assert.ErrorContains(t, err, "404")
	})

	t.Run("POST /allow with empty entries returns error", func(t *testing.T) {
		_, err := client.Allow([]string{})
		assert.Error(t, err)
		assert.ErrorContains(t, err, "400")
	})
}
