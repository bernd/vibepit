package cmd

import (
	"testing"

	"github.com/bernd/vibepit/config"
	"github.com/stretchr/testify/assert"
)

func TestBuildMCPEnvVars(t *testing.T) {
	servers := []config.MCPServerConfig{
		{Name: "intellij", Port: 9100},
		{Name: "vs-code", Port: 9101},
		{Name: "my_server", Port: 9102},
	}

	envVars := BuildMCPEnvVars(servers, "10.0.0.2")

	assert.Equal(t, []string{
		"VIBEPIT_MCP_INTELLIJ=http://10.0.0.2:9100",
		"VIBEPIT_MCP_VS_CODE=http://10.0.0.2:9101",
		"VIBEPIT_MCP_MY_SERVER=http://10.0.0.2:9102",
	}, envVars)
}

func TestBuildMCPEnvVarsEmpty(t *testing.T) {
	envVars := BuildMCPEnvVars(nil, "10.0.0.2")
	assert.Empty(t, envVars)
}
