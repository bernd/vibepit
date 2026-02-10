//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
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

func controlAPIPostJSON(t *testing.T, client *http.Client, url string, body string) *http.Response {
	t.Helper()

	resp, err := client.Post(url, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("control API POST request: %v", err)
	}
	return resp
}

func mustGetFreePort(t *testing.T) int {
	t.Helper()

	for i := 0; i < 10; i++ {
		tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("allocate free TCP port: %v", err)
		}
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
	if err != nil {
		t.Fatalf("GenerateMTLSCredentials: %v", err)
	}

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

	resp, err := tlsClient.Get(fmt.Sprintf("https://127.0.0.1:%d/config", controlPort))
	if err != nil {
		t.Fatalf("control API request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("control API status = %d, want 200", resp.StatusCode)
	}

	allowHTTPResp := controlAPIPostJSON(t, tlsClient, fmt.Sprintf("https://127.0.0.1:%d/allow-http", controlPort), `{"entries":["added-http.example:80"]}`)
	defer allowHTTPResp.Body.Close()
	if allowHTTPResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(allowHTTPResp.Body)
		t.Errorf("control API allow-http status = %d, want 200 (body: %s)", allowHTTPResp.StatusCode, string(body))
	}

	allowHTTPBadResp := controlAPIPostJSON(t, tlsClient, fmt.Sprintf("https://127.0.0.1:%d/allow-http", controlPort), `{"entries":["added-http.example"]}`)
	defer allowHTTPBadResp.Body.Close()
	if allowHTTPBadResp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(allowHTTPBadResp.Body)
		t.Errorf("control API allow-http malformed status = %d, want 400 (body: %s)", allowHTTPBadResp.StatusCode, string(body))
	}

	allowDNSResp := controlAPIPostJSON(t, tlsClient, fmt.Sprintf("https://127.0.0.1:%d/allow-dns", controlPort), `{"entries":["internal.example.com"]}`)
	defer allowDNSResp.Body.Close()
	if allowDNSResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(allowDNSResp.Body)
		t.Errorf("control API allow-dns status = %d, want 200 (body: %s)", allowDNSResp.StatusCode, string(body))
	}

	allowDNSBadResp := controlAPIPostJSON(t, tlsClient, fmt.Sprintf("https://127.0.0.1:%d/allow-dns", controlPort), `{"entries":["internal.example.com:443"]}`)
	defer allowDNSBadResp.Body.Close()
	if allowDNSBadResp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(allowDNSBadResp.Body)
		t.Errorf("control API allow-dns malformed status = %d, want 400 (body: %s)", allowDNSBadResp.StatusCode, string(body))
	}

	// Use the HTTP proxy to verify blocked requests.
	proxyClient := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(mustParseURL(fmt.Sprintf("http://localhost:%d", proxyPort))),
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
