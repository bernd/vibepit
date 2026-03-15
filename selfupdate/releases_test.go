package selfupdate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
