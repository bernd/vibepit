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
	mux.HandleFunc("GET /0.2.0.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"version":"0.2.0","timestamp":"2026-03-10T14:32:00Z","changelog":"- Fix","assets":[]}`)
	})
	mux.HandleFunc("GET /9.9.9.json", func(w http.ResponseWriter, r *http.Request) {
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
	mux.HandleFunc("GET /0.2.0.json", func(w http.ResponseWriter, r *http.Request) {
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

func TestReleasesBetween(t *testing.T) {
	idx := &ChannelIndex{
		Releases: []ReleaseEntry{
			{Version: "0.4.0"}, {Version: "0.3.0"},
			{Version: "0.2.0"}, {Version: "0.1.0"},
		},
	}
	tests := []struct {
		name            string
		current, target string
		want            []string
	}{
		{"spans multiple", "0.1.0", "0.4.0", []string{"0.4.0", "0.3.0", "0.2.0"}},
		{"single step", "0.3.0", "0.4.0", []string{"0.4.0"}},
		{"current equals target", "0.4.0", "0.4.0", nil},
		{"current above latest", "0.5.0", "0.4.0", nil},
		{"target excludes newer", "0.1.0", "0.3.0", []string{"0.3.0", "0.2.0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := idx.ReleasesBetween(tt.current, tt.target)
			var versions []string
			for _, r := range got {
				versions = append(versions, r.Version)
			}
			assert.Equal(t, tt.want, versions)
		})
	}
}

func TestReleasesBetweenSortsNewestFirst(t *testing.T) {
	idx := &ChannelIndex{
		Releases: []ReleaseEntry{
			{Version: "0.2.0"}, {Version: "0.4.0"}, {Version: "0.3.0"},
		},
	}
	got := idx.ReleasesBetween("0.1.0", "0.4.0")
	require.Len(t, got, 3)
	assert.Equal(t, "0.4.0", got[0].Version)
	assert.Equal(t, "0.3.0", got[1].Version)
	assert.Equal(t, "0.2.0", got[2].Version)
}

func TestOtherChannel(t *testing.T) {
	assert.Equal(t, ChannelStable, OtherChannel(ChannelPrerelease))
	assert.Equal(t, ChannelPrerelease, OtherChannel(ChannelStable))
}

func TestCombineReleases(t *testing.T) {
	stable := &ChannelIndex{Releases: []ReleaseEntry{
		{Version: "0.4.0"}, {Version: "0.3.0"},
	}}
	pre := &ChannelIndex{Releases: []ReleaseEntry{
		{Version: "0.4.0-rc.1"}, {Version: "0.3.0-rc.1"},
	}}

	t.Run("union of both channels", func(t *testing.T) {
		got := CombineReleases(stable, pre)
		versions := make([]string, len(got))
		for i, r := range got {
			versions[i] = r.Version
		}
		assert.ElementsMatch(t, []string{"0.4.0", "0.3.0", "0.4.0-rc.1", "0.3.0-rc.1"}, versions)
	})

	t.Run("deduplicates by version", func(t *testing.T) {
		dup := &ChannelIndex{Releases: []ReleaseEntry{{Version: "0.4.0"}, {Version: "0.5.0"}}}
		got := CombineReleases(stable, dup)
		versions := make([]string, len(got))
		for i, r := range got {
			versions[i] = r.Version
		}
		assert.ElementsMatch(t, []string{"0.4.0", "0.3.0", "0.5.0"}, versions)
	})

	t.Run("ignores nil index", func(t *testing.T) {
		got := CombineReleases(stable, nil)
		assert.Len(t, got, 2)
	})
}
