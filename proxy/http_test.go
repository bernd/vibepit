package proxy

import (
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
		al := NewHTTPAllowlist([]string{"httpbin.org:443"})
		blocker := NewCIDRBlocker(nil)
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

		al := NewHTTPAllowlist([]string{host})
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
		al := NewHTTPAllowlist([]string{"allowed.example.com:443"})
		blocker := NewCIDRBlocker(nil)
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
		al := NewHTTPAllowlist([]string{"httpbin.org:443"})
		blocker := NewCIDRBlocker(nil)
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
		al := NewHTTPAllowlist(nil)
		blocker := NewCIDRBlocker(nil)
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
		al := NewHTTPAllowlist(nil)
		blocker := NewCIDRBlocker(nil)
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
		al := NewHTTPAllowlist([]string{"host.vibepit:" + backendPortStr})
		blocker := NewCIDRBlocker(nil)
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
