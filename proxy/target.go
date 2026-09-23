package proxy

import (
	"fmt"
	"net"
	"strings"
)

// Target is what a user allows or denies: a host and port for proxy
// requests, a bare domain for DNS queries. Its String form is the value used
// in allowlist entries, on the control API, and in prompts.
type Target struct {
	Source Source
	Host   string
	Port   string
}

// Target returns the entry's allow/deny target.
func (e LogEntry) Target() Target {
	return Target{Source: e.Source, Host: e.Domain, Port: e.Port}
}

// String brackets IPv6 hosts so ParseTarget can split the value again.
func (t Target) String() string {
	if t.Source == SourceProxy && t.Port != "" {
		return net.JoinHostPort(t.Host, t.Port)
	}
	return t.Host
}

// ParseTarget is the inverse of String. Proxy targets must carry a port;
// DNS targets are bare domains. Hosts are lowercased to match how the proxy
// logs them.
func ParseTarget(source, s string) (Target, error) {
	if s == "" {
		return Target{}, fmt.Errorf("target required")
	}
	switch Source(source) {
	case SourceProxy:
		host, port, err := net.SplitHostPort(s)
		if err != nil {
			return Target{}, fmt.Errorf("proxy target must be host:port: %w", err)
		}
		return Target{Source: SourceProxy, Host: strings.ToLower(host), Port: port}, nil
	case SourceDNS:
		return Target{Source: SourceDNS, Host: strings.ToLower(s)}, nil
	default:
		return Target{}, fmt.Errorf("source must be proxy or dns, got %q", source)
	}
}
