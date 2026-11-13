package proxy

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestDenyRules(t *testing.T) {
	for _, tt := range []struct {
		name, entry, host, port string
		want                    bool
	}{
		{"exact", "example.com:443", "example.com", "443", true},
		{"single wildcard", "*.example.com:443", "a.example.com", "443", true},
		{"wrong port", "*.example.com:443", "a.example.com", "80", false},
		{"one label only", "*.example.com:443", "a.b.example.com", "443", false},
		{"multiple labels", "**.example.com:*", "a.b.example.com", "80", true},
		{"wildcard excludes apex", "**.example.com:*", "example.com", "80", false},
		{"case insensitive", "EXAMPLE.com:*", "Example.COM", "443", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, err := NewDenyRules([]string{tt.entry}, nil)
			require.NoError(t, err)
			assert.Equal(t, tt.want, d.Denied(Target{Source: SourceProxy, Host: tt.host, Port: tt.port}))
			assert.False(t, d.Denied(Target{Source: SourceDNS, Host: tt.host}))
		})
	}
	d, err := NewDenyRules(nil, []string{"**.example.com"})
	require.NoError(t, err)
	assert.True(t, d.Denied(Target{Source: SourceDNS, Host: "A.B.example.com"}))
	assert.False(t, d.Denied(Target{Source: SourceProxy, Host: "a.b.example.com", Port: "443"}))
	require.NoError(t, d.AddHTTP([]string{"new.example:443"}))
	require.NoError(t, d.AddDNS([]string{"new.example"}))
	assert.True(t, d.Denied(Target{Source: SourceProxy, Host: "new.example", Port: "443"}))
	assert.True(t, d.Denied(Target{Source: SourceDNS, Host: "new.example"}))
	require.Error(t, d.AddHTTP([]string{"partial.example:443", "bad"}))
	require.Error(t, d.AddDNS([]string{"partial.example", "bad:443"}))
	assert.False(t, d.Denied(Target{Source: SourceProxy, Host: "partial.example", Port: "443"}))
	assert.False(t, d.Denied(Target{Source: SourceDNS, Host: "partial.example"}))
	_, err = NewDenyRules([]string{"bad"}, nil)
	require.ErrorContains(t, err, "deny-http")
	_, err = NewDenyRules(nil, []string{"bad:443"})
	require.ErrorContains(t, err, "deny-dns")
	var absent *DenyRules
	assert.False(t, absent.Denied(Target{Source: SourceProxy, Host: "example.com", Port: "443"}))
	assert.False(t, d.Denied(Target{Source: "unknown", Host: "new.example"}))
}

func TestDenyRulesNumericPortEquivalence(t *testing.T) {
	for _, runtime := range []bool{false, true} {
		for _, rulePort := range []string{"443", "00443", "00000000000000000000443"} {
			t.Run(fmt.Sprintf("runtime=%v/rule=%s", runtime, rulePort), func(t *testing.T) {
				entries := []string{"example.com:" + rulePort}
				initial := entries
				if runtime {
					initial = nil
				}
				d, err := NewDenyRules(initial, nil)
				require.NoError(t, err)
				if runtime {
					require.NoError(t, d.AddHTTP(entries))
				}
				for _, port := range []string{"443", "00443", "00000000000000000000443"} {
					assert.True(t, d.Denied(Target{Source: SourceProxy, Host: "example.com", Port: port}), "request port %s", port)
				}
				assert.False(t, d.Denied(Target{Source: SourceProxy, Host: "example.com", Port: "444"}))
				require.Error(t, d.AddHTTP([]string{"other.com:00443", "bad:abc"}))
				assert.False(t, d.Denied(Target{Source: SourceProxy, Host: "other.com", Port: "443"}))
			})
		}
	}
}

func TestDenyRulesDedup(t *testing.T) {
	d, err := NewDenyRules([]string{"a.com:443", "A.com:443"}, []string{"a.com", "a.com"})
	require.NoError(t, err)
	for range 3 {
		require.NoError(t, d.AddHTTP([]string{"a.com:443", "a.com:00443", "b.com:443"}))
		require.NoError(t, d.AddDNS([]string{"A.COM", "b.com"}))
	}
	assert.Len(t, *d.http.rules.Load(), 2)
	assert.Len(t, *d.dns.rules.Load(), 2)
}
