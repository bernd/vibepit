package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPortGlobMatch(t *testing.T) {
	tests := []struct {
		pattern string
		port    string
		want    bool
	}{
		// Exact match
		{"443", "443", true},
		{"443", "80", false},
		{"443", "", false},

		// Full wildcard
		{"*", "443", true},
		{"*", "80", true},
		{"*", "12345", true},
		{"*", "", true},

		// Trailing wildcard (prefix)
		{"80*", "80", true},
		{"80*", "800", true},
		{"80*", "8080", true},
		{"80*", "8000", true},
		{"80*", "443", false},
		{"80*", "180", false},

		// Leading wildcard (suffix)
		{"*80", "80", true},
		{"*80", "180", true},
		{"*80", "8080", true},
		{"*80", "443", false},
		{"*80", "801", false},

		// Infix wildcard
		{"8*0", "80", true},
		{"8*0", "800", true},
		{"8*0", "8000", true},
		{"8*0", "8010", true},
		{"8*0", "81", false},
		{"8*0", "443", false},

		// Multiple wildcards
		{"*4*", "443", true},
		{"*4*", "8443", true},
		{"*4*", "80", false},

		// Empty pattern
		{"", "", true},
		{"", "80", false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"/"+tt.port, func(t *testing.T) {
			assert.Equal(t, tt.want, portGlobMatch(tt.pattern, tt.port))
		})
	}
}

func TestHTTPAllowlist(t *testing.T) {
	al := NewHTTPAllowlist([]string{
		"github.com:443",
		"*.example.com:*",
		"api.stripe.com:443",
		"*.cdn.example.com:443",
		"dev.local:80*",
	})

	tests := []struct {
		name string
		host string
		port string
		want bool
	}{
		// Exact domain with exact port
		{"exact match", "github.com", "443", true},
		{"exact port mismatch", "github.com", "80", false},
		{"subdomain match", "api.github.com", "443", true},
		{"deep subdomain", "raw.api.github.com", "443", true},
		{"unrelated domain", "gitlab.com", "443", false},

		// Wildcard domain with full port wildcard
		{"wildcard subdomain any port", "foo.example.com", "80", true},
		{"wildcard subdomain https", "foo.example.com", "443", true},
		{"wildcard apex rejected", "example.com", "443", false},
		{"wildcard deep subdomain", "a.b.example.com", "80", true},

		// Exact domain with port-specific
		{"port match", "api.stripe.com", "443", true},
		{"port mismatch", "api.stripe.com", "80", false},
		{"subdomain of exact", "foo.api.stripe.com", "443", true},

		// Wildcard domain with exact port — these also match *.example.com:*
		// so we test the *.cdn.example.com:443 rule with an isolated wildcard below
		{"wildcard port match via broader rule", "img.cdn.example.com", "443", true},
		{"wildcard port match via broader wildcard", "img.cdn.example.com", "80", true},
		{"cdn apex matches broader wildcard", "cdn.example.com", "443", true},

		// Port glob
		{"port glob 80", "dev.local", "80", true},
		{"port glob 800", "dev.local", "800", true},
		{"port glob 8080", "dev.local", "8080", true},
		{"port glob no match", "dev.local", "443", false},

		// Edge cases
		{"empty host", "", "443", false},
		{"empty port with exact port rule", "github.com", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, al.Allows(tt.host, tt.port))
		})
	}

	// Test wildcard + exact port in isolation (no broader rule to mask it).
	t.Run("isolated wildcard port", func(t *testing.T) {
		isolated := NewHTTPAllowlist([]string{"*.cdn.other.com:443"})
		assert.True(t, isolated.Allows("img.cdn.other.com", "443"))
		assert.False(t, isolated.Allows("img.cdn.other.com", "80"))
		assert.False(t, isolated.Allows("cdn.other.com", "443"), "wildcard should not match apex")
	})
}

func TestHTTPAllowlistAdd(t *testing.T) {
	al := NewHTTPAllowlist([]string{"github.com:443"})

	assert.True(t, al.Allows("github.com", "443"))
	assert.False(t, al.Allows("bun.sh", "443"))

	al.Add([]string{"bun.sh:443", "esm.sh:*"})

	assert.True(t, al.Allows("bun.sh", "443"), "added port-specific entry should match")
	assert.False(t, al.Allows("bun.sh", "80"), "port mismatch should be rejected")
	assert.True(t, al.Allows("esm.sh", "443"), "wildcard port entry should match any port")
	assert.True(t, al.Allows("esm.sh", "80"), "wildcard port entry should match any port")
	assert.True(t, al.Allows("github.com", "443"), "original entries should still work")
}

func TestDNSAllowlist(t *testing.T) {
	al := NewDNSAllowlist([]string{
		"github.com",
		"*.example.com",
		"api.openai.com",
	})

	tests := []struct {
		name string
		host string
		want bool
	}{
		{"exact match", "github.com", true},
		{"subdomain of exact", "api.github.com", true},
		{"wildcard subdomain", "foo.example.com", true},
		{"wildcard apex rejected", "example.com", false},
		{"portless rule", "api.openai.com", true},
		{"not in list", "evil.com", false},
		{"empty host", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, al.Allows(tt.host))
		})
	}
}
