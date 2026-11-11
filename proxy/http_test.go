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
	"time"

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

		result := p.checkRequest(context.Background(), "example.com", "443")
		assert.Equal(t, ActionBlock, result.action)
		entries := log.Entries()
		require.NotEmpty(t, entries)
		assert.Equal(t, "DNS resolution failed during CIDR check", entries[len(entries)-1].Reason)
	})
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

func TestCheckRequestBlockCode(t *testing.T) {
	failingResolver := &net.Resolver{
		PreferGo: true,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("resolver down")
		},
	}

	tests := []struct {
		name      string
		allowlist []string
		setup     func(p *HTTPProxy)
		host      string
		port      string
		want      BlockCode
	}{
		{
			name: "not in allowlist",
			host: "evil.com",
			port: "443",
			want: BlockNotAllowlisted,
		},
		{
			name:      "resolver failure",
			allowlist: []string{"example.com:443"},
			setup:     func(p *HTTPProxy) { p.resolver = failingResolver },
			host:      "example.com",
			port:      "443",
			want:      BlockResolutionFailed,
		},
		{
			name:      "blocked CIDR",
			allowlist: []string{"10.0.0.1:443"},
			host:      "10.0.0.1",
			port:      "443",
			want:      BlockCIDR,
		},
		{
			name:  "allowed",
			setup: func(p *HTTPProxy) { p.SetHostVibepit("127.0.0.1:8080", []int{8080}) },
			host:  "host.vibepit",
			port:  "8080",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			al, err := NewHTTPAllowlist(tt.allowlist)
			require.NoError(t, err)
			log := NewLogBuffer(100)
			p := NewHTTPProxy(al, NewCIDRBlocker(nil, nil), log, DefaultUpstreamDNS)
			if tt.setup != nil {
				tt.setup(p)
			}

			result := p.checkRequest(context.Background(), tt.host, tt.port)
			assert.Equal(t, tt.want, result.code)

			entries := log.Entries()
			require.NotEmpty(t, entries)
			assert.Equal(t, tt.want, entries[len(entries)-1].Code)
		})
	}
}

// heldProxy starts an HTTP proxy wired to a holds fixture.
func heldProxy(t *testing.T, timeout time.Duration, watch bool, cidr *CIDRBlocker) (*holdsFixture, *http.Client) {
	t.Helper()
	f := newHoldsFixture(t, timeout, watch)
	p := NewHTTPProxy(f.allowlist, cidr, f.log, DefaultUpstreamDNS)
	p.SetHolds(f.holds)
	srv := httptest.NewServer(p.Handler())
	t.Cleanup(srv.Close)
	proxyURL, _ := url.Parse(srv.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	return f, client
}

type heldResult struct {
	status int
	body   string
	err    error
}

func getAsync(client *http.Client, u string) <-chan heldResult {
	ch := make(chan heldResult, 1)
	go func() {
		resp, err := client.Get(u)
		if err != nil {
			ch <- heldResult{err: err}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		ch <- heldResult{status: resp.StatusCode, body: string(b)}
	}()
	return ch
}

func waitForHold(t *testing.T, f *holdsFixture) {
	t.Helper()
	require.Eventually(t, func() bool { return len(f.holdEntries()) > 0 }, 2*time.Second, 5*time.Millisecond)
}

func logActions(f *holdsFixture) []Action {
	var actions []Action
	for _, e := range f.log.Entries() {
		actions = append(actions, e.Action)
	}
	return actions
}

func TestHTTPProxy_HeldThenAllowed(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer backend.Close()
	backendURL, _ := url.Parse(backend.URL)

	f, client := heldProxy(t, 5*time.Second, true, NewCIDRBlocker(nil, []string{"127.0.0.0/8"}))
	res := getAsync(client, backend.URL+"/")
	waitForHold(t, f)

	require.NoError(t, f.allowlist.Add([]string{backendURL.Host}))
	f.holds.EndAllowed()

	r := <-res
	require.NoError(t, r.err)
	assert.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, "ok", r.body)
	assert.Equal(t, []Action{ActionHold, ActionAllow}, logActions(f))
}

func TestHTTPProxy_HeldThenCIDRBlocked(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer backend.Close()
	backendURL, _ := url.Parse(backend.URL)

	f, client := heldProxy(t, 5*time.Second, true, NewCIDRBlocker(nil, nil))
	res := getAsync(client, backend.URL+"/")
	waitForHold(t, f)

	require.NoError(t, f.allowlist.Add([]string{backendURL.Host}))
	f.holds.EndAllowed()

	r := <-res
	require.NoError(t, r.err)
	assert.Equal(t, http.StatusForbidden, r.status)
	assert.Contains(t, r.body, "resolves to a blocked IP")
	entries := f.log.Entries()
	require.Len(t, entries, 2)
	assert.Equal(t, ActionHold, entries[0].Action)
	assert.Equal(t, ActionBlock, entries[1].Action)
	assert.Equal(t, BlockCIDR, entries[1].Code)
}

func TestHTTPProxy_HeldCONNECTDenied(t *testing.T) {
	f, client := heldProxy(t, 5*time.Second, true, NewCIDRBlocker(nil, nil))
	res := getAsync(client, "https://denied.example:443/")
	waitForHold(t, f)

	target := Target{Source: SourceProxy, Host: "denied.example", Port: "443"}
	f.denied.Add(target)
	f.holds.End(target, HoldDenied)

	r := <-res
	require.Error(t, r.err)
	entries := f.log.Entries()
	require.Len(t, entries, 2)
	assert.Equal(t, ActionHold, entries[0].Action)
	assert.Equal(t, ActionBlock, entries[1].Action)
	assert.Equal(t, BlockNotAllowlisted, entries[1].Code)
	assert.Equal(t, entries[0].ID, entries[1].HoldID)
}

func TestHTTPProxy_HoldTimesOut(t *testing.T) {
	f, client := heldProxy(t, 200*time.Millisecond, true, NewCIDRBlocker(nil, nil))
	start := time.Now()
	r := <-getAsync(client, "http://slow.example/")
	require.NoError(t, r.err)
	assert.GreaterOrEqual(t, time.Since(start), 200*time.Millisecond)
	assert.Equal(t, http.StatusForbidden, r.status)
	assert.Contains(t, r.body, "not in the allowlist")
	assert.Equal(t, []Action{ActionHold, ActionBlock}, logActions(f))
	entries := f.log.Entries()
	assert.Equal(t, entries[0].ID, entries[1].HoldID, "the block names the hold it ended")
}

func TestHTTPProxy_NoWatcherBlocksImmediately(t *testing.T) {
	f, client := heldProxy(t, 5*time.Second, false, NewCIDRBlocker(nil, nil))
	start := time.Now()
	r := <-getAsync(client, "http://nobody.example/")
	require.NoError(t, r.err)
	assert.Less(t, time.Since(start), 100*time.Millisecond)
	assert.Equal(t, http.StatusForbidden, r.status)
	assert.Equal(t, []Action{ActionBlock}, logActions(f))
	assert.Zero(t, f.log.Entries()[0].HoldID)
}

func TestHTTPProxy_AbandonedHoldNotLogged(t *testing.T) {
	f, client := heldProxy(t, 5*time.Second, true, NewCIDRBlocker(nil, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://gone.example/", nil)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		done <- err
	}()
	waitForHold(t, f)
	cancel()
	require.Error(t, <-done)

	timeout := time.After(2 * time.Second)
	for {
		select {
		case e := <-f.events:
			if e.Type == EventHoldEnd {
				assert.Equal(t, HoldAbandoned, e.HoldEnd.Outcome)
				assert.Equal(t, []Action{ActionHold}, logActions(f))
				return
			}
		case <-timeout:
			require.FailNow(t, "no abandoned hold-end event")
		}
	}
}
