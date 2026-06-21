package selfupdate

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"golang.org/x/mod/semver"
)

const (
	baseURL           = "https://vibepit.dev/releases"
	httpTimeout       = 30 * time.Second
	ChannelStable     = "stable"
	ChannelPrerelease = "prerelease"
)

// ChannelIndex represents a channel index file (e.g., stable.json).
type ChannelIndex struct {
	Latest   string         `json:"latest"`
	Releases []ReleaseEntry `json:"releases"`
}

// ReleaseEntry is a single entry in the channel index releases array.
type ReleaseEntry struct {
	Version   string `json:"version"`
	Timestamp string `json:"timestamp"`
}

// VersionMetadata represents a per-version metadata file (e.g., 0.2.0.json).
type VersionMetadata struct {
	Version   string                      `json:"version"`
	Timestamp string                      `json:"timestamp"`
	Changelog string                      `json:"changelog"`
	Changes   map[string][]ChangelogEntry `json:"changes,omitempty"`
	Assets    []Asset                     `json:"assets"`
}

// ChangelogEntry is a single structured changelog entry parsed from a
// docs/changelogs/{version}.yml file.
type ChangelogEntry struct {
	Msg   string `json:"msg"`
	PR    string `json:"pr,omitempty"`
	Issue string `json:"issue,omitempty"`
}

// MergedEntry is a ChangelogEntry tagged with the release version it came
// from, used when merging changelogs across multiple releases.
type MergedEntry struct {
	Entry   ChangelogEntry
	Version string
}

// Asset represents a platform-specific release asset.
type Asset struct {
	OS              string `json:"os"`
	Arch            string `json:"arch"`
	URL             string `json:"url"`
	SHA256          string `json:"sha256"`
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

// ReleasesBetween returns the releases newer than current and no newer than
// target (current < v <= target), sorted newest-first. The caller must ensure
// current is a valid release version; an invalid semver sorts below all
// releases and would match everything.
func (idx *ChannelIndex) ReleasesBetween(current, target string) []ReleaseEntry {
	vCurrent, vTarget := addV(current), addV(target)
	var out []ReleaseEntry
	for _, r := range idx.Releases {
		if semver.Compare(vCurrent, addV(r.Version)) < 0 &&
			semver.Compare(addV(r.Version), vTarget) <= 0 {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return semver.Compare(addV(out[i].Version), addV(out[j].Version)) > 0
	})
	return out
}

// OtherChannel returns the opposite release channel.
func OtherChannel(channel string) string {
	if channel == ChannelPrerelease {
		return ChannelStable
	}
	return ChannelPrerelease
}

// CombineReleases returns the union of the two indexes' release entries,
// deduplicated by version (first occurrence wins). Stable and prerelease
// releases live in separate indexes, so a cross-channel update needs both to
// see every release it skipped; a single index alone would omit the other
// channel's intervening releases. nil indexes are ignored.
func CombineReleases(a, b *ChannelIndex) []ReleaseEntry {
	seen := make(map[string]bool)
	var out []ReleaseEntry
	for _, idx := range []*ChannelIndex{a, b} {
		if idx == nil {
			continue
		}
		for _, r := range idx.Releases {
			if seen[r.Version] {
				continue
			}
			seen[r.Version] = true
			out = append(out, r)
		}
	}
	return out
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

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
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
	url := fmt.Sprintf("%s/%s.json", c.BaseURL, version)
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

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
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
