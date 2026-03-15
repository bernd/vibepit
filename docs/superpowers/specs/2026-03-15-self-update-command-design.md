# Self-Update Command Design

## Overview

Enhance the existing `update` command to handle both binary self-update and
container image updates. The binary update uses release metadata served from
`vibepit.dev`, with SHA256 checksum and cosign signature verification.

## Release Metadata

### Structure

Per-version JSON files served from `vibepit.dev/releases/`, with per-channel
pointer files to the latest version.

**`vibepit.dev/releases/stable.json`:**

```json
{
  "latest": "0.2.0",
  "releases": [
    {"version": "0.2.0", "timestamp": "2026-03-10T14:32:00Z"},
    {"version": "0.1.0", "timestamp": "2026-02-20T09:00:00Z"}
  ]
}
```

**`vibepit.dev/releases/prerelease.json`:**

```json
{
  "latest": "0.3.0-alpha.1",
  "releases": [
    {"version": "0.3.0-alpha.1", "timestamp": "2026-03-12T10:00:00Z"},
    {"version": "0.2.0-alpha.1", "timestamp": "2026-02-18T08:00:00Z"},
    {"version": "0.1.0-alpha.7", "timestamp": "2026-02-15T12:00:00Z"}
  ]
}
```

- Each channel has its own index file. The default channel is `stable`.
- `latest` field enables quick version comparison without parsing the list.
- `releases` array is sorted newest-first with version and timestamp per entry,
  sufficient for `--list` and `--check` in a single fetch.
- Additional channels can be added in the future (e.g., `nightly.json`) without
  schema changes.
- A channel file may not exist if no release has been published in that channel
  yet.

**`vibepit.dev/releases/v0.2.0.json`:**

```json
{
  "version": "0.2.0",
  "timestamp": "2026-03-10T14:32:00Z",
  "changelog": "- Added self-update command\n- Fixed proxy DNS resolution on macOS",
  "assets": [
    {
      "os": "linux",
      "arch": "amd64",
      "url": "https://github.com/bernd/vibepit/releases/download/v0.2.0/vibepit-0.2.0-linux-x86_64.tar.gz",
      "sha256": "abc123...",
      "cosign_bundle_url": "https://github.com/bernd/vibepit/releases/download/v0.2.0/vibepit-0.2.0-linux-x86_64.tar.gz.bundle"
    }
  ]
}
```

- Per-version files contain the full release metadata (changelog, assets,
  checksums) and are only fetched when actually updating.
- The channel index file provides the version list and timestamps, so per-version
  files do not need to link to each other.
- **Version string convention:** Version strings in JSON payloads are bare (e.g.,
  `0.2.0`). File paths and git tags use the `v` prefix (e.g., `v0.2.0.json`,
  `v0.2.0` tag). The client constructs file paths by prepending `v` to the bare
  version string.

### Generation

Release metadata generation is decoupled from the build workflow. The existing
`build.yml` creates a draft prerelease. When a maintainer publishes the release
from the GitHub UI, a separate workflow generates and deploys the metadata.

**`build.yml` (requires changes):**

1. Build archives (`release-build`).
2. Sign each platform archive with cosign using keyless OIDC signing. This
   requires adding `id-token: write` permission to the workflow (similar to
   `docker-publish.yml`) and a new cosign signing step after `release-build`.
   Each signing produces a `.bundle` file alongside the archive.
3. Create draft prerelease (`release-archive`, `release-publish`). The
   `release-publish` Makefile target must be updated to upload `.bundle` files
   alongside the archive tarballs. The `.bundle` files are not included inside
   the tarballs -- they are separate release assets.

**New `release-metadata.yml` workflow**, triggered by `release: published`:

Permissions: `contents: write` (to push to `main`).
Concurrency group: `release-metadata` with no cancel-in-progress, so concurrent
releases are serialized rather than racing.

1. Read the changelog from `docs/changelogs/v{VERSION}.yml`.
2. Get the timestamp from the git tag.
3. Parse SHA256 checksums from the `checksums.txt` release asset (already
   produced by `release-archive`). Collect asset URLs and cosign bundle URLs
   from the published release assets.
4. Render the changelog YAML into a plain text string for the version JSON.
   Rendering rules: group entries under category headers (`Added`, `Changed`,
   `Fixed`, etc.), each entry as `- {description}`. PR and issue references are
   omitted from the rendered string (they are for documentation, not the update
   CLI output).
5. Write `docs/content/releases/v{VERSION}.json` and update the appropriate
   channel index file (`docs/content/releases/stable.json` or
   `docs/content/releases/prerelease.json`) based on whether the version has a
   prerelease suffix: set `latest` to the new version and prepend the new
   release entry to the `releases` array.
6. Commit to `main` and push. This triggers the `pages.yml` workflow to
   deploy the updated metadata to `vibepit.dev/releases/`.

The existing `pages.yml` trigger on `docs/content/**` already covers the
`docs/content/releases/` path, so no workflow change is needed for deployment.
MkDocs serves files in `docs/content/` as static assets, so JSON files placed
there are deployed as-is without needing nav entries.

## Changelog Files

Structured YAML files at `docs/changelogs/v{VERSION}.yml` using keep-a-changelog
categories. Each entry is a map to support structured metadata.

**`docs/changelogs/v0.2.0.yml`:**

```yaml
version: "0.2.0"
added:
  - description: Self-update command for binary and images
    pr: 42
  - description: Cosign signature verification for downloads
    pr: 45
changed:
  - description: Combined binary and image update into single command
    pr: 43
fixed:
  - description: Proxy DNS resolution on macOS
    issue: 38
    pr: 40
```

No `date` field -- the timestamp is derived from the git tag when generating
release metadata.

## Version Comparison

Release versions follow semver, including prerelease tags (e.g., `0.1.0-alpha.7`,
`0.2.0`). At build time, `config.Version` is set via `git describe --tags`,
which may produce non-semver strings for dev builds (e.g.,
`0.1.0-alpha.7-3-gabcdef`).

### Release Channels

Each channel has its own index file (see Structure above):

- **`stable.json`:** versions without prerelease suffixes (e.g., `0.2.0`).
- **`prerelease.json`:** versions with prerelease suffixes (e.g.,
  `0.3.0-alpha.1`).

Channel selection:

- The default channel is always `stable`.
- Use `--pre` to select the prerelease channel instead.
- **Implicit fallback (default channel only):** If `stable.json` does not exist
  (HTTP 404) and the user did not explicitly pass `--pre`, fall back to
  `prerelease.json`. This handles the early project lifecycle where only
  prerelease versions exist. If both are missing, report that no releases are
  available.
- **Explicit `--pre`:** No fallback. If `prerelease.json` does not exist, report
  that no prerelease versions are available.

### Comparison Rules

- **Same-channel comparison:** If the current binary's version belongs to the
  same channel being checked, compare using full semver ordering. If the local
  version equals or exceeds the channel's latest, report "already up to date."
- **Cross-channel switch:** If the current binary is on a different channel than
  the one being checked (e.g., on prerelease `0.3.0-alpha.1` but checking
  stable), always offer the channel's latest version regardless of semver
  ordering. The user explicitly chose to switch channels.
- **Dev builds:** If `config.Version` contains a `git describe` suffix (e.g.,
  `-3-gabcdef`) or is the default `0.0`, always offer the channel's latest
  release as an update.

## Platform Detection

The `os` and `arch` fields in release metadata use Go's naming convention
(`runtime.GOOS` and `runtime.GOARCH`): `linux`, `darwin`, `amd64`, `arm64`.
Archive filenames may differ (e.g., `x86_64` instead of `amd64`) but the JSON
metadata uses Go conventions for matching.

## Update Command

### Flags

| Flag | Description |
|---|---|
| `--yes` / `-y` | Skip confirmation prompt |
| `--bin` | Update binary only |
| `--images` | Update images only |
| `--use` | Install a specific version (e.g., `--use 0.1.0`); implies `--bin` |
| `--list` | List available releases |
| `--check` | Check for updates without applying |
| `--pre` | Use the prerelease channel instead of stable |

When neither `--bin` nor `--images` is specified, both are updated.

**Flag rules:**

- `--use` implies `--bin` -- it only affects the binary, not images. Using
  `--use` with `--images` is an error.
- `--use` bypasses channel logic entirely -- it fetches the version file
  directly regardless of whether it is stable or prerelease.
- `--list` and `--check` are mutually exclusive with each other and with
  `--use`. They are informational and do not perform updates.
- `--yes` and `--pre` can be combined with any non-informational flag.

### Update Flow

Binary and image updates are independent paths. Neither gates the other.

**Binary update** (when `--bin` is set or no filter flags are given):

1. If `--use` is set, fetch `releases/v{VERSION}.json` directly and skip
   the version comparison. Otherwise, fetch the channel index file
   (`stable.json` by default, or `prerelease.json` with `--pre`) with a 30s
   HTTP timeout and compare
   versions (see Version Comparison above).
2. If a newer version is available (or `--use` was specified):
   a. Display current version, target version, timestamp, and changelog.
   b. Prompt for confirmation ("Install vibepit v0.2.0? [y/N]"). Skipped with
      `--yes`.
   c. Download the platform-appropriate archive with a progress bar. Degrade to
      line-based progress when stdout is not a TTY. Check the `Content-Length`
      header before downloading and reject archives over 256 MB. If the header
      is absent, proceed but cap the reader at 256 MB during streaming as a
      defense-in-depth measure.
   d. Verify SHA256 checksum.
   e. Verify cosign bundle against Sigstore public good instance (Rekor +
      Fulcio).
   f. Replace the binary (see Binary Replacement below).
3. If already up to date, print "binary is up to date" and continue.

**Image update** (when `--images` is set or no filter flags are given):

4. Pull latest container images (existing image update logic).

**Summary:**

5. Print results for each step that ran.

### List Flow

`vibepit update --list` fetches the channel index file and prints available
releases:

```
VERSION         TIMESTAMP
0.2.0           2026-03-10T14:32:00Z
0.1.0           2026-02-20T09:00:00Z  (installed)
```

- Single fetch of the channel index file — no additional requests needed.
- Marks the currently installed version with `(installed)`.
- Use `--pre` to list prerelease versions instead of stable.

### Check Flow

`vibepit update --check` fetches the channel index file (`stable.json` by
default, or `prerelease.json` with `--pre`), compares versions, and prints the
result without downloading or applying anything.

## Package Manager Detection

Before attempting binary replacement, check whether the binary was installed via
a package manager (e.g., Homebrew, system package). Detect by checking if the
resolved binary path is inside a known package-managed prefix:

- `/opt/homebrew/` or `/usr/local/Cellar/` (Homebrew)
- `/usr/bin/`, `/usr/sbin/` (system packages)
- `/nix/store/` (Nix)
- `/snap/` (Snap)

If detected, refuse the update with a message like:
"vibepit appears to be managed by Homebrew. Use `brew upgrade vibepit` instead."

This check only applies to binary self-update. Image updates proceed regardless.

## Binary Replacement

### POSIX (Linux/macOS)

1. Resolve the running binary path via `os.Executable()` and
   `filepath.EvalSymlinks`.
2. Check write permission to that directory. If not writable, fail with a
   message suggesting `sudo` or relocating the binary.
3. Download archive to a temp file in the same directory as the binary (ensures
   same filesystem for atomic rename).
4. Verify checksum and cosign signature.
5. Extract the `vibepit` binary from the tarball to a temp file in the same
   directory. Validate that the extracted path is exactly the expected filename
   -- reject any path containing separators or traversal components.
6. `os.Rename` the temp file over the current binary (atomic on POSIX).
7. Preserve original file permissions via `os.Chmod`.
8. Clean up the archive temp file after successful extraction.
9. Clean up all temp files on failure.

### Windows (future work)

No Windows build target exists yet. When Windows support is added, the
replacement strategy is:

1. Rename current binary to `vibepit.old` (Windows allows renaming a locked
   file but not deleting it).
2. Rename new binary into place.
3. On next run, clean up `vibepit.old` if it exists.

## Cosign Verification

### Signing (CI)

- Keyless signing via GitHub Actions OIDC using `sigstore/cosign`.
- Sign each platform archive after building.
- Produces a `.bundle` file per archive, uploaded alongside release assets.

### Verification (Client)

- Use `sigstore/sigstore-go` to verify bundles programmatically.
- Verify against Sigstore's public good instance (Rekor + Fulcio).
- Certificate issuer must match
  `https://token.actions.githubusercontent.com`.
- Certificate SAN (identity) must match
  `https://github.com/bernd/vibepit/.github/workflows/build.yml`.
- Verification runs after SHA256 check -- fail fast on checksum mismatch before
  the more expensive signature verification.
- Verification is mandatory. No `--skip-verify` flag.

## Error Handling

- **No write permission:** Clear message suggesting `sudo` or relocating the
  binary.
- **Network failure:** Report which step failed (metadata fetch, download, or
  verification).
- **Checksum mismatch:** Abort with error, do not attempt signature
  verification.
- **Signature verification failure:** Hard stop, do not replace binary.
- **Channel file not found (404):** If using the default stable channel, fall
  back to prerelease. If `--pre` was explicit, report that no prerelease
  versions are available. If both channels are missing, report that no releases
  are available.
- **Version metadata not found (404):** Report the requested version and suggest
  `vibepit update --list` to see available releases.
- **Package-managed binary:** Refuse self-update with guidance to use the
  package manager instead.

## Package Structure

New `selfupdate/` package:

| File | Responsibility |
|---|---|
| `selfupdate/releases.go` | Fetch and parse channel pointer files and version metadata |
| `selfupdate/download.go` | Download archive with progress bar |
| `selfupdate/verify.go` | SHA256 checksum and cosign bundle verification |
| `selfupdate/replace.go` | Binary replacement (POSIX and Windows strategies) |
| `selfupdate/version.go` | Semver comparison |

`cmd/update.go` orchestrates the flow, calling into `selfupdate/` for the
binary update and `container/` for image updates.

### Dependencies

- `sigstore/sigstore-go` -- Sigstore bundle verification (new dependency).
- `golang.org/x/mod/semver` -- already an indirect dependency in `go.mod`,
  promoted to direct for version comparison.
- No other new dependencies.

## No Automatic Update Checks

The CLI does not check for updates automatically. Updates are only performed when
the user explicitly runs `vibepit update`.
