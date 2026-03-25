package proxy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
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
	SSHForwardAddr string   `json:"ssh-forward-addr,omitempty"`
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
	allowlist, err := NewHTTPAllowlist(s.config.AllowHTTP)
	if err != nil {
		return fmt.Errorf("allow-http: %w", err)
	}
	dnsAllowlist, err := NewDNSAllowlist(s.config.AllowDNS)
	if err != nil {
		return fmt.Errorf("allow-dns: %w", err)
	}
	cidr := NewCIDRBlocker(s.config.BlockCIDR)
	log := NewLogBuffer(LogBufferCapacity)

	httpProxy := NewHTTPProxy(allowlist, cidr, log, s.config.Upstream)
	dnsServer := NewDNSServer(dnsAllowlist, cidr, log, s.config.Upstream)
	controlAPI := NewControlAPI(log, s.config, allowlist, dnsAllowlist)

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

	services := 3
	if s.config.SSHForwardAddr != "" {
		services++
	}
	errCh := make(chan error, services)

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

	if s.config.SSHForwardAddr != "" {
		go func() {
			errCh <- s.runSSHForwarder(s.config.SSHForwardAddr)
		}()
	}

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SSHForwardPort is the port the SSH forwarder listens on inside the proxy container.
const SSHForwardPort = 2222

// runSSHForwarder accepts TCP connections and forwards them to the sandbox SSH server.
func (s *Server) runSSHForwarder(targetAddr string) error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", SSHForwardPort))
	if err != nil {
		return fmt.Errorf("ssh forwarder listen: %w", err)
	}
	fmt.Printf("proxy: SSH forwarder listening on :%d -> %s\n", SSHForwardPort, targetAddr)
	for {
		conn, err := ln.Accept()
		if err != nil {
			return fmt.Errorf("ssh forwarder accept: %w", err)
		}
		go forwardTCP(conn, targetAddr)
	}
}

func forwardTCP(client net.Conn, targetAddr string) {
	defer client.Close()
	target, err := net.Dial("tcp", targetAddr)
	if err != nil {
		return
	}
	defer target.Close()

	done := make(chan struct{}, 2)
	go func() {
		io.Copy(target, client) //nolint:errcheck
		done <- struct{}{}
	}()
	go func() {
		io.Copy(client, target) //nolint:errcheck
		done <- struct{}{}
	}()
	<-done
}

func (s *Server) dnsPort() int {
	if s.config.DNSPort > 0 {
		return s.config.DNSPort
	}
	return DefaultDNSPort
}
