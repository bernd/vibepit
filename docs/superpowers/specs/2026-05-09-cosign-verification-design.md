# Cosign Verification

Implement sigstore/cosign signature verification for both binary self-update
archives and container images. Uses the `sigstore-go` library for verification
against Sigstore's public-good infrastructure.

## Context

Binary archives are signed in `build.yml` via `cosign sign-blob`. Binary
verification is scaffolded in `selfupdate/cosign.go` but returns a stub error.

Container images are pushed in `docker-publish.yml` with a signing step, but
the signing step has a bug: it uses `steps.meta.outputs.tags` (metadata action
tags like `:main`) instead of the actual pushed tag
(`:r2-uid-<uid>-gid-<gid>`). The metadata action tags are only used for OCI
labels, not for the push, so the cosign sign command references a tag that
doesn't exist in the registry. While cosign signs by digest (the `@sha256:...`
part), the workflow should be fixed to use the correct tag for clarity and
reliability. Container image verification does not exist on the client side.

## Scope

- Fix the `docker-publish.yml` signing step to use the actual pushed image tag.
- Implement binary archive cosign bundle verification (complete the existing stub).
- Implement container image cosign signature verification.
- Hard fail on verification failure; abort the operation.
- Only verify the vibepit image, not the third-party distroless proxy image.
- Skip verification when `--local` flag is set (local images are unsigned).

## Dependencies

Two new direct dependencies:

- `github.com/sigstore/sigstore-go` — verification, bundle loading, TUF root.
- `github.com/sigstore/protobuf-specs` — protobuf types for constructing
  bundles from cosign OCI annotations (`protobundle`, `protocommon`,
  `protorekor`). Already a transitive dependency of sigstore-go; promoted to
  direct because `cosign/image.go` imports the generated Go types.

No additional OCI client libraries. Registry access for fetching cosign
signatures uses direct HTTP calls against the OCI distribution API.

## Package Structure

New `cosign/` package at the project root:

- `cosign/verify.go` — shared verifier construction, identity policy constants,
  and `VerifyBlob()` for binary bundle verification. Accepts a
  `root.TrustedMaterial` parameter so callers can inject a mock trusted root
  in tests or use `root.FetchTrustedRoot()` in production.
- `cosign/image.go` — `VerifyImage()` for container image signature
  verification. Takes an `ImageDigestResolver` interface (not concrete
  `*container.Client`) so tests can mock digest resolution without a Docker
  daemon. Accepts injectable registry and trusted-material providers.
- `cosign/registry.go` — minimal OCI registry HTTP client with token-challenge
  auth to fetch cosign signature manifests and payloads from ghcr.io.

`selfupdate/cosign.go` delegates to `cosign.VerifyBlob()` for the actual
sigstore verification, keeping bundle download logic in `selfupdate/`.

## Identity Policies

Both verification paths use keyless signing with GitHub OIDC. The certificate
identity checks are:

### Binary Archives

- **Issuer:** `https://token.actions.githubusercontent.com`
- **SAN regex:** `https://github.com/bernd/vibepit/.github/workflows/build.yml@.*`

### Container Images

- **Issuer:** `https://token.actions.githubusercontent.com`
- **SAN regex:** `https://github.com/bernd/vibepit/.github/workflows/docker-publish.yml@.*`

## Verifier Options

Both verification paths create a `verify.NewVerifier` with these options:

- `verify.WithTransparencyLog(1)` — require at least one Rekor tlog entry.
- `verify.WithObserverTimestamps(1)` — require at least one observer timestamp
  (Rekor integrated timestamp or RFC 3161 TSA timestamp).
- `verify.WithSignedCertificateTimestamps(1)` — require at least one SCT
  embedded in the Fulcio certificate, verified against CT log authorities in
  the trusted root. Sigstore public-good Fulcio certs include SCTs; requiring
  them ensures the certificate was logged to a CT log.

## CI Workflow Fix (`docker-publish.yml`)

The signing step currently uses `steps.meta.outputs.tags` which produces tags
based on git metadata (e.g. `:main`), not the actual pushed tag. Fix by using
the explicit tag from the `build-push-action`:

```yaml
- name: Sign the published Docker image
  if: ${{ github.event_name != 'pull_request' }}
  env:
    TAG: "${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}:${{ env.IMAGE_REVISION }}-uid-${{ matrix.edition.uid }}-gid-${{ matrix.edition.gid }}"
    DIGEST: ${{ steps.build-and-push.outputs.digest }}
  run: cosign sign --yes "${TAG}@${DIGEST}"
```

This ensures cosign signs the exact image reference that was pushed, making the
signature discoverable and the workflow easier to understand.

## Binary Bundle Verification

**File:** `selfupdate/cosign.go` (existing) + `cosign/verify.go` (new)

Flow (already wired into `cmd/update.go:243-247`):

1. `VerifyCosignBundle()` downloads the `.bundle` file (existing code).
2. Calls `cosign.VerifyBlob(archivePath, bundlePath)`.
3. `VerifyBlob` loads the bundle with `bundle.LoadJSONFromPath()`.
4. Fetches Sigstore's trusted root via `root.FetchTrustedRoot()`.
5. Creates `verify.SignedEntityVerifier` with the trusted root.
6. Builds identity policy with issuer + SAN for `build.yml`.
7. Verifies the artifact against the bundle.

Additionally, remove the `if asset.CosignBundleURL != ""` guard in
`cmd/update.go` so that a missing bundle URL is a hard error. All releases
produced by the CI pipeline include bundle URLs. Allowing an empty URL would
let a compromised metadata file bypass verification entirely, leaving only the
attacker-controlled SHA-256 checksum.

## Container Image Verification

**Files:** `cosign/image.go`, `cosign/registry.go`

### Flow

1. After image pull, resolve the image reference to its digest via Docker API
   (`ImageInspect` → `RepoDigests`). Match the repo digest whose repository
   matches the requested image reference; error if no match or empty list.
2. Derive the cosign signature tag from the digest
   (`sha256-<hash>.sig`).
3. Fetch the signature manifest from ghcr.io using the OCI distribution HTTP
   API (with token-challenge auth).
4. For each signature layer in the manifest, extract the cosign simple-signing
   components from the layer annotations:
   - `dev.cosignproject.cosign/signature` — base64 signature
   - `dev.sigstore.cosign/certificate` — PEM Fulcio certificate
   - `dev.sigstore.cosign/bundle` — JSON Rekor bundle (SET + entry body)
5. Download the simple-signing payload from the layer blob.
6. Parse the payload JSON and validate that
   `critical.image.docker-manifest-digest` matches the expected image digest
   and `critical.identity.docker-reference` matches the expected repository.
   Without this check, a valid signature from `docker-publish.yml` over a
   different image (e.g., a different UID/GID variant) would pass
   cryptographic verification.
7. Construct a `protobundle.Bundle` (v0.1 media type) from these components:
   - `MessageSignature` with SHA-256 digest of payload and decoded signature
   - `VerificationMaterial.Certificate` with DER-decoded cert
   - `TransparencyLogEntry` with canonicalized body, SET as inclusion promise,
     integrated time, log index, and log ID from the Rekor bundle annotation
8. Verify the constructed bundle against Sigstore's trusted root with the
   `docker-publish.yml` identity policy.

### Registry Client (`cosign/registry.go`)

Minimal HTTP client for the OCI distribution spec:

- `GET /v2/<name>/manifests/<tag>` — fetch cosign signature manifest.
- `GET /v2/<name>/blobs/<digest>` — fetch signature layer.
- Authentication: OCI token-challenge flow. Public GHCR returns 401 with a
  `WWW-Authenticate: Bearer realm=...,service=...,scope=...` challenge. The
  client must parse the challenge, fetch an anonymous token from the realm URL,
  and retry the request with `Authorization: Bearer <token>`. No credentials
  are needed for public images but the challenge-response is mandatory.

No go-containerregistry dependency. The OCI distribution API is simple enough
for the three endpoints needed (token, manifest, blob).

## Integration Points

### `container/client.go`

Add `ImageDigest(ctx, ref) (registry, repo, digest string, err error)` method
that calls `ImageInspect` and finds the repo digest whose repository matches
the requested image reference. Returns an error if `RepoDigests` is empty or
no entry matches the requested repo. The verified digest is the manifest-list
digest (what Docker stores after a multi-platform pull).

### `cmd/bootstrap.go`

After `EnsureImage(ctx, u.Image, false)` at line 235:

```go
if !cmd.Bool(localFlag) {
    if err := cosign.VerifyImage(ctx, client, u.Image); err != nil {
        return nil, cleanups, fmt.Errorf("image verification: %w", err)
    }
}
```

### `cmd/update.go`

After `PullImage(ctx, imageName(u), false)` at line 276:

```go
if err := cosign.VerifyImage(ctx, client, imageName(u)); err != nil {
    return fmt.Errorf("image verification: %w", err)
}
```

### `selfupdate/cosign.go`

Replace the stub implementation:

```go
func VerifyCosignBundle(httpClient *http.Client, archivePath, bundleURL string) error {
    bundlePath, err := downloadBundle(httpClient, bundleURL)
    if err != nil {
        return err
    }
    defer os.Remove(bundlePath)
    return cosign.VerifyBlob(archivePath, bundlePath)
}
```

## Error Messages

- Binary: `"cosign verification failed for binary archive: <detail>"`
- Image: `"cosign verification failed for image <ref>: <detail>"`

Both result in hard failures that abort the operation.

## Testing

- Unit tests with mock TUF root and test bundles for `cosign/verify.go`.
- Unit tests for registry client with httptest server for `cosign/registry.go`.
- Unit tests for image verification flow with mocked `ImageDigestResolver` and
  httptest registry server for `cosign/image.go`.
- Integration tests (build-tagged) that verify against real Sigstore
  infrastructure.
- Update existing `selfupdate/cosign_test.go` for the refactored code path.
