//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/bernd/vibepit/proxy"
	"github.com/nats-io/nats.go"
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
	t.Setenv(proxy.EnvProxyInternalCert, string(creds.InternalClientCertPEM()))
	t.Setenv(proxy.EnvProxyInternalKey, string(creds.InternalClientKeyPEM()))

	proxyPort := mustGetFreePort(t)
	controlPort := mustGetFreePort(t)
	dnsPort := mustGetFreePort(t)

	cfg := proxy.ProxyConfig{
		AllowHTTP:      []string{"httpbin.org:443", "example.com:443"},
		AllowDNS:       []string{"dns-only.example.com"},
		Upstream:       "8.8.8.8:53",
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

	// Connect to the NATS control bus with the user client cert.
	clientTLS, err := creds.ClientTLSConfig()
	require.NoError(t, err, "ClientTLSConfig")
	nc, err := nats.Connect(
		fmt.Sprintf("tls://127.0.0.1:%d", controlPort),
		nats.Secure(clientTLS),
		nats.TLSHandshakeFirst(),
		nats.Timeout(5*time.Second),
	)
	require.NoError(t, err, "connect control bus")
	defer nc.Close()

	type addedResp struct {
		Added []string `json:"added"`
	}

	// config returns successfully and reflects the seeded ProxyConfig.
	cfgMsg, err := nc.Request(proxy.SubjectConfig, []byte("{}"), 5*time.Second)
	require.NoError(t, err, "config request")
	assert.Empty(t, cfgMsg.Header.Get("Nats-Service-Error-Code"), "config error header")
	var gotCfg proxy.ProxyConfig
	require.NoError(t, json.Unmarshal(cfgMsg.Data, &gotCfg), "decode config reply")
	assert.Equal(t, controlPort, gotCfg.ControlAPIPort, "config control port")
	assert.Equal(t, proxyPort, gotCfg.ProxyPort, "config proxy port")
	assert.Contains(t, gotCfg.AllowHTTP, "httpbin.org:443", "config seeded allow-http")
	assert.Contains(t, gotCfg.AllowDNS, "dns-only.example.com", "config seeded allow-dns")

	// allow-http: valid entry succeeds and echoes the added entry.
	okMsg, err := nc.Request(proxy.SubjectAllowHTTP, []byte(`{"entries":["added-http.example:80"]}`), 5*time.Second)
	require.NoError(t, err, "allow-http request")
	assert.Empty(t, okMsg.Header.Get("Nats-Service-Error-Code"), "allow-http should succeed")
	var okHTTP addedResp
	require.NoError(t, json.Unmarshal(okMsg.Data, &okHTTP), "decode allow-http reply")
	assert.Equal(t, []string{"added-http.example:80"}, okHTTP.Added, "allow-http added entries")

	// allow-http: malformed entry (missing port) returns a service error with no body.
	badMsg, err := nc.Request(proxy.SubjectAllowHTTP, []byte(`{"entries":["added-http.example"]}`), 5*time.Second)
	require.NoError(t, err, "allow-http malformed request")
	assert.NotEmpty(t, badMsg.Header.Get("Nats-Service-Error-Code"), "allow-http malformed should error")
	assert.Empty(t, badMsg.Data, "allow-http malformed reply body")

	// allow-dns: valid entry succeeds and echoes the added entry.
	okDNS, err := nc.Request(proxy.SubjectAllowDNS, []byte(`{"entries":["internal.example.com"]}`), 5*time.Second)
	require.NoError(t, err, "allow-dns request")
	assert.Empty(t, okDNS.Header.Get("Nats-Service-Error-Code"), "allow-dns should succeed")
	var okDNSResp addedResp
	require.NoError(t, json.Unmarshal(okDNS.Data, &okDNSResp), "decode allow-dns reply")
	assert.Equal(t, []string{"internal.example.com"}, okDNSResp.Added, "allow-dns added entries")

	// allow-dns: malformed entry (port not allowed for DNS) returns a service error with no body.
	badDNS, err := nc.Request(proxy.SubjectAllowDNS, []byte(`{"entries":["internal.example.com:443"]}`), 5*time.Second)
	require.NoError(t, err, "allow-dns malformed request")
	assert.NotEmpty(t, badDNS.Header.Get("Nats-Service-Error-Code"), "allow-dns malformed should error")
	assert.Empty(t, badDNS.Data, "allow-dns malformed reply body")

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
}
