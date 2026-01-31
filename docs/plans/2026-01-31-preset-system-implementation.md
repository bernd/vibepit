# Preset System Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace the hardcoded allowlist presets with a data-driven registry of 19 categorized presets, add project auto-detection, and build an interactive preset selector using charmbracelet/huh.

**Architecture:** New preset registry in `proxy/presets.go` replaces the old `config.DefaultPresets()`. Auto-detection in `config/detect.go` scans marker files. Interactive selector in `config/setup.go` uses `huh` for a grouped multi-select. The `--reconfigure` flag re-shows the selector while preserving manual config entries.

**Tech Stack:** Go, charmbracelet/huh, koanf (YAML), testify

---

### Task 1: Add charmbracelet/huh dependency

**Files:**
- Modify: `go.mod`

**Step 1: Add the dependency**

Run: `cd /home/bernd/Code/vibepit && go get github.com/charmbracelet/huh@latest`

**Step 2: Verify it resolved**

Run: `grep charmbracelet go.mod`
Expected: Line containing `github.com/charmbracelet/huh`

**Step 3: Tidy**

Run: `go mod tidy`

**Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "feat: add charmbracelet/huh dependency for interactive preset selector"
```

---

### Task 2: Create preset registry with tests

**Files:**
- Create: `proxy/presets.go`
- Create: `proxy/presets_test.go`

**Step 1: Write failing tests for the preset registry**

Create `proxy/presets_test.go`:

```go
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
			"default", "anthropic", "vcs-github", "vcs-other",
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
		assert.Contains(t, p.Includes, "anthropic")
		assert.Contains(t, p.Includes, "vcs-github")
		assert.Empty(t, p.Domains, "default should have no domains of its own")
	})

	t.Run("expand resolves includes recursively", func(t *testing.T) {
		domains := reg.Expand([]string{"default"})
		// Should contain domains from anthropic and vcs-github
		assert.Contains(t, domains, "api.anthropic.com")
		assert.Contains(t, domains, "github.com")
	})

	t.Run("expand deduplicates domains", func(t *testing.T) {
		domains := reg.Expand([]string{"default", "anthropic"})
		count := 0
		for _, d := range domains {
			if d == "api.anthropic.com" {
				count++
			}
		}
		assert.Equal(t, 1, count)
	})

	t.Run("expand handles unknown presets gracefully", func(t *testing.T) {
		domains := reg.Expand([]string{"nonexistent", "anthropic"})
		assert.Contains(t, domains, "api.anthropic.com")
	})

	t.Run("expand detects cycles", func(t *testing.T) {
		// The built-in registry has no cycles, so this just verifies
		// the mechanism doesn't panic on valid data.
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

	t.Run("non-pkg presets have no matchers", func(t *testing.T) {
		noMatchers := []string{
			"default", "anthropic", "vcs-github", "vcs-other",
			"containers", "cloud", "linux-distros", "devtools",
			"monitoring", "cdn", "schema", "mcp",
		}
		for _, name := range noMatchers {
			p, _ := reg.Get(name)
			assert.Empty(t, p.Matchers, "preset %q should have no matchers", name)
		}
	})
}
```

**Step 2: Run tests to verify they fail**

Run: `cd /home/bernd/Code/vibepit && go test ./proxy/ -run TestPresetRegistry -v`
Expected: Compilation error — `NewPresetRegistry` undefined

**Step 3: Write the preset registry**

Create `proxy/presets.go` with the `Preset` struct, `PresetRegistry` type, `NewPresetRegistry()` constructor containing all 19 presets, and `Get`, `All`, `Expand` methods.

```go
package proxy

// Preset defines a network allowlist preset with optional auto-detection
// matchers and the ability to include other presets.
type Preset struct {
	Name        string
	Group       string
	Description string
	Domains     []string
	Matchers    []string // file glob patterns for project auto-detection
	Includes    []string // other preset names (meta-presets)
}

// PresetRegistry holds all built-in presets in definition order.
type PresetRegistry struct {
	presets []Preset
	index   map[string]int
}

// NewPresetRegistry returns the built-in preset registry.
func NewPresetRegistry() *PresetRegistry {
	presets := []Preset{
		// --- Defaults ---
		{
			Name:        "default",
			Group:       "Defaults",
			Description: "Anthropic services and GitHub",
			Includes:    []string{"anthropic", "vcs-github"},
		},

		// --- Infrastructure ---
		{
			Name:        "anthropic",
			Group:       "Infrastructure",
			Description: "Anthropic services",
			Domains: []string{
				"api.anthropic.com",
				"statsig.anthropic.com",
				"docs.claude.com",
				"code.claude.com",
				"claude.ai",
			},
		},
		{
			Name:        "vcs-github",
			Group:       "Infrastructure",
			Description: "GitHub",
			Domains: []string{
				"github.com",
				"www.github.com",
				"api.github.com",
				"npm.pkg.github.com",
				"raw.githubusercontent.com",
				"pkg-npm.githubusercontent.com",
				"objects.githubusercontent.com",
				"codeload.github.com",
				"avatars.githubusercontent.com",
				"camo.githubusercontent.com",
				"gist.github.com",
			},
		},
		{
			Name:        "vcs-other",
			Group:       "Infrastructure",
			Description: "GitLab and Bitbucket",
			Domains: []string{
				"gitlab.com",
				"www.gitlab.com",
				"registry.gitlab.com",
				"bitbucket.org",
				"www.bitbucket.org",
				"api.bitbucket.org",
			},
		},
		{
			Name:        "containers",
			Group:       "Infrastructure",
			Description: "Container registries",
			Domains: []string{
				"registry-1.docker.io",
				"auth.docker.io",
				"index.docker.io",
				"hub.docker.com",
				"www.docker.com",
				"production.cloudflare.docker.com",
				"download.docker.com",
				"gcr.io",
				"*.gcr.io",
				"ghcr.io",
				"mcr.microsoft.com",
				"*.data.mcr.microsoft.com",
				"public.ecr.aws",
			},
		},
		{
			Name:        "cloud",
			Group:       "Infrastructure",
			Description: "Cloud platforms (GCP, Azure, AWS, Oracle)",
			Domains: []string{
				"cloud.google.com",
				"accounts.google.com",
				"gcloud.google.com",
				"*.googleapis.com",
				"storage.googleapis.com",
				"compute.googleapis.com",
				"container.googleapis.com",
				"azure.com",
				"portal.azure.com",
				"microsoft.com",
				"www.microsoft.com",
				"*.microsoftonline.com",
				"packages.microsoft.com",
				"dotnet.microsoft.com",
				"dot.net",
				"visualstudio.com",
				"dev.azure.com",
				"*.amazonaws.com",
				"*.api.aws",
				"oracle.com",
				"www.oracle.com",
				"java.com",
				"www.java.com",
				"java.net",
				"www.java.net",
				"download.oracle.com",
				"yum.oracle.com",
			},
		},
		{
			Name:        "linux-distros",
			Group:       "Infrastructure",
			Description: "Linux distribution repositories",
			Domains: []string{
				"archive.ubuntu.com",
				"security.ubuntu.com",
				"ubuntu.com",
				"www.ubuntu.com",
				"*.ubuntu.com",
				"ppa.launchpad.net",
				"launchpad.net",
				"www.launchpad.net",
			},
		},
		{
			Name:        "devtools",
			Group:       "Infrastructure",
			Description: "Development tools and platforms",
			Domains: []string{
				"dl.k8s.io",
				"pkgs.k8s.io",
				"k8s.io",
				"www.k8s.io",
				"releases.hashicorp.com",
				"apt.releases.hashicorp.com",
				"rpm.releases.hashicorp.com",
				"archive.releases.hashicorp.com",
				"hashicorp.com",
				"www.hashicorp.com",
				"repo.anaconda.com",
				"conda.anaconda.org",
				"anaconda.org",
				"www.anaconda.com",
				"anaconda.com",
				"continuum.io",
				"apache.org",
				"www.apache.org",
				"archive.apache.org",
				"downloads.apache.org",
				"eclipse.org",
				"www.eclipse.org",
				"download.eclipse.org",
				"nodejs.org",
				"www.nodejs.org",
			},
		},
		{
			Name:        "monitoring",
			Group:       "Infrastructure",
			Description: "Monitoring and observability services",
			Domains: []string{
				"statsig.com",
				"www.statsig.com",
				"api.statsig.com",
				"sentry.io",
				"*.sentry.io",
				"http-intake.logs.datadoghq.com",
				"*.datadoghq.com",
				"*.datadoghq.eu",
			},
		},
		{
			Name:        "cdn",
			Group:       "Infrastructure",
			Description: "Content delivery and mirrors",
			Domains: []string{
				"sourceforge.net",
				"*.sourceforge.net",
				"packagecloud.io",
				"*.packagecloud.io",
			},
		},
		{
			Name:        "schema",
			Group:       "Infrastructure",
			Description: "Schema and configuration registries",
			Domains: []string{
				"json-schema.org",
				"www.json-schema.org",
				"json.schemastore.org",
				"www.schemastore.org",
			},
		},
		{
			Name:        "mcp",
			Group:       "Infrastructure",
			Description: "Model Context Protocol",
			Domains: []string{
				"*.modelcontextprotocol.io",
			},
		},

		// --- Package Managers ---
		{
			Name:        "pkg-node",
			Group:       "Package Managers",
			Description: "JavaScript/Node.js",
			Matchers:    []string{"package.json", "yarn.lock", "pnpm-lock.yaml", "bun.lockb"},
			Domains: []string{
				"registry.npmjs.org",
				"www.npmjs.com",
				"www.npmjs.org",
				"npmjs.com",
				"npmjs.org",
				"yarnpkg.com",
				"registry.yarnpkg.com",
			},
		},
		{
			Name:        "pkg-python",
			Group:       "Package Managers",
			Description: "Python",
			Matchers:    []string{"pyproject.toml", "requirements.txt", "setup.py", "Pipfile", "poetry.lock"},
			Domains: []string{
				"pypi.org",
				"www.pypi.org",
				"files.pythonhosted.org",
				"pythonhosted.org",
				"test.pypi.org",
				"pypi.python.org",
				"pypa.io",
				"www.pypa.io",
			},
		},
		{
			Name:        "pkg-ruby",
			Group:       "Package Managers",
			Description: "Ruby",
			Matchers:    []string{"Gemfile", "*.gemspec"},
			Domains: []string{
				"rubygems.org",
				"www.rubygems.org",
				"api.rubygems.org",
				"index.rubygems.org",
				"ruby-lang.org",
				"www.ruby-lang.org",
				"rubyforge.org",
				"www.rubyforge.org",
				"rubyonrails.org",
				"www.rubyonrails.org",
				"rvm.io",
				"get.rvm.io",
			},
		},
		{
			Name:        "pkg-rust",
			Group:       "Package Managers",
			Description: "Rust",
			Matchers:    []string{"Cargo.toml"},
			Domains: []string{
				"crates.io",
				"www.crates.io",
				"index.crates.io",
				"static.crates.io",
				"rustup.rs",
				"static.rust-lang.org",
				"www.rust-lang.org",
			},
		},
		{
			Name:        "pkg-go",
			Group:       "Package Managers",
			Description: "Go",
			Matchers:    []string{"go.mod"},
			Domains: []string{
				"proxy.golang.org",
				"sum.golang.org",
				"index.golang.org",
				"golang.org",
				"www.golang.org",
				"goproxy.io",
				"pkg.go.dev",
			},
		},
		{
			Name:        "pkg-jvm",
			Group:       "Package Managers",
			Description: "JVM (Maven, Gradle, Kotlin, Spring)",
			Matchers:    []string{"pom.xml", "build.gradle", "build.gradle.kts", "build.sbt"},
			Domains: []string{
				"maven.org",
				"repo.maven.org",
				"central.maven.org",
				"repo1.maven.org",
				"jcenter.bintray.com",
				"gradle.org",
				"www.gradle.org",
				"services.gradle.org",
				"plugins.gradle.org",
				"kotlin.org",
				"www.kotlin.org",
				"spring.io",
				"repo.spring.io",
			},
		},
		{
			Name:        "pkg-others",
			Group:       "Package Managers",
			Description: "Other languages (PHP, .NET, Dart, Elixir, Perl, CocoaPods, Haskell, Swift)",
			Matchers:    []string{"composer.json", "*.csproj", "*.sln", "pubspec.yaml", "mix.exs", "Podfile", "Package.swift", "*.cabal", "stack.yaml"},
			Domains: []string{
				"packagist.org",
				"www.packagist.org",
				"repo.packagist.org",
				"nuget.org",
				"www.nuget.org",
				"api.nuget.org",
				"pub.dev",
				"api.pub.dev",
				"hex.pm",
				"www.hex.pm",
				"cpan.org",
				"www.cpan.org",
				"metacpan.org",
				"www.metacpan.org",
				"api.metacpan.org",
				"cocoapods.org",
				"www.cocoapods.org",
				"cdn.cocoapods.org",
				"haskell.org",
				"www.haskell.org",
				"hackage.haskell.org",
				"swift.org",
				"www.swift.org",
			},
		},
	}

	index := make(map[string]int, len(presets))
	for i, p := range presets {
		index[p.Name] = i
	}

	return &PresetRegistry{presets: presets, index: index}
}

// Get returns a preset by name.
func (r *PresetRegistry) Get(name string) (Preset, bool) {
	i, ok := r.index[name]
	if !ok {
		return Preset{}, false
	}
	return r.presets[i], true
}

// All returns all presets in definition order.
func (r *PresetRegistry) All() []Preset {
	return r.presets
}

// Expand resolves a list of preset names into a deduplicated flat list of
// domains, recursively expanding Includes with cycle detection.
func (r *PresetRegistry) Expand(names []string) []string {
	seen := make(map[string]bool)
	var domains []string
	visited := make(map[string]bool)

	var expand func(name string)
	expand = func(name string) {
		if visited[name] {
			return
		}
		visited[name] = true
		p, ok := r.Get(name)
		if !ok {
			return
		}
		for _, inc := range p.Includes {
			expand(inc)
		}
		for _, d := range p.Domains {
			if !seen[d] {
				seen[d] = true
				domains = append(domains, d)
			}
		}
	}

	for _, name := range names {
		expand(name)
	}
	return domains
}
```

**Step 4: Run tests to verify they pass**

Run: `cd /home/bernd/Code/vibepit && go test ./proxy/ -run TestPresetRegistry -v`
Expected: All tests PASS

**Step 5: Commit**

```bash
git add proxy/presets.go proxy/presets_test.go
git commit -m "feat: add data-driven preset registry with 19 presets from Claude Code allow list"
```

---

### Task 3: Create project auto-detection with tests

**Files:**
- Create: `config/detect.go`
- Create: `config/detect_test.go`

**Step 1: Write failing tests for auto-detection**

Create `config/detect_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDetectPresets(t *testing.T) {
	t.Run("detects go project", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example"), 0o644)

		detected := DetectPresets(dir)
		assert.Contains(t, detected, "pkg-go")
	})

	t.Run("detects node project", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644)

		detected := DetectPresets(dir)
		assert.Contains(t, detected, "pkg-node")
	})

	t.Run("detects multiple ecosystems", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example"), 0o644)
		os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644)

		detected := DetectPresets(dir)
		assert.Contains(t, detected, "pkg-go")
		assert.Contains(t, detected, "pkg-node")
	})

	t.Run("detects glob patterns like *.gemspec", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "mygem.gemspec"), []byte(""), 0o644)

		detected := DetectPresets(dir)
		assert.Contains(t, detected, "pkg-ruby")
	})

	t.Run("empty directory detects nothing", func(t *testing.T) {
		dir := t.TempDir()

		detected := DetectPresets(dir)
		assert.Empty(t, detected)
	})

	t.Run("does not detect non-pkg presets", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example"), 0o644)

		detected := DetectPresets(dir)
		assert.NotContains(t, detected, "default")
		assert.NotContains(t, detected, "anthropic")
		assert.NotContains(t, detected, "cloud")
	})

	t.Run("detects python project via pyproject.toml", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(""), 0o644)

		detected := DetectPresets(dir)
		assert.Contains(t, detected, "pkg-python")
	})

	t.Run("detects python project via requirements.txt", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte(""), 0o644)

		detected := DetectPresets(dir)
		assert.Contains(t, detected, "pkg-python")
	})

	t.Run("detects jvm project via pom.xml", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "pom.xml"), []byte(""), 0o644)

		detected := DetectPresets(dir)
		assert.Contains(t, detected, "pkg-jvm")
	})

	t.Run("detects rust project", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte(""), 0o644)

		detected := DetectPresets(dir)
		assert.Contains(t, detected, "pkg-rust")
	})

	t.Run("detects pkg-others via composer.json", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "composer.json"), []byte("{}"), 0o644)

		detected := DetectPresets(dir)
		assert.Contains(t, detected, "pkg-others")
	})

	t.Run("detects pkg-others via csproj glob", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "MyApp.csproj"), []byte(""), 0o644)

		detected := DetectPresets(dir)
		assert.Contains(t, detected, "pkg-others")
	})
}
```

**Step 2: Run tests to verify they fail**

Run: `cd /home/bernd/Code/vibepit && go test ./config/ -run TestDetectPresets -v`
Expected: Compilation error — `DetectPresets` undefined

**Step 3: Write the detection function**

Create `config/detect.go`:

```go
package config

import (
	"os"
	"path/filepath"

	"github.com/bernd/vibepit/proxy"
)

// DetectPresets scans the project root for marker files and returns the names
// of presets that match. Only presets with Matchers are considered.
func DetectPresets(projectDir string) []string {
	reg := proxy.NewPresetRegistry()
	var detected []string

	for _, p := range reg.All() {
		if len(p.Matchers) == 0 {
			continue
		}
		if matchesAny(projectDir, p.Matchers) {
			detected = append(detected, p.Name)
		}
	}

	return detected
}

// matchesAny returns true if any of the patterns match a file in dir.
func matchesAny(dir string, patterns []string) bool {
	for _, pattern := range patterns {
		// Glob patterns contain wildcard characters.
		if containsGlob(pattern) {
			matches, _ := filepath.Glob(filepath.Join(dir, pattern))
			if len(matches) > 0 {
				return true
			}
		} else {
			if _, err := os.Stat(filepath.Join(dir, pattern)); err == nil {
				return true
			}
		}
	}
	return false
}

func containsGlob(pattern string) bool {
	for _, c := range pattern {
		if c == '*' || c == '?' || c == '[' {
			return true
		}
	}
	return false
}
```

**Step 4: Run tests to verify they pass**

Run: `cd /home/bernd/Code/vibepit && go test ./config/ -run TestDetectPresets -v`
Expected: All tests PASS

**Step 5: Commit**

```bash
git add config/detect.go config/detect_test.go
git commit -m "feat: add project auto-detection for pkg presets"
```

---

### Task 4: Refactor config to use preset registry

This task replaces the old `config.Preset`/`config.DefaultPresets()` with the new `proxy.PresetRegistry` and removes custom preset support from the global config.

**Files:**
- Modify: `config/config.go:12-14` (remove old `Preset` struct)
- Modify: `config/config.go:16-22` (remove `Presets` field from `GlobalConfig`)
- Modify: `config/config.go:70-127` (rewrite `Merge` to use `PresetRegistry.Expand`)
- Modify: `config/config.go:129-147` (remove `DefaultPresets`)
- Modify: `config/config_test.go` (update tests)

**Step 1: Update the test for the new behavior**

The test at `config/config_test.go:10-60` currently tests with user-defined presets in global config. Update it to use the new registry-based presets.

Replace the "merges global and project configs" subtest body so that:
- The global config YAML no longer has a `presets:` map section
- The project config uses `presets: [pkg-go]` instead of `presets: [node]`
- The expected domains come from the `pkg-go` preset in the registry

Replace the "CLI overrides add to merged config" subtest so it uses `pkg-node` as CLI preset and checks for `registry.npmjs.org`.

**Step 2: Run tests to verify they fail**

Run: `cd /home/bernd/Code/vibepit && go test ./config/ -run TestLoadAndMerge -v`
Expected: FAIL — old preset names no longer resolve

**Step 3: Refactor config.go**

Remove:
- The `Preset` struct (lines 12-14)
- The `Presets` field from `GlobalConfig` (line 21)
- The `DefaultPresets()` function (lines 129-147)

Update `Merge` method:
- Remove the `availablePresets` map logic (lines 94-103)
- Instead, call `proxy.NewPresetRegistry().Expand(allPresets)` and add the result via `addUnique`

The new `Merge` should look like:

```go
func (c *Config) Merge(cliAllow []string, cliPresets []string) MergedConfig {
	seen := make(map[string]bool)
	var allow []string

	addUnique := func(entries []string) {
		for _, e := range entries {
			if !seen[e] {
				seen[e] = true
				allow = append(allow, e)
			}
		}
	}

	addUnique(c.Global.Allow)
	addUnique(c.Project.Allow)
	addUnique(cliAllow)

	allPresets := make([]string, 0, len(c.Project.Presets)+len(cliPresets))
	allPresets = append(allPresets, c.Project.Presets...)
	allPresets = append(allPresets, cliPresets...)

	reg := proxy.NewPresetRegistry()
	addUnique(reg.Expand(allPresets))

	// ... dns-only and block-cidr merging stays the same ...
}
```

**Step 4: Run all config tests**

Run: `cd /home/bernd/Code/vibepit && go test ./config/ -v`
Expected: All tests PASS

**Step 5: Run full test suite to check nothing else broke**

Run: `cd /home/bernd/Code/vibepit && go test ./... -v`
Expected: All PASS

**Step 6: Commit**

```bash
git add config/config.go config/config_test.go
git commit -m "refactor: replace hardcoded presets with registry-based expansion"
```

---

### Task 5: Rewrite interactive selector with huh

**Files:**
- Modify: `config/setup.go` (full rewrite)

**Step 1: Rewrite setup.go**

Replace the entire `RunFirstTimeSetup` function. The new version:
- Takes `projectDir` in addition to config paths
- Calls `DetectPresets(projectDir)` to find matching presets
- Uses `huh.NewMultiSelect` to show a grouped, toggleable preset list
- Pre-checks `default` and all detected presets
- Writes only the `presets` list to `.vibepit/network.yaml` via `writeProjectConfig`

Also add a new `RunReconfigure` function that:
- Reads existing `.vibepit/network.yaml`
- Pre-checks saved presets instead of defaults
- Re-runs detection (for display only, not pre-checking)
- Writes back the new `presets` list, preserving `allow` and `dns-only`

The `writeProjectConfig` function should be updated:
- Accept a list of preset names
- Write YAML with `presets:` list
- Add commented-out `allow:` and `dns-only:` sections
- Update the comment listing built-in presets

Key code for the selector:

```go
func buildPresetOptions(detected []string, preChecked []string) []huh.Option[string] {
	reg := proxy.NewPresetRegistry()
	detectedSet := toSet(detected)
	preCheckedSet := toSet(preChecked)

	type group struct {
		name    string
		options []huh.Option[string]
	}

	// Build detected group first, then remaining groups.
	var groups []group
	detectedGroup := group{name: "Detected"}
	groupMap := make(map[string]*group)

	for _, p := range reg.All() {
		label := fmt.Sprintf("%s - %s", p.Name, p.Description)
		opt := huh.NewOption(label, p.Name).Selected(preCheckedSet[p.Name])

		if detectedSet[p.Name] {
			detectedGroup.options = append(detectedGroup.options, opt)
			continue
		}

		g, ok := groupMap[p.Group]
		if !ok {
			g = &group{name: p.Group}
			groupMap[p.Group] = g
			groups = append(groups, *g)
		}
		g.options = append(g.options, opt)
	}

	// Flatten: detected first, then defined groups.
	var all []huh.Option[string]
	// Add group headers as disabled options for visual separation.
	// (Depends on huh API — may need adjustment.)
	all = append(all, detectedGroup.options...)
	for _, g := range groups {
		all = append(all, groupMap[g.name].options...)
	}
	return all
}
```

Note: The exact `huh` API for grouping options may need adaptation during implementation. Check `go doc github.com/charmbracelet/huh` for the current multi-select API.

**Step 2: Verify it compiles**

Run: `cd /home/bernd/Code/vibepit && go build ./...`
Expected: Clean build

**Step 3: Commit**

```bash
git add config/setup.go
git commit -m "feat: replace plain-text selector with huh-based interactive preset picker"
```

---

### Task 6: Wire up --reconfigure flag and update root command

**Files:**
- Modify: `cmd/root.go:26-44` (add `--reconfigure` flag)
- Modify: `cmd/root.go:90-108` (update first-run logic to pass `projectRoot`, handle reconfigure)

**Step 1: Add the flag**

Add to `RootFlags()`:

```go
&cli.BoolFlag{
	Name:  "reconfigure",
	Usage: "Re-run the network preset selector",
},
```

**Step 2: Update RootAction**

In `RootAction`, after loading config (line 93), add reconfigure handling:

```go
projectPath := config.DefaultProjectPath(projectRoot)

if cmd.Bool("reconfigure") {
	selected, err := config.RunReconfigure(projectPath, projectRoot)
	if err != nil {
		return fmt.Errorf("reconfigure: %w", err)
	}
	_ = selected
	cfg, err = config.Load(globalPath, projectPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
} else if _, err := os.Stat(projectPath); os.IsNotExist(err) {
	selected, err := config.RunFirstTimeSetup(projectRoot, projectPath)
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	cfg, err = config.Load(globalPath, projectPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	_ = selected
}
```

Note: `RunFirstTimeSetup` signature changes — it no longer takes `globalPath`, takes `projectDir` instead. Update the call accordingly.

**Step 3: Verify it compiles**

Run: `cd /home/bernd/Code/vibepit && go build ./...`
Expected: Clean build

**Step 4: Commit**

```bash
git add cmd/root.go
git commit -m "feat: add --reconfigure flag to re-run preset selector"
```

---

### Task 7: Update writeProjectConfig and comment text

**Files:**
- Modify: `config/setup.go:65-100` (update header comments and preset list)

**Step 1: Update the generated YAML comments**

The `writeProjectConfig` function currently references "Built-in presets: go, node, python". Update this to list the new preset names. Also update the commented example to use the new names:

```go
sb.WriteString("# Presets bundle common domains for a language ecosystem.\n")
sb.WriteString("# Use 'vibepit --reconfigure' to change presets interactively.\n")
sb.WriteString("# Available: default, anthropic, vcs-github, vcs-other, containers,\n")
sb.WriteString("# cloud, pkg-node, pkg-python, pkg-ruby, pkg-rust, pkg-go, pkg-jvm,\n")
sb.WriteString("# pkg-others, linux-distros, devtools, monitoring, cdn, schema, mcp\n")
```

**Step 2: Verify it compiles**

Run: `cd /home/bernd/Code/vibepit && go build ./...`
Expected: Clean build

**Step 3: Commit**

```bash
git add config/setup.go
git commit -m "docs: update generated YAML comments with new preset names"
```

---

### Task 8: Run full test suite and fix any issues

**Step 1: Run all tests**

Run: `cd /home/bernd/Code/vibepit && go test ./... -v`
Expected: All PASS

**Step 2: Run gofmt/goimports**

Run: `cd /home/bernd/Code/vibepit && gofmt -l . && goimports -l .`
Expected: No output (all files formatted)

**Step 3: Fix any issues found**

If tests fail or formatting issues exist, fix them.

**Step 4: Commit any fixes**

```bash
git add -A
git commit -m "fix: resolve test and formatting issues"
```
