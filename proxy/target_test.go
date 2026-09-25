package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTargetString(t *testing.T) {
	tests := []struct {
		name  string
		entry LogEntry
		want  string
	}{
		{name: "proxy domain", entry: LogEntry{Source: SourceProxy, Domain: "a.com", Port: "443"}, want: "a.com:443"},
		{name: "proxy ipv4", entry: LogEntry{Source: SourceProxy, Domain: "192.0.2.1", Port: "80"}, want: "192.0.2.1:80"},
		{name: "proxy ipv6 gets brackets", entry: LogEntry{Source: SourceProxy, Domain: "2001:db8::1", Port: "443"}, want: "[2001:db8::1]:443"},
		{name: "proxy without port", entry: LogEntry{Source: SourceProxy, Domain: "a.com"}, want: "a.com"},
		{name: "dns", entry: LogEntry{Source: SourceDNS, Domain: "a.com"}, want: "a.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.entry.Target().String())
		})
	}
}

func TestParseTarget(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		target  string
		want    Target
		wantErr bool
	}{
		{name: "proxy host and port", source: "proxy", target: "api.example.com:443", want: Target{Source: SourceProxy, Host: "api.example.com", Port: "443"}},
		{name: "proxy ipv6", source: "proxy", target: "[2001:db8::1]:443", want: Target{Source: SourceProxy, Host: "2001:db8::1", Port: "443"}},
		{name: "host is lowercased", source: "proxy", target: "API.Example.com:443", want: Target{Source: SourceProxy, Host: "api.example.com", Port: "443"}},
		{name: "dns bare domain", source: "dns", target: "example.com", want: Target{Source: SourceDNS, Host: "example.com"}},
		{name: "proxy without port", source: "proxy", target: "example.com", wantErr: true},
		{name: "unknown source", source: "smtp", target: "example.com:25", wantErr: true},
		{name: "empty target", source: "proxy", target: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTarget(tt.source, tt.target)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseTarget_RoundTrip(t *testing.T) {
	for _, e := range []LogEntry{
		{Source: SourceProxy, Domain: "a.com", Port: "443"},
		{Source: SourceProxy, Domain: "2001:db8::1", Port: "443"},
		{Source: SourceDNS, Domain: "a.com"},
	} {
		got, err := ParseTarget(string(e.Source), e.Target().String())
		require.NoError(t, err)
		assert.Equal(t, e.Target(), got)
	}
}
