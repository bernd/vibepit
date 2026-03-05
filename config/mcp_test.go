package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadMCPServers(t *testing.T) {
	dir := t.TempDir()
	projectPath := filepath.Join(dir, "network.yaml")
	err := os.WriteFile(projectPath, []byte(`
mcp-servers:
  - name: intellij
    url: http://127.0.0.1:6589
    transport: sse
    allow-tools:
      - "get_*"
      - "find_*"
`), 0o644)
	require.NoError(t, err)

	cfg, err := Load("", projectPath)
	require.NoError(t, err)
	require.Len(t, cfg.Project.MCPServers, 1)

	s := cfg.Project.MCPServers[0]
	assert.Equal(t, "intellij", s.Name)
	assert.Equal(t, "http://127.0.0.1:6589", s.URL)
	assert.Equal(t, "sse", s.Transport)
	assert.Equal(t, []string{"get_*", "find_*"}, s.AllowTools)
}

func TestMergeMCPServers(t *testing.T) {
	dir := t.TempDir()
	projectPath := filepath.Join(dir, "network.yaml")
	err := os.WriteFile(projectPath, []byte(`
mcp-servers:
  - name: intellij
    url: http://127.0.0.1:6589
    allow-tools:
      - "get_*"
`), 0o644)
	require.NoError(t, err)

	cfg, err := Load("", projectPath)
	require.NoError(t, err)

	merged, err := cfg.Merge(nil, nil)
	require.NoError(t, err)
	require.Len(t, merged.MCPServers, 1)
	assert.Equal(t, "intellij", merged.MCPServers[0].Name)
}

func TestMergeMCPServersDefaultTransport(t *testing.T) {
	dir := t.TempDir()
	projectPath := filepath.Join(dir, "network.yaml")
	err := os.WriteFile(projectPath, []byte(`
mcp-servers:
  - name: test
    url: http://127.0.0.1:8080
    allow-tools:
      - "*"
`), 0o644)
	require.NoError(t, err)

	cfg, err := Load("", projectPath)
	require.NoError(t, err)

	merged, err := cfg.Merge(nil, nil)
	require.NoError(t, err)
	require.Len(t, merged.MCPServers, 1)
	assert.Equal(t, "sse", merged.MCPServers[0].Transport)
}
