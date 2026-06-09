package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

const (
	DefaultUpstreamDNS = "9.9.9.9:53"
	DefaultDNSPort     = 53
	LogBufferCapacity  = 10000

	httpProxyReadHeaderTimeout = 10 * time.Second
	httpProxyIdleTimeout       = 2 * time.Minute
)

// ProxyConfig is the JSON config file passed to the proxy container.
type ProxyConfig struct {
	AllowHTTP      []string `json:"allow-http"`
	AllowDNS       []string `json:"allow-dns"`
	BlockCIDR      []string `json:"block-cidr"`
	AllowCIDR      []string `json:"allow-cidr"`
	ExtraHosts     []string `json:"extra-hosts,omitempty"`
	UpstreamDNS    string   `json:"upstream-dns"`
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

	if cfg.UpstreamDNS == "" {
		cfg.UpstreamDNS = DefaultUpstreamDNS
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
	cidr := NewCIDRBlocker(s.config.BlockCIDR, s.config.AllowCIDR)

	serverTLS, err := LoadServerTLSConfigFromEnv()
	if err != nil {
		return fmt.Errorf("control bus TLS: %w", err)
	}
	internalTLS, err := LoadInternalClientTLSConfigFromEnv()
	if err != nil {
		return fmt.Errorf("control bus internal TLS: %w", err)
	}
	bus, err := NewBus(BusOptions{
		Port:          s.config.ControlAPIPort,
		ServerTLS:     serverTLS,
		InternalTLS:   internalTLS,
		HTTPAllowlist: allowlist,
		DNSAllowlist:  dnsAllowlist,
		Config:        s.config,
	})
	if err != nil {
		return fmt.Errorf("control bus: %w", err)
	}
	if err := bus.RegisterHandlers(); err != nil {
		bus.Shutdown()
		return fmt.Errorf("register handlers: %w", err)
	}

	pub := bus.LogPublisher()
	httpProxy := NewHTTPProxy(allowlist, cidr, pub, s.config.UpstreamDNS)
	dnsServer := NewDNSServer(dnsAllowlist, cidr, pub, s.config.UpstreamDNS)

	// Configure host.vibepit support.
	if proxyIP := net.ParseIP(s.config.ProxyIP); proxyIP != nil {
		dnsServer.SetProxyIP(proxyIP)
	}
	if s.config.HostGateway != "" {
		httpProxy.SetHostVibepit(s.config.HostGateway, s.config.AllowHostPorts)
	}

	proxyAddr := fmt.Sprintf(":%d", s.config.ProxyPort)
	dnsAddr := fmt.Sprintf(":%d", s.dnsPort())
	proxyServer := &http.Server{
		Addr:              proxyAddr,
		Handler:           httpProxy.Handler(),
		ReadHeaderTimeout: httpProxyReadHeaderTimeout,
		IdleTimeout:       httpProxyIdleTimeout,
	}

	services := 2
	if s.config.SSHForwardAddr != "" {
		services++
	}
	errCh := make(chan error, services)

	go func() {
		fmt.Printf("proxy: HTTP proxy listening on %s\n", proxyAddr)
		if err := proxyServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	go func() {
		fmt.Printf("proxy: DNS server listening on %s\n", dnsAddr)
		errCh <- dnsServer.ListenAndServe(dnsAddr)
	}()

	if s.config.SSHForwardAddr != "" {
		go func() {
			errCh <- s.runSSHForwarder(s.config.SSHForwardAddr)
		}()
	}

	fmt.Printf("proxy: control bus (NATS) listening on :%d\n", s.config.ControlAPIPort)

	select {
	case err := <-errCh:
		bus.Shutdown()
		return err
	case err := <-bus.Fatal():
		bus.Shutdown()
		return fmt.Errorf("control bus: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		proxyServer.Shutdown(shutdownCtx) //nolint:errcheck
		bus.Shutdown()
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
	defer client.Close() //nolint:errcheck
	target, err := net.Dial("tcp", targetAddr)
	if err != nil {
		return
	}
	defer target.Close() //nolint:errcheck

	done := make(chan struct{}, 2)
	go func() {
		io.Copy(target, client) //nolint:errcheck
		// Half-close the write side so the target sees EOF while keeping
		// the read side open for remaining output.
		if tc, ok := target.(*net.TCPConn); ok {
			tc.CloseWrite() //nolint:errcheck
		}
		done <- struct{}{}
	}()
	go func() {
		io.Copy(client, target) //nolint:errcheck
		done <- struct{}{}
	}()
	// Wait for both directions to finish.
	<-done
	<-done
}

func (s *Server) dnsPort() int {
	if s.config.DNSPort > 0 {
		return s.config.DNSPort
	}
	return DefaultDNSPort
}
