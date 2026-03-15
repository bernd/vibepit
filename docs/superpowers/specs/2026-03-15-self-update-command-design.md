# Self-Update Command Design

## Overview

Enhance the existing `update` command to handle both binary self-update and
container image updates. The binary update uses release metadata served from
`vibepit.dev`, with SHA256 checksum and cosign signature verification.

## Release Metadata

### Structure

Per-version JSON files served from `vibepit.dev/releases/`, with a lightweight
pointer to the latest version.

**`vibepit.dev/releases/latest.json`:**

```json
{
  "version": "0.2.0",
  "previous": "0.1.0",
  "url": "https://vibepit.dev/releases/v0.2.0.json"
}
```

**`vibepit.dev/releases/v0.2.0.json`:**

```json
{
  "version": "0.2.0",
  "previous": "0.1.0",
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

- `latest.json` enables quick version comparison without downloading full
  metadata.
- Each version file links to its predecessor via the `previous` field, forming a
  linked list for rollback navigation.
- First release has `"previous": null`.

### Generation

Release metadata generation happens as the final step of the release workflow in
`build.yml`, after the GitHub release is published (not while it is still a
draft). The publish order is:

1. Build and sign archives (`release-build`, cosign signing).
2. Create GitHub release and upload assets (`release-archive`,
   `release-publish`). The release must be published (not draft) before
   proceeding.
3. Generate release metadata:
   a. Read the changelog from `docs/changelogs/v{VERSION}.yml`.
   b. Get the timestamp from the git tag.
   c. Collect asset URLs, SHA256 checksums, and cosign bundle URLs from the
      published release assets.
   d. Write `docs/content/releases/v{VERSION}.json` and update
      `docs/content/releases/latest.json`.
   e. Commit to `main` and push. This triggers the `pages.yml` workflow to
      deploy the updated metadata to `vibepit.dev/releases/`.

The `pages.yml` workflow must be updated to also trigger on changes to
`docs/content/releases/**`. MkDocs serves files in `docs/content/` as static
assets, so JSON files placed there are deployed as-is without needing nav
entries.

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

`latest.json` contains two optional pointers:

```json
{
  "stable": "0.2.0",
  "prerelease": "0.3.0-alpha.1",
  "url": "https://vibepit.dev/releases/v0.2.0.json"
}
```

- **Stable channel:** versions without prerelease suffixes (e.g., `0.2.0`).
- **Prerelease channel:** versions with prerelease suffixes (e.g.,
  `0.3.0-alpha.1`).
- The `url` field always points to the latest release metadata (stable or
  prerelease, whichever is newer).

Channel selection:

- If the current binary is a prerelease version, compare against the
  `prerelease` field.
- If the current binary is a stable version, compare against the `stable` field.
- Override with `--channel stable` or `--channel prerelease` to switch channels.

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
| `--rollback` | Fetch previous version |
| `--version` | Target version for rollback (used with `--rollback`) |
| `--check` | Check for updates without applying |
| `--channel` | Override channel: `stable` or `prerelease` |

When neither `--bin` nor `--images` is specified, both are updated.

### Update Flow

Binary and image updates are independent paths. Neither gates the other.

**Binary update** (when `--bin` is set or no filter flags are given):

1. Fetch `vibepit.dev/releases/latest.json` (HTTP timeout: 30s).
2. Compare versions (see Version Comparison above).
3. If a newer version is available:
   a. Fetch version-specific metadata (`releases/v{VERSION}.json`).
   b. Display current version, new version, timestamp, and changelog.
   c. Prompt for confirmation ("Update vibepit to v0.2.0? [y/N]"). Skipped with
      `--yes`.
   d. Download the platform-appropriate archive with a progress bar. Degrade to
      line-based progress when stdout is not a TTY. Enforce a maximum archive
      size of 256 MB.
   e. Verify SHA256 checksum.
   f. Verify cosign bundle against Sigstore public good instance (Rekor +
      Fulcio).
   g. Replace the binary (see Binary Replacement below).
4. If already up to date, print "binary is up to date" and continue.

**Image update** (when `--images` is set or no filter flags are given):

5. Pull latest container images (existing image update logic).

**Summary:**

6. Print results for each step that ran.

### Rollback Flow

- `vibepit update --rollback` -- fetch `releases/v{config.Version}.json`, read
  its `previous` field, fetch and install that version. If the current version
  has no release metadata (dev build), fail with an error suggesting
  `--rollback --version v0.1.0`.
- `vibepit update --rollback --version v0.1.0` -- fetch
  `releases/v0.1.0.json` directly.
- Same download, verify, and replace flow as a normal update.
- Rollback applies to the binary only. `--images` is not supported with
  `--rollback` (container image tags are mutable and previous images may not be
  locally cached). Using both flags together is an error.

### Check Flow

`vibepit update --check` fetches `latest.json`, compares versions, and prints
the result without downloading or applying anything.

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
8. Clean up temp files on failure.

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
- **Dev build rollback without version:** Error with guidance to use
  `--rollback --version v{VERSION}`.
- **Release metadata not found (404):** Report the version and suggest using
  `--rollback --version` with a known version.
- **`--rollback` combined with `--images`:** Error explaining that image
  rollback is not supported.
- **Package-managed binary:** Refuse self-update with guidance to use the
  package manager instead.

## Package Structure

New `selfupdate/` package:

| File | Responsibility |
|---|---|
| `selfupdate/releases.go` | Fetch and parse `latest.json` and version metadata |
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
