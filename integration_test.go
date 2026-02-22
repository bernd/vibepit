//go:build integration

package main

import (
	"bytes"
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
}
