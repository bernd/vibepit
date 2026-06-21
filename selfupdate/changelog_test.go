package selfupdate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionMetadataChangesParsing(t *testing.T) {
	data := `{
		"version": "0.3.0",
		"timestamp": "2026-06-09T00:04:34+02:00",
		"changelog": "\nAdded:\n- Thing",
		"changes": {
			"added": [{"msg": "Thing"}],
			"fixed": [{"msg": "Bug", "pr": "8"}]
		},
		"assets": []
	}`
	var meta VersionMetadata
	require.NoError(t, json.Unmarshal([]byte(data), &meta))

	require.Len(t, meta.Changes["added"], 1)
	assert.Equal(t, "Thing", meta.Changes["added"][0].Msg)
	assert.Empty(t, meta.Changes["added"][0].PR)

	require.Len(t, meta.Changes["fixed"], 1)
	assert.Equal(t, "Bug", meta.Changes["fixed"][0].Msg)
	assert.Equal(t, "8", meta.Changes["fixed"][0].PR)
}

func TestRenderChanges(t *testing.T) {
	changes := map[string][]ChangelogEntry{
		"added": {
			{Msg: "Plain feature"},
			{Msg: "Feature with PR", PR: "11"},
		},
		"fixed": {
			{Msg: "Bug with issue", Issue: "5"},
			{Msg: "Bug with both", PR: "8", Issue: "9"},
		},
	}
	want := "\nAdded:\n" +
		"- Plain feature\n" +
		"- Feature with PR ([#11](https://github.com/bernd/vibepit/pull/11))\n" +
		"\nFixed:\n" +
		"- Bug with issue ([#5](https://github.com/bernd/vibepit/issues/5))\n" +
		"- Bug with both ([#8](https://github.com/bernd/vibepit/pull/8), [#9](https://github.com/bernd/vibepit/issues/9))"
	assert.Equal(t, want, RenderChanges(changes))
}

func TestRenderChangesEmpty(t *testing.T) {
	assert.Equal(t, "", RenderChanges(map[string][]ChangelogEntry{}))
	assert.Equal(t, "", RenderChanges(nil))
}

func TestRenderChangesMatchesReleaseJSON(t *testing.T) {
	paths, err := filepath.Glob("../docs/content/releases/*.json")
	require.NoError(t, err)
	require.NotEmpty(t, paths)

	checked := 0
	for _, p := range paths {
		base := filepath.Base(p)
		if base == "stable.json" || base == "prerelease.json" {
			continue
		}
		data, err := os.ReadFile(p)
		require.NoError(t, err, p)

		var meta VersionMetadata
		require.NoError(t, json.Unmarshal(data, &meta), p)

		if len(meta.Changes) == 0 {
			continue
		}
		assert.Equal(t, meta.Changelog, RenderChanges(meta.Changes),
			"RenderChanges output drifted from the rendered changelog in %s", base)
		checked++
	}
	assert.Positive(t, checked, "expected at least one release JSON with a changes field")
}

func TestMergeChangesAndRenderMerged(t *testing.T) {
	newer := &VersionMetadata{
		Version: "0.4.0",
		Changes: map[string][]ChangelogEntry{
			"fixed": {{Msg: "Resolve issue from 0.3.0", PR: "21"}},
			"added": {{Msg: "New preset"}},
		},
	}
	older := &VersionMetadata{
		Version: "0.3.0",
		Changes: map[string][]ChangelogEntry{
			"fixed": {{Msg: "Partial fix", PR: "14"}},
			"added": {{Msg: "extra-hosts option", PR: "11"}},
		},
	}

	// Passed newest-first.
	merged := MergeChanges([]*VersionMetadata{newer, older})

	require.Len(t, merged["fixed"], 2)
	assert.Equal(t, "0.4.0", merged["fixed"][0].Version)
	assert.Equal(t, "0.3.0", merged["fixed"][1].Version)

	want := "\nAdded:\n" +
		"- v0.4.0: New preset\n" +
		"- v0.3.0: extra-hosts option ([#11](https://github.com/bernd/vibepit/pull/11))\n" +
		"\nFixed:\n" +
		"- v0.4.0: Resolve issue from 0.3.0 ([#21](https://github.com/bernd/vibepit/pull/21))\n" +
		"- v0.3.0: Partial fix ([#14](https://github.com/bernd/vibepit/pull/14))"
	assert.Equal(t, want, RenderMerged(merged))
}
