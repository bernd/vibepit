package proxy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
)

const (
	DefaultUpstreamDNS = "9.9.9.9:53"
	DNSPort            = ":53"
	LogBufferCapacity  = 10000
)

// ProxyConfig is the JSON config file passed to the proxy container.
type ProxyConfig struct {
	Allow          []string `json:"allow"`
	DNSOnly        []string `json:"dns-only"`
	BlockCIDR      []string `json:"block-cidr"`
	Upstream       string   `json:"upstream"`
	AllowHTTP      bool     `json:"allow-http"`
	AllowHostPorts []int    `json:"allow-host-ports"`
	ProxyIP        string   `json:"proxy-ip"`
	HostGateway    string   `json:"host-gateway"`
	ProxyPort      int      `json:"proxy-port"`
	ControlAPIPort int      `json:"control-api-port"`
}

// Server runs the HTTP proxy, DNS server, and control API.
type Server struct {
	config ProxyConfig
}

func NewServer(configPath string) (*Server, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg ProxyConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if cfg.Upstream == "" {
		cfg.Upstream = DefaultUpstreamDNS
	}

	return &Server{config: cfg}, nil
}

func (s *Server) Run(ctx context.Context) error {
	allowlist := NewAllowlist(s.config.Allow)
	dnsOnlyList := NewAllowlist(s.config.DNSOnly)
	cidr := NewCIDRBlocker(s.config.BlockCIDR)
	log := NewLogBuffer(LogBufferCapacity)

	httpProxy := NewHTTPProxy(allowlist, cidr, log, s.config.AllowHTTP)
	dnsServer := NewDNSServer(allowlist, dnsOnlyList, cidr, log, s.config.Upstream)
	controlAPI := NewControlAPI(log, s.config, allowlist)

	// Configure host.vibepit support.
	if proxyIP := net.ParseIP(s.config.ProxyIP); proxyIP != nil {
		dnsServer.SetProxyIP(proxyIP)
	}
	if s.config.HostGateway != "" {
		httpProxy.SetHostVibepit(s.config.HostGateway, s.config.AllowHostPorts)
	}

	proxyAddr := fmt.Sprintf(":%d", s.config.ProxyPort)
	controlAddr := fmt.Sprintf(":%d", s.config.ControlAPIPort)

	errCh := make(chan error, 3)

	go func() {
		fmt.Printf("proxy: HTTP proxy listening on %s\n", proxyAddr)
		errCh <- http.ListenAndServe(proxyAddr, httpProxy.Handler())
	}()

	go func() {
		fmt.Printf("proxy: DNS server listening on %s\n", DNSPort)
		errCh <- dnsServer.ListenAndServe(DNSPort)
	}()

	go func() {
		tlsCfg, err := LoadServerTLSConfigFromEnv()
		if err != nil {
			errCh <- fmt.Errorf("control API TLS: %w", err)
			return
		}
		fmt.Printf("proxy: control API listening on %s (mTLS)\n", controlAddr)
		ln, err := tls.Listen("tcp", controlAddr, tlsCfg)
		if err != nil {
			errCh <- err
			return
		}
		errCh <- http.Serve(ln, controlAPI)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
