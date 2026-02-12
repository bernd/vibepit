package proxy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

const (
	DefaultUpstreamDNS = "9.9.9.9:53"
	DefaultDNSPort     = 53
	LogBufferCapacity  = 10000
)

// ProxyConfig is the JSON config file passed to the proxy container.
type ProxyConfig struct {
	AllowHTTP      []string `json:"allow-http"`
	AllowDNS       []string `json:"allow-dns"`
	BlockCIDR      []string `json:"block-cidr"`
	Upstream       string   `json:"upstream"`
	AllowHostPorts []int    `json:"allow-host-ports"`
	ProxyIP        string   `json:"proxy-ip"`
	HostGateway    string   `json:"host-gateway"`
	ProxyPort      int      `json:"proxy-port"`
	ControlAPIPort int      `json:"control-api-port"`
	DNSPort        int      `json:"dns-port"`
	OTLPPort       int      `json:"otlp-port,omitempty"`
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
	allowlist := NewHTTPAllowlist(s.config.AllowHTTP)
	dnsAllowlist := NewDNSAllowlist(s.config.AllowDNS)
	cidr := NewCIDRBlocker(s.config.BlockCIDR)
	log := NewLogBuffer(LogBufferCapacity)
	telemetry := NewTelemetryBuffer(TelemetryBufferCapacity)

	httpProxy := NewHTTPProxy(allowlist, cidr, log, s.config.Upstream)
	dnsServer := NewDNSServer(dnsAllowlist, cidr, log, s.config.Upstream)
	controlAPI := NewControlAPI(log, s.config, allowlist, dnsAllowlist, telemetry)

	// Configure host.vibepit support.
	if proxyIP := net.ParseIP(s.config.ProxyIP); proxyIP != nil {
		dnsServer.SetProxyIP(proxyIP)
	}
	if s.config.HostGateway != "" {
		httpProxy.SetHostVibepit(s.config.HostGateway, s.config.AllowHostPorts)
	}

	proxyAddr := fmt.Sprintf(":%d", s.config.ProxyPort)
	controlAddr := fmt.Sprintf(":%d", s.config.ControlAPIPort)
	dnsAddr := fmt.Sprintf(":%d", s.dnsPort())

	serviceCount := 3
	if s.config.OTLPPort > 0 {
		serviceCount = 4
	}
	errCh := make(chan error, serviceCount)

	go func() {
		fmt.Printf("proxy: HTTP proxy listening on %s\n", proxyAddr)
		errCh <- http.ListenAndServe(proxyAddr, httpProxy.Handler())
	}()

	go func() {
		fmt.Printf("proxy: DNS server listening on %s\n", dnsAddr)
		errCh <- dnsServer.ListenAndServe(dnsAddr)
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

	if s.config.OTLPPort > 0 {
		otlpReceiver := NewOTLPReceiver(telemetry)
		otlpAddr := fmt.Sprintf(":%d", s.config.OTLPPort)
		go func() {
			fmt.Printf("proxy: OTLP receiver listening on %s\n", otlpAddr)
			srv := &http.Server{
				Addr:         otlpAddr,
				Handler:      otlpReceiver,
				ReadTimeout:  30 * time.Second,
				WriteTimeout: 30 * time.Second,
				IdleTimeout:  60 * time.Second,
			}
			errCh <- srv.ListenAndServe()
		}()
	}

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) dnsPort() int {
	if s.config.DNSPort > 0 {
		return s.config.DNSPort
	}
	return DefaultDNSPort
}
