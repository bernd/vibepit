package container

import (
	"net"
	"testing"

	"github.com/bernd/vibepit/internal/runtimeenv"
	"github.com/stretchr/testify/assert"
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
		assert.Equal(t, tt.expected, got.String(), "nextIP(%s)", tt.input)
	}
}

func TestSandboxEnvironmentIncludesExtraEnv(t *testing.T) {
	env := sandboxEnvironment(SandboxContainerConfig{
		ProjectDir: "/project",
		ProxyIP:    "172.28.0.2",
		ProxyPort:  3128,
		Term:       "xterm-256color",
		ExtraEnv:   []string{runtimeenv.WorkingDir + "=/project/nested/project"},
	})

	assert.Contains(t, env, runtimeenv.ProjectDir+"=/project")
	assert.Contains(t, env, runtimeenv.WorkingDir+"=/project/nested/project")
}
