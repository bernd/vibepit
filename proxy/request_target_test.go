package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The filter must see the port the transport will dial: the scheme's
// default when the request names none, also for "host:" with an empty port.
func TestPlainHTTPChecksDialedPort(t *testing.T) {
	for _, tc := range []struct {
		name, url, allow, deny, wantPort string
	}{
		{"https URL defaults to 443", "https://example.com/", "example.com:80", "example.com:443", "443"},
		{"http empty port defaults to 80", "http://api.example.com:/", "*.example.com:*", "api.example.com:80", "80"},
		{"https empty port defaults to 443", "https://api.example.com:/", "*.example.com:*", "api.example.com:443", "443"},
		{"zero-padded port", "http://api.example.com:0080/", "*.example.com:*", "api.example.com:80", "80"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules, err := NewDenyRules([]string{tc.deny}, nil)
			require.NoError(t, err)
			al, err := NewHTTPAllowlist([]string{tc.allow})
			require.NoError(t, err)
			log := NewLogBuffer(10)
			p := NewHTTPProxy(NewPolicy(al, nil, rules), NewCIDRBlocker(nil, nil), log, "127.0.0.1:1")
			w := httptest.NewRecorder()
			p.Handler().ServeHTTP(w, httptest.NewRequest("GET", tc.url, nil))
			assert.Equal(t, 403, w.Code)
			entries := log.Entries()
			require.Len(t, entries, 1)
			assert.Equal(t, tc.wantPort, entries[0].Port)
			assert.Equal(t, BlockDenied, entries[0].Code)
		})
	}
}

func TestCanonicalTarget(t *testing.T) {
	for _, tc := range []struct {
		in, host, port string
		ok             bool
	}{
		{"Example.COM.", "example.com", "443", true},
		{"example.com:", "example.com", "443", true},
		{"example.com:0443", "example.com", "443", true},
		{"example.com:8080", "example.com", "8080", true},
		{"[::1]:80", "::1", "80", true},
		{"[::1]", "::1", "443", true},
		{"[2001:DB8::1]:", "2001:db8::1", "443", true},
		{"[::ffff:10.0.0.1]:80", "10.0.0.1", "80", true},
		{"[0:0::1]:80", "::1", "80", true},
		{"example.com:+80", "example.com", "+80", false},
		{"example.com:0", "example.com", "0", false},
		{"example.com:65536", "example.com", "65536", false},
		{"example.com:https", "example.com", "https", false},
		{"example.com:80:443", "example.com:80:443", "443", false},
		{"[a.com:80]:443", "a.com:80", "443", false},
		{".:443", "", "443", false},
		{"", "", "443", false},
	} {
		host, port, ok := canonicalTarget(tc.in, "443")
		assert.Equal(t, []any{tc.host, tc.port, tc.ok}, []any{host, port, ok}, tc.in)
	}
}

func TestConnectChecksDialedPort(t *testing.T) {
	rules, _ := NewDenyRules([]string{"api.example.com:443"}, nil)
	al, _ := NewHTTPAllowlist([]string{"*.example.com:*"})
	log := NewLogBuffer(10)
	p := NewHTTPProxy(NewPolicy(al, nil, rules), NewCIDRBlocker(nil, nil), log, "127.0.0.1:1")
	for _, target := range []string{"api.example.com:", "api.example.com:00443", "API.example.com.:443"} {
		req := httptest.NewRequest("CONNECT", "http://"+target, nil)
		req.Host = target
		req.URL.Host = target
		w := httptest.NewRecorder()
		p.Handler().ServeHTTP(w, req)
		assert.NotEqual(t, 200, w.Code, target)
	}
	for _, e := range log.Entries() {
		assert.Equal(t, "api.example.com", e.Domain)
		assert.Equal(t, "443", e.Port)
		assert.Equal(t, BlockDenied, e.Code)
	}
	require.Len(t, log.Entries(), 3)
}

func TestPlainHTTPUnknownSchemeBlocked(t *testing.T) {
	al, _ := NewHTTPAllowlist([]string{"example.com:*"})
	log := NewLogBuffer(10)
	p := NewHTTPProxy(NewPolicy(al, nil, nil), NewCIDRBlocker(nil, nil), log, "127.0.0.1:1")
	w := httptest.NewRecorder()
	p.Handler().ServeHTTP(w, httptest.NewRequest("GET", "ftp://example.com/", nil))
	assert.Equal(t, 403, w.Code)
	assert.Contains(t, w.Body.String(), `scheme "ftp" is not supported`)
	require.Len(t, log.Entries(), 1)
	assert.Equal(t, BlockInvalidTarget, log.Entries()[0].Code)
	assert.False(t, AllowFixes(log.Entries()[0].HostKey(), BlockInvalidTarget))
}

// Allowed requests dial the canonical target and still reach the backend,
// with the client's Host header intact.
func TestPlainHTTPForwardsToCanonicalTarget(t *testing.T) {
	var gotHost string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { gotHost = r.Host }))
	defer backend.Close()
	u, _ := url.Parse(backend.URL)
	al, _ := NewHTTPAllowlist([]string{"127.0.0.1:" + u.Port()})
	p := NewHTTPProxy(NewPolicy(al, nil, nil), NewCIDRBlocker(nil, []string{"127.0.0.0/8"}), NewLogBuffer(10), "127.0.0.1:1")
	proxyServer := httptest.NewServer(p.Handler())
	defer proxyServer.Close()
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(mustURL(t, proxyServer.URL))}}
	resp, err := client.Get("http://127.0.0.1:0" + u.Port() + "/")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "127.0.0.1:0"+u.Port(), gotHost)
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}

// Allow rules parse ports as numbers, as deny rules do: "0443" is port 443,
// whichever way the rule or the target spells it.
func TestAllowRulePortsAreNumeric(t *testing.T) {
	al, err := NewHTTPAllowlist([]string{"a.example.com:0443", "b.example.com:*"})
	require.NoError(t, err)
	require.NoError(t, al.Add([]string{"c.example.com:00080"}))
	for _, tc := range []struct {
		host, port string
		want       bool
	}{
		{"a.example.com", "443", true},
		{"a.example.com", "0443", true},
		{"a.example.com", "4430", false},
		{"b.example.com", "8080", true},
		{"c.example.com", "80", true},
		{"c.example.com", "8", false},
	} {
		assert.Equal(t, tc.want, al.Allows(tc.host, tc.port), "%s:%s", tc.host, tc.port)
	}
}

func TestEntryKey(t *testing.T) {
	for in, want := range map[string]string{
		"Example.com:0443": "example.com:443",
		"example.com:443":  "example.com:443",
		"example.com:000":  "example.com:0",
		"*.Example.com:*":  "*.example.com:*",
		"Example.com":      "example.com",
	} {
		assert.Equal(t, want, EntryKey(in), in)
	}
}

// An allow saved with a padded port admits the real request end to end.
func TestPaddedAllowPortForwards(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer backend.Close()
	u, _ := url.Parse(backend.URL)
	al, _ := NewHTTPAllowlist([]string{"127.0.0.1:00" + u.Port()})
	p := NewHTTPProxy(NewPolicy(al, nil, nil), NewCIDRBlocker(nil, []string{"127.0.0.0/8"}), NewLogBuffer(10), "127.0.0.1:1")
	proxyServer := httptest.NewServer(p.Handler())
	defer proxyServer.Close()
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(mustURL(t, proxyServer.URL))}}
	resp, err := client.Get(backend.URL + "/")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
}
