package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadAndMerge(t *testing.T) {
	t.Run("merges global and project configs", func(t *testing.T) {
		dir := t.TempDir()
		globalDir := filepath.Join(dir, "global")
		projectDir := filepath.Join(dir, "project", ".vibepit")
		os.MkdirAll(globalDir, 0o755)
		os.MkdirAll(projectDir, 0o755)

		globalFile := filepath.Join(globalDir, "config.yaml")
		os.WriteFile(globalFile, []byte(`
allow-http:
  - github.com:443
allow-dns:
  - internal.example.com
block-cidr:
  - 203.0.113.0/24
`), 0o644)

		projectFile := filepath.Join(projectDir, "network.yaml")
		os.WriteFile(projectFile, []byte(`
presets:
  - pkg-go
allow-http:
  - api.anthropic.com:443
`), 0o644)

		cfg, err := Load(globalFile, projectFile)
		require.NoError(t, err)

		merged := cfg.Merge(nil, nil)

		for _, want := range []string{"github.com:443", "api.anthropic.com:443", "proxy.golang.org:443", "sum.golang.org:443"} {
			assert.Contains(t, merged.AllowHTTP, want)
		}
		assert.Contains(t, merged.AllowDNS, "internal.example.com")
		assert.Contains(t, merged.BlockCIDR, "203.0.113.0/24")
	})

	t.Run("CLI overrides add to merged config", func(t *testing.T) {
		cfg := &Config{}
		merged := cfg.Merge([]string{"extra.com:443"}, []string{"pkg-node"})

		assert.Contains(t, merged.AllowHTTP, "extra.com:443")
		assert.Contains(t, merged.AllowHTTP, "registry.npmjs.org:443")
	})

	t.Run("merges allow-host-ports from project config", func(t *testing.T) {
		dir := t.TempDir()
		projectDir := filepath.Join(dir, "project", ".vibepit")
		os.MkdirAll(projectDir, 0o755)

		projectFile := filepath.Join(projectDir, "network.yaml")
		os.WriteFile(projectFile, []byte(`
allow-host-ports:
  - 9200
  - 5432
`), 0o644)

		cfg, err := Load("/nonexistent/global.yaml", projectFile)
		require.NoError(t, err)

		merged := cfg.Merge(nil, nil)
		assert.Equal(t, []int{9200, 5432}, merged.AllowHostPorts)
	})

	t.Run("generates random port in ephemeral range avoiding excluded", func(t *testing.T) {
		excluded := []int{55000, 55001}
		for range 100 {
			port, err := RandomProxyPort(excluded)
			require.NoError(t, err)
			assert.GreaterOrEqual(t, port, 49152)
			assert.LessOrEqual(t, port, 65535)
			assert.False(t, slices.Contains(excluded, port), "port %d is in excluded set", port)
		}
	})

	t.Run("missing files are not errors", func(t *testing.T) {
		cfg, err := Load("/nonexistent/global.yaml", "/nonexistent/project.yaml")
		require.NoError(t, err)
		merged := cfg.Merge(nil, nil)
		assert.Empty(t, merged.AllowHTTP)
	})
}

func TestAppendAllowHTTP(t *testing.T) {
	t.Run("adds to existing allow-http section", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "network.yaml")
		os.WriteFile(path, []byte("presets:\n  - node\n\nallow-http:\n  - github.com:443\n"), 0o644)

		require.NoError(t, AppendAllowHTTP(path, []string{"bun.sh:443", "esm.sh:*"}))

		cfg := &ProjectConfig{}
		require.NoError(t, loadFile(path, cfg))

		assert.Contains(t, cfg.AllowHTTP, "github.com:443")
		assert.Contains(t, cfg.AllowHTTP, "bun.sh:443")
		assert.Contains(t, cfg.AllowHTTP, "esm.sh:*")
	})

	t.Run("creates allow-http section from commented template", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "network.yaml")
		os.WriteFile(path, []byte("presets:\n  - node\n\n# allow-http:\n#   - api.openai.com:443\n"), 0o644)

		require.NoError(t, AppendAllowHTTP(path, []string{"bun.sh:443"}))

		cfg := &ProjectConfig{}
		require.NoError(t, loadFile(path, cfg))
		assert.Contains(t, cfg.AllowHTTP, "bun.sh:443")
	})

	t.Run("deduplicates existing entries", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "network.yaml")
		os.WriteFile(path, []byte("allow-http:\n  - github.com:443\n"), 0o644)

		require.NoError(t, AppendAllowHTTP(path, []string{"github.com:443", "bun.sh:443"}))

		cfg := &ProjectConfig{}
		require.NoError(t, loadFile(path, cfg))

		count := 0
		for _, d := range cfg.AllowHTTP {
			if d == "github.com:443" {
				count++
			}
		}
		assert.Equal(t, 1, count, "github.com:443 should appear exactly once")
		assert.Contains(t, cfg.AllowHTTP, "bun.sh:443")
	})
}

func TestAppendAllowDNS(t *testing.T) {
	t.Run("adds to existing allow-dns section", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "network.yaml")
		os.WriteFile(path, []byte("presets:\n  - node\n\nallow-dns:\n  - internal.example.com\n"), 0o644)

		require.NoError(t, AppendAllowDNS(path, []string{"svc.local", "*.corp.example"}))

		cfg := &ProjectConfig{}
		require.NoError(t, loadFile(path, cfg))

		assert.Contains(t, cfg.AllowDNS, "internal.example.com")
		assert.Contains(t, cfg.AllowDNS, "svc.local")
		assert.Contains(t, cfg.AllowDNS, "*.corp.example")
	})

	t.Run("creates allow-dns section from commented template", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "network.yaml")
		os.WriteFile(path, []byte("presets:\n  - node\n\n# allow-dns:\n#   - internal.corp.example.com\n"), 0o644)

		require.NoError(t, AppendAllowDNS(path, []string{"svc.local"}))

		cfg := &ProjectConfig{}
		require.NoError(t, loadFile(path, cfg))
		assert.Contains(t, cfg.AllowDNS, "svc.local")
	})

	t.Run("deduplicates existing entries", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "network.yaml")
		os.WriteFile(path, []byte("allow-dns:\n  - internal.example.com\n"), 0o644)

		require.NoError(t, AppendAllowDNS(path, []string{"internal.example.com", "svc.local"}))

		cfg := &ProjectConfig{}
		require.NoError(t, loadFile(path, cfg))

		count := 0
		for _, d := range cfg.AllowDNS {
			if d == "internal.example.com" {
				count++
			}
		}
		assert.Equal(t, 1, count, "internal.example.com should appear exactly once")
		assert.Contains(t, cfg.AllowDNS, "svc.local")
	})
}
