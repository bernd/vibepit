package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAllowlist(t *testing.T) {
	al := NewAllowlist([]string{
		"github.com",
		"*.example.com",
		"api.stripe.com:443",
		"*.cdn.example.com:443",
	})

	tests := []struct {
		name string
		host string
		port string
		want bool
	}{
		// Exact with automatic subdomains
		{"exact match", "github.com", "443", true},
		{"subdomain match", "api.github.com", "443", true},
		{"deep subdomain", "raw.api.github.com", "8080", true},
		{"unrelated domain", "gitlab.com", "443", false},

		// Wildcard: subdomains only, not apex
		{"wildcard subdomain", "foo.example.com", "80", true},
		{"wildcard apex rejected", "example.com", "443", false},
		{"wildcard deep subdomain", "a.b.example.com", "80", true},

		// Port-specific exact
		{"port match", "api.stripe.com", "443", true},
		{"port mismatch", "api.stripe.com", "80", false},
		{"port subdomain no match", "foo.api.stripe.com", "443", false},

		// Port-specific wildcard
		{"wildcard port match", "img.cdn.example.com", "443", true},
		{"wildcard port mismatch", "img.cdn.example.com", "80", false},
		{"wildcard port apex rejected", "cdn.example.com", "443", false},

		// Edge cases
		{"empty host", "", "443", false},
		{"empty port still matches portless rule", "github.com", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := al.Allows(tt.host, tt.port)
			if got != tt.want {
				t.Errorf("Allows(%q, %q) = %v, want %v", tt.host, tt.port, got, tt.want)
			}
		})
	}
}

func TestAllowlistAdd(t *testing.T) {
	al := NewAllowlist([]string{"github.com"})

	assert.True(t, al.Allows("github.com", "443"))
	assert.False(t, al.Allows("bun.sh", "443"))

	al.Add([]string{"bun.sh:443", "esm.sh"})

	assert.True(t, al.Allows("bun.sh", "443"), "added port-specific entry should match")
	assert.False(t, al.Allows("bun.sh", "80"), "port mismatch should be rejected")
	assert.True(t, al.Allows("esm.sh", "443"), "added portless entry should match any port")
	assert.True(t, al.Allows("github.com", "443"), "original entries should still work")
}

func TestAllowsPort(t *testing.T) {
	al := NewAllowlist([]string{
		"host.vibepit",
		"host.vibepit:8000",
		"*.vibepit:9000",
	})

	tests := []struct {
		name string
		host string
		port string
		want bool
	}{
		{"port-specific match", "host.vibepit", "8000", true},
		{"port mismatch", "host.vibepit", "8002", false},
		{"portless rule ignored", "host.vibepit", "9999", false},
		{"wildcard port match", "sub.vibepit", "9000", true},
		{"wildcard port mismatch", "sub.vibepit", "80", false},
		{"empty host", "", "8000", false},
		{"empty port", "host.vibepit", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := al.AllowsPort(tt.host, tt.port)
			if got != tt.want {
				t.Errorf("AllowsPort(%q, %q) = %v, want %v", tt.host, tt.port, got, tt.want)
			}
		})
	}
}

func TestAllowlistDNS(t *testing.T) {
	al := NewAllowlist([]string{
		"github.com:443",
		"*.example.com:8080",
		"api.openai.com",
	})

	tests := []struct {
		name string
		host string
		want bool
	}{
		// DNS ignores port, so port-specific rules still match the domain
		{"port rule domain allowed for DNS", "github.com", true},
		{"wildcard port rule domain allowed for DNS", "foo.example.com", true},
		{"portless rule", "api.openai.com", true},
		{"not in list", "evil.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := al.AllowsDNS(tt.host)
			if got != tt.want {
				t.Errorf("AllowsDNS(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}
