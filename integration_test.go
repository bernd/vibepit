//go:build integration

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/miekg/dns"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bernd/vibepit/config"
	"github.com/bernd/vibepit/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}

func controlAPIPostJSON(t *testing.T, client *http.Client, url string, body string) *http.Response {
	t.Helper()

	resp, err := client.Post(url, "application/json", bytes.NewBufferString(body))
	require.NoError(t, err, "control API POST request")
	return resp
}

func mustGetFreePort(t *testing.T) int {
	t.Helper()

	for i := 0; i < 10; i++ {
		tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err, "allocate free TCP port")
		port := tcpLn.Addr().(*net.TCPAddr).Port
		_ = tcpLn.Close()

		udpLn, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		_ = udpLn.Close()
		return port
	}

	t.Fatalf("allocate free port for both tcp+udp: exhausted retries")
	return 0
}

// TestProxyServerIntegration starts the proxy server and validates filtering.
// Run with: go test -tags=integration -v -run TestProxyServerIntegration
func TestProxyServerIntegration(t *testing.T) {
	// Generate ephemeral mTLS credentials for the control API.
	creds, err := proxy.GenerateMTLSCredentials(10 * time.Minute)
	require.NoError(t, err, "GenerateMTLSCredentials")

	// Set the env vars required by LoadServerTLSConfigFromEnv.
	t.Setenv(proxy.EnvProxyTLSKey, string(creds.ServerKeyPEM()))
	t.Setenv(proxy.EnvProxyTLSCert, string(creds.ServerCertPEM()))
	t.Setenv(proxy.EnvProxyCACert, string(creds.CACertPEM()))

	proxyPort := mustGetFreePort(t)
	controlPort := mustGetFreePort(t)
	dnsPort := mustGetFreePort(t)

	cfg := proxy.ProxyConfig{
		AllowHTTP:      []string{"httpbin.org:443", "example.com:443"},
		AllowDNS:       []string{"dns-only.example.com"},
		UpstreamDNS:    "8.8.8.8:53",
		ProxyPort:      proxyPort,
		ControlAPIPort: controlPort,
		DNSPort:        dnsPort,
	}
	data, _ := json.Marshal(cfg)
	tmpFile, _ := os.CreateTemp("", "proxy-test-*.json")
	tmpFile.Write(data)
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	srv, err := proxy.NewServer(tmpFile.Name())
	require.NoError(t, err, "NewServer")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.Run(ctx)
	time.Sleep(500 * time.Millisecond)

	// Build an mTLS client for the control API.
	clientTLS, err := creds.ClientTLSConfig()
	require.NoError(t, err, "ClientTLSConfig")
	tlsClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: clientTLS},
	}

	resp, err := tlsClient.Get(fmt.Sprintf("https://127.0.0.1:%d/config", controlPort))
	require.NoError(t, err, "control API request")
	defer resp.Body.Close()

	assert.Equal(t, 200, resp.StatusCode, "control API status")

	allowHTTPResp := controlAPIPostJSON(t, tlsClient, fmt.Sprintf("https://127.0.0.1:%d/allow-http", controlPort), `{"entries":["added-http.example:80"]}`)
	defer allowHTTPResp.Body.Close()
	assert.Equal(t, http.StatusOK, allowHTTPResp.StatusCode, "control API allow-http status")

	allowHTTPBadResp := controlAPIPostJSON(t, tlsClient, fmt.Sprintf("https://127.0.0.1:%d/allow-http", controlPort), `{"entries":["added-http.example"]}`)
	defer allowHTTPBadResp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, allowHTTPBadResp.StatusCode, "control API allow-http malformed status")

	allowDNSResp := controlAPIPostJSON(t, tlsClient, fmt.Sprintf("https://127.0.0.1:%d/allow-dns", controlPort), `{"entries":["internal.example.com"]}`)
	defer allowDNSResp.Body.Close()
	assert.Equal(t, http.StatusOK, allowDNSResp.StatusCode, "control API allow-dns status")

	allowDNSBadResp := controlAPIPostJSON(t, tlsClient, fmt.Sprintf("https://127.0.0.1:%d/allow-dns", controlPort), `{"entries":["internal.example.com:443"]}`)
	defer allowDNSBadResp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, allowDNSBadResp.StatusCode, "control API allow-dns malformed status")

	// Use the HTTP proxy to verify blocked requests.
	proxyClient := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(mustParseURL(fmt.Sprintf("http://localhost:%d", proxyPort))),
		},
	}

	blockedResp, err := proxyClient.Get("http://evil.com/")
	require.NoError(t, err, "blocked request")
	defer blockedResp.Body.Close()

	assert.Equal(t, http.StatusForbidden, blockedResp.StatusCode, "blocked status")

	// A blocked target is neither allowed nor denied until a user decides.
	checkURL := fmt.Sprintf("https://127.0.0.1:%d/check?source=proxy&target=evil.com:80", controlPort)
	checkResp, err := tlsClient.Get(checkURL)
	require.NoError(t, err, "control API check")
	defer checkResp.Body.Close()
	var check struct{ Allowed, Denied bool }
	require.NoError(t, json.NewDecoder(checkResp.Body).Decode(&check))
	assert.Equal(t, struct{ Allowed, Denied bool }{}, check)

	denyResp := controlAPIPostJSON(t, tlsClient, fmt.Sprintf("https://127.0.0.1:%d/deny", controlPort), `{"source":"proxy","target":"evil.com:80"}`)
	defer denyResp.Body.Close()
	assert.Equal(t, http.StatusOK, denyResp.StatusCode, "control API deny status")

	deniedResp, err := tlsClient.Get(checkURL)
	require.NoError(t, err, "control API check after deny")
	defer deniedResp.Body.Close()
	require.NoError(t, json.NewDecoder(deniedResp.Body).Decode(&check))
	assert.True(t, check.Denied, "deny is recorded")
	assert.False(t, check.Allowed, "deny doesn't allow")

	t.Run("held request is released by allow", func(t *testing.T) {
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		defer backend.Close()
		backendHost, backendPort, err := net.SplitHostPort(mustParseURL(backend.URL).Host)
		require.NoError(t, err)

		heldProxyPort := mustGetFreePort(t)
		heldControlPort := mustGetFreePort(t)
		data, err := json.Marshal(proxy.ProxyConfig{
			AllowCIDR:      []string{"127.0.0.0/8"},
			HoldTimeout:    "5s",
			ProxyPort:      heldProxyPort,
			ControlAPIPort: heldControlPort,
			DNSPort:        mustGetFreePort(t),
		})
		require.NoError(t, err)
		cfgPath := t.TempDir() + "/proxy.json"
		require.NoError(t, os.WriteFile(cfgPath, data, 0o600))
		heldSrv, err := proxy.NewServer(cfgPath)
		require.NoError(t, err, "NewServer")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go heldSrv.Run(ctx)

		controlURL := fmt.Sprintf("https://127.0.0.1:%d", heldControlPort)
		require.Eventually(t, func() bool {
			resp, err := tlsClient.Get(controlURL + "/config")
			if err != nil {
				return false
			}
			resp.Body.Close()
			return resp.StatusCode == http.StatusOK
		}, 5*time.Second, 50*time.Millisecond, "proxy control API never came up")

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, controlURL+"/events", nil)
		require.NoError(t, err)
		streamResp, err := tlsClient.Do(req)
		require.NoError(t, err, "open /events")
		defer streamResp.Body.Close()
		require.Equal(t, http.StatusOK, streamResp.StatusCode)

		events := make(chan proxy.Event, 64)
		go func() {
			defer close(events)
			r := bufio.NewReader(streamResp.Body)
			for {
				e, err := proxy.ReadEvent(r)
				if err != nil {
					return
				}
				events <- e
			}
		}()
		nextEvent := func(match func(proxy.Event) bool) proxy.Event {
			t.Helper()
			timeout := time.After(5 * time.Second)
			for {
				select {
				case e, ok := <-events:
					require.True(t, ok, "event stream ended")
					if match(e) {
						return e
					}
				case <-timeout:
					require.FailNow(t, "timed out waiting for an event")
				}
			}
		}
		// The cursor means the stream is subscribed, so the proxy holds.
		nextEvent(func(e proxy.Event) bool { return e.Type == proxy.EventCursor })

		heldProxyClient := &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(mustParseURL(fmt.Sprintf("http://127.0.0.1:%d", heldProxyPort))),
			},
		}
		status := make(chan int, 1)
		go func() {
			resp, err := heldProxyClient.Get(backend.URL)
			if err != nil {
				status <- 0
				return
			}
			resp.Body.Close()
			status <- resp.StatusCode
		}()

		hold := nextEvent(func(e proxy.Event) bool {
			return e.Type == proxy.EventLog && e.Log.Action == proxy.ActionHold
		})
		assert.Equal(t, backendHost, hold.Log.Domain)
		assert.Equal(t, backendPort, hold.Log.Port)

		allowResp := controlAPIPostJSON(t, tlsClient, controlURL+"/allow-http",
			fmt.Sprintf(`{"entries":["%s:%s"]}`, backendHost, backendPort))
		allowResp.Body.Close()
		require.Equal(t, http.StatusOK, allowResp.StatusCode)

		select {
		case code := <-status:
			assert.Equal(t, http.StatusOK, code, "held request goes through once allowed")
		case <-time.After(2 * time.Second):
			require.FailNow(t, "held request didn't return within 2s of the allow")
		}

		end := nextEvent(func(e proxy.Event) bool { return e.Type == proxy.EventHoldEnd })
		assert.Equal(t, hold.Log.ID, end.HoldEnd.ID)
		assert.Equal(t, proxy.HoldAllowed, end.HoldEnd.Outcome)
	})
}

// TestPersistentDenyReloadIntegration follows a deny from the running proxy
// to network.yaml and back: a deny installed and saved the way deny-http
// does it must still block after the proxy restarts from the reloaded config.
func TestPersistentDenyReloadIntegration(t *testing.T) {
	creds, err := proxy.GenerateMTLSCredentials(10 * time.Minute)
	require.NoError(t, err)
	t.Setenv(proxy.EnvProxyTLSKey, string(creds.ServerKeyPEM()))
	t.Setenv(proxy.EnvProxyTLSCert, string(creds.ServerCertPEM()))
	t.Setenv(proxy.EnvProxyCACert, string(creds.CACertPEM()))
	tlsConfig, err := creds.ClientTLSConfig()
	require.NoError(t, err)
	controlClient := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig}, Timeout: 5 * time.Second}

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer backend.Close()
	backendPort := mustParseURL(backend.URL).Port()

	projectDir := t.TempDir()
	projectPath := config.DefaultProjectPath(projectDir)
	require.NoError(t, os.MkdirAll(filepath.Dir(projectPath), 0o700))
	require.NoError(t, os.WriteFile(projectPath, []byte("allow-host-ports:\n  - "+backendPort+"\nallow-dns:\n  - host.vibepit\nhold-timeout: 5s\n"), 0o600))

	type running struct {
		base, dnsAddr string
		client        *http.Client
	}
	// start runs a proxy from the project config as bootstrap does: load,
	// merge, then hand the merged config to the proxy as JSON.
	start := func() running {
		cfg, err := config.Load("", projectPath)
		require.NoError(t, err)
		merged, err := cfg.Merge(nil, nil)
		require.NoError(t, err)
		merged.HostGateway = "127.0.0.1"
		merged.ProxyIP = "127.0.0.1"
		merged.ProxyPort = mustGetFreePort(t)
		merged.ControlAPIPort = mustGetFreePort(t)
		data, err := json.Marshal(merged)
		require.NoError(t, err)
		var pc proxy.ProxyConfig
		require.NoError(t, json.Unmarshal(data, &pc))
		pc.DNSPort = mustGetFreePort(t)
		data, err = json.Marshal(pc)
		require.NoError(t, err)
		configPath := filepath.Join(t.TempDir(), "proxy.json")
		require.NoError(t, os.WriteFile(configPath, data, 0o600))
		server, err := proxy.NewServer(configPath)
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go server.Run(ctx)
		base := fmt.Sprintf("https://127.0.0.1:%d", pc.ControlAPIPort)
		require.Eventually(t, func() bool {
			r, e := controlClient.Get(base + "/config")
			if e != nil {
				return false
			}
			r.Body.Close()
			return r.StatusCode == 200
		}, 3*time.Second, 10*time.Millisecond)
		proxyURL := mustParseURL(fmt.Sprintf("http://127.0.0.1:%d", pc.ProxyPort))
		return running{
			base:    base,
			dnsAddr: fmt.Sprintf("127.0.0.1:%d", pc.DNSPort),
			client:  &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 5 * time.Second},
		}
	}
	get := func(r running, host string) int {
		resp, err := r.client.Get("http://" + host + "/")
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	resolve := func(r running) int {
		query := new(dns.Msg)
		query.SetQuestion("host.vibepit.", dns.TypeA)
		answer, _, err := new(dns.Client).Exchange(query, r.dnsAddr)
		require.NoError(t, err)
		return answer.Rcode
	}
	target := "host.vibepit:" + backendPort

	first := start()
	require.Equal(t, 200, get(first, target), "allowed before the deny")
	require.Equal(t, dns.RcodeSuccess, resolve(first))

	// The stream subscription enables holding: two requests a wildcard deny
	// matches wait in holds, and the runtime deny must end both.
	streamCtx, stopStream := context.WithCancel(context.Background())
	defer stopStream()
	req, err := http.NewRequestWithContext(streamCtx, "GET", first.base+"/events", nil)
	require.NoError(t, err)
	stream, err := controlClient.Do(req)
	require.NoError(t, err)
	defer stream.Body.Close()
	events := make(chan proxy.Event, 32)
	go func() {
		reader := bufio.NewReader(stream.Body)
		for {
			e, err := proxy.ReadEvent(reader)
			if err != nil {
				return
			}
			events <- e
		}
	}()
	results := make(chan int, 2)
	for _, host := range []string{"a.example.com", "b.example.com"} {
		go func() {
			r, e := first.client.Get("http://" + host + "/")
			if e != nil {
				results <- 0
				return
			}
			r.Body.Close()
			results <- r.StatusCode
		}()
	}
	opened := 0
	for opened < 2 {
		select {
		case e := <-events:
			if e.Type == proxy.EventLog && e.Log.Action == proxy.ActionHold {
				opened++
			}
		case <-time.After(2 * time.Second):
			require.FailNow(t, "requests were not held")
		}
	}

	// Install and save the denies as deny-http and deny-dns do.
	httpDenies := []string{target, "*.example.com:80"}
	for _, d := range []struct {
		path    string
		entries []string
		save    func(string, []string) error
	}{
		{"/deny-http", httpDenies, config.AppendDenyHTTP},
		{"/deny-dns", []string{"host.vibepit"}, config.AppendDenyDNS},
	} {
		body, err := json.Marshal(map[string][]string{"entries": d.entries})
		require.NoError(t, err)
		resp := controlAPIPostJSON(t, controlClient, first.base+d.path, string(body))
		resp.Body.Close()
		require.Equal(t, 200, resp.StatusCode)
		require.NoError(t, d.save(projectPath, d.entries))
	}

	for range 2 {
		select {
		case status := <-results:
			require.Equal(t, 403, status)
		case <-time.After(2 * time.Second):
			require.FailNow(t, "denied request remained held")
		}
	}
	ended := 0
	for ended < 2 {
		select {
		case e := <-events:
			if e.Type == proxy.EventHoldEnd {
				require.Equal(t, proxy.HoldDenied, e.HoldEnd.Outcome)
				ended++
			}
		case <-time.After(2 * time.Second):
			require.FailNow(t, "missing denied hold settlement events")
		}
	}
	require.Equal(t, 403, get(first, target), "denied at runtime")
	require.Equal(t, dns.RcodeNameError, resolve(first))
	stopStream()

	saved, err := config.Load("", projectPath)
	require.NoError(t, err)
	require.Equal(t, httpDenies, saved.Project.DenyHTTP)
	require.Equal(t, []string{"host.vibepit"}, saved.Project.DenyDNS)

	// A fresh proxy knows only what network.yaml holds.
	second := start()
	require.Equal(t, 403, get(second, target), "deny survives the restart")
	require.Equal(t, 403, get(second, "a.example.com"), "wildcard deny survives the restart")
	require.Equal(t, dns.RcodeNameError, resolve(second))
}
