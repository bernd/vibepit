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

	httpAL, err := proxy.NewHTTPAllowlist(nil)
	require.NoError(t, err)
	dnsAL, err := proxy.NewDNSAllowlist(nil)
	require.NoError(t, err)
	api := proxy.NewControlAPI(log, nil, httpAL, dnsAL)
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
		httpAL2, err := proxy.NewHTTPAllowlist(nil)
		require.NoError(t, err)
		dnsAL2, err := proxy.NewDNSAllowlist(nil)
		require.NoError(t, err)
		emptyAPI := proxy.NewControlAPI(proxy.NewLogBuffer(100), nil, httpAL2, dnsAL2)
		c := testControlClient(t, emptyAPI)

		entries, err := c.Logs()
		require.NoError(t, err)
		assert.Empty(t, entries)
	})
}

func TestControlClient_LogsAfter(t *testing.T) {
	log := proxy.NewLogBuffer(100)
	for range 30 {
		log.Add(proxy.LogEntry{Domain: "a.com", Action: proxy.ActionAllow, Source: proxy.SourceProxy})
	}

	httpAL, err := proxy.NewHTTPAllowlist(nil)
	require.NoError(t, err)
	dnsAL, err := proxy.NewDNSAllowlist(nil)
	require.NoError(t, err)
	api := proxy.NewControlAPI(log, nil, httpAL, dnsAL)
	client := testControlClient(t, api)

	t.Run("returns last 25 entries for initial request", func(t *testing.T) {
		entries, err := client.LogsAfter(0)
		require.NoError(t, err)
		require.Len(t, entries, 25)
		assert.Equal(t, uint64(6), entries[0].ID)
		assert.Equal(t, uint64(30), entries[24].ID)
	})

	t.Run("returns only new entries after cursor", func(t *testing.T) {
		entries, err := client.LogsAfter(28)
		require.NoError(t, err)
		require.Len(t, entries, 2)
		assert.Equal(t, uint64(29), entries[0].ID)
		assert.Equal(t, uint64(30), entries[1].ID)
	})

	t.Run("returns empty when cursor is current", func(t *testing.T) {
		entries, err := client.LogsAfter(30)
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

	httpAL, err := proxy.NewHTTPAllowlist(nil)
	require.NoError(t, err)
	dnsAL, err := proxy.NewDNSAllowlist(nil)
	require.NoError(t, err)
	api := proxy.NewControlAPI(log, nil, httpAL, dnsAL)
	client := testControlClient(t, api)

	stats, err := client.Stats()
	require.NoError(t, err)
	assert.Equal(t, proxy.DomainStats{Allowed: 2, Blocked: 1}, stats["a.com"])
	assert.Equal(t, proxy.DomainStats{Allowed: 0, Blocked: 1}, stats["b.com"])
}

func TestControlClient_Config(t *testing.T) {
	merged := config.MergedConfig{
		AllowHTTP: []string{"a.com:443", "b.com:443"},
		AllowDNS:  []string{"c.com"},
		BlockCIDR: []string{"10.0.0.0/8"},
	}

	httpAL, err := proxy.NewHTTPAllowlist(nil)
	require.NoError(t, err)
	dnsAL, err := proxy.NewDNSAllowlist(nil)
	require.NoError(t, err)
	api := proxy.NewControlAPI(proxy.NewLogBuffer(100), merged, httpAL, dnsAL)
	client := testControlClient(t, api)

	cfg, err := client.Config()
	require.NoError(t, err)
	assert.Equal(t, []string{"a.com:443", "b.com:443"}, cfg.AllowHTTP)
	assert.Equal(t, []string{"c.com"}, cfg.AllowDNS)
	assert.Equal(t, []string{"10.0.0.0/8"}, cfg.BlockCIDR)
}

func TestControlClient_AllowHTTP(t *testing.T) {
	allowlist, err := proxy.NewHTTPAllowlist([]string{"existing.com:443"})
	require.NoError(t, err)
	dnsAL, err := proxy.NewDNSAllowlist(nil)
	require.NoError(t, err)
	api := proxy.NewControlAPI(proxy.NewLogBuffer(100), nil, allowlist, dnsAL)
	client := testControlClient(t, api)

	t.Run("adds entries and returns them", func(t *testing.T) {
		added, err := client.AllowHTTP([]string{"new.com:443", "other.com:8080"})
		require.NoError(t, err)
		assert.Equal(t, []string{"new.com:443", "other.com:8080"}, added)
	})

	t.Run("allowlist is updated on the server", func(t *testing.T) {
		assert.True(t, allowlist.Allows("new.com", "443"))
		assert.True(t, allowlist.Allows("other.com", "8080"))
	})

	t.Run("malformed entries return error and are not added", func(t *testing.T) {
		_, err := client.AllowHTTP([]string{"github.com"})
		require.Error(t, err)
		assert.ErrorContains(t, err, "400")
		assert.False(t, allowlist.Allows("github.com", "443"))
	})
}

func TestControlClient_AllowDNS(t *testing.T) {
	dnsAllowlist, err := proxy.NewDNSAllowlist([]string{"existing.com"})
	require.NoError(t, err)
	httpAL, err := proxy.NewHTTPAllowlist(nil)
	require.NoError(t, err)
	api := proxy.NewControlAPI(proxy.NewLogBuffer(100), nil, httpAL, dnsAllowlist)
	client := testControlClient(t, api)

	t.Run("adds entries and returns them", func(t *testing.T) {
		added, err := client.AllowDNS([]string{"internal.example.com", "*.svc.local"})
		require.NoError(t, err)
		assert.Equal(t, []string{"internal.example.com", "*.svc.local"}, added)
	})

	t.Run("dns allowlist is updated on the server", func(t *testing.T) {
		assert.True(t, dnsAllowlist.Allows("internal.example.com"))
		assert.True(t, dnsAllowlist.Allows("api.svc.local"))
		assert.False(t, dnsAllowlist.Allows("svc.local"))
	})

	t.Run("malformed entries return error and are not added", func(t *testing.T) {
		_, err := client.AllowDNS([]string{"github.com:443"})
		require.Error(t, err)
		assert.ErrorContains(t, err, "400")
		assert.False(t, dnsAllowlist.Allows("github.com"))
	})
}

func TestControlClient_Close(t *testing.T) {
	log := proxy.NewLogBuffer(100)
	api := proxy.NewControlAPI(log, nil, nil, nil)
	client := testControlClient(t, api)

	// Should not panic and should be safe to call multiple times.
	client.Close()
	client.Close()
}

func TestControlClient_ServerError(t *testing.T) {
	log := proxy.NewLogBuffer(100)
	allowlist, err := proxy.NewHTTPAllowlist(nil)
	require.NoError(t, err)
	dnsAL, err := proxy.NewDNSAllowlist(nil)
	require.NoError(t, err)
	api := proxy.NewControlAPI(log, nil, allowlist, dnsAL)
	client := testControlClient(t, api)

	t.Run("GET non-existent path returns error", func(t *testing.T) {
		err := client.get("/nonexistent", &struct{}{})
		assert.ErrorContains(t, err, "404")
	})

	t.Run("POST /allow-http with empty entries returns error", func(t *testing.T) {
		_, err := client.AllowHTTP([]string{})
		assert.Error(t, err)
		assert.ErrorContains(t, err, "400")
	})

	t.Run("POST /allow-dns with empty entries returns error", func(t *testing.T) {
		_, err := client.AllowDNS([]string{})
		assert.Error(t, err)
		assert.ErrorContains(t, err, "400")
	})
}
