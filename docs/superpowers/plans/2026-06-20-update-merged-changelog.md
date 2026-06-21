# Merged Changelog for Multi-Release `vibepit update` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When the installed `vibepit` is several releases behind, `update` shows a single merged changelog covering every skipped release, with each entry prefixed by its source version.

**Architecture:** Add a structured `changes` field to each per-version release JSON (alongside the existing rendered `changelog` string). The Go client enumerates the releases between the installed and target versions from the channel index, fetches each one's metadata, merges entries by category, and renders one version-prefixed block. Pure rendering/merge logic lives in the `selfupdate` package and is unit-tested; the `cmd/update.go` wiring fetches and displays.

**Tech Stack:** Go (`golang.org/x/mod/semver`, `github.com/stretchr/testify`), Python 3 + PyYAML (release-metadata generator).

## Global Constraints

- Format code with `gofmt`; comments explain *why*, not *what*.
- Tests are table-driven with subtests; assertions use `github.com/stretchr/testify` (`assert`/`require`).
- Use `any`, not `interface{}`.
- Category order is canonical everywhere: `added, changed, fixed, deprecated, removed, security`.
- Repo URL in changelog links: `https://github.com/bernd/vibepit`.
- The rendered `changelog` string field is **retained** in every JSON (feeds GitHub release notes and acts as the fallback).
- `pr`/`issue` are emitted as **strings** in the structured `changes` field.
- The Go renderer output must be byte-for-byte identical to the Python `parse_changelog` output (enforced by a consistency test).
- Work happens on branch `update-merged-changelog`. Commit after every task.

---

### Task 1: Structured changelog types in `selfupdate`

**Files:**
- Modify: `selfupdate/releases.go` (add `ChangelogEntry`, `MergedEntry`, and the `Changes` field on `VersionMetadata`)
- Test: `selfupdate/changelog_test.go` (create)

**Interfaces:**
- Produces:
  - `type ChangelogEntry struct { Msg string; PR string; Issue string }` with JSON tags `msg`, `pr,omitempty`, `issue,omitempty`.
  - `VersionMetadata.Changes map[string][]ChangelogEntry` with JSON tag `changes,omitempty`.
  - `type MergedEntry struct { Entry ChangelogEntry; Version string }`.

- [ ] **Step 1: Write the failing test**

Create `selfupdate/changelog_test.go`:

```go
package selfupdate

import (
	"encoding/json"
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./selfupdate/ -run TestVersionMetadataChangesParsing -v`
Expected: FAIL — compile error, `meta.Changes` undefined / `VersionMetadata` has no field `Changes`.

- [ ] **Step 3: Add the types**

In `selfupdate/releases.go`, replace the `VersionMetadata` struct with:

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./selfupdate/ -run TestVersionMetadataChangesParsing -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add selfupdate/releases.go selfupdate/changelog_test.go
git commit -m "Add structured changelog types to selfupdate"
```

---

### Task 2: `ReleasesBetween` on `ChannelIndex`

**Files:**
- Modify: `selfupdate/releases.go` (add method; add `sort` import)
- Test: `selfupdate/releases_test.go` (append test)

**Interfaces:**
- Consumes: `ChannelIndex`, `ReleaseEntry` (existing), `addV` (from `version.go`).
- Produces: `func (idx *ChannelIndex) ReleasesBetween(current, target string) []ReleaseEntry` — entries with `current < v <= target` by semver, sorted newest-first. Callers must guard dev-build `current` themselves (an invalid semver `current` sorts below everything and would match all releases).

- [ ] **Step 1: Write the failing test**

Append to `selfupdate/releases_test.go`:

```go
func TestReleasesBetween(t *testing.T) {
	idx := &ChannelIndex{
		Releases: []ReleaseEntry{
			{Version: "0.4.0"}, {Version: "0.3.0"},
			{Version: "0.2.0"}, {Version: "0.1.0"},
		},
	}
	tests := []struct {
		name           string
		current, target string
		want           []string
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./selfupdate/ -run TestReleasesBetween -v`
Expected: FAIL — `idx.ReleasesBetween` undefined.

- [ ] **Step 3: Implement the method**

Add `"sort"` to the imports in `selfupdate/releases.go`, then add:

```go
// ReleasesBetween returns the releases newer than current and no newer than
// target (current < v <= target), sorted newest-first. The caller must ensure
// current is a valid release version; an invalid semver sorts below all
// releases and would match everything.
func (idx *ChannelIndex) ReleasesBetween(current, target string) []ReleaseEntry {
	var out []ReleaseEntry
	for _, r := range idx.Releases {
		if semver.Compare(addV(current), addV(r.Version)) < 0 &&
			semver.Compare(addV(r.Version), addV(target)) <= 0 {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return semver.Compare(addV(out[i].Version), addV(out[j].Version)) > 0
	})
	return out
}
```

Add `"golang.org/x/mod/semver"` to the imports if not already present (it is used by `version.go` in the same package, but `releases.go` needs its own import).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./selfupdate/ -run TestReleasesBetween -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add selfupdate/releases.go selfupdate/releases_test.go
git commit -m "Add ChannelIndex.ReleasesBetween for changelog range enumeration"
```

---

### Task 3: Single-version renderer (`RenderChanges`)

**Files:**
- Create: `selfupdate/changelog.go`
- Test: `selfupdate/changelog_test.go` (append)

**Interfaces:**
- Produces:
  - `func RenderChanges(changes map[string][]ChangelogEntry) string` — no version prefix.
  - Unexported helpers `formatEntry`, `refSuffix`, `capitalizeCategory`, and `changelogCategories` / `repoURL` constants (reused by Task 4).

- [ ] **Step 1: Write the failing test**

Append to `selfupdate/changelog_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./selfupdate/ -run TestRenderChanges -v`
Expected: FAIL — `RenderChanges` undefined.

- [ ] **Step 3: Implement the renderer**

Create `selfupdate/changelog.go`:

```go
package selfupdate

import (
	"fmt"
	"strings"
)

// repoURL is the GitHub repository used to build changelog PR/issue links.
// Must match REPO_URL in .github/scripts/generate-release-metadata.py.
const repoURL = "https://github.com/bernd/vibepit"

// changelogCategories is the canonical render order for changelog sections.
// Must match CHANGELOG_CATEGORIES in the metadata generator.
var changelogCategories = []string{"added", "changed", "fixed", "deprecated", "removed", "security"}

// refSuffix renders the trailing " ([#pr](...), [#issue](...))" for an entry,
// or "" when it has no references.
func refSuffix(e ChangelogEntry) string {
	var refs []string
	if e.PR != "" {
		refs = append(refs, fmt.Sprintf("[#%s](%s/pull/%s)", e.PR, repoURL, e.PR))
	}
	if e.Issue != "" {
		refs = append(refs, fmt.Sprintf("[#%s](%s/issues/%s)", e.Issue, repoURL, e.Issue))
	}
	if len(refs) == 0 {
		return ""
	}
	return " (" + strings.Join(refs, ", ") + ")"
}

// formatEntry renders a single entry line without a version prefix.
func formatEntry(e ChangelogEntry) string {
	return "- " + e.Msg + refSuffix(e)
}

// capitalizeCategory upper-cases the first letter of a category name to match
// Python's str.capitalize() for these single lowercase words.
func capitalizeCategory(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// RenderChanges renders a single version's structured changes to the same text
// the Python generator produces for the rendered "changelog" field. No version
// prefix is added.
func RenderChanges(changes map[string][]ChangelogEntry) string {
	var lines []string
	for _, cat := range changelogCategories {
		entries := changes[cat]
		if len(entries) == 0 {
			continue
		}
		lines = append(lines, "\n"+capitalizeCategory(cat)+":")
		for _, e := range entries {
			lines = append(lines, formatEntry(e))
		}
	}
	return strings.Join(lines, "\n")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./selfupdate/ -run TestRenderChanges -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add selfupdate/changelog.go selfupdate/changelog_test.go
git commit -m "Add RenderChanges single-version changelog renderer"
```

---

### Task 4: Merge + version-prefixed renderer (`MergeChanges`, `RenderMerged`)

**Files:**
- Modify: `selfupdate/changelog.go`
- Test: `selfupdate/changelog_test.go` (append)

**Interfaces:**
- Consumes: `VersionMetadata`, `MergedEntry`, `ChangelogEntry`, `refSuffix`, `capitalizeCategory`, `changelogCategories`.
- Produces:
  - `func MergeChanges(metas []*VersionMetadata) map[string][]MergedEntry` — `metas` passed newest→oldest; concatenates per category, tagging each entry with its `Version`.
  - `func RenderMerged(merged map[string][]MergedEntry) string` — same layout as `RenderChanges`, each line prefixed `- v{version}: `.

- [ ] **Step 1: Write the failing test**

Append to `selfupdate/changelog_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./selfupdate/ -run TestMergeChangesAndRenderMerged -v`
Expected: FAIL — `MergeChanges` / `RenderMerged` undefined.

- [ ] **Step 3: Implement merge + merged renderer**

Append to `selfupdate/changelog.go`:

```go
// MergeChanges concatenates the structured changes of multiple releases by
// category, tagging each entry with its source version. metas must be ordered
// newest-first so that the newest release's entries lead each category.
func MergeChanges(metas []*VersionMetadata) map[string][]MergedEntry {
	merged := make(map[string][]MergedEntry)
	for _, m := range metas {
		for cat, entries := range m.Changes {
			for _, e := range entries {
				merged[cat] = append(merged[cat], MergedEntry{Entry: e, Version: m.Version})
			}
		}
	}
	return merged
}

// RenderMerged renders merged changes as a single block, prefixing every entry
// with its source version (e.g. "- v0.4.0: ...").
func RenderMerged(merged map[string][]MergedEntry) string {
	var lines []string
	for _, cat := range changelogCategories {
		entries := merged[cat]
		if len(entries) == 0 {
			continue
		}
		lines = append(lines, "\n"+capitalizeCategory(cat)+":")
		for _, m := range entries {
			lines = append(lines, "- v"+m.Version+": "+m.Entry.Msg+refSuffix(m.Entry))
		}
	}
	return strings.Join(lines, "\n")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./selfupdate/ -run TestMergeChangesAndRenderMerged -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add selfupdate/changelog.go selfupdate/changelog_test.go
git commit -m "Add MergeChanges and RenderMerged version-prefixed renderer"
```

---

### Task 5: Generator emits `changes` + backfill existing JSONs

**Files:**
- Modify: `.github/scripts/generate-release-metadata.py`
- Modify (regenerated output): `docs/content/releases/0.1.0.json`, `0.2.0.json`, `0.3.0.json`

**Interfaces:**
- Produces: every per-version JSON gains a `changes` object (between `changelog` and `assets`), built from the same YAML source. `pr`/`issue` coerced to strings.

- [ ] **Step 1: Add `build_changes` and wire it into metadata generation**

In `.github/scripts/generate-release-metadata.py`, add this function after `parse_changelog` (after line 74):

```python
def build_changes(version: str) -> dict:
    changelog_file = Path(f"docs/changelogs/{version}.yml")
    if not changelog_file.exists():
        return {}

    with open(changelog_file) as f:
        data = yaml.safe_load(f)

    changes = {}
    for category in CHANGELOG_CATEGORIES:
        entries = data.get(category, [])
        if not entries:
            continue
        out = []
        for entry in entries:
            item = {"msg": entry["msg"]}
            if (pr := entry.get("pr")) is not None:
                item["pr"] = str(pr)
            if (issue := entry.get("issue")) is not None:
                item["issue"] = str(issue)
            out.append(item)
        changes[category] = out
    return changes
```

Change `write_version_metadata` (currently lines 103-114) to accept and write `changes`:

```python
def write_version_metadata(bare_version: str, timestamp: str, changelog: str, changes: dict, assets: list[dict]):
    RELEASES_DIR.mkdir(parents=True, exist_ok=True)
    meta = {
        "version": bare_version,
        "timestamp": timestamp,
        "changelog": changelog,
        "changes": changes,
        "assets": assets,
    }
    path = RELEASES_DIR / f"{bare_version}.json"
    with open(path, "w") as f:
        json.dump(meta, f, indent=2)
        f.write("\n")
```

In `generate_metadata_cmd` (currently lines 146-160), build and pass `changes`:

```python
def generate_metadata_cmd():
    version = os.environ.get("VERSION")
    timestamp = os.environ.get("TIMESTAMP")

    if not version or not timestamp:
        sys.exit("VERSION and TIMESTAMP environment variables are required")

    bare_version = version.lstrip("v")

    changelog = parse_changelog(bare_version)
    changes = build_changes(bare_version)
    checksums = parse_checksums("/tmp/checksums.txt")
    assets = build_assets(version, bare_version, checksums)

    write_version_metadata(bare_version, timestamp, changelog, changes, assets)
    update_channel_index(bare_version, timestamp)
```

- [ ] **Step 2: Add a `--backfill-changes` mode**

Add this function after `generate_metadata_cmd`:

```python
def backfill_changes_cmd():
    for path in sorted(RELEASES_DIR.glob("*.json")):
        if path.name in ("stable.json", "prerelease.json"):
            continue
        with open(path) as f:
            meta = json.load(f)
        version = meta.get("version")
        if not version:
            continue
        changes = build_changes(version)
        new_meta = {}
        for key, value in meta.items():
            if key == "changes":
                continue
            new_meta[key] = value
            if key == "changelog":
                new_meta["changes"] = changes
        if "changes" not in new_meta:
            new_meta["changes"] = changes
        with open(path, "w") as f:
            json.dump(new_meta, f, indent=2)
            f.write("\n")
        print(f"backfilled {path.name}")
```

Wire it into `__main__` (currently lines 163-172):

```python
if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--render-changelog", action="store_true",
                        help="Render changelog to stdout and exit")
    parser.add_argument("--backfill-changes", action="store_true",
                        help="Add the structured changes field to existing version JSONs and exit")
    args = parser.parse_args()

    if args.render_changelog:
        render_changelog_cmd()
    elif args.backfill_changes:
        backfill_changes_cmd()
    else:
        generate_metadata_cmd()
```

- [ ] **Step 3: Run the backfill to regenerate committed JSONs**

Run from the repo root:

```bash
python3 .github/scripts/generate-release-metadata.py --backfill-changes
```

Expected output (order may vary):
```
backfilled 0.1.0.json
backfilled 0.2.0.json
backfilled 0.3.0.json
```

- [ ] **Step 4: Verify the diff only adds `changes`**

Run: `git diff --stat docs/content/releases/`
Expected: only `0.1.0.json`, `0.2.0.json`, `0.3.0.json` changed.

Run: `git diff docs/content/releases/0.3.0.json`
Expected: a new `"changes": { ... }` block inserted after the `"changelog"` line; `version`, `timestamp`, `changelog`, and `assets` unchanged. Spot-check that `added`/`fixed` entries with PR numbers show `"pr": "10"` etc. as strings.

- [ ] **Step 5: Commit**

```bash
git add .github/scripts/generate-release-metadata.py docs/content/releases/0.1.0.json docs/content/releases/0.2.0.json docs/content/releases/0.3.0.json
git commit -m "Emit structured changes field and backfill release JSONs"
```

---

### Task 6: Go ↔ Python consistency test

**Files:**
- Test: `selfupdate/changelog_test.go` (append)

**Interfaces:**
- Consumes: `RenderChanges`, `VersionMetadata`, the committed `docs/content/releases/*.json` (now containing `changes` after Task 5).

- [ ] **Step 1: Write the test**

Append to `selfupdate/changelog_test.go` (and ensure imports include `os` and `path/filepath`):

```go
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
```

The `changelog_test.go` import block must be:

```go
import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)
```

- [ ] **Step 2: Run the test**

Run: `go test ./selfupdate/ -run TestRenderChangesMatchesReleaseJSON -v`
Expected: PASS, having checked the 3 backfilled release JSONs. If it FAILS, the Go renderer and Python generator disagree — diff the expected vs actual to find the formatting mismatch and fix `selfupdate/changelog.go` (do not edit the JSON to match a wrong renderer).

- [ ] **Step 3: Commit**

```bash
git add selfupdate/changelog_test.go
git commit -m "Pin Go changelog renderer to generator output via consistency test"
```

---

### Task 7: Wire merged changelog into `cmd/update.go`

**Files:**
- Modify: `cmd/update.go` (`runBinaryUpdate`, lines 149-190; add `updateChangelog` helper)
- Test: `cmd/update_changelog_test.go` (create)

**Interfaces:**
- Consumes: `selfupdate.Client`, `selfupdate.ChannelIndex`, `selfupdate.VersionMetadata`, `selfupdate.IsDevBuild`, `selfupdate.MergeChanges`, `selfupdate.RenderMerged`, `config.Version`.
- Produces: `func updateChangelog(client *selfupdate.Client, idx *selfupdate.ChannelIndex, current string, meta *selfupdate.VersionMetadata) (text string, merged bool)`.
  - `idx == nil` (direct `--use` path) or `IsDevBuild(current)` → `(meta.Changelog, false)`.
  - `len(ReleasesBetween) <= 1` → `(meta.Changelog, false)`.
  - Otherwise fetch each in-range version (reusing `meta` for the target). On any fetch error, or any in-range version missing structured `Changes` → `(meta.Changelog, false)`. On success → `(RenderMerged(MergeChanges(metas)), true)`.

- [ ] **Step 1: Write the failing test**

Create `cmd/update_changelog_test.go`:

```go
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

// newTestClient returns a selfupdate.Client whose BaseURL points at an
// httptest server serving the given versions as {version}.json (404 otherwise).
func newTestClient(t *testing.T, versions map[string]selfupdate.VersionMetadata) *selfupdate.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ver := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), ".json")
		meta, ok := versions[ver]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(meta)
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
	client := newTestClient(t, nil)
	text, merged := updateChangelog(client, idx, "0.3.0", target040())
	assert.False(t, merged)
	assert.Equal(t, "\nAdded:\n- v0.4.0 target rendered", text)
}

func TestUpdateChangelogMerged(t *testing.T) {
	idx := &selfupdate.ChannelIndex{Releases: []selfupdate.ReleaseEntry{
		{Version: "0.4.0"}, {Version: "0.3.0"}, {Version: "0.2.0"},
	}}
	client := newTestClient(t, map[string]selfupdate.VersionMetadata{
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
	client := newTestClient(t, nil) // 0.3.0 will 404
	text, merged := updateChangelog(client, idx, "0.2.0", target040())
	assert.False(t, merged)
	assert.Equal(t, target040().Changelog, text)
}

func TestUpdateChangelogDevBuildFallsBack(t *testing.T) {
	idx := &selfupdate.ChannelIndex{Releases: []selfupdate.ReleaseEntry{
		{Version: "0.4.0"}, {Version: "0.3.0"}, {Version: "0.2.0"},
	}}
	client := newTestClient(t, nil)
	text, merged := updateChangelog(client, idx, "0.0", target040())
	assert.False(t, merged)
	assert.Equal(t, target040().Changelog, text)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run TestUpdateChangelog -v`
Expected: FAIL — `updateChangelog` undefined.

- [ ] **Step 3: Implement `updateChangelog` and rewire `runBinaryUpdate`**

Add the helper to `cmd/update.go`:

```go
// updateChangelog returns the changelog text to display for an update, and
// whether it merges multiple releases. For a dev-build current version, the
// direct --use path (idx == nil), a single-release step, a fetch failure, or
// any in-range release lacking structured changes, it falls back to the
// target's rendered changelog string (today's behavior).
func updateChangelog(client *selfupdate.Client, idx *selfupdate.ChannelIndex, current string, meta *selfupdate.VersionMetadata) (string, bool) {
	if idx == nil || selfupdate.IsDevBuild(current) {
		return meta.Changelog, false
	}

	rng := idx.ReleasesBetween(current, meta.Version)
	if len(rng) <= 1 {
		return meta.Changelog, false
	}

	metas := make([]*selfupdate.VersionMetadata, 0, len(rng))
	for _, r := range rng {
		vm := meta
		if r.Version != meta.Version {
			fetched, err := client.FetchVersionMetadata(r.Version)
			if err != nil {
				return meta.Changelog, false
			}
			vm = fetched
		}
		if len(vm.Changes) == 0 {
			return meta.Changelog, false
		}
		metas = append(metas, vm)
	}

	return selfupdate.RenderMerged(selfupdate.MergeChanges(metas)), true
}
```

In `runBinaryUpdate`, hoist `idx` to function scope so it is available at display time. Replace the block at lines 150-176 with:

```go
	var meta *selfupdate.VersionMetadata
	var idx *selfupdate.ChannelIndex

	if useVersion != "" {
		// Direct version fetch, bypass channel logic.
		var err error
		meta, err = client.FetchVersionMetadata(useVersion)
		if err != nil {
			return err
		}
	} else {
		// Channel-based update check.
		resolved, channel, err := client.ResolveChannel(pre)
		if err != nil {
			return err
		}
		idx = resolved

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
```

Replace the display block at lines 184-190 with:

```go
	// Display update info.
	label := lipgloss.NewStyle().Foreground(tui.ColorCyan).Bold(true)
	fmt.Printf("%s %s\n", label.Render("Current version:"), config.Version)
	fmt.Printf("%s  %s (%s)\n", label.Render("Target version:"), meta.Version, meta.Timestamp)

	changelog, merged := updateChangelog(client, idx, config.Version, meta)
	if changelog != "" {
		heading := "Changelog:"
		if merged {
			heading = fmt.Sprintf("Changelog (v%s → v%s):", config.Version, meta.Version)
		}
		fmt.Printf("\n%s\n\n%s\n", label.Render(heading), changelog)
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/ -run TestUpdateChangelog -v`
Expected: PASS (all four cases).

Run: `go build ./...`
Expected: builds cleanly.

- [ ] **Step 5: Commit**

```bash
git add cmd/update.go cmd/update_changelog_test.go
git commit -m "Show merged changelog when update spans multiple releases"
```

---

### Task 8: Full verification

- [ ] **Step 1: Run the package tests**

Run: `make test`
Expected: PASS, including `selfupdate` and `cmd` packages.

- [ ] **Step 2: Confirm gofmt cleanliness**

Run: `gofmt -l selfupdate/ cmd/`
Expected: no output (no files need formatting).

- [ ] **Step 3: Final commit (only if Step 2 reformatted anything)**

```bash
git add -u && git commit -m "gofmt"
```

---

## Self-Review

**Spec coverage:**
- Structured `changes` field + retained rendered `changelog` → Tasks 1, 5. ✓
- `pr`/`issue` as strings → Task 5 `build_changes`. ✓
- Backfill of historical JSONs → Task 5. ✓
- `ReleasesBetween` newest-first semver filter → Task 2. ✓
- `MergeChanges` newest-first + version tagging → Task 4. ✓
- `RenderChanges` (no prefix) + `RenderMerged` (`- vX.Y.Z:` prefix) → Tasks 3, 4. ✓
- Consistency test `RenderChanges == changelog` → Task 6. ✓
- Channel-path wiring, single vs merged, `Changelog (v.. → v..)` label → Task 7. ✓
- Fallbacks: dev build, `--use` path, single step, fetch error, missing structured data → Task 7 `updateChangelog` + its tests. ✓
- Direct `--use` path unchanged behavior → Task 7 (`idx == nil`). ✓

**Placeholder scan:** No TBD/TODO; every code step shows full, final code (no scratch shims to clean up).

**Type consistency:** `ChangelogEntry{Msg,PR,Issue}`, `MergedEntry{Entry,Version}`, `VersionMetadata.Changes`, `ReleasesBetween`, `MergeChanges`, `RenderChanges`, `RenderMerged`, and `updateChangelog(client, idx, current, meta) (string, bool)` are used consistently across tasks. Python `build_changes`/`write_version_metadata(..., changes, assets)` signatures match their call sites.
