//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/bernd/vibepit/proxy"
)

func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}

// TestProxyServerIntegration starts the proxy server and validates filtering.
// Run with: go test -tags=integration -v -run TestProxyServerIntegration
func TestProxyServerIntegration(t *testing.T) {
	// Generate ephemeral mTLS credentials for the control API.
	creds, err := proxy.GenerateMTLSCredentials(10 * time.Minute)
	if err != nil {
		t.Fatalf("GenerateMTLSCredentials: %v", err)
	}

	// Set the env vars required by LoadServerTLSConfigFromEnv.
	t.Setenv(proxy.EnvProxyTLSKey, string(creds.ServerKeyPEM()))
	t.Setenv(proxy.EnvProxyTLSCert, string(creds.ServerCertPEM()))
	t.Setenv(proxy.EnvProxyCACert, string(creds.CACertPEM()))

	cfg := proxy.ProxyConfig{
		Allow:    []string{"httpbin.org", "example.com"},
		DNSOnly:  []string{"dns-only.example.com"},
		Upstream: "8.8.8.8:53",
	}
	data, _ := json.Marshal(cfg)
	tmpFile, _ := os.CreateTemp("", "proxy-test-*.json")
	tmpFile.Write(data)
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	srv, err := proxy.NewServer(tmpFile.Name())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.Run(ctx)
	time.Sleep(500 * time.Millisecond)

	// Build an mTLS client for the control API.
	clientTLS, err := creds.ClientTLSConfig()
	if err != nil {
		t.Fatalf("ClientTLSConfig: %v", err)
	}
	tlsClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: clientTLS},
	}

	resp, err := tlsClient.Get("https://127.0.0.1:3129/config")
	if err != nil {
		t.Fatalf("control API request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("control API status = %d, want 200", resp.StatusCode)
	}

	// Use the HTTP proxy to verify blocked requests.
	proxyClient := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(mustParseURL("http://localhost:3128")),
		},
	}

	blockedResp, err := proxyClient.Get("http://evil.com/")
	if err != nil {
		t.Fatalf("blocked request: %v", err)
	}
	defer blockedResp.Body.Close()

	if blockedResp.StatusCode != http.StatusForbidden {
		t.Errorf("blocked status = %d, want 403", blockedResp.StatusCode)
	}
}
