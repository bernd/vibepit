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

func TestExecSessionOptions(t *testing.T) {
	size := &[2]uint{24, 80}
	env := []string{runtimeenv.WorkingDir + "=/project/nested/project"}

	opts := execSessionOptions(size, "/project/nested/project", env)

	assert.True(t, opts.Tty)
	assert.True(t, opts.AttachStdin)
	assert.True(t, opts.AttachStdout)
	assert.True(t, opts.AttachStderr)
	assert.Equal(t, []string{"/bin/bash", "--login"}, opts.Cmd)
	assert.Equal(t, size, opts.ConsoleSize)
	assert.Equal(t, "/project/nested/project", opts.WorkingDir)
	assert.Equal(t, env, opts.Env)
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
