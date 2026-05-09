# Self-Update Command Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enhance the `update` command to self-update the vibepit binary from GitHub Releases with cosign signature verification, in addition to the existing container image updates.

**Architecture:** New `selfupdate/` package handles release metadata fetching, version comparison, archive download with progress, SHA256 + cosign verification, and atomic binary replacement. The existing `cmd/update.go` is enhanced with new flags and orchestrates both binary and image update paths independently. A separate `release-metadata.yml` CI workflow generates per-version JSON metadata files deployed to `vibepit.dev/releases/` via GitHub Pages.

**Tech Stack:** Go, `golang.org/x/mod/semver`, `sigstore/sigstore-go`, `sigstore/cosign` (CI), `urfave/cli/v3`

**Spec:** `docs/superpowers/specs/2026-03-15-self-update-command-design.md`

---

## File Structure

### New files

| File | Responsibility |
|---|---|
| `selfupdate/version.go` | Semver comparison, dev build detection, channel membership |
| `selfupdate/version_test.go` | Tests for version comparison |
| `selfupdate/releases.go` | Types and HTTP client for channel index + version metadata |
| `selfupdate/releases_test.go` | Tests for metadata parsing and channel logic |
| `selfupdate/download.go` | Archive download with progress bar, size cap |
| `selfupdate/download_test.go` | Tests for download with mock HTTP server |
| `selfupdate/verify.go` | SHA256 checksum verification |
| `selfupdate/verify_test.go` | Tests for checksum verification |
| `selfupdate/replace.go` | Binary replacement: resolve path, permission check, atomic rename |
| `selfupdate/replace_test.go` | Tests for binary replacement |
| `selfupdate/cosign.go` | Cosign bundle verification via sigstore-go |
| `selfupdate/cosign_test.go` | Tests for cosign verification |
| `selfupdate/packagemanager.go` | Package manager detection |
| `selfupdate/packagemanager_test.go` | Tests for package manager detection |
| `.github/workflows/release-metadata.yml` | Workflow to generate release metadata on publish |
| `docs/changelogs/` | Directory for structured changelog YAML files |

### Modified files

| File | Change |
|---|---|
| `cmd/update.go` | Add flags, orchestrate binary + image update flows |
| `go.mod` | Add `sigstore/sigstore-go`, promote `golang.org/x/mod/semver` |
| `Makefile:61-66` | Update `release-publish` to upload `.bundle` files |
| `.github/workflows/build.yml:16,46-54` | Add `id-token: write`, cosign signing step |

---

## Chunk 1: Core selfupdate package

### Task 1: Version comparison

**Files:**
- Create: `selfupdate/version.go`
- Create: `selfupdate/version_test.go`

- [ ] **Step 1: Add `golang.org/x/mod` as direct dependency**

Run: `go get golang.org/x/mod/semver`

- [ ] **Step 2: Write failing tests for version comparison**

Create `selfupdate/version_test.go`:

```go
package selfupdate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsDevBuild(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    bool
	}{
		{"default version", "0.0", true},
		{"empty string", "", true},
		{"git describe suffix", "0.1.0-alpha.7-3-gabcdef", true},
		{"stable release", "0.2.0", false},
		{"prerelease", "0.1.0-alpha.7", false},
		{"prerelease rc", "0.2.0-rc.1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsDevBuild(tt.version))
		})
	}
}

func TestIsPrerelease(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    bool
	}{
		{"stable", "0.2.0", false},
		{"alpha", "0.1.0-alpha.7", true},
		{"rc", "0.2.0-rc.1", true},
		{"beta", "0.3.0-beta.2", true},
		{"dev build", "0.0", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsPrerelease(tt.version))
		})
	}
}

func TestShouldUpdate(t *testing.T) {
	tests := []struct {
		name        string
		current     string
		latest      string
		crossChannel bool
		want        bool
	}{
		{"newer available", "0.1.0", "0.2.0", false, true},
		{"already up to date", "0.2.0", "0.2.0", false, false},
		{"ahead of latest", "0.3.0", "0.2.0", false, false},
		{"dev build always updates", "0.0", "0.1.0", false, true},
		{"cross-channel always updates", "0.3.0-alpha.1", "0.2.0", true, true},
		{"cross-channel lower to higher", "0.1.0", "0.2.0-alpha.1", true, true},
		{"prerelease to newer prerelease", "0.1.0-alpha.1", "0.1.0-alpha.7", false, true},
		{"prerelease at latest", "0.1.0-alpha.7", "0.1.0-alpha.7", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ShouldUpdate(tt.current, tt.latest, tt.crossChannel))
		})
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./selfupdate/ -v -run 'TestIsDevBuild|TestIsPrerelease|TestShouldUpdate'`
Expected: FAIL — functions not defined.

- [ ] **Step 4: Implement version comparison**

Create `selfupdate/version.go`:

```go
package selfupdate

import (
	"golang.org/x/mod/semver"
)

// addV prepends "v" to a bare version string for use with golang.org/x/mod/semver,
// which requires the "v" prefix for all operations.
func addV(version string) string {
	return "v" + version
}

// IsDevBuild returns true if the version is not a valid semver string.
// This includes the default "0.0" and git describe outputs like
// "0.1.0-alpha.7-3-gabcdef".
func IsDevBuild(version string) bool {
	return !semver.IsValid(addV(version))
}

// IsPrerelease returns true if the version is a valid semver string
// with a prerelease suffix (e.g., "0.1.0-alpha.7").
func IsPrerelease(version string) bool {
	v := addV(version)
	return semver.IsValid(v) && semver.Prerelease(v) != ""
}

// ShouldUpdate returns true if the binary should be updated from current to
// latest. If crossChannel is true, the update is always offered (the user
// explicitly chose to switch channels). Dev builds always get offered updates.
func ShouldUpdate(current, latest string, crossChannel bool) bool {
	if IsDevBuild(current) || crossChannel {
		return true
	}
	return semver.Compare(addV(current), addV(latest)) < 0
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./selfupdate/ -v -run 'TestIsDevBuild|TestIsPrerelease|TestShouldUpdate'`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add selfupdate/version.go selfupdate/version_test.go go.mod go.sum
git commit -m "feat(selfupdate): add semver comparison and dev build detection"
```

---

### Task 2: Release metadata types and fetching

**Files:**
- Create: `selfupdate/releases.go`
- Create: `selfupdate/releases_test.go`

- [ ] **Step 1: Write failing tests for metadata types and parsing**

Create `selfupdate/releases_test.go`:

```go
package selfupdate

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelIndexParsing(t *testing.T) {
	data := `{
		"latest": "0.2.0",
		"releases": [
			{"version": "0.2.0", "timestamp": "2026-03-10T14:32:00Z"},
			{"version": "0.1.0", "timestamp": "2026-02-20T09:00:00Z"}
		]
	}`
	var idx ChannelIndex
	err := json.Unmarshal([]byte(data), &idx)
	require.NoError(t, err)
	assert.Equal(t, "0.2.0", idx.Latest)
	assert.Len(t, idx.Releases, 2)
	assert.Equal(t, "0.2.0", idx.Releases[0].Version)
}

func TestVersionMetadataParsing(t *testing.T) {
	data := `{
		"version": "0.2.0",
		"timestamp": "2026-03-10T14:32:00Z",
		"changelog": "- Added feature\n- Fixed bug",
		"assets": [
			{
				"os": "linux",
				"arch": "amd64",
				"url": "https://example.com/vibepit-0.2.0-linux-x86_64.tar.gz",
				"sha256": "abc123",
				"cosign_bundle_url": "https://example.com/vibepit-0.2.0-linux-x86_64.tar.gz.bundle"
			}
		]
	}`
	var meta VersionMetadata
	err := json.Unmarshal([]byte(data), &meta)
	require.NoError(t, err)
	assert.Equal(t, "0.2.0", meta.Version)
	assert.Len(t, meta.Assets, 1)
	assert.Equal(t, "linux", meta.Assets[0].OS)
	assert.Equal(t, "amd64", meta.Assets[0].Arch)
}

func TestFindAsset(t *testing.T) {
	meta := &VersionMetadata{
		Assets: []Asset{
			{OS: "linux", Arch: "amd64", URL: "https://example.com/linux-amd64.tar.gz"},
			{OS: "darwin", Arch: "arm64", URL: "https://example.com/darwin-arm64.tar.gz"},
		},
	}

	asset, err := meta.FindAsset("linux", "amd64")
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/linux-amd64.tar.gz", asset.URL)

	_, err = meta.FindAsset("windows", "amd64")
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./selfupdate/ -v -run 'TestChannelIndex|TestVersionMetadata|TestFindAsset'`
Expected: FAIL — types not defined.

- [ ] **Step 3: Implement metadata types**

Create `selfupdate/releases.go`:

```go
package selfupdate

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	baseURL        = "https://vibepit.dev/releases"
	httpTimeout    = 30 * time.Second
	ChannelStable  = "stable"
	ChannelPrerelease = "prerelease"
)

// ChannelIndex represents a channel index file (e.g., stable.json).
type ChannelIndex struct {
	Latest   string          `json:"latest"`
	Releases []ReleaseEntry  `json:"releases"`
}

// ReleaseEntry is a single entry in the channel index releases array.
type ReleaseEntry struct {
	Version   string `json:"version"`
	Timestamp string `json:"timestamp"`
}

// VersionMetadata represents a per-version metadata file (e.g., v0.2.0.json).
type VersionMetadata struct {
	Version   string  `json:"version"`
	Timestamp string  `json:"timestamp"`
	Changelog string  `json:"changelog"`
	Assets    []Asset `json:"assets"`
}

// Asset represents a platform-specific release asset.
type Asset struct {
	OS             string `json:"os"`
	Arch           string `json:"arch"`
	URL            string `json:"url"`
	SHA256         string `json:"sha256"`
	CosignBundleURL string `json:"cosign_bundle_url"`
}

// FindAsset returns the asset matching the given OS and architecture.
func (m *VersionMetadata) FindAsset(os, arch string) (*Asset, error) {
	for i := range m.Assets {
		if m.Assets[i].OS == os && m.Assets[i].Arch == arch {
			return &m.Assets[i], nil
		}
	}
	return nil, fmt.Errorf("no asset found for %s/%s", os, arch)
}

// Client fetches release metadata from vibepit.dev.
type Client struct {
	HTTPClient *http.Client
	BaseURL    string
}

// NewClient creates a new release metadata client.
func NewClient() *Client {
	return &Client{
		HTTPClient: &http.Client{Timeout: httpTimeout},
		BaseURL:    baseURL,
	}
}

// FetchChannelIndex fetches and parses a channel index file.
// Returns the index and a boolean indicating whether the file was found.
func (c *Client) FetchChannelIndex(channel string) (*ChannelIndex, bool, error) {
	url := fmt.Sprintf("%s/%s.json", c.BaseURL, channel)
	resp, err := c.HTTPClient.Get(url)
	if err != nil {
		return nil, false, fmt.Errorf("fetch channel index: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("fetch channel index: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("read channel index: %w", err)
	}

	var idx ChannelIndex
	if err := json.Unmarshal(body, &idx); err != nil {
		return nil, false, fmt.Errorf("parse channel index: %w", err)
	}
	return &idx, true, nil
}

// FetchVersionMetadata fetches and parses a version metadata file.
func (c *Client) FetchVersionMetadata(version string) (*VersionMetadata, error) {
	url := fmt.Sprintf("%s/v%s.json", c.BaseURL, version)
	resp, err := c.HTTPClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch version metadata: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("version %s not found; run 'vibepit update --list' to see available releases", version)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch version metadata: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read version metadata: %w", err)
	}

	var meta VersionMetadata
	if err := json.Unmarshal(body, &meta); err != nil {
		return nil, fmt.Errorf("parse version metadata: %w", err)
	}
	return &meta, nil
}

// ResolveChannel determines which channel to use and fetches the index.
// Implements fallback logic: if stable is not found and --pre was not
// explicitly set, falls back to prerelease.
func (c *Client) ResolveChannel(preferPre bool) (*ChannelIndex, string, error) {
	if preferPre {
		idx, found, err := c.FetchChannelIndex(ChannelPrerelease)
		if err != nil {
			return nil, "", err
		}
		if !found {
			return nil, "", fmt.Errorf("no prerelease versions are available")
		}
		return idx, ChannelPrerelease, nil
	}

	// Default: try stable, fall back to prerelease.
	idx, found, err := c.FetchChannelIndex(ChannelStable)
	if err != nil {
		return nil, "", err
	}
	if found {
		return idx, ChannelStable, nil
	}

	idx, found, err = c.FetchChannelIndex(ChannelPrerelease)
	if err != nil {
		return nil, "", err
	}
	if !found {
		return nil, "", fmt.Errorf("no releases are available")
	}
	return idx, ChannelPrerelease, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./selfupdate/ -v -run 'TestChannelIndex|TestVersionMetadata|TestFindAsset'`
Expected: PASS

- [ ] **Step 5: Write tests for HTTP fetching with mock server**

Add to `selfupdate/releases_test.go`:

```go
func TestFetchChannelIndex(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stable.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"latest":"0.2.0","releases":[{"version":"0.2.0","timestamp":"2026-03-10T14:32:00Z"}]}`)
	})
	mux.HandleFunc("GET /prerelease.json", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := &Client{HTTPClient: srv.Client(), BaseURL: srv.URL}

	idx, found, err := client.FetchChannelIndex(ChannelStable)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "0.2.0", idx.Latest)

	_, found, err = client.FetchChannelIndex(ChannelPrerelease)
	require.NoError(t, err)
	assert.False(t, found)
}

func TestResolveChannelFallback(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stable.json", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("GET /prerelease.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"latest":"0.1.0-alpha.7","releases":[{"version":"0.1.0-alpha.7","timestamp":"2026-02-15T12:00:00Z"}]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := &Client{HTTPClient: srv.Client(), BaseURL: srv.URL}

	// Default channel falls back to prerelease when stable is missing.
	idx, channel, err := client.ResolveChannel(false)
	require.NoError(t, err)
	assert.Equal(t, ChannelPrerelease, channel)
	assert.Equal(t, "0.1.0-alpha.7", idx.Latest)

	// Explicit --pre with missing prerelease is an error.
	mux2 := http.NewServeMux()
	mux2.HandleFunc("GET /prerelease.json", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv2 := httptest.NewServer(mux2)
	defer srv2.Close()

	client2 := &Client{HTTPClient: srv2.Client(), BaseURL: srv2.URL}
	_, _, err = client2.ResolveChannel(true)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no prerelease versions")
}

func TestFetchVersionMetadata(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v0.2.0.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"version":"0.2.0","timestamp":"2026-03-10T14:32:00Z","changelog":"- Fix","assets":[]}`)
	})
	mux.HandleFunc("GET /v9.9.9.json", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := &Client{HTTPClient: srv.Client(), BaseURL: srv.URL}

	meta, err := client.FetchVersionMetadata("0.2.0")
	require.NoError(t, err)
	assert.Equal(t, "0.2.0", meta.Version)

	_, err = client.FetchVersionMetadata("9.9.9")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
```

Add `"fmt"`, `"net/http"`, and `"net/http/httptest"` to the imports.

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./selfupdate/ -v -run 'TestFetch|TestResolve'`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add selfupdate/releases.go selfupdate/releases_test.go
git commit -m "feat(selfupdate): add release metadata types and HTTP client"
```

---

### Task 3: SHA256 checksum verification

**Files:**
- Create: `selfupdate/verify.go`
- Create: `selfupdate/verify_test.go`

- [ ] **Step 1: Write failing tests for checksum verification**

Create `selfupdate/verify_test.go`:

```go
package selfupdate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifySHA256(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "testfile")
	content := []byte("hello world\n")
	require.NoError(t, os.WriteFile(path, content, 0644))

	// Correct checksum for "hello world\n"
	err := VerifySHA256(path, "ecf701f727d9e2d77c4aa49ac6fbbcc997278aca010bddeeb961c10cf54d435a")
	assert.NoError(t, err)

	// Wrong checksum
	err = VerifySHA256(path, "0000000000000000000000000000000000000000000000000000000000000000")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "checksum mismatch")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./selfupdate/ -v -run TestVerifySHA256`
Expected: FAIL — function not defined.

- [ ] **Step 3: Implement checksum verification**

Create `selfupdate/verify.go`:

```go
package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// VerifySHA256 checks that the SHA256 hash of the file at path matches the
// expected hex-encoded checksum.
func VerifySHA256(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open file for checksum: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("read file for checksum: %w", err)
	}

	actual := hex.EncodeToString(h.Sum(nil))
	if actual != expected {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expected, actual)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./selfupdate/ -v -run TestVerifySHA256`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add selfupdate/verify.go selfupdate/verify_test.go
git commit -m "feat(selfupdate): add SHA256 checksum verification"
```

---

### Task 4: Package manager detection

**Files:**
- Create: `selfupdate/packagemanager.go`
- Create: `selfupdate/packagemanager_test.go`

- [ ] **Step 1: Write failing tests**

Create `selfupdate/packagemanager_test.go`:

```go
package selfupdate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDetectPackageManager(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		want   string
		managed bool
	}{
		{"homebrew arm", "/opt/homebrew/bin/vibepit", "Homebrew", true},
		{"homebrew intel", "/usr/local/Cellar/vibepit/0.1.0/bin/vibepit", "Homebrew", true},
		{"system usr bin", "/usr/bin/vibepit", "system package manager", true},
		{"system usr sbin", "/usr/sbin/vibepit", "system package manager", true},
		{"nix", "/nix/store/abc123-vibepit/bin/vibepit", "Nix", true},
		{"snap", "/snap/vibepit/123/vibepit", "Snap", true},
		{"user local", "/usr/local/bin/vibepit", "", false},
		{"home dir", "/home/user/bin/vibepit", "", false},
		{"current dir", "./vibepit", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, managed := DetectPackageManager(tt.path)
			assert.Equal(t, tt.managed, managed)
			if managed {
				assert.Equal(t, tt.want, manager)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./selfupdate/ -v -run TestDetectPackageManager`
Expected: FAIL — function not defined.

- [ ] **Step 3: Implement package manager detection**

Create `selfupdate/packagemanager.go`:

```go
package selfupdate

import "strings"

// packageManagerPrefixes maps path prefixes to package manager names.
var packageManagerPrefixes = []struct {
	prefix  string
	manager string
}{
	{"/opt/homebrew/", "Homebrew"},
	{"/usr/local/Cellar/", "Homebrew"},
	{"/usr/bin/", "system package manager"},
	{"/usr/sbin/", "system package manager"},
	{"/nix/store/", "Nix"},
	{"/snap/", "Snap"},
}

// DetectPackageManager checks if the binary path is inside a known
// package-managed prefix. Returns the manager name and whether it was detected.
func DetectPackageManager(binaryPath string) (string, bool) {
	for _, pm := range packageManagerPrefixes {
		if strings.HasPrefix(binaryPath, pm.prefix) {
			return pm.manager, true
		}
	}
	return "", false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./selfupdate/ -v -run TestDetectPackageManager`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add selfupdate/packagemanager.go selfupdate/packagemanager_test.go
git commit -m "feat(selfupdate): add package manager detection"
```

---

### Task 5: Binary replacement

**Files:**
- Create: `selfupdate/replace.go`
- Create: `selfupdate/replace_test.go`

- [ ] **Step 1: Write failing tests for binary replacement**

Create `selfupdate/replace_test.go`:

```go
package selfupdate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplaceBinary(t *testing.T) {
	dir := t.TempDir()
	oldBin := filepath.Join(dir, "vibepit")
	require.NoError(t, os.WriteFile(oldBin, []byte("old"), 0755))

	newBin := filepath.Join(dir, "vibepit-new")
	require.NoError(t, os.WriteFile(newBin, []byte("new"), 0755))

	err := ReplaceBinary(oldBin, newBin)
	require.NoError(t, err)

	content, err := os.ReadFile(oldBin)
	require.NoError(t, err)
	assert.Equal(t, "new", string(content))

	// New temp file should be cleaned up.
	_, err = os.Stat(newBin)
	assert.True(t, os.IsNotExist(err))

	// Permissions should be preserved.
	info, err := os.Stat(oldBin)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm())
}

func TestReplaceBinaryReadOnlyDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test as root")
	}
	dir := t.TempDir()
	oldBin := filepath.Join(dir, "vibepit")
	require.NoError(t, os.WriteFile(oldBin, []byte("old"), 0755))

	newBin := filepath.Join(dir, "vibepit-new")
	require.NoError(t, os.WriteFile(newBin, []byte("new"), 0755))

	// Remove write permission from directory after writing both files.
	require.NoError(t, os.Chmod(dir, 0555))
	t.Cleanup(func() { os.Chmod(dir, 0755) })

	err := ReplaceBinary(oldBin, newBin)
	assert.Error(t, err)
}

func TestCheckWritePermission(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test as root")
	}

	writable := t.TempDir()
	assert.NoError(t, CheckWritePermission(writable))

	readOnly := t.TempDir()
	require.NoError(t, os.Chmod(readOnly, 0555))
	t.Cleanup(func() { os.Chmod(readOnly, 0755) })
	assert.Error(t, CheckWritePermission(readOnly))
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./selfupdate/ -v -run 'TestReplaceBinary|TestCheckWrite'`
Expected: FAIL — functions not defined.

- [ ] **Step 3: Implement binary replacement**

Create `selfupdate/replace.go`:

```go
package selfupdate

import (
	"fmt"
	"os"
	"path/filepath"
)

// CheckWritePermission checks if the directory is writable by attempting to
// create a temporary file.
func CheckWritePermission(dir string) error {
	f, err := os.CreateTemp(dir, ".vibepit-permission-check-*")
	if err != nil {
		return fmt.Errorf("no write permission to %s: try running with sudo or move the binary to a writable location", dir)
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return nil
}

// ReplaceBinary atomically replaces the binary at targetPath with the new
// binary at newPath using os.Rename (atomic on POSIX). Preserves the original
// file permissions.
func ReplaceBinary(targetPath, newPath string) error {
	// Get original permissions.
	info, err := os.Stat(targetPath)
	if err != nil {
		return fmt.Errorf("stat current binary: %w", err)
	}
	origMode := info.Mode().Perm()

	// Set permissions on new binary before rename.
	if err := os.Chmod(newPath, origMode); err != nil {
		return fmt.Errorf("set permissions on new binary: %w", err)
	}

	// Atomic rename.
	if err := os.Rename(newPath, targetPath); err != nil {
		return fmt.Errorf("replace binary: %w", err)
	}
	return nil
}

// ResolveBinaryPath returns the absolute path to the currently running binary,
// resolving any symlinks.
func ResolveBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("resolve symlinks: %w", err)
	}
	return resolved, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./selfupdate/ -v -run 'TestReplaceBinary|TestCheckWrite'`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add selfupdate/replace.go selfupdate/replace_test.go
git commit -m "feat(selfupdate): add atomic binary replacement"
```

---

### Task 6: Archive download with progress bar

**Files:**
- Create: `selfupdate/download.go`
- Create: `selfupdate/download_test.go`

- [ ] **Step 1: Write failing tests for download**

Create `selfupdate/download_test.go`:

```go
package selfupdate

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDownloadArchive(t *testing.T) {
	content := "fake archive content"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		fmt.Fprint(w, content)
	}))
	defer srv.Close()

	dir := t.TempDir()
	path, err := DownloadArchive(srv.URL, dir, false)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, content, string(data))
}

func TestDownloadArchiveTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "300000000") // 300MB > 256MB limit
		fmt.Fprint(w, "data")
	}))
	defer srv.Close()

	dir := t.TempDir()
	_, err := DownloadArchive(srv.URL, dir, false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum")
}

func TestDownloadArchiveStreamCap(t *testing.T) {
	// Use a small cap for testing to avoid transferring large amounts of data.
	origMax := maxArchiveSizeLimit
	maxArchiveSizeLimit = 1024 // 1 KB for test
	t.Cleanup(func() { maxArchiveSizeLimit = origMax })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Don't set Content-Length. Write more than test cap.
		data := strings.Repeat("x", 512)
		for i := 0; i < 10; i++ { // 5 KB > 1 KB test cap
			w.Write([]byte(data))
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	_, err := DownloadArchive(srv.URL, dir, false)
	assert.Error(t, err)
}

func createTestTarball(t *testing.T, dir, filename, content string) string {
	t.Helper()
	tarPath := filepath.Join(dir, "test.tar.gz")
	f, err := os.Create(tarPath)
	require.NoError(t, err)
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	hdr := &tar.Header{
		Name: filename,
		Mode: 0755,
		Size: int64(len(content)),
	}
	require.NoError(t, tw.WriteHeader(hdr))
	_, err = tw.Write([]byte(content))
	require.NoError(t, err)
	return tarPath
}

func TestExtractBinary(t *testing.T) {
	dir := t.TempDir()
	tarPath := createTestTarball(t, dir, "vibepit", "binary content")

	outDir := t.TempDir()
	binPath, err := ExtractBinary(tarPath, outDir, "vibepit")
	require.NoError(t, err)

	content, err := os.ReadFile(binPath)
	require.NoError(t, err)
	assert.Equal(t, "binary content", string(content))
}

func TestExtractBinaryPathTraversal(t *testing.T) {
	dir := t.TempDir()
	tarPath := createTestTarball(t, dir, "../../../etc/malicious", "bad")

	outDir := t.TempDir()
	_, err := ExtractBinary(tarPath, outDir, "vibepit")
	assert.Error(t, err)
}
```

Add `"archive/tar"` and `"compress/gzip"` to the test file imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./selfupdate/ -v -run 'TestDownload|TestExtract'`
Expected: FAIL — functions not defined.

- [ ] **Step 3: Implement download and extraction**

Create `selfupdate/download.go`:

```go
package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// maxArchiveSizeLimit is the maximum allowed archive size in bytes.
// It is a var (not const) so tests can override it.
var maxArchiveSizeLimit int64 = 256 * 1024 * 1024 // 256 MB

// DownloadArchive downloads a file from url to a temp file in dir.
// Returns the path to the downloaded file. If isTTY is true, displays a
// progress bar; otherwise uses line-based progress.
// Checks Content-Length header and caps streaming at maxArchiveSizeLimit.
func DownloadArchive(url, dir string, isTTY bool) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("download archive: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download archive: HTTP %d", resp.StatusCode)
	}

	// Check Content-Length if present.
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		size, err := strconv.ParseInt(cl, 10, 64)
		if err == nil && size > maxArchiveSizeLimit {
			return "", fmt.Errorf("archive size %d bytes exceeds maximum %d bytes", size, maxArchiveSizeLimit)
		}
	}

	f, err := os.CreateTemp(dir, ".vibepit-download-*")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	path := f.Name()

	// Cap the reader at maxArchiveSize as defense-in-depth.
	reader := io.LimitReader(resp.Body, maxArchiveSizeLimit+1)

	// TODO: Add progress bar (isTTY) or line-based progress (!isTTY).
	// For now, just copy.
	n, err := io.Copy(f, reader)
	f.Close()
	if err != nil {
		os.Remove(path)
		return "", fmt.Errorf("write archive: %w", err)
	}
	if n > maxArchiveSizeLimit {
		os.Remove(path)
		return "", fmt.Errorf("archive size exceeds maximum %d bytes", maxArchiveSizeLimit)
	}

	return path, nil
}

// ExtractBinary extracts the named binary from a .tar.gz archive to a temp file
// in outDir. Validates that the extracted path contains no separators or
// traversal components.
func ExtractBinary(archivePath, outDir, binaryName string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read tar: %w", err)
		}

		// Reject path traversal first.
		if strings.Contains(hdr.Name, "..") {
			return "", fmt.Errorf("archive contains path traversal: %s", hdr.Name)
		}

		// Get the base name and match.
		name := filepath.Base(hdr.Name)
		if name != binaryName {
			continue
		}

		outPath, err := os.CreateTemp(outDir, ".vibepit-extract-*")
		if err != nil {
			return "", fmt.Errorf("create temp file: %w", err)
		}

		if _, err := io.Copy(outPath, tr); err != nil {
			outPath.Close()
			os.Remove(outPath.Name())
			return "", fmt.Errorf("extract binary: %w", err)
		}
		outPath.Close()
		return outPath.Name(), nil
	}

	return "", fmt.Errorf("binary %q not found in archive", binaryName)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./selfupdate/ -v -run 'TestDownload|TestExtract'`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add selfupdate/download.go selfupdate/download_test.go
git commit -m "feat(selfupdate): add archive download and extraction"
```

---

## Chunk 2: Cosign verification and command wiring

### Task 7: Cosign bundle verification

**Files:**
- Create: `selfupdate/cosign.go`
- Create: `selfupdate/cosign_test.go`

- [ ] **Step 1: Add sigstore-go dependency**

Run: `go get github.com/sigstore/sigstore-go`

- [ ] **Step 2: Write failing test for cosign verification interface**

Create `selfupdate/cosign_test.go`:

```go
package selfupdate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVerifyCosignBundleBadURL(t *testing.T) {
	err := VerifyCosignBundle("/nonexistent/file", "http://invalid.test/bundle")
	assert.Error(t, err)
}
```

Note: Full integration tests for cosign verification require real signed
artifacts. This test validates error handling. The implementer should consult
the `sigstore-go` documentation at `https://pkg.go.dev/github.com/sigstore/sigstore-go`
for the `CertificateIdentity` API and add integration tests when real signed
release artifacts are available.

- [ ] **Step 3: Implement cosign verification**

Create `selfupdate/cosign.go`. This file wraps `sigstore-go` to verify a cosign
bundle against Sigstore's public good instance. The implementer must:

1. Download the bundle from the `cosign_bundle_url`.
2. Use `sigstore-go`'s `bundle` package to load the bundle.
3. Use `sigstore-go`'s `verify` package with `CertificateIdentity` to verify:
   - Issuer: `https://token.actions.githubusercontent.com`
   - SAN regex: `^https://github.com/bernd/vibepit/.github/workflows/build.yml@`
4. Verify against Sigstore's trusted root (public good instance).
5. Return an error if verification fails.

```go
package selfupdate

import (
	"fmt"
	"io"
	"net/http"
	"os"
)

// VerifyCosignBundle verifies the cosign bundle for the archive at archivePath.
// Downloads the bundle from bundleURL and verifies against Sigstore's public
// good instance.
//
// Verification checks:
// - Certificate issuer: https://token.actions.githubusercontent.com
// - Certificate SAN (prefix): https://github.com/bernd/vibepit/.github/workflows/build.yml
func VerifyCosignBundle(archivePath, bundleURL string) error {
	// Download bundle.
	bundlePath, err := downloadBundle(bundleURL)
	if err != nil {
		return err
	}
	defer os.Remove(bundlePath)

	// TODO: Implement sigstore-go verification.
	// The implementer should:
	// 1. Load bundle with sigstore-go's bundle package
	// 2. Get trusted root from sigstore TUF
	// 3. Create verifier with CertificateIdentity policy
	// 4. Verify the artifact against the bundle
	//
	// See: https://pkg.go.dev/github.com/sigstore/sigstore-go
	// Example pattern:
	//   root, _ := root.FetchTrustedRoot()
	//   verifierConfig := verify.VerifierConfig{...}
	//   verifier, _ := verify.NewSignedEntityVerifier(root, verifierConfig)
	//   policy := verify.NewPolicy(verify.WithCertificateIdentity(...))
	//   result, _ := verifier.Verify(entity, policy)

	return fmt.Errorf("cosign verification not yet implemented")
}

func downloadBundle(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("download cosign bundle: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download cosign bundle: HTTP %d", resp.StatusCode)
	}

	f, err := os.CreateTemp("", ".vibepit-bundle-*")
	if err != nil {
		return "", fmt.Errorf("create temp bundle file: %w", err)
	}

	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", fmt.Errorf("write bundle: %w", err)
	}
	f.Close()
	return f.Name(), nil
}
```

The `TODO` block contains detailed guidance for the implementer. The full
sigstore-go API requires consulting the library docs since the API may have
changed since this plan was written.

**Note:** This task produces a scaffold/stub. The full sigstore-go integration
is a follow-up task that requires real signed release artifacts to test against.
Task 8's `runBinaryUpdate` should skip cosign verification if the bundle URL is
empty, allowing the update flow to work end-to-end before cosign signing is
deployed in CI.

- [ ] **Step 4: Run test to verify error handling works**

Run: `go test ./selfupdate/ -v -run TestVerifyCosignBundle`
Expected: PASS (error for nonexistent file)

- [ ] **Step 5: Commit**

```bash
git add selfupdate/cosign.go selfupdate/cosign_test.go go.mod go.sum
git commit -m "feat(selfupdate): add cosign verification scaffold"
```

---

### Task 8: Enhanced update command

**Files:**
- Modify: `cmd/update.go`

This task rewires the existing `update` command with all the new flags and
orchestration logic.

- [ ] **Step 1: Write failing test for flag validation**

Create `cmd/update_test.go`:

```go
package cmd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/urfave/cli/v3"
)

func TestUpdateFlagValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			"use with images is error",
			[]string{"vibepit", "update", "--use", "0.1.0", "--images"},
			"--use cannot be combined with --images",
		},
		{
			"list with check is error",
			[]string{"vibepit", "update", "--list", "--check"},
			"--list and --check are mutually exclusive",
		},
		{
			"list with use is error",
			[]string{"vibepit", "update", "--list", "--use", "0.1.0"},
			"--list and --use are mutually exclusive",
		},
		{
			"check with use is error",
			[]string{"vibepit", "update", "--check", "--use", "0.1.0"},
			"--check and --use are mutually exclusive",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := RootCommand()
			err := app.Run(context.Background(), tt.args)
			if assert.Error(t, err) {
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -v -run TestUpdateFlagValidation`
Expected: FAIL — new flags don't exist yet.

- [ ] **Step 3: Implement enhanced update command**

Replace `cmd/update.go` with the new implementation. Key structure:

```go
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"

	"github.com/bernd/vibepit/config"
	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/selfupdate"
	"github.com/urfave/cli/v3"
	"golang.org/x/term"
)

func UpdateCommand() *cli.Command {
	return &cli.Command{
		Name:     "update",
		Usage:    "Update binary and pull latest container images",
		Category: "Utilities",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "Skip confirmation prompt"},
			&cli.BoolFlag{Name: "bin", Usage: "Update binary only"},
			&cli.BoolFlag{Name: "images", Usage: "Update images only"},
			&cli.StringFlag{Name: "use", Usage: "Install a specific version"},
			&cli.BoolFlag{Name: "list", Usage: "List available releases"},
			&cli.BoolFlag{Name: "check", Usage: "Check for updates"},
			&cli.BoolFlag{Name: "pre", Usage: "Use prerelease channel"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return runUpdate(ctx, cmd)
		},
	}
}

func runUpdate(ctx context.Context, cmd *cli.Command) error {
	// Validate flag combinations.
	if err := validateUpdateFlags(cmd); err != nil {
		return err
	}

	useVersion := cmd.String("use")
	list := cmd.Bool("list")
	check := cmd.Bool("check")
	pre := cmd.Bool("pre")
	binOnly := cmd.Bool("bin") || useVersion != ""
	imagesOnly := cmd.Bool("images")
	yes := cmd.Bool("yes")

	client := selfupdate.NewClient()

	// Handle --list.
	if list {
		return runList(client, pre)
	}

	// Handle --check.
	if check {
		return runCheck(client, pre)
	}

	// Determine what to update.
	doBin := !imagesOnly
	doImages := !binOnly

	// Binary update path.
	if doBin {
		if err := runBinaryUpdate(ctx, client, useVersion, pre, yes); err != nil {
			return err
		}
	}

	// Image update path.
	if doImages {
		if err := runImageUpdate(ctx); err != nil {
			return err
		}
	}

	return nil
}

func validateUpdateFlags(cmd *cli.Command) error {
	use := cmd.String("use")
	list := cmd.Bool("list")
	check := cmd.Bool("check")
	images := cmd.Bool("images")

	if use != "" && images {
		return fmt.Errorf("--use cannot be combined with --images")
	}
	if list && check {
		return fmt.Errorf("--list and --check are mutually exclusive")
	}
	if list && use != "" {
		return fmt.Errorf("--list and --use are mutually exclusive")
	}
	if check && use != "" {
		return fmt.Errorf("--check and --use are mutually exclusive")
	}
	return nil
}

func runList(client *selfupdate.Client, pre bool) error {
	idx, _, err := client.ResolveChannel(pre)
	if err != nil {
		return err
	}

	fmt.Printf("%-20s %s\n", "VERSION", "TIMESTAMP")
	for _, r := range idx.Releases {
		suffix := ""
		if r.Version == config.Version {
			suffix = "  (installed)"
		}
		fmt.Printf("%-20s %s%s\n", r.Version, r.Timestamp, suffix)
	}
	return nil
}

func runCheck(client *selfupdate.Client, pre bool) error {
	idx, channel, err := client.ResolveChannel(pre)
	if err != nil {
		return err
	}

	crossChannel := isCrossChannel(config.Version, channel)
	if selfupdate.ShouldUpdate(config.Version, idx.Latest, crossChannel) {
		fmt.Printf("Update available: %s -> %s (%s channel)\n", config.Version, idx.Latest, channel)
	} else {
		fmt.Println("Already up to date.")
	}
	return nil
}

func runBinaryUpdate(ctx context.Context, client *selfupdate.Client, useVersion string, pre, yes bool) error {
	var meta *selfupdate.VersionMetadata

	if useVersion != "" {
		// Direct version fetch, bypass channel logic.
		var err error
		meta, err = client.FetchVersionMetadata(useVersion)
		if err != nil {
			return err
		}
	} else {
		// Channel-based update check.
		idx, channel, err := client.ResolveChannel(pre)
		if err != nil {
			return err
		}

		crossChannel := isCrossChannel(config.Version, channel)
		if !selfupdate.ShouldUpdate(config.Version, idx.Latest, crossChannel) {
			fmt.Println("Binary is up to date.")
			return nil
		}

		meta, err = client.FetchVersionMetadata(idx.Latest)
		if err != nil {
			return err
		}
	}

	// Find asset for current platform.
	asset, err := meta.FindAsset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	// Display update info.
	fmt.Printf("Current version: %s\n", config.Version)
	fmt.Printf("Target version:  %s (%s)\n", meta.Version, meta.Timestamp)
	if meta.Changelog != "" {
		fmt.Printf("\nChangelog:\n%s\n", meta.Changelog)
	}

	// Confirm.
	if !yes {
		fmt.Printf("\nInstall vibepit v%s? [y/N] ", meta.Version)
		var answer string
		fmt.Scanln(&answer)
		if answer != "y" && answer != "Y" {
			fmt.Println("Update cancelled.")
			return nil
		}
	}

	// Resolve binary path and check permissions.
	binPath, err := selfupdate.ResolveBinaryPath()
	if err != nil {
		return err
	}

	if manager, managed := selfupdate.DetectPackageManager(binPath); managed {
		return fmt.Errorf("vibepit appears to be managed by %s; use your package manager to update instead", manager)
	}

	binDir := filepath.Dir(binPath)
	if err := selfupdate.CheckWritePermission(binDir); err != nil {
		return err
	}

	// Download.
	isTTY := term.IsTerminal(int(os.Stdout.Fd()))
	archivePath, err := selfupdate.DownloadArchive(asset.URL, binDir, isTTY)
	if err != nil {
		return err
	}
	defer os.Remove(archivePath)

	// Verify SHA256.
	if err := selfupdate.VerifySHA256(archivePath, asset.SHA256); err != nil {
		return err
	}

	// Verify cosign bundle (skip if bundle URL is empty — cosign signing
	// may not yet be deployed in CI).
	if asset.CosignBundleURL != "" {
		if err := selfupdate.VerifyCosignBundle(archivePath, asset.CosignBundleURL); err != nil {
			return err
		}
	}

	// Extract and replace.
	extractedPath, err := selfupdate.ExtractBinary(archivePath, binDir, "vibepit")
	if err != nil {
		return err
	}

	if err := selfupdate.ReplaceBinary(binPath, extractedPath); err != nil {
		os.Remove(extractedPath)
		return err
	}

	fmt.Printf("Updated vibepit %s -> %s\n", config.Version, meta.Version)
	return nil
}

func runImageUpdate(ctx context.Context) error {
	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("cannot determine current user: %w", err)
	}

	client, err := ctr.NewClient()
	if err != nil {
		return err
	}
	defer client.Close()

	if err := client.PullImage(ctx, imageName(u), false); err != nil {
		return fmt.Errorf("pull image: %w", err)
	}
	if err := client.PullImage(ctx, ctr.ProxyImage, false); err != nil {
		return fmt.Errorf("pull image: %w", err)
	}
	fmt.Println("Container images updated.")
	return nil
}

// isCrossChannel returns true if the current version is on a different channel
// than the one being checked.
func isCrossChannel(currentVersion, targetChannel string) bool {
	if selfupdate.IsDevBuild(currentVersion) {
		return false
	}
	currentIsPre := selfupdate.IsPrerelease(currentVersion)
	targetIsPre := targetChannel == selfupdate.ChannelPrerelease
	return currentIsPre != targetIsPre
}
```

Note: The `imageName` function is already defined in `cmd/run.go`.

- [ ] **Step 4: Run flag validation tests**

Run: `go test ./cmd/ -v -run TestUpdateFlagValidation`
Expected: PASS

- [ ] **Step 5: Run full test suite**

Run: `make test`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add cmd/update.go cmd/update_test.go
git commit -m "feat: enhance update command with self-update, list, and check"
```

---

## Chunk 3: CI pipeline and changelog system

### Task 9: Add cosign signing to build.yml

**Files:**
- Modify: `.github/workflows/build.yml`

- [ ] **Step 1: Add id-token permission and cosign signing step**

In `.github/workflows/build.yml`, add `id-token: write` to the permissions
block and add a cosign signing step between build and release:

```yaml
permissions:
  contents: "write"
  id-token: "write"
```

Add after the "Build" step and before the "Release" step:

```yaml
      - name: "Install Cosign"
        if: "startsWith(github.ref, 'refs/tags/')"
        uses: "sigstore/cosign-installer@v3"

      - name: "Sign Archives"
        if: "startsWith(github.ref, 'refs/tags/')"
        run: |
          for archive in dist/*.tar.gz; do
            cosign sign-blob --yes --bundle "${archive}.bundle" "$archive"
          done
```

- [ ] **Step 2: Update release-publish to upload .bundle files**

In `Makefile`, update the `release-publish` target to include `.bundle` files:

Change line 66 from:
```makefile
		v$(VERSION) dist/*.tar.gz dist/checksums.txt
```
to:
```makefile
		v$(VERSION) dist/*.tar.gz dist/*.bundle dist/checksums.txt
```

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/build.yml Makefile
git commit -m "ci: add cosign signing to release pipeline"
```

---

### Task 10: Create release-metadata.yml workflow

**Files:**
- Create: `.github/workflows/release-metadata.yml`

- [ ] **Step 1: Create the workflow file**

Create `.github/workflows/release-metadata.yml`:

```yaml
name: "Release Metadata"

on:
  release:
    types: ["published"]

permissions:
  contents: "write"

concurrency:
  group: "release-metadata"
  cancel-in-progress: false

defaults:
  run:
    shell: "bash"

jobs:
  generate:
    runs-on: "ubuntu-slim"

    steps:
      - name: "Checkout repository"
        uses: "actions/checkout@v6"
        with:
          ref: "main"
          fetch-depth: 0
          token: "${{ secrets.GITHUB_TOKEN }}"

      - name: "Setup Python"
        uses: "actions/setup-python@v6"
        with:
          python-version: "3.x"

      - name: "Install PyYAML"
        run: "pip install pyyaml"

      - name: "Generate release metadata"
        env:
          GH_TOKEN: "${{ github.token }}"
          VERSION: "${{ github.event.release.tag_name }}"
        run: |
          # Strip v prefix for bare version.
          BARE_VERSION="${VERSION#v}"

          # Get timestamp from git tag.
          TIMESTAMP=$(git log -1 --format=%aI "$VERSION")

          # Download checksums.txt from release assets.
          gh release download "$VERSION" --pattern "checksums.txt" --dir /tmp

          # Parse changelog YAML and render to plain text.
          CHANGELOG_FILE="docs/changelogs/${VERSION}.yml"
          if [ -f "$CHANGELOG_FILE" ]; then
            CHANGELOG=$(python3 -c "
          import yaml, json
          with open('$CHANGELOG_FILE') as f:
              data = yaml.safe_load(f)
          lines = []
          for category in ['added', 'changed', 'fixed', 'deprecated', 'removed', 'security']:
              entries = data.get(category, [])
              if entries:
                  lines.append(category.capitalize() + ':')
                  for entry in entries:
                      lines.append('- ' + entry['description'])
          print(json.dumps('\n'.join(lines)))
          ")
          else
            CHANGELOG='""'
          fi

          # Build assets array from release assets.
          ASSETS=$(python3 -c "
          import json, sys

          checksums = {}
          with open('/tmp/checksums.txt') as f:
              for line in f:
                  parts = line.strip().split()
                  if len(parts) == 2:
                      checksums[parts[1]] = parts[0]

          platform_map = {
              'linux-x86_64': ('linux', 'amd64'),
              'linux-aarch64': ('linux', 'arm64'),
              'darwin-x86_64': ('darwin', 'amd64'),
              'darwin-aarch64': ('darwin', 'arm64'),
          }

          assets = []
          base_url = 'https://github.com/bernd/vibepit/releases/download/${VERSION}'
          for suffix, (os_name, arch) in platform_map.items():
              tarball = 'vibepit-${BARE_VERSION}-' + suffix + '.tar.gz'
              if tarball in checksums:
                  assets.append({
                      'os': os_name,
                      'arch': arch,
                      'url': base_url + '/' + tarball,
                      'sha256': checksums[tarball],
                      'cosign_bundle_url': base_url + '/' + tarball + '.bundle',
                  })

          print(json.dumps(assets, indent=2))
          ")

          # Write version metadata file.
          mkdir -p docs/content/releases
          python3 -c "
          import json
          meta = {
              'version': '${BARE_VERSION}',
              'timestamp': '${TIMESTAMP}',
              'changelog': ${CHANGELOG},
              'assets': json.loads('''${ASSETS}'''),
          }
          with open('docs/content/releases/v${BARE_VERSION}.json', 'w') as f:
              json.dump(meta, f, indent=2)
          "

          # Update channel index file.
          python3 -c "
          import json, sys

          version = '${BARE_VERSION}'
          timestamp = '${TIMESTAMP}'

          # Determine channel.
          channel = 'prerelease' if '-' in version else 'stable'
          channel_file = f'docs/content/releases/{channel}.json'

          try:
              with open(channel_file) as f:
                  idx = json.load(f)
          except FileNotFoundError:
              idx = {'latest': '', 'releases': []}

          idx['latest'] = version
          idx['releases'].insert(0, {'version': version, 'timestamp': timestamp})

          with open(channel_file, 'w') as f:
              json.dump(idx, f, indent=2)
          "

      - name: "Commit and push metadata"
        run: |
          git config user.name "github-actions[bot]"
          git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
          git add docs/content/releases/
          git commit -m "chore: generate release metadata for ${{ github.event.release.tag_name }}"
          git push
```

- [ ] **Step 2: Create changelog directory**

```bash
mkdir -p docs/changelogs
touch docs/changelogs/.gitkeep
```

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/release-metadata.yml docs/changelogs/.gitkeep
git commit -m "ci: add release metadata generation workflow and changelog directory"
```

---

### Task 11: Add progress bar to download

**Files:**
- Modify: `selfupdate/download.go`

- [ ] **Step 1: Implement progress display**

Update the `DownloadArchive` function in `selfupdate/download.go` to show
progress. Use a simple byte counter with `\r` for TTY output or periodic
line-based messages for non-TTY output. Do not add a Bubble Tea dependency
for this — keep it simple with `fmt.Fprintf` to stderr.

Replace the `// TODO: Add progress bar` section with:

```go
	var written int64
	buf := make([]byte, 32*1024)
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				f.Close()
				os.Remove(path)
				return "", fmt.Errorf("write archive: %w", err)
			}
			written += int64(n)
			if written > maxArchiveSizeLimit {
				f.Close()
				os.Remove(path)
				return "", fmt.Errorf("archive size exceeds maximum %d bytes", maxArchiveSizeLimit)
			}
			if totalSize > 0 {
				pct := float64(written) / float64(totalSize) * 100
				if isTTY {
					fmt.Fprintf(os.Stderr, "\rDownloading... %.1f%% (%d / %d bytes)", pct, written, totalSize)
				} else if int(pct)%25 == 0 {
					fmt.Fprintf(os.Stderr, "Downloading... %.0f%%\n", pct)
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			f.Close()
			os.Remove(path)
			return "", fmt.Errorf("download archive: %w", readErr)
		}
	}
	if isTTY {
		fmt.Fprintln(os.Stderr)
	}
```

Also parse `totalSize` from `Content-Length` earlier in the function for the
progress calculation.

- [ ] **Step 2: Run tests**

Run: `go test ./selfupdate/ -v -run 'TestDownload'`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add selfupdate/download.go
git commit -m "feat(selfupdate): add download progress display"
```

---

### Task 12: Metadata flow integration test

**Files:**
- Modify: `selfupdate/releases_test.go`

- [ ] **Step 1: Write an end-to-end test with mock server**

Add a test to `selfupdate/releases_test.go` that sets up a mock HTTP server
serving a full channel index + version metadata + archive, then verifies the
full flow from `ResolveChannel` through `FetchVersionMetadata` to `FindAsset`.

```go
func TestEndToEndUpdateFlow(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stable.json", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ChannelIndex{
			Latest: "0.2.0",
			Releases: []ReleaseEntry{
				{Version: "0.2.0", Timestamp: "2026-03-10T14:32:00Z"},
				{Version: "0.1.0", Timestamp: "2026-02-20T09:00:00Z"},
			},
		})
	})
	mux.HandleFunc("GET /v0.2.0.json", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(VersionMetadata{
			Version:   "0.2.0",
			Timestamp: "2026-03-10T14:32:00Z",
			Changelog: "- New feature",
			Assets: []Asset{
				{OS: "linux", Arch: "amd64", URL: "https://example.com/linux.tar.gz", SHA256: "abc"},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := &Client{HTTPClient: srv.Client(), BaseURL: srv.URL}

	// Resolve channel.
	idx, channel, err := client.ResolveChannel(false)
	require.NoError(t, err)
	assert.Equal(t, ChannelStable, channel)
	assert.Equal(t, "0.2.0", idx.Latest)

	// Check should update.
	assert.True(t, ShouldUpdate("0.1.0", idx.Latest, false))
	assert.False(t, ShouldUpdate("0.2.0", idx.Latest, false))

	// Fetch version metadata.
	meta, err := client.FetchVersionMetadata(idx.Latest)
	require.NoError(t, err)
	assert.Equal(t, "0.2.0", meta.Version)

	// Find asset.
	asset, err := meta.FindAsset("linux", "amd64")
	require.NoError(t, err)
	assert.Equal(t, "abc", asset.SHA256)
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./selfupdate/ -v -run TestEndToEndUpdateFlow`
Expected: PASS

- [ ] **Step 3: Run full test suite**

Run: `make test`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add selfupdate/releases_test.go
git commit -m "test(selfupdate): add end-to-end update flow test"
```
