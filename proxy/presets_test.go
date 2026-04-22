package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPresetRegistry(t *testing.T) {
	reg := NewPresetRegistry()

	t.Run("all expected presets exist", func(t *testing.T) {
		expected := []string{
			"default", "anthropic", "openai", "vcs-github", "vcs-other",
			"containers", "cloud", "pkg-node", "pkg-python",
			"pkg-ruby", "pkg-rust", "pkg-go", "pkg-jvm",
			"pkg-others", "linux-distros", "devtools", "monitoring",
			"cdn", "schema", "mcp",
		}
		for _, name := range expected {
			_, ok := reg.Get(name)
			assert.True(t, ok, "preset %q should exist", name)
		}
	})

	t.Run("default meta-preset includes anthropic and vcs-github", func(t *testing.T) {
		p, ok := reg.Get("default")
		require.True(t, ok)
		assert.Exactly(t, []string{
			"anthropic",
			"cdn-github",
			"homebrew",
			"mistral",
			"openai",
			"vcs-github",
		}, p.Includes)
		assert.Empty(t, p.Domains, "default should have no domains of its own")
	})

	t.Run("expand resolves includes recursively", func(t *testing.T) {
		domains := reg.Expand([]string{"default"})
		assert.Contains(t, domains, "api.anthropic.com:443")
		assert.Contains(t, domains, "github.com:443")
	})

	t.Run("expand deduplicates domains", func(t *testing.T) {
		domains := reg.Expand([]string{"default", "anthropic"})
		count := 0
		for _, d := range domains {
			if d == "api.anthropic.com:443" {
				count++
			}
		}
		assert.Equal(t, 1, count)
	})

	t.Run("expand handles unknown presets gracefully", func(t *testing.T) {
		domains := reg.Expand([]string{"nonexistent", "anthropic"})
		assert.Contains(t, domains, "api.anthropic.com:443")
	})

	t.Run("expand detects cycles", func(t *testing.T) {
		domains := reg.Expand([]string{"default"})
		assert.NotEmpty(t, domains)
	})

	t.Run("all presets have descriptions", func(t *testing.T) {
		for _, p := range reg.All() {
			assert.NotEmpty(t, p.Description, "preset %q needs a description", p.Name)
		}
	})

	t.Run("all presets have groups", func(t *testing.T) {
		for _, p := range reg.All() {
			assert.NotEmpty(t, p.Group, "preset %q needs a group", p.Name)
		}
	})

	t.Run("pkg presets have matchers", func(t *testing.T) {
		pkgPresets := []string{
			"pkg-node", "pkg-python", "pkg-ruby", "pkg-rust",
			"pkg-go", "pkg-jvm", "pkg-others",
		}
		for _, name := range pkgPresets {
			p, _ := reg.Get(name)
			assert.NotEmpty(t, p.Matchers, "preset %q should have matchers", name)
		}
	})

	t.Run("all preset domains pass validation", func(t *testing.T) {
		allNames := make([]string, 0, len(reg.All()))
		for _, p := range reg.All() {
			if len(p.Domains) > 0 {
				allNames = append(allNames, p.Name)
			}
		}
		domains := reg.Expand(allNames)
		_, err := NewHTTPAllowlist(domains)
		require.NoError(t, err, "expanded preset domains must pass validation")
	})

	t.Run("multi-level presets use double-star", func(t *testing.T) {
		mustUseDoubleStar := map[string][]string{
			"cloud":      {"**.amazonaws.com:443", "**.api.aws:443"},
			"monitoring": {"**.sentry.io:443", "**.datadoghq.com:443", "**.datadoghq.eu:443"},
		}
		for presetName, expected := range mustUseDoubleStar {
			p, ok := reg.Get(presetName)
			require.True(t, ok, "preset %q must exist", presetName)
			for _, want := range expected {
				assert.Contains(t, p.Domains, want,
					"preset %q must contain %q (not single-star variant)", presetName, want)
			}
		}
	})

	t.Run("non-pkg presets have no matchers", func(t *testing.T) {
		noMatchers := []string{
			"default", "anthropic", "openai", "vcs-github", "vcs-other",
			"containers", "cloud", "linux-distros", "devtools",
			"monitoring", "cdn", "schema", "mcp",
		}
		for _, name := range noMatchers {
			p, _ := reg.Get(name)
			assert.Empty(t, p.Matchers, "preset %q should have no matchers", name)
		}
	})
}
