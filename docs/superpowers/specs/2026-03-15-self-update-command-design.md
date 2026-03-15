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

### Generation

Release metadata generation is decoupled from the build workflow. The existing
`build.yml` creates a draft prerelease. When a maintainer publishes the release
from the GitHub UI, a separate workflow generates and deploys the metadata.

**`build.yml` (existing, unchanged):**

1. Build and sign archives (`release-build`, cosign signing).
2. Create draft prerelease and upload assets (`release-archive`,
   `release-publish`).

**New `release-metadata.yml` workflow**, triggered by `release: published`:

1. Read the changelog from `docs/changelogs/v{VERSION}.yml`.
2. Get the timestamp from the git tag.
3. Collect asset URLs, SHA256 checksums, and cosign bundle URLs from the
   published release assets.
4. Write `docs/content/releases/v{VERSION}.json` and update the appropriate
   channel index file (`docs/content/releases/stable.json` or
   `docs/content/releases/prerelease.json`) based on whether the version has a
   prerelease suffix: set `latest` to the new version and prepend the new
   release entry to the `releases` array.
5. Commit to `main` and push. This triggers the `pages.yml` workflow to
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

Each channel has its own pointer file (see Structure above):

- **`stable.json`:** versions without prerelease suffixes (e.g., `0.2.0`).
- **`prerelease.json`:** versions with prerelease suffixes (e.g.,
  `0.3.0-alpha.1`).

Channel selection:

- If the current binary is a prerelease version, fetch `prerelease.json`.
- If the current binary is a stable version, fetch `stable.json`.
- Override with `--channel stable` or `--channel prerelease` to switch channels.
- Default channel (for dev builds) is `stable`.

### Comparison Rules

- **Tagged release builds (stable or prerelease):** Compare using full semver
  ordering (prerelease versions sort lower than their release counterpart per
  semver spec). If the local version equals or exceeds the channel's latest,
  report "already up to date."
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
| `--version` | Install a specific version (e.g., `--version 0.1.0`) |
| `--list` | List available releases |
| `--check` | Check for updates without applying |
| `--channel` | Override channel: `stable` or `prerelease` |

When neither `--bin` nor `--images` is specified, both are updated.

### Update Flow

Binary and image updates are independent paths. Neither gates the other.

**Binary update** (when `--bin` is set or no filter flags are given):

1. If `--version` is set, fetch `releases/v{VERSION}.json` directly and skip
   the version comparison. Otherwise, fetch the channel pointer file (e.g.,
   `vibepit.dev/releases/stable.json`) with a 30s HTTP timeout and compare
   versions (see Version Comparison above).
2. If a newer version is available (or `--version` was specified):
   a. Display current version, target version, timestamp, and changelog.
   b. Prompt for confirmation ("Install vibepit v0.2.0? [y/N]"). Skipped with
      `--yes`.
   c. Download the platform-appropriate archive with a progress bar. Degrade to
      line-based progress when stdout is not a TTY. Check the `Content-Length`
      header before downloading and reject archives over 256 MB. Also cap the
      reader during streaming as a defense-in-depth measure.
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
- Respects `--channel` to list a specific channel's releases.

### Check Flow

`vibepit update --check` fetches the channel pointer file, compares versions,
and prints the result without downloading or applying anything.

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

### Windows

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
- **Release metadata not found (404):** Report the requested version and suggest
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
- No other new dependencies.

## No Automatic Update Checks

The CLI does not check for updates automatically. Updates are only performed when
the user explicitly runs `vibepit update`.
