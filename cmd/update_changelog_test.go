package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bernd/vibepit/selfupdate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newMockServer returns a selfupdate.Client whose BaseURL points at an httptest
// server serving each entry of data as {key}.json (404 otherwise). It serves
// both version metadata and channel indexes, which share the {name}.json scheme.
func newMockServer[T any](t *testing.T, data map[string]T) *selfupdate.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), ".json")
		v, ok := data[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}))
	t.Cleanup(srv.Close)
	c := selfupdate.NewClient()
	c.BaseURL = srv.URL
	return c
}

func target040() *selfupdate.VersionMetadata {
	return &selfupdate.VersionMetadata{
		Version:   "0.4.0",
		Changelog: "\nAdded:\n- v0.4.0 target rendered",
		Changes: map[string][]selfupdate.ChangelogEntry{
			"added": {{Msg: "New preset"}},
		},
	}
}

func TestUpdateChangelogSingleRelease(t *testing.T) {
	idx := &selfupdate.ChannelIndex{Releases: []selfupdate.ReleaseEntry{
		{Version: "0.4.0"}, {Version: "0.3.0"},
	}}
	client := newMockServer[selfupdate.VersionMetadata](t, nil)
	text, merged := updateChangelog(client, idx, "0.3.0", target040())
	assert.False(t, merged)
	assert.Equal(t, "\nAdded:\n- v0.4.0 target rendered", text)
}

func TestUpdateChangelogMerged(t *testing.T) {
	idx := &selfupdate.ChannelIndex{Releases: []selfupdate.ReleaseEntry{
		{Version: "0.4.0"}, {Version: "0.3.0"}, {Version: "0.2.0"},
	}}
	client := newMockServer(t, map[string]selfupdate.VersionMetadata{
		"0.3.0": {
			Version:   "0.3.0",
			Changelog: "ignored",
			Changes: map[string][]selfupdate.ChangelogEntry{
				"added": {{Msg: "extra-hosts option", PR: "11"}},
			},
		},
	})
	text, merged := updateChangelog(client, idx, "0.2.0", target040())
	require.True(t, merged)
	assert.Contains(t, text, "- v0.4.0: New preset")
	assert.Contains(t, text, "- v0.3.0: extra-hosts option ([#11]")
}

func TestUpdateChangelogFetchErrorFallsBack(t *testing.T) {
	idx := &selfupdate.ChannelIndex{Releases: []selfupdate.ReleaseEntry{
		{Version: "0.4.0"}, {Version: "0.3.0"}, {Version: "0.2.0"},
	}}
	client := newMockServer[selfupdate.VersionMetadata](t, nil) // 0.3.0 will 404
	text, merged := updateChangelog(client, idx, "0.2.0", target040())
	assert.False(t, merged)
	assert.Equal(t, target040().Changelog, text)
}

func TestUpdateChangelogDevBuildFallsBack(t *testing.T) {
	idx := &selfupdate.ChannelIndex{Releases: []selfupdate.ReleaseEntry{
		{Version: "0.4.0"}, {Version: "0.3.0"}, {Version: "0.2.0"},
	}}
	client := newMockServer[selfupdate.VersionMetadata](t, nil)
	text, merged := updateChangelog(client, idx, "0.0", target040())
	assert.False(t, merged)
	assert.Equal(t, target040().Changelog, text)
}

// A target release with no notes (empty, non-nil changes map) must not suppress
// the notes of intermediate releases that do have them.
func TestUpdateChangelogEmptyTargetStillMergesIntermediate(t *testing.T) {
	idx := &selfupdate.ChannelIndex{Releases: []selfupdate.ReleaseEntry{
		{Version: "0.4.0"}, {Version: "0.3.0"}, {Version: "0.2.0"},
	}}
	emptyTarget := &selfupdate.VersionMetadata{
		Version:   "0.4.0",
		Changelog: "",
		Changes:   map[string][]selfupdate.ChangelogEntry{}, // non-nil, no notes
	}
	client := newMockServer(t, map[string]selfupdate.VersionMetadata{
		"0.3.0": {
			Version: "0.3.0",
			Changes: map[string][]selfupdate.ChangelogEntry{
				"fixed": {{Msg: "Real intermediate fix", PR: "14"}},
			},
		},
	})
	text, merged := updateChangelog(client, idx, "0.2.0", emptyTarget)
	require.True(t, merged)
	assert.Contains(t, text, "- v0.3.0: Real intermediate fix ([#14]")
}

// A release whose JSON predates the structured changes field (nil map) cannot
// be merged, so the whole update falls back to the rendered changelog.
func TestUpdateChangelogNilIntermediateChangesFallsBack(t *testing.T) {
	idx := &selfupdate.ChannelIndex{Releases: []selfupdate.ReleaseEntry{
		{Version: "0.4.0"}, {Version: "0.3.0"}, {Version: "0.2.0"},
	}}
	client := newMockServer(t, map[string]selfupdate.VersionMetadata{
		"0.3.0": {
			Version:   "0.3.0",
			Changelog: "0.3.0 rendered",
			Changes:   nil, // no structured field; encoded JSON omits "changes"
		},
	})
	text, merged := updateChangelog(client, idx, "0.2.0", target040())
	assert.False(t, merged)
	assert.Equal(t, target040().Changelog, text)
}

func TestEnumerationIndexSameChannelUnchanged(t *testing.T) {
	stable := &selfupdate.ChannelIndex{Latest: "0.4.0", Releases: []selfupdate.ReleaseEntry{
		{Version: "0.4.0"}, {Version: "0.3.0"},
	}}
	client := newMockServer[selfupdate.ChannelIndex](t, nil)
	got := enumerationIndex(client, stable, selfupdate.ChannelStable, false)
	assert.Same(t, stable, got)
}

func TestEnumerationIndexCrossChannelCombines(t *testing.T) {
	stable := &selfupdate.ChannelIndex{Latest: "0.4.0", Releases: []selfupdate.ReleaseEntry{
		{Version: "0.4.0"}, {Version: "0.3.0"},
	}}
	client := newMockServer(t, map[string]selfupdate.ChannelIndex{
		selfupdate.ChannelPrerelease: {Latest: "0.5.0-rc.1", Releases: []selfupdate.ReleaseEntry{
			{Version: "0.5.0-rc.1"}, {Version: "0.3.5-rc.1"},
		}},
	})
	got := enumerationIndex(client, stable, selfupdate.ChannelStable, true)
	assert.Equal(t, "0.4.0", got.Latest, "target version must be preserved")
	versions := make([]string, len(got.Releases))
	for i, r := range got.Releases {
		versions[i] = r.Version
	}
	assert.ElementsMatch(t, []string{"0.4.0", "0.3.0", "0.5.0-rc.1", "0.3.5-rc.1"}, versions)
}

// When the other channel index can't be fetched, enumeration would be
// incomplete, so the helper signals fallback by returning nil rather than the
// partial resolved index.
func TestEnumerationIndexCrossChannelOtherMissingSignalsIncomplete(t *testing.T) {
	stable := &selfupdate.ChannelIndex{Latest: "0.4.0", Releases: []selfupdate.ReleaseEntry{
		{Version: "0.4.0"}, {Version: "0.3.0"},
	}}
	client := newMockServer[selfupdate.ChannelIndex](t, nil) // prerelease.json 404s
	got := enumerationIndex(client, stable, selfupdate.ChannelStable, true)
	assert.Nil(t, got)
}

// A nil enumeration index (incomplete cross-channel enumeration) must produce
// the target changelog only — never a partial merge under a full-range heading.
func TestUpdateChangelogNilIndexFallsBack(t *testing.T) {
	client := newMockServer[selfupdate.VersionMetadata](t, nil)
	text, merged := updateChangelog(client, nil, "0.2.0", target040())
	assert.False(t, merged)
	assert.Equal(t, target040().Changelog, text)
}
