package container

import (
	"net"
	"testing"
)

func TestNextIP(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"172.28.0.1", "172.28.0.2"},
		{"10.0.0.0", "10.0.0.1"},
		{"192.168.1.254", "192.168.1.255"},
	}
	for _, tt := range tests {
		got := nextIP(net.ParseIP(tt.input))
		if got.String() != tt.expected {
			t.Errorf("nextIP(%s) = %s, want %s", tt.input, got, tt.expected)
		}
	}
}

