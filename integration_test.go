//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/bernd/vibepit/proxy"
)

// TestProxyServerIntegration starts the proxy server and validates filtering.
// Run with: go test -tags=integration -v -run TestProxyServerIntegration
func TestProxyServerIntegration(t *testing.T) {
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

	resp, err := http.Get("http://localhost:3129/config")
	if err != nil {
		t.Fatalf("control API request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("control API status = %d, want 200", resp.StatusCode)
	}

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
		},
	}
	os.Setenv("HTTP_PROXY", "http://localhost:3128")
	defer os.Unsetenv("HTTP_PROXY")

	blockedResp, err := client.Get("http://evil.com/")
	if err != nil {
		t.Fatalf("blocked request: %v", err)
	}
	defer blockedResp.Body.Close()

	if blockedResp.StatusCode != http.StatusForbidden {
		t.Errorf("blocked status = %d, want 403", blockedResp.StatusCode)
	}
}
