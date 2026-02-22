package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPAllowlist(t *testing.T) {
	al, err := NewHTTPAllowlist([]string{
		"github.com:443",
		"*.example.com:*",
		"api.stripe.com:443",
		"*.cdn.example.com:443",
		"dev.local:*",
		"**.amazonaws.com:443",
		"bedrock.*.amazonaws.com:443",
	})
	require.NoError(t, err)

	tests := []struct {
		name string
		host string
		port string
		want bool
	}{
		// Exact domain with exact port
		{"exact match", "github.com", "443", true},
		{"exact port mismatch", "github.com", "80", false},
		{"subdomain does not match exact", "api.github.com", "443", false},
		{"unrelated domain", "gitlab.com", "443", false},

		// Single-label wildcard domain with wildcard port
		{"wildcard subdomain any port", "foo.example.com", "80", true},
		{"wildcard subdomain https", "foo.example.com", "443", true},
		{"wildcard apex rejected", "example.com", "443", false},
		{"deep subdomain rejected by *", "a.b.example.com", "80", false},

		// Exact domain
		{"port match", "api.stripe.com", "443", true},
		{"port mismatch", "api.stripe.com", "80", false},

		// Single-label wildcard of cdn.example.com
		{"cdn subdomain", "img.cdn.example.com", "443", true},
		{"cdn deep subdomain rejected", "a.b.cdn.example.com", "443", false},

		// Wildcard port
		{"any port 80", "dev.local", "80", true},
		{"any port 443", "dev.local", "443", true},
		{"any port 8080", "dev.local", "8080", true},

		// Multi-label wildcard
		{"** single level", "s3.amazonaws.com", "443", true},
		{"** multi level", "s3.us-east-1.amazonaws.com", "443", true},
		{"** apex rejected", "amazonaws.com", "443", false},

		// Mid-domain single-label wildcard (also matched by **.amazonaws.com)
		{"mid * matches", "bedrock.us-east-1.amazonaws.com", "443", true},
		{"mid * wrong prefix matches ** rule", "other.us-east-1.amazonaws.com", "443", true},
		{"mid * too many labels matches ** rule", "bedrock.a.b.amazonaws.com", "443", true},
		{"mid * too few labels matches ** rule", "bedrock.amazonaws.com", "443", true},

		// Edge cases
		{"empty host", "", "443", false},
		{"empty port with exact port rule", "github.com", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, al.Allows(tt.host, tt.port))
		})
	}

	t.Run("combined exact and wildcard rules", func(t *testing.T) {
		combined, err := NewHTTPAllowlist([]string{"example.com:443", "*.example.com:443"})
		require.NoError(t, err)
		assert.True(t, combined.Allows("example.com", "443"))
		assert.True(t, combined.Allows("api.example.com", "443"))
		assert.False(t, combined.Allows("a.b.example.com", "443"))
		assert.False(t, combined.Allows("api.example.com", "80"))
	})

	t.Run("isolated mid-domain single-label wildcard", func(t *testing.T) {
		isolated, err := NewHTTPAllowlist([]string{"bedrock.*.amazonaws.com:443"})
		require.NoError(t, err)
		assert.True(t, isolated.Allows("bedrock.us-east-1.amazonaws.com", "443"))
		assert.False(t, isolated.Allows("other.us-east-1.amazonaws.com", "443"), "wrong prefix")
		assert.False(t, isolated.Allows("bedrock.a.b.amazonaws.com", "443"), "too many labels")
		assert.False(t, isolated.Allows("bedrock.amazonaws.com", "443"), "too few labels")
	})
}

func TestHTTPAllowlistAdd(t *testing.T) {
	al, err := NewHTTPAllowlist([]string{"github.com:443"})
	require.NoError(t, err)

	assert.True(t, al.Allows("github.com", "443"))
	assert.False(t, al.Allows("bun.sh", "443"))

	require.NoError(t, al.Add([]string{"bun.sh:443", "esm.sh:*"}))

	assert.True(t, al.Allows("bun.sh", "443"), "added port-specific entry should match")
	assert.False(t, al.Allows("bun.sh", "80"), "port mismatch should be rejected")
	assert.True(t, al.Allows("esm.sh", "443"), "wildcard port entry should match any port")
	assert.True(t, al.Allows("esm.sh", "80"), "wildcard port entry should match any port")
	assert.True(t, al.Allows("github.com", "443"), "original entries should still work")
}

func TestDNSAllowlist(t *testing.T) {
	al, err := NewDNSAllowlist([]string{
		"github.com",
		"*.example.com",
		"api.openai.com",
		"**.amazonaws.com",
	})
	require.NoError(t, err)

	tests := []struct {
		name string
		host string
		want bool
	}{
		{"exact match", "github.com", true},
		{"subdomain of exact does not match", "api.github.com", false},
		{"wildcard subdomain", "foo.example.com", true},
		{"wildcard apex rejected", "example.com", false},
		{"deep subdomain rejected by *", "a.b.example.com", false},
		{"portless rule", "api.openai.com", true},
		{"not in list", "evil.com", false},
		{"empty host", "", false},
		{"** single level", "s3.amazonaws.com", true},
		{"** multi level", "s3.us-east-1.amazonaws.com", true},
		{"** apex rejected", "amazonaws.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, al.Allows(tt.host))
		})
	}

	t.Run("combined exact and wildcard rules", func(t *testing.T) {
		combined, err := NewDNSAllowlist([]string{"example.com", "**.example.com"})
		require.NoError(t, err)
		assert.True(t, combined.Allows("example.com"))
		assert.True(t, combined.Allows("api.example.com"))
		assert.True(t, combined.Allows("a.b.example.com"))
	})
}

func TestDomainMatches(t *testing.T) {
	tests := []struct {
		pattern string
		host    string
		want    bool
	}{
		// Exact match
		{"example.com", "example.com", true},
		{"example.com", "Example.COM", true},
		{"example.com", "other.com", false},
		{"example.com", "sub.example.com", false},

		// Single-label wildcard at prefix
		{"*.example.com", "foo.example.com", true},
		{"*.example.com", "bar.example.com", true},
		{"*.example.com", "example.com", false},
		{"*.example.com", "a.b.example.com", false},

		// Single-label wildcard mid-domain
		{"bedrock.*.amazonaws.com", "bedrock.us-east-1.amazonaws.com", true},
		{"bedrock.*.amazonaws.com", "bedrock.eu-west-2.amazonaws.com", true},
		{"bedrock.*.amazonaws.com", "bedrock.amazonaws.com", false},
		{"bedrock.*.amazonaws.com", "bedrock.a.b.amazonaws.com", false},
		{"bedrock.*.amazonaws.com", "other.us-east-1.amazonaws.com", false},

		// Multi-label wildcard at prefix
		{"**.example.com", "foo.example.com", true},
		{"**.example.com", "a.b.example.com", true},
		{"**.example.com", "a.b.c.example.com", true},
		{"**.example.com", "example.com", false},

		// Multi-label wildcard mid-domain
		{"bedrock.**.amazonaws.com", "bedrock.us-east-1.amazonaws.com", true},
		{"bedrock.**.amazonaws.com", "bedrock.a.b.amazonaws.com", true},
		{"bedrock.**.amazonaws.com", "bedrock.amazonaws.com", false},

		// Multi-label wildcard suffix-adjacent
		{"example.**", "example.com", true},
		{"example.**", "example.co.uk", true},
		{"example.**", "example.a.b.c", true},
		{"example.**", "other.com", false},
		{"example.**", "notexample.com", false},

		// Combined * and **
		{"*.**.example.com", "foo.bar.example.com", true},
		{"*.**.example.com", "foo.bar.baz.example.com", true},
		{"*.**.example.com", "foo.example.com", false},

		// Multiple single-label wildcards
		{"*.*.example.com", "a.b.example.com", true},
		{"*.*.example.com", "a.example.com", false},
		{"*.*.example.com", "a.b.c.example.com", false},

		// Edge cases
		{"", "example.com", false},
		{"example.com", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"/"+tt.host, func(t *testing.T) {
			rule := parseDomainPattern(tt.pattern)
			assert.Equal(t, tt.want, rule.matches(tt.host))
		})
	}
}

func TestPortMatch(t *testing.T) {
	tests := []struct {
		pattern string
		port    string
		want    bool
	}{
		{"443", "443", true},
		{"443", "80", false},
		{"443", "", false},
		{"*", "443", true},
		{"*", "80", true},
		{"*", "12345", true},
		{"*", "", true},
		{"", "", true},
		{"", "80", false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"/"+tt.port, func(t *testing.T) {
			assert.Equal(t, tt.want, portMatches(tt.pattern, tt.port))
		})
	}
}

func TestValidateHTTPEntry(t *testing.T) {
	tests := []struct {
		name  string
		entry string
		valid bool
	}{
		// Valid patterns
		{"exact domain exact port", "github.com:443", true},
		{"single-label wildcard prefix", "*.example.com:443", true},
		{"multi-label wildcard prefix", "**.example.com:443", true},
		{"mid-domain single wildcard", "bedrock.*.amazonaws.com:443", true},
		{"mid-domain multi wildcard", "bedrock.**.amazonaws.com:443", true},
		{"wildcard port", "github.com:*", true},
		{"single wildcard with wildcard port", "*.example.com:*", true},
		{"multi wildcard with wildcard port", "**.example.com:*", true},
		{"combined * and **", "*.**.example.com:443", true},

		// Invalid: port patterns
		{"partial port glob trailing", "github.com:80*", false},
		{"partial port glob leading", "github.com:*80", false},
		{"partial port glob infix", "github.com:8*0", false},
		{"non-digit port", "github.com:44a", false},
		{"empty port", "github.com:", false},
		{"missing port", "github.com", false},

		// Invalid: domain label grammar
		{"mixed wildcard label", "a*.example.com:443", false},
		{"mixed wildcard label suffix", "*foo.example.com:443", false},
		{"triple star label", "***.example.com:443", false},
		{"mixed double star label", "foo**.example.com:443", false},
		{"empty label leading dot", ".example.com:443", false},
		{"empty label double dot", "foo..example.com:443", false},
		{"empty label trailing dot", "example.com.:443", false},

		// Invalid: structural rules
		{"bare single wildcard", "*:443", false},
		{"bare double wildcard", "**:443", false},
		{"two double wildcards", "**.**.example.com:443", false},
		{"empty domain", ":443", false},
		{"domain contains colon", "a:b:443", false},
		{"space in domain", "git hub.com:443", false},
		{"space in port", "github.com:44 3", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateHTTPEntry(tt.entry)
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestValidateDNSEntry(t *testing.T) {
	tests := []struct {
		name  string
		entry string
		valid bool
	}{
		// Valid patterns
		{"exact domain", "github.com", true},
		{"single-label wildcard", "*.example.com", true},
		{"multi-label wildcard", "**.example.com", true},
		{"mid-domain wildcard", "bedrock.*.amazonaws.com", true},
		{"combined * and **", "*.**.example.com", true},

		// Invalid patterns
		{"empty string", "", false},
		{"entry with port", "github.com:443", false},
		{"space in domain", "git hub.com", false},
		{"bare single wildcard", "*", false},
		{"bare double wildcard", "**", false},
		{"two double wildcards", "**.**.example.com", false},
		{"mixed label", "a*.example.com", false},
		{"empty label", ".example.com", false},
		{"empty label double dot", "foo..example.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDNSEntry(tt.entry)
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestValidateDNSEntries(t *testing.T) {
	assert.NoError(t, ValidateDNSEntries([]string{"example.com", "*.svc.local"}))
	assert.Error(t, ValidateDNSEntries([]string{"example.com:443"}))
}

func TestNewHTTPAllowlistValidation(t *testing.T) {
	t.Run("valid entries succeed", func(t *testing.T) {
		_, err := NewHTTPAllowlist([]string{"github.com:443", "*.example.com:*"})
		assert.NoError(t, err)
	})
	t.Run("nil entries succeed", func(t *testing.T) {
		_, err := NewHTTPAllowlist(nil)
		assert.NoError(t, err)
	})
	t.Run("invalid entry returns error", func(t *testing.T) {
		_, err := NewHTTPAllowlist([]string{"github.com:443", "bad:entry:here"})
		assert.Error(t, err)
	})
	t.Run("partial port glob rejected", func(t *testing.T) {
		_, err := NewHTTPAllowlist([]string{"dev.local:80*"})
		assert.Error(t, err)
	})
}

func TestNewDNSAllowlistValidation(t *testing.T) {
	t.Run("valid entries succeed", func(t *testing.T) {
		_, err := NewDNSAllowlist([]string{"github.com", "*.example.com"})
		assert.NoError(t, err)
	})
	t.Run("nil entries succeed", func(t *testing.T) {
		_, err := NewDNSAllowlist(nil)
		assert.NoError(t, err)
	})
	t.Run("invalid entry returns error", func(t *testing.T) {
		_, err := NewDNSAllowlist([]string{"github.com:443"})
		assert.Error(t, err)
	})
}
