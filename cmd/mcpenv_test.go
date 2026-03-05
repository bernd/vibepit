package cmd

import (
	"net/url"
	"testing"

	"github.com/bernd/vibepit/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildMCPEnvVars(t *testing.T) {
	servers := []config.MCPServerConfig{
		{Name: "intellij", Port: 9100},
		{Name: "vs-code", Port: 9101},
		{Name: "my_server", Port: 9102},
	}

	envVars, err := BuildMCPEnvVars(servers, "10.0.0.2")
	require.NoError(t, err)

	assert.Equal(t, []string{
		"VIBEPIT_MCP_INTELLIJ=http://10.0.0.2:9100",
		"VIBEPIT_MCP_VS_CODE=http://10.0.0.2:9101",
		"VIBEPIT_MCP_MY_SERVER=http://10.0.0.2:9102",
	}, envVars)
}

func TestBuildMCPEnvVarsEmpty(t *testing.T) {
	envVars, err := BuildMCPEnvVars(nil, "10.0.0.2")
	require.NoError(t, err)
	assert.Empty(t, envVars)
}

func TestBuildMCPEnvVarsDuplicateDetection(t *testing.T) {
	servers := []config.MCPServerConfig{
		{Name: "my-server", Port: 9100},
		{Name: "my_server", Port: 9101},
	}

	_, err := BuildMCPEnvVars(servers, "10.0.0.2")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "VIBEPIT_MCP_MY_SERVER")
}

func TestMCPTargetAddr(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		want    string
		wantErr bool
	}{
		{"http with port", "http://127.0.0.1:6589", "127.0.0.1:6589", false},
		{"http without port", "http://127.0.0.1", "127.0.0.1:80", false},
		{"https without port", "https://127.0.0.1", "127.0.0.1:443", false},
		{"ipv6 with port", "http://[::1]:6589", "[::1]:6589", false},
		{"ipv6 without port", "http://[::1]", "[::1]:80", false},
		{"ipv6 with path", "http://[::1]/sse", "[::1]:80", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := url.Parse(tt.rawURL)
			require.NoError(t, err)
			got, err := mcpTargetAddr(u)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}
