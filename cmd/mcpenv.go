package cmd

import (
	"fmt"
	"strings"

	"github.com/bernd/vibepit/config"
)

// BuildMCPEnvVars constructs VIBEPIT_MCP_<NAME> environment variables
// for each configured MCP server, pointing to the proxy endpoint.
func BuildMCPEnvVars(servers []config.MCPServerConfig, proxyIP string) []string {
	var envVars []string
	for _, s := range servers {
		name := strings.ToUpper(strings.ReplaceAll(s.Name, "-", "_"))
		envVars = append(envVars, fmt.Sprintf("VIBEPIT_MCP_%s=http://%s:%d", name, proxyIP, s.Port))
	}
	return envVars
}
