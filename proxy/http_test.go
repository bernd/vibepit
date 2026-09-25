package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPProxy(t *testing.T) {
	t.Run("blocks plain HTTP by default", func(t *testing.T) {
		al, err := NewHTTPAllowlist([]string{"httpbin.org:443"})
		require.NoError(t, err)
		blocker := NewCIDRBlocker(nil, nil)
		log := NewLogBuffer(100)
		p := NewHTTPProxy(al, blocker, log, DefaultUpstreamDNS)

		srv := httptest.NewServer(p.Handler())
		defer srv.Close()

		proxyURL, _ := url.Parse(srv.URL)
		client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

		resp, err := client.Get("http://httpbin.org/")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		body, _ := io.ReadAll(resp.Body)
		assert.Contains(t, string(body), "not in the allowlist")

		entries := log.Entries()
		var found bool
		for _, e := range entries {
			if e.Domain == "httpbin.org" && e.Action == ActionBlock && e.Reason == "domain not in allowlist" {
				found = true
				break
			}
		}
		assert.True(t, found, "expected log entry for blocked plain HTTP")
	})

	t.Run("allows plain HTTP when domain:port matches allowlist", func(t *testing.T) {
		// Use a backend that responds to verify the request goes through.
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok"))
		}))
		defer backend.Close()

		backendURL, _ := url.Parse(backend.URL)
		host := backendURL.Host

		al, err := NewHTTPAllowlist([]string{host})
		require.NoError(t, err)
		// Empty blocker so localhost backend isn't blocked by default private CIDRs.
		blocker := &CIDRBlocker{}
		log := NewLogBuffer(100)
		p := NewHTTPProxy(al, blocker, log, DefaultUpstreamDNS)

		srv := httptest.NewServer(p.Handler())
		defer srv.Close()

		proxyURL, _ := url.Parse(srv.URL)
		client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

		resp, err := client.Get("http://" + host + "/")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("blocks disallowed domain for plain HTTP", func(t *testing.T) {
		al, err := NewHTTPAllowlist([]string{"allowed.example.com:443"})
		require.NoError(t, err)
		blocker := NewCIDRBlocker(nil, nil)
		log := NewLogBuffer(100)
		p := NewHTTPProxy(al, blocker, log, DefaultUpstreamDNS)

		srv := httptest.NewServer(p.Handler())
		defer srv.Close()

		proxyURL, _ := url.Parse(srv.URL)
		client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

		resp, err := client.Get("http://evil.com/")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		body, _ := io.ReadAll(resp.Body)
		assert.Contains(t, string(body), "not in the allowlist")
	})

	t.Run("logs blocked request", func(t *testing.T) {
		al, err := NewHTTPAllowlist([]string{"httpbin.org:443"})
		require.NoError(t, err)
		blocker := NewCIDRBlocker(nil, nil)
		log := NewLogBuffer(100)
		p := NewHTTPProxy(al, blocker, log, DefaultUpstreamDNS)

		srv := httptest.NewServer(p.Handler())
		defer srv.Close()

		proxyURL, _ := url.Parse(srv.URL)
		client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

		resp, err := client.Get("http://evil.com/")
		require.NoError(t, err)
		resp.Body.Close()

		entries := log.Entries()
		var found bool
		for _, e := range entries {
			if e.Domain == "evil.com" && e.Action == ActionBlock {
				found = true
				break
			}
		}
		assert.True(t, found, "blocked request not found in log")
	})

	t.Run("blocks when resolver errors with no addresses", func(t *testing.T) {
		al, err := NewHTTPAllowlist([]string{"example.com:443"})
		require.NoError(t, err)
		blocker := NewCIDRBlocker(nil, nil)
		log := NewLogBuffer(100)
		p := NewHTTPProxy(al, blocker, log, DefaultUpstreamDNS)
		p.resolver = &net.Resolver{
			PreferGo: true,
			Dial: func(context.Context, string, string) (net.Conn, error) {
				return nil, errors.New("resolver down")
			},
		}

		result := p.checkRequest("example.com", "443")
		assert.Equal(t, ActionBlock, result.action)
		assert.Equal(t, "DNS resolution failed during CIDR check", result.reason)
	})
}

func TestCheckRequestCause(t *testing.T) {
	failingResolver := &net.Resolver{
		PreferGo: true,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("resolver down")
		},
	}
	tests := []struct {
		name     string
		host     string
		resolver *net.Resolver
		want     Cause
	}{
		{name: "not in allowlist", host: "evil.com", want: CauseAllowlist},
		{name: "blocked IP", host: "10.0.0.1", want: CauseBlockedIP},
		{name: "resolution failed", host: "example.com", resolver: failingResolver, want: CauseResolveFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			al, err := NewHTTPAllowlist([]string{"10.0.0.1:443", "example.com:443"})
			require.NoError(t, err)
			log := NewLogBuffer(10)
			p := NewHTTPProxy(al, NewCIDRBlocker(nil, nil), log, DefaultUpstreamDNS)
			if tt.resolver != nil {
				p.resolver = tt.resolver
			}

			result := p.checkRequest(tt.host, "443")
			assert.Equal(t, ActionBlock, result.action)
			assert.Equal(t, tt.want, result.cause)
			entries := log.Entries()
			require.Len(t, entries, 1)
			assert.Equal(t, tt.want, entries[0].Cause)
		})
	}
}

func TestHTTPProxyBlockMessage(t *testing.T) {
	al, err := NewHTTPAllowlist([]string{"10.0.0.1:80"})
	require.NoError(t, err)
	p := NewHTTPProxy(al, NewCIDRBlocker(nil, nil), NewLogBuffer(10), DefaultUpstreamDNS)
	srv := httptest.NewServer(p.Handler())
	defer srv.Close()
	proxyURL, _ := url.Parse(srv.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

	for _, tt := range []struct{ url, want string }{
		{"http://evil.com/", "not in the allowlist"},
		{"http://10.0.0.1/", "resolves to a blocked IP"},
	} {
		t.Run(tt.url, func(t *testing.T) {
			resp, err := client.Get(tt.url)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			assert.Equal(t, http.StatusForbidden, resp.StatusCode)
			assert.Contains(t, string(body), tt.want)
		})
	}
}

func TestHTTPProxyHostVibepit(t *testing.T) {
	// Backend server that returns "host-service".
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("host-service"))
	}))
	defer backend.Close()

	backendURL, err := url.Parse(backend.URL)
	require.NoError(t, err)

	_, backendPortStr, err := net.SplitHostPort(backendURL.Host)
	require.NoError(t, err)
	backendPortInt, err := strconv.Atoi(backendPortStr)
	require.NoError(t, err)

	t.Run("auto-allows host.vibepit for configured port", func(t *testing.T) {
		al, err := NewHTTPAllowlist(nil)
		require.NoError(t, err)
		blocker := NewCIDRBlocker(nil, nil)
		log := NewLogBuffer(100)
		p := NewHTTPProxy(al, blocker, log, DefaultUpstreamDNS)
		p.SetHostVibepit(backendURL.Host, []int{backendPortInt})

		srv := httptest.NewServer(p.Handler())
		defer srv.Close()

		proxyURL, _ := url.Parse(srv.URL)
		client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

		resp, err := client.Get("http://host.vibepit:" + backendPortStr + "/")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		body, _ := io.ReadAll(resp.Body)
		assert.Equal(t, "host-service", string(body))
	})

	t.Run("blocks host.vibepit for unconfigured port", func(t *testing.T) {
		al, err := NewHTTPAllowlist(nil)
		require.NoError(t, err)
		blocker := NewCIDRBlocker(nil, nil)
		log := NewLogBuffer(100)
		p := NewHTTPProxy(al, blocker, log, DefaultUpstreamDNS)
		p.SetHostVibepit(backendURL.Host, []int{9999})

		srv := httptest.NewServer(p.Handler())
		defer srv.Close()

		proxyURL, _ := url.Parse(srv.URL)
		client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

		resp, err := client.Get("http://host.vibepit:" + backendPortStr + "/")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("host.vibepit allowed via allowlist bypasses CIDR", func(t *testing.T) {
		al, err := NewHTTPAllowlist([]string{"host.vibepit:" + backendPortStr})
		require.NoError(t, err)
		blocker := NewCIDRBlocker(nil, nil)
		log := NewLogBuffer(100)
		p := NewHTTPProxy(al, blocker, log, DefaultUpstreamDNS)
		p.SetHostVibepit(backendURL.Host, nil)

		srv := httptest.NewServer(p.Handler())
		defer srv.Close()

		proxyURL, _ := url.Parse(srv.URL)
		client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

		resp, err := client.Get("http://host.vibepit:" + backendPortStr + "/")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		body, _ := io.ReadAll(resp.Body)
		assert.Equal(t, "host-service", string(body))
	})
}
