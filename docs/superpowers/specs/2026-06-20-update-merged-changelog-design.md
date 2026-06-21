# Design: Merged changelog for multi-release `vibepit update`

Date: 2026-06-20

## Problem

`vibepit update` shows the changelog for only the *target* release. When the
installed binary is more than one release behind, the changelogs for the
skipped intermediate releases are never shown. A user on 0.1.0 updating to
0.3.0 sees 0.3.0's notes but never 0.2.0's.

In the channel-based update path (`cmd/update.go`, `runBinaryUpdate`), the code
resolves the channel index — which lists *every* release — but then fetches and
prints metadata for only `idx.Latest`. The rendered changelog text exists only
in each per-version JSON (`docs/content/releases/{version}.json`), not in the
channel index.

## Goal

When the installed version is several releases behind, show a single **merged**
changelog covering every skipped release (every release strictly newer than the
installed version, up to and including the target), grouped by category. Each
entry is prefixed with the version it came from so cross-release context is
clear (e.g. a 0.4.0 fix for an issue introduced in 0.3.0).

Non-goals: changing the release pipeline beyond adding structured changelog
data; changing the `--version` direct-install path; redesigning terminal
changelog styling.

## Approach

Client-side enumeration and merge. The channel index already lists all
releases with versions and timestamps, so the client can compute exactly which
releases sit between the installed and target versions, fetch each one's
metadata, and merge their changelog entries.

To merge client-side we add a **structured** changelog field to each
per-version JSON (in addition to the existing rendered `changelog` string).
The Go client renders the merged result, including PR/issue links.

Alternatives considered and rejected:

- **Per-version rendered blocks** (stacked, no merge): simpler, but the user
  wants a single merged changelog.
- **Server-side aggregated changelog endpoint**: one fetch, but changes the
  data format/CI for the sake of 1–3 tiny fetches. YAGNI.
- **Embed changelogs into the channel index**: zero extra fetches, but bloats
  the index with full changelog history and duplicates data.

## Data format

Each `docs/content/releases/{version}.json` gains a `changes` field alongside
the existing rendered `changelog` string. The rendered string is retained: it
still feeds GitHub release notes (`--render-changelog`) and acts as a fallback.

```json
{
  "version": "0.3.0",
  "timestamp": "…",
  "changelog": "\nAdded:\n- …",
  "changes": {
    "added": [
      {"msg": "JetBrains AI preset"},
      {"msg": "extra-hosts option", "pr": "11"}
    ],
    "fixed": [
      {"msg": "Race in DNS server", "pr": "8"}
    ]
  },
  "assets": [ … ]
}
```

- Keys are the canonical categories, only present when non-empty:
  `added, changed, fixed, deprecated, removed, security`.
- Each entry has `msg`, and optional `pr` / `issue`, both emitted as **strings**
  (coerced from the YAML, which may use integers) so Go unmarshalling is
  trivial.

## Generator changes (`.github/scripts/generate-release-metadata.py`)

- Add `build_changes(version) -> dict` that reads the same
  `docs/changelogs/{version}.yml`, iterates `CHANGELOG_CATEGORIES`, and emits
  `{category: [{"msg": …, "pr": str(pr)?, "issue": str(issue)?}, …]}` for
  non-empty categories. `pr`/`issue` coerced to strings.
- `write_version_metadata` includes `"changes"` in the written JSON.
- The existing `parse_changelog` / rendered `changelog` string is unchanged.

### Backfill

Existing committed JSONs (`0.1.0.json`, `0.2.0.json`, `0.3.0.json`) predate the
`changes` field. Add a `--backfill-changes` mode to the script that, for every
existing `docs/content/releases/{version}.json`, injects `changes` from its YAML
source while leaving `timestamp`, `changelog`, and `assets` untouched. Run once
to update the committed historical files. Future releases get `changes`
automatically via the modified generator.

A version JSON whose YAML source is missing is left without `changes` (the
client falls back to its rendered string).

## Go changes (`selfupdate` package — pure, unit-tested)

### Types

```go
type ChangelogEntry struct {
    Msg   string `json:"msg"`
    PR    string `json:"pr,omitempty"`
    Issue string `json:"issue,omitempty"`
}

// VersionMetadata gains:
//   Changes map[string][]ChangelogEntry `json:"changes,omitempty"`

type MergedEntry struct {
    Entry   ChangelogEntry
    Version string
}
```

### Functions

- `func (idx *ChannelIndex) ReleasesBetween(current, target string) []ReleaseEntry`
  — returns the entries from `idx.Releases` where `current < v <= target` by
  `semver.Compare`, ordered **newest-first**.

- `func MergeChanges(metas []*VersionMetadata) map[string][]MergedEntry`
  — `metas` passed newest→oldest; concatenates entries per category, tagging
  each with its source version. Within a category, newest version's entries
  come first (driven by input order).

- `func RenderChanges(changes map[string][]ChangelogEntry) string`
  — renders a **single** version's changes with **no** version prefix. Byte-for-
  byte reproduction of the Python output: leading `\n`; per non-empty category
  in canonical order a `\n` + `Capitalized:` header; each entry `- msg`; ` (`
  + comma-joined `[#PR](REPO/pull/PR)` / `[#ISSUE](REPO/issues/ISSUE)` + `)`
  when refs exist; lines joined by `\n`.

- `func RenderMerged(merged map[string][]MergedEntry) string`
  — same layout, but each entry line is prefixed with its version:
  `- v0.4.0: msg ([#8](…))`.

`REPO_URL` constant mirrors the Python `https://github.com/bernd/vibepit`.

### Tests

- **Consistency test (key safeguard):** for every committed
  `docs/content/releases/*.json`, assert
  `RenderChanges(meta.Changes) == meta.Changelog`. Pins the Go renderer to the
  Python generator; catches drift on either side. (Versions without `changes`
  are skipped — there is nothing to compare.)
- Table-driven tests for `ReleasesBetween` (current in/below/above range, dev
  build, equal-to-target, empty index), `MergeChanges` (order, concat,
  multi-category), `formatEntry`/render (pr only, issue only, both, none),
  `RenderMerged` (version prefix placement).

## CLI changes (`cmd/update.go`, channel path only)

In `runBinaryUpdate`, after `ShouldUpdate` confirms an update and the target
`meta` is fetched:

1. Compute `rng := idx.ReleasesBetween(config.Version, idx.Latest)`.
2. Decide what to display:
   - **`len(rng) <= 1`** → render the target's `meta.Changes` with
     `RenderChanges` (no prefix). Identical to today's output.
   - **`len(rng) > 1`** → fetch metadata for each in-range version (reusing the
     already-fetched target `meta`), `MergeChanges`, and render with
     `RenderMerged` as one block.
3. Label: `Changelog (v{current} → v{target}):` when more than one release is
   spanned; plain `Changelog:` for the single-release case.

The direct `--version` / `useVersion` path is unchanged (explicit single
install).

### Fallbacks (degrade, never block the update)

- Installed version is a dev build / unparseable (`IsDevBuild`) → no computable
  range → show the target's changelog only (today's behavior).
- Any in-range version lacks structured `changes`, **or** a metadata fetch for
  an in-range version fails → fall back to the target's rendered `changelog`
  string. No silent partial merges.

## Example output

Updating 0.2.0 → 0.4.0:

```
Current version: 0.2.0
Target version:  0.4.0 (2026-…)

Changelog (v0.2.0 → v0.4.0):

Fixed:
- v0.4.0: Properly resolve the issue from 0.3.0 ([#21](…))
- v0.3.0: Partial fix for race ([#14](…))

Added:
- v0.4.0: New preset
- v0.2.0: extra-hosts option ([#11](…))
```

## Verification

- `make test` (selfupdate unit tests, including the consistency test).
- Manual: regenerate the committed JSONs via `--backfill-changes`, confirm
  `make test` passes and the JSON diffs only add `changes`.
- Generator output validated indirectly by the Go consistency test against the
  committed JSONs (the repo has no Python test harness).
