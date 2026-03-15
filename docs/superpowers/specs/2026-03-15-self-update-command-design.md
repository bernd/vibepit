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

A CI step in `build.yml` generates the version JSON after `release-publish`:

1. Read the changelog from `docs/changelogs/v{VERSION}.yml`.
2. Get the timestamp from the git tag.
3. Collect asset URLs, SHA256 checksums, and cosign bundle URLs from the release
   artifacts.
4. Write `releases/v{VERSION}.json` and update `releases/latest.json`.
5. Commit to the docs branch for GitHub Pages deployment.

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

## Update Command

### Flags

| Flag | Description |
|---|---|
| `--yes` / `-y` | Skip confirmation prompt |
| `--bin` | Update binary only |
| `--images` | Update images only |
| `--rollback [version]` | Fetch previous version (or a specific version) |
| `--check` | Check for updates without applying |

When neither `--bin` nor `--images` is specified, both are updated.

### Update Flow

1. Fetch `vibepit.dev/releases/latest.json`.
2. Compare `latest.version` with `config.Version`. If already up to date, print
   message and exit.
3. Fetch version-specific metadata (`releases/v{VERSION}.json`).
4. Display current version, new version, timestamp, and changelog.
5. Prompt for confirmation ("Update vibepit to v0.2.0? [y/N]"). Skipped with
   `--yes`.
6. Download the platform-appropriate archive with a progress bar.
7. Verify SHA256 checksum.
8. Verify cosign bundle against Sigstore public good instance (Rekor + Fulcio).
   Check that the OIDC identity matches the expected GitHub Actions workflow.
9. Replace the binary (see Binary Replacement below).
10. Pull latest container images (existing image update logic).
11. Print summary: "Updated vibepit v0.1.0 -> v0.2.0".

### Rollback Flow

- `vibepit update --rollback` -- fetch `releases/v{config.Version}.json`, read
  its `previous` field, fetch and install that version.
- `vibepit update --rollback v0.1.0` -- fetch `releases/v0.1.0.json` directly.
- Same download, verify, and replace flow as a normal update.
- `--bin` and `--images` flags apply to rollback as well.

### Check Flow

`vibepit update --check` fetches `latest.json`, compares versions, and prints
the result without downloading or applying anything.

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
   directory.
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

- Use `sigstore/cosign-go` to verify bundles programmatically.
- Verify against Sigstore's public good instance (Rekor + Fulcio).
- Check that the OIDC identity matches
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

- `sigstore/cosign-go` -- cosign signature verification (new dependency).
- No other new dependencies.

## No Automatic Update Checks

The CLI does not check for updates automatically. Updates are only performed when
the user explicitly runs `vibepit update`.
