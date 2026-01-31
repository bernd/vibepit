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

func TestDevContainerConfigEnvBuild(t *testing.T) {
	cfg := DevContainerConfig{
		Image:      "vibepit:latest",
		ProjectDir: "/home/user/project",
		WorkDir:    "/home/user/project",
		VolumeName: "vibepit-vol",
		NetworkID:  "net123",
		ProxyIP:    "172.28.0.2",
		Name:       "vibepit-dev",
		Term:       "xterm-256color",
		ColorTerm:  "truecolor",
		UID:        1000,
		User:       "testuser",
	}
	if cfg.Image != "vibepit:latest" {
		t.Error("unexpected image")
	}
	if cfg.UID != 1000 {
		t.Error("unexpected UID")
	}
	if cfg.ColorTerm != "truecolor" {
		t.Error("unexpected COLORTERM")
	}
}

func TestProxyContainerConfig(t *testing.T) {
	cfg := ProxyContainerConfig{
		BinaryPath: "/usr/local/bin/vibepit",
		ConfigPath: "/tmp/config.json",
		NetworkID:  "net456",
		ProxyIP:    "172.28.0.2",
		Name:       "vibepit-proxy",
	}
	if cfg.Name != "vibepit-proxy" {
		t.Error("unexpected proxy container name")
	}
	if cfg.BinaryPath != "/usr/local/bin/vibepit" {
		t.Error("unexpected binary path")
	}
}

func TestBoolPtr(t *testing.T) {
	p := boolPtr(true)
	if p == nil || !*p {
		t.Error("boolPtr(true) should return pointer to true")
	}
	p = boolPtr(false)
	if p == nil || *p {
		t.Error("boolPtr(false) should return pointer to false")
	}
}
