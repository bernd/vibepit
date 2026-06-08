package proxy

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCIDRBlocker(t *testing.T) {
	blocker := NewCIDRBlocker(nil, nil)

	tests := []struct {
		name string
		ip   string
		want bool
	}{
		{"private 10.x", "10.0.0.1", true},
		{"private 172.16.x", "172.16.0.1", true},
		{"private 172.31.x", "172.31.255.255", true},
		{"not private 172.15.x", "172.15.0.1", false},
		{"private 192.168.x", "192.168.1.1", true},
		{"loopback", "127.0.0.1", true},
		{"link-local", "169.254.1.1", true},
		{"public IP", "8.8.8.8", false},
		{"another public", "1.1.1.1", false},
		{"ipv6 loopback", "::1", true},
		{"ipv6 ULA", "fd00::1", true},
		{"ipv6 link-local", "fe80::1", true},
		{"ipv6 public", "2607:f8b0:4004:800::200e", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			require.NotNil(t, ip, "invalid test IP: %s", tt.ip)
			assert.Equal(t, tt.want, blocker.IsBlocked(ip), "IsBlocked(%s)", tt.ip)
		})
	}
}

func TestCIDRBlockerCustomRanges(t *testing.T) {
	blocker := NewCIDRBlocker([]string{"203.0.113.0/24"}, nil)

	tests := []struct {
		name string
		ip   string
		want bool
	}{
		{"custom blocked", "203.0.113.50", true},
		{"custom not blocked", "203.0.114.1", false},
		{"default still blocked", "10.0.0.1", true},
		{"public still allowed", "8.8.8.8", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			assert.Equal(t, tt.want, blocker.IsBlocked(ip), "IsBlocked(%s)", tt.ip)
		})
	}
}

func TestCIDRBlockerAllowCIDR(t *testing.T) {
	blocker := NewCIDRBlocker(nil, []string{"10.0.0.0/24"})

	tests := []struct {
		name        string
		ip          string
		wantBlocked bool
	}{
		{"in allow range, normally blocked", "10.0.0.5", false},
		{"in allow range /24 boundary", "10.0.0.255", false},
		{"just outside allow range", "10.0.1.1", true},
		{"other private IP still blocked", "192.168.1.1", true},
		{"public IP allowed (not in blocked ranges)", "8.8.8.8", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			require.NotNil(t, ip, "invalid test IP: %s", tt.ip)
			assert.Equal(t, tt.wantBlocked, blocker.IsBlocked(ip), "IsBlocked(%s)", tt.ip)
			if !tt.wantBlocked && tt.name == "in allow range, normally blocked" {
				assert.True(t, blocker.IsAllowed(ip), "IsAllowed(%s) should be true for allowed CIDR", tt.ip)
			}
		})
	}
}

func TestCIDRBlockerAllowOverridesBlock(t *testing.T) {
	blocker := NewCIDRBlocker(
		[]string{"172.16.0.0/12"},
		[]string{"172.16.0.0/24"},
	)

	tests := []struct {
		name        string
		ip          string
		wantBlocked bool
	}{
		{"in both block and allow, allow wins", "172.16.0.5", false},
		{"in block but outside allow", "172.20.0.1", true},
		{"in default block, not in custom block", "10.0.0.1", true},
		{"in default allow (public)", "8.8.8.8", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			require.NotNil(t, ip, "invalid test IP: %s", tt.ip)
			assert.Equal(t, tt.wantBlocked, blocker.IsBlocked(ip), "IsBlocked(%s)", tt.ip)
		})
	}
}

func TestCIDRBlockerAllowEmpty(t *testing.T) {
	blocker := NewCIDRBlocker(nil, nil)

	tests := []struct {
		name        string
		ip          string
		wantBlocked bool
	}{
		{"private blocked", "10.0.0.1", true},
		{"public allowed", "8.8.8.8", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			require.NotNil(t, ip, "invalid test IP: %s", tt.ip)
			assert.Equal(t, tt.wantBlocked, blocker.IsBlocked(ip), "IsBlocked(%s)", tt.ip)
		})
	}
}

func TestCIDRBlockerInvalidCIDRs(t *testing.T) {
	blocker := NewCIDRBlocker(
		[]string{"not-a-cidr", "10.0.0.0/33"},
		[]string{"also-invalid", "192.168.0.0/24"},
	)

	tests := []struct {
		name        string
		ip          string
		wantBlocked bool
	}{
		{"valid allow CIDR", "192.168.0.1", false},
		{"default still blocked", "10.0.0.1", true},
		{"public allowed", "8.8.8.8", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			require.NotNil(t, ip, "invalid test IP: %s", tt.ip)
			assert.Equal(t, tt.wantBlocked, blocker.IsBlocked(ip), "IsBlocked(%s)", tt.ip)
		})
	}
}
