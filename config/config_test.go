package config

import (
	"os"
	"path/filepath"
	"testing"
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
allow:
  - github.com
dns-only:
  - internal.example.com
block-cidr:
  - 203.0.113.0/24
`), 0o644)

		projectFile := filepath.Join(projectDir, "network.yaml")
		os.WriteFile(projectFile, []byte(`
presets:
  - pkg-go
allow:
  - api.anthropic.com
`), 0o644)

		cfg, err := Load(globalFile, projectFile)
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}

		merged := cfg.Merge(nil, nil)

		wants := []string{"github.com", "api.anthropic.com", "proxy.golang.org:443", "sum.golang.org:443"}
		for _, w := range wants {
			if !contains(merged.Allow, w) {
				t.Errorf("merged.Allow missing %q, got %v", w, merged.Allow)
			}
		}

		if !contains(merged.DNSOnly, "internal.example.com") {
			t.Errorf("merged.DNSOnly missing internal.example.com")
		}

		if !contains(merged.BlockCIDR, "203.0.113.0/24") {
			t.Errorf("merged.BlockCIDR missing 203.0.113.0/24")
		}
	})

	t.Run("CLI overrides add to merged config", func(t *testing.T) {
		cfg := &Config{}
		merged := cfg.Merge([]string{"extra.com"}, []string{"pkg-node"})

		if !contains(merged.Allow, "extra.com") {
			t.Errorf("CLI --allow not in merged result")
		}
		if !contains(merged.Allow, "registry.npmjs.org:443") {
			t.Errorf("CLI --preset pkg-node not expanded, got %v", merged.Allow)
		}
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
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}

		merged := cfg.Merge(nil, nil)

		if len(merged.AllowHostPorts) != 2 {
			t.Fatalf("expected 2 host ports, got %d: %v", len(merged.AllowHostPorts), merged.AllowHostPorts)
		}
		if merged.AllowHostPorts[0] != 9200 || merged.AllowHostPorts[1] != 5432 {
			t.Errorf("expected [9200, 5432], got %v", merged.AllowHostPorts)
		}
	})

	t.Run("rejects reserved proxy ports", func(t *testing.T) {
		reserved := []int{53, 2222, 3128, 3129}
		for _, port := range reserved {
			mc := MergedConfig{AllowHostPorts: []int{port}}
			if err := mc.ValidateHostPorts(); err == nil {
				t.Errorf("expected error for reserved port %d, got nil", port)
			}
		}

		// Non-reserved port should pass.
		mc := MergedConfig{AllowHostPorts: []int{8080}}
		if err := mc.ValidateHostPorts(); err != nil {
			t.Errorf("unexpected error for port 8080: %v", err)
		}
	})

	t.Run("missing files are not errors", func(t *testing.T) {
		cfg, err := Load("/nonexistent/global.yaml", "/nonexistent/project.yaml")
		if err != nil {
			t.Fatalf("Load() should not error on missing files: %v", err)
		}
		merged := cfg.Merge(nil, nil)
		if len(merged.Allow) != 0 {
			t.Errorf("expected empty allow list, got %v", merged.Allow)
		}
	})
}

func TestAppendAllow(t *testing.T) {
	t.Run("adds to existing allow section", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "network.yaml")
		os.WriteFile(path, []byte("presets:\n  - node\n\nallow:\n  - github.com\n"), 0o644)

		err := AppendAllow(path, []string{"bun.sh:443", "esm.sh"})
		if err != nil {
			t.Fatalf("AppendAllow() error: %v", err)
		}

		cfg := &ProjectConfig{}
		if err := loadFile(path, cfg); err != nil {
			t.Fatalf("loadFile() error: %v", err)
		}

		if !contains(cfg.Allow, "github.com") {
			t.Error("original entry github.com missing")
		}
		if !contains(cfg.Allow, "bun.sh:443") {
			t.Error("new entry bun.sh:443 missing")
		}
		if !contains(cfg.Allow, "esm.sh") {
			t.Error("new entry esm.sh missing")
		}
	})

	t.Run("creates allow section from commented template", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "network.yaml")
		os.WriteFile(path, []byte("presets:\n  - node\n\n# allow:\n#   - api.openai.com\n"), 0o644)

		err := AppendAllow(path, []string{"bun.sh"})
		if err != nil {
			t.Fatalf("AppendAllow() error: %v", err)
		}

		cfg := &ProjectConfig{}
		if err := loadFile(path, cfg); err != nil {
			t.Fatalf("loadFile() error: %v", err)
		}

		if !contains(cfg.Allow, "bun.sh") {
			t.Errorf("expected bun.sh in allow list, got %v", cfg.Allow)
		}
	})

	t.Run("deduplicates existing entries", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "network.yaml")
		os.WriteFile(path, []byte("allow:\n  - github.com\n"), 0o644)

		err := AppendAllow(path, []string{"github.com", "bun.sh"})
		if err != nil {
			t.Fatalf("AppendAllow() error: %v", err)
		}

		cfg := &ProjectConfig{}
		loadFile(path, cfg)

		count := 0
		for _, d := range cfg.Allow {
			if d == "github.com" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("github.com appears %d times, want 1", count)
		}
		if !contains(cfg.Allow, "bun.sh") {
			t.Error("bun.sh missing")
		}
	})
}

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
