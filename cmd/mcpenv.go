package cmd

import (
	"fmt"
	"strings"

	"github.com/bernd/vibepit/config"
)

// MCPEnvName converts an MCP server name to its environment variable name.
func MCPEnvName(serverName string) string {
	return "VIBEPIT_MCP_" + strings.ToUpper(strings.ReplaceAll(serverName, "-", "_"))
}

// BuildMCPEnvVars constructs VIBEPIT_MCP_<NAME> environment variables
// for each configured MCP server, pointing to the proxy endpoint.
// Returns an error if two servers map to the same env var name.
func BuildMCPEnvVars(servers []config.MCPServerConfig, proxyIP string) ([]string, error) {
	seen := make(map[string]string) // env name -> original server name
	var envVars []string
	for _, s := range servers {
		envName := MCPEnvName(s.Name)
		if prev, ok := seen[envName]; ok {
			return nil, fmt.Errorf("MCP servers %q and %q both map to env var %s", prev, s.Name, envName)
		}
		seen[envName] = s.Name
		envVars = append(envVars, fmt.Sprintf("%s=http://%s:%d", envName, proxyIP, s.Port))
	}
	return envVars, nil
}
