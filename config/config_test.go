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

		merged, err := cfg.Merge(nil, nil)
		require.NoError(t, err)

		for _, want := range []string{"github.com:443", "api.anthropic.com:443", "proxy.golang.org:443", "sum.golang.org:443"} {
			assert.Contains(t, merged.AllowHTTP, want)
		}
		assert.Contains(t, merged.AllowDNS, "internal.example.com")
		assert.Contains(t, merged.BlockCIDR, "203.0.113.0/24")
	})

	t.Run("CLI overrides add to merged config", func(t *testing.T) {
		cfg := &Config{}
		merged, err := cfg.Merge([]string{"extra.com:443"}, []string{"pkg-node"})
		require.NoError(t, err)

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

		merged, err := cfg.Merge(nil, nil)
		require.NoError(t, err)
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
		merged, err := cfg.Merge(nil, nil)
		require.NoError(t, err)
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

	t.Run("inserts into allow-http when another list follows", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "network.yaml")
		os.WriteFile(path, []byte("allow-http:\n  - github.com:443\n\nallow-host-ports:\n  - 3000\n"), 0o644)

		require.NoError(t, AppendAllowHTTP(path, []string{"bun.sh:443"}))

		cfg := &ProjectConfig{}
		require.NoError(t, loadFile(path, cfg))

		assert.Contains(t, cfg.AllowHTTP, "github.com:443")
		assert.Contains(t, cfg.AllowHTTP, "bun.sh:443")
		assert.Equal(t, []int{3000}, cfg.AllowHostPorts)
	})

	t.Run("adds to existing flow-style allow-http section", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "network.yaml")
		os.WriteFile(path, []byte("allow-http: [github.com:443]\nallow-host-ports:\n  - 3000\n"), 0o644)

		require.NoError(t, AppendAllowHTTP(path, []string{"bun.sh:443"}))

		cfg := &ProjectConfig{}
		require.NoError(t, loadFile(path, cfg))

		assert.Contains(t, cfg.AllowHTTP, "github.com:443")
		assert.Contains(t, cfg.AllowHTTP, "bun.sh:443")
		assert.Equal(t, []int{3000}, cfg.AllowHostPorts)
	})

	t.Run("preserves comments and blank lines while appending a new allow-http section", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "network.yaml")
		before := "# project network\npresets:\n  - node # package manager\n\n# local services\nallow-host-ports:\n  - 3000 # web app\n"
		want := "# project network\npresets:\n  - node # package manager\n\n# local services\nallow-host-ports:\n  - 3000 # web app\n\nallow-http:\n  - bun.sh:443\n"
		os.WriteFile(path, []byte(before), 0o644)

		require.NoError(t, AppendAllowHTTP(path, []string{"bun.sh:443"}))

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, want, string(data))
	})

	t.Run("preserves blank lines when appending to existing block allow-http section", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "network.yaml")
		before := "presets:\n  - homebrew\n  - openai\n\n# Additional domains to allow HTTP access for this project.\nallow-http:\n  - storage.googleapis.com:443\n\n# Domains that only need DNS resolution (no HTTP proxy).\n# allow-dns:\n#   - internal.corp.example.com\n"
		want := "presets:\n  - homebrew\n  - openai\n\n# Additional domains to allow HTTP access for this project.\nallow-http:\n  - storage.googleapis.com:443\n  - foo:8080\n\n# Domains that only need DNS resolution (no HTTP proxy).\n# allow-dns:\n#   - internal.corp.example.com\n"
		os.WriteFile(path, []byte(before), 0o644)

		require.NoError(t, AppendAllowHTTP(path, []string{"foo:8080"}))

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, want, string(data))
	})

	t.Run("creates allow-http section from commented template", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "network.yaml")
		before := "presets:\n  - node\n\n# allow-http:\n#   - api.openai.com:443\n"
		want := "presets:\n  - node\n\nallow-http:\n  - bun.sh:443\n"
		os.WriteFile(path, []byte(before), 0o644)

		require.NoError(t, AppendAllowHTTP(path, []string{"bun.sh:443"}))

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, want, string(data))
	})

	t.Run("keeps header comment above the section when replacing the default template", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "network.yaml")
		before := "# Vibepit network config for this project.\n\n# Presets bundle common domains for a language ecosystem.\npresets:\n  - default\n  - pkg-node\n\n# Additional domains to allow HTTP access for this project.\n# allow-http:\n#   - api.openai.com:443\n#   - api.anthropic.com:443\n\n# Domains that only need DNS resolution (no HTTP proxy).\n# allow-dns:\n#   - internal.corp.example.com\n"
		want := "# Vibepit network config for this project.\n\n# Presets bundle common domains for a language ecosystem.\npresets:\n  - default\n  - pkg-node\n\n# Additional domains to allow HTTP access for this project.\nallow-http:\n  - bun.sh:443\n\n# Domains that only need DNS resolution (no HTTP proxy).\n# allow-dns:\n#   - internal.corp.example.com\n"
		os.WriteFile(path, []byte(before), 0o644)

		require.NoError(t, AppendAllowHTTP(path, []string{"bun.sh:443"}))

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, want, string(data))
	})

	t.Run("appends to allow-http with a null value", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "network.yaml")
		before := "presets:\n  - node\nallow-http:\nallow-host-ports:\n  - 3000\n"
		want := "presets:\n  - node\nallow-http:\n  - bun.sh:443\nallow-host-ports:\n  - 3000\n"
		os.WriteFile(path, []byte(before), 0o644)

		require.NoError(t, AppendAllowHTTP(path, []string{"bun.sh:443"}))

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, want, string(data))
	})

	t.Run("tolerates non-standard spacing in the commented template", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "network.yaml")
		before := "presets:\n  - node\n\n# allow-http:\n#  - foo:443\n#- bar:443\n"
		want := "presets:\n  - node\n\nallow-http:\n  - new:443\n"
		os.WriteFile(path, []byte(before), 0o644)

		require.NoError(t, AppendAllowHTTP(path, []string{"new:443"}))

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, want, string(data))
	})

	t.Run("does not treat a freeform comment line as a commented template", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "network.yaml")
		before := "presets:\n  - node\n# allow-http: see other config\n"
		want := "presets:\n  - node\n# allow-http: see other config\n\nallow-http:\n  - bun.sh:443\n"
		os.WriteFile(path, []byte(before), 0o644)

		require.NoError(t, AppendAllowHTTP(path, []string{"bun.sh:443"}))

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, want, string(data))
	})

	t.Run("appends to allow-http with an explicit null value", func(t *testing.T) {
		for _, nullVal := range []string{"null", "~"} {
			dir := t.TempDir()
			path := filepath.Join(dir, "network.yaml")
			os.WriteFile(path, []byte("presets:\n  - node\nallow-http: "+nullVal+"\n"), 0o644)

			require.NoError(t, AppendAllowHTTP(path, []string{"bun.sh:443"}))

			cfg := &ProjectConfig{}
			require.NoError(t, loadFile(path, cfg))
			assert.Equal(t, []string{"bun.sh:443"}, cfg.AllowHTTP,
				"explicit %q value should be replaced with a real list", nullVal)
		}
	})

	t.Run("preserves CRLF line endings when appending", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "network.yaml")
		before := "allow-http:\r\n  - github.com:443\r\n"
		want := "allow-http:\r\n  - github.com:443\r\n  - bun.sh:443\r\n"
		os.WriteFile(path, []byte(before), 0o644)

		require.NoError(t, AppendAllowHTTP(path, []string{"bun.sh:443"}))

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, want, string(data))
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

func TestMergeValidation(t *testing.T) {
	t.Run("invalid allow-http entry fails merge", func(t *testing.T) {
		cfg := &Config{
			Project: ProjectConfig{
				AllowHTTP: []string{"github.com:443", "bad:entry:here"},
			},
		}
		_, err := cfg.Merge(nil, nil)
		assert.Error(t, err)
	})
	t.Run("invalid allow-dns entry fails merge", func(t *testing.T) {
		cfg := &Config{
			Project: ProjectConfig{
				AllowDNS: []string{"github.com:443"},
			},
		}
		_, err := cfg.Merge(nil, nil)
		assert.Error(t, err)
	})
	t.Run("invalid CLI allow entry fails merge", func(t *testing.T) {
		cfg := &Config{}
		_, err := cfg.Merge([]string{"a*.example.com:443"}, nil)
		assert.Error(t, err)
	})
	t.Run("valid entries succeed", func(t *testing.T) {
		cfg := &Config{
			Project: ProjectConfig{
				AllowHTTP: []string{"github.com:443"},
				AllowDNS:  []string{"example.com"},
			},
		}
		_, err := cfg.Merge(nil, nil)
		assert.NoError(t, err)
	})
}

func TestUnmarshalUpstreamDNS(t *testing.T) {
	t.Run("unmarshal upstream-dns string", func(t *testing.T) {
		dir := t.TempDir()
		globalFile := filepath.Join(dir, "config.yaml")
		os.WriteFile(globalFile, []byte(`
allow-http:
  - github.com:443
upstream-dns: 8.8.8.8:53
`), 0o644)

		cfg, err := Load(globalFile, "/nonexistent/project.yaml")
		require.NoError(t, err)
		assert.Equal(t, "8.8.8.8:53", cfg.Global.Upstream)
	})

	t.Run("unmarshal upstream-dns with port only", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		os.WriteFile(path, []byte(`upstream-dns: "1.1.1.1:5353"`), 0o644)

		cfg := &GlobalConfig{}
		require.NoError(t, loadFile(path, cfg))
		assert.Equal(t, "1.1.1.1:5353", cfg.Upstream)
	})

	t.Run("missing upstream-dns is empty string", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		os.WriteFile(path, []byte(`allow-dns:
  - example.com`), 0o644)

		cfg := &GlobalConfig{}
		require.NoError(t, loadFile(path, cfg))
		assert.Empty(t, cfg.Upstream)
	})

	t.Run("merged config carries upstream", func(t *testing.T) {
		dir := t.TempDir()
		globalFile := filepath.Join(dir, "config.yaml")
		os.WriteFile(globalFile, []byte(`
upstream-dns: 9.9.9.9:53
allow-dns:
  - internal.example.com
`), 0o644)

		cfg, err := Load(globalFile, "/nonexistent/project.yaml")
		require.NoError(t, err)

		merged, err := cfg.Merge(nil, nil)
		require.NoError(t, err)
		assert.Equal(t, "9.9.9.9:53", merged.Upstream)
	})
}
