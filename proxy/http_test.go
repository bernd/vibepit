package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPProxy(t *testing.T) {
	t.Run("blocks plain HTTP by default", func(t *testing.T) {
		al := NewAllowlist([]string{"httpbin.org"})
		blocker := NewCIDRBlocker(nil)
		log := NewLogBuffer(100)
		p := NewHTTPProxy(al, blocker, log, false)

		srv := httptest.NewServer(p.Handler())
		defer srv.Close()

		proxyURL, _ := url.Parse(srv.URL)
		client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

		resp, err := client.Get("http://httpbin.org/")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		body, _ := io.ReadAll(resp.Body)
		assert.Contains(t, string(body), "plain HTTP blocked")

		entries := log.Entries()
		var found bool
		for _, e := range entries {
			if e.Domain == "httpbin.org" && e.Action == ActionBlock && e.Reason == "plain HTTP blocked, use HTTPS" {
				found = true
				break
			}
		}
		assert.True(t, found, "expected log entry for blocked plain HTTP")
	})

	t.Run("allows plain HTTP when allowHTTP is true", func(t *testing.T) {
		// Use a backend that responds to verify the request goes through.
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok"))
		}))
		defer backend.Close()

		backendURL, _ := url.Parse(backend.URL)
		host := backendURL.Host

		al := NewAllowlist([]string{host})
		// Empty blocker so localhost backend isn't blocked by default private CIDRs.
		blocker := &CIDRBlocker{}
		log := NewLogBuffer(100)
		p := NewHTTPProxy(al, blocker, log, true)

		srv := httptest.NewServer(p.Handler())
		defer srv.Close()

		proxyURL, _ := url.Parse(srv.URL)
		client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

		resp, err := client.Get("http://" + host + "/")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("blocks disallowed domain even with allowHTTP", func(t *testing.T) {
		al := NewAllowlist([]string{"allowed.example.com"})
		blocker := NewCIDRBlocker(nil)
		log := NewLogBuffer(100)
		p := NewHTTPProxy(al, blocker, log, true)

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
		al := NewAllowlist([]string{"httpbin.org"})
		blocker := NewCIDRBlocker(nil)
		log := NewLogBuffer(100)
		p := NewHTTPProxy(al, blocker, log, false)

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
