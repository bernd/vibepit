package proxy

import (
	"net"
	"testing"
)

func TestCIDRBlocker(t *testing.T) {
	blocker := NewCIDRBlocker(nil)

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
			if ip == nil {
				t.Fatalf("invalid test IP: %s", tt.ip)
			}
			got := blocker.IsBlocked(ip)
			if got != tt.want {
				t.Errorf("IsBlocked(%s) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

func TestCIDRBlockerCustomRanges(t *testing.T) {
	blocker := NewCIDRBlocker([]string{"203.0.113.0/24"})

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
			got := blocker.IsBlocked(ip)
			if got != tt.want {
				t.Errorf("IsBlocked(%s) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}
