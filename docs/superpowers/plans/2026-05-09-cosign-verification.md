# Cosign Verification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Verify cosign signatures on binary release archives and container images using sigstore-go, refusing to use unsigned or tampered artifacts.

**Architecture:** New `cosign/` package provides shared verification logic. Binary verification completes the existing stub in `selfupdate/cosign.go` by delegating to `cosign.VerifyBlob()`. Image verification fetches cosign signatures from the OCI registry via HTTP, constructs a sigstore-go bundle, and verifies with `cosign.VerifyImage()`. Both paths use keyless identity policies matching GitHub Actions OIDC certificates.

**Tech Stack:** `github.com/sigstore/sigstore-go` v1.x (bundle loading, TUF trusted root, certificate identity verification), `github.com/sigstore/protobuf-specs` (protobuf types for constructing bundles from cosign OCI annotations)

---

### Task 1: Add sigstore-go dependency

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: Add the dependency**

```bash
go get github.com/sigstore/sigstore-go@latest
```

- [ ] **Step 2: Tidy modules**

```bash
go mod tidy
```

- [ ] **Step 3: Verify the project builds**

```bash
make build
```

Expected: Build succeeds with no errors.

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "Add sigstore-go dependency for cosign verification"
```

---

### Task 2: Fix docker-publish.yml signing step

**Files:**
- Modify: `.github/workflows/docker-publish.yml`

- [ ] **Step 1: Update the signing step**

Replace the signing step (lines 102-110) so it uses the actual pushed image tag instead of metadata action tags:

```yaml
      - name: Sign the published Docker image
        if: ${{ github.event_name != 'pull_request' }}
        env:
          TAG: "${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}:${{ env.IMAGE_REVISION }}-uid-${{ matrix.edition.uid }}-gid-${{ matrix.edition.gid }}"
          DIGEST: ${{ steps.build-and-push.outputs.digest }}
        run: cosign sign --yes "${TAG}@${DIGEST}"
```

- [ ] **Step 2: Commit**

```bash
git add .github/workflows/docker-publish.yml
git commit -m "Fix cosign signing to use actual pushed image tag"
```

---

### Task 3: Implement blob verification (`cosign/verify.go`)

This is the shared verification core. It provides `VerifyBlob()` for binary
archive bundles and exports identity policy constants used by image
verification.

**Files:**
- Create: `cosign/verify.go`
- Create: `cosign/verify_test.go`

- [ ] **Step 1: Write the failing test**

Create `cosign/verify_test.go`:

```go
package cosign

import (
	"crypto/sha256"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyBlobInvalidBundle(t *testing.T) {
	// Create a temp artifact file.
	artifact, err := os.CreateTemp("", "artifact-*")
	require.NoError(t, err)
	defer os.Remove(artifact.Name())
	_, err = artifact.WriteString("test artifact content")
	require.NoError(t, err)
	artifact.Close()

	// Create a temp bundle file with invalid JSON.
	bundleFile, err := os.CreateTemp("", "bundle-*.json")
	require.NoError(t, err)
	defer os.Remove(bundleFile.Name())
	_, err = bundleFile.WriteString("{}")
	require.NoError(t, err)
	bundleFile.Close()

	err = VerifyBlob(artifact.Name(), bundleFile.Name())
	assert.Error(t, err)
}

func TestVerifyBlobMissingArtifact(t *testing.T) {
	bundleFile, err := os.CreateTemp("", "bundle-*.json")
	require.NoError(t, err)
	defer os.Remove(bundleFile.Name())
	bundleFile.Close()

	err = VerifyBlob("/nonexistent/artifact", bundleFile.Name())
	assert.Error(t, err)
}

func TestVerifyBlobMissingBundle(t *testing.T) {
	artifact, err := os.CreateTemp("", "artifact-*")
	require.NoError(t, err)
	defer os.Remove(artifact.Name())
	artifact.Close()

	err = VerifyBlob(artifact.Name(), "/nonexistent/bundle.json")
	assert.Error(t, err)
}

func TestIdentityConstants(t *testing.T) {
	assert.Equal(t, "https://token.actions.githubusercontent.com", OIDCIssuer)
	assert.Contains(t, BinarySANRegex, "build.yml")
	assert.Contains(t, ImageSANRegex, "docker-publish.yml")
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./cosign/ -run TestVerifyBlob -v
```

Expected: Compilation error — `cosign` package does not exist yet.

- [ ] **Step 3: Write the implementation**

Create `cosign/verify.go`:

```go
package cosign

import (
	"fmt"
	"os"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

const (
	OIDCIssuer     = "https://token.actions.githubusercontent.com"
	BinarySANRegex = `^https://github\.com/bernd/vibepit/\.github/workflows/build\.yml@.*`
	ImageSANRegex  = `^https://github\.com/bernd/vibepit/\.github/workflows/docker-publish\.yml@.*`
)

// DefaultTrustedMaterial fetches Sigstore's public-good trusted root via TUF.
// Exposed as a variable so tests can replace it with a mock.
var DefaultTrustedMaterial func() (root.TrustedMaterial, error) = func() (root.TrustedMaterial, error) {
	return root.FetchTrustedRoot()
}

// newVerifier creates a SignedEntityVerifier with the standard verification
// options: SCT, transparency log, and observer timestamps.
func newVerifier(tm root.TrustedMaterial) (*verify.Verifier, error) {
	return verify.NewVerifier(tm,
		verify.WithSignedCertificateTimestamps(1),
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	)
}

// VerifyBlob verifies a cosign bundle for the artifact at artifactPath.
// The bundle must be a Sigstore bundle JSON file (as produced by cosign
// sign-blob --bundle).
func VerifyBlob(artifactPath, bundlePath string) error {
	b, err := bundle.LoadJSONFromPath(bundlePath)
	if err != nil {
		return fmt.Errorf("load cosign bundle: %w", err)
	}

	tm, err := DefaultTrustedMaterial()
	if err != nil {
		return fmt.Errorf("fetch sigstore trusted root: %w", err)
	}

	verifier, err := newVerifier(tm)
	if err != nil {
		return fmt.Errorf("create verifier: %w", err)
	}

	certID, err := verify.NewShortCertificateIdentity(
		OIDCIssuer, "", "", BinarySANRegex,
	)
	if err != nil {
		return fmt.Errorf("create certificate identity: %w", err)
	}

	artifact, err := os.Open(artifactPath)
	if err != nil {
		return fmt.Errorf("open artifact: %w", err)
	}
	defer artifact.Close()

	_, err = verifier.Verify(b, verify.NewPolicy(
		verify.WithArtifact(artifact),
		verify.WithCertificateIdentity(certID),
	))
	if err != nil {
		return fmt.Errorf("cosign verification failed for binary archive: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests**

```bash
go test ./cosign/ -v
```

Expected: All tests pass (invalid bundles produce errors, missing files produce errors, constants are correct).

- [ ] **Step 5: Commit**

```bash
git add cosign/verify.go cosign/verify_test.go
git commit -m "Add cosign blob verification with sigstore-go"
```

---

### Task 4: Wire blob verification into selfupdate

Replace the stub in `selfupdate/cosign.go` so it delegates to `cosign.VerifyBlob()`.
Also remove the `if asset.CosignBundleURL != ""` guard in `cmd/update.go` so a
missing bundle URL is a hard error (all CI-produced releases include bundles;
allowing empty would let compromised metadata bypass verification).

**Files:**
- Modify: `selfupdate/cosign.go`
- Modify: `selfupdate/cosign_test.go`
- Modify: `cmd/update.go`

- [ ] **Step 1: Update the test for the new behavior**

Replace `selfupdate/cosign_test.go`:

```go
package selfupdate

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVerifyCosignBundleBadURL(t *testing.T) {
	err := VerifyCosignBundle(&http.Client{}, "/nonexistent/file", "http://invalid.test/bundle")
	assert.Error(t, err)
}

func TestVerifyCosignBundleEmptyBundle(t *testing.T) {
	// Serve an empty JSON object as the bundle — verification should fail.
	ts := httpTestServer(t, "{}")
	defer ts.Close()

	err := VerifyCosignBundle(&http.Client{}, "/nonexistent/artifact", ts.URL)
	assert.Error(t, err)
}
```

Note: `httpTestServer` is a helper. If the selfupdate package doesn't have one, add it inline:

```go
func httpTestServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
}
```

- [ ] **Step 2: Run the test to confirm current behavior**

```bash
go test ./selfupdate/ -run TestVerifyCosignBundle -v
```

Expected: `TestVerifyCosignBundleBadURL` passes (download fails). `TestVerifyCosignBundleEmptyBundle` may fail if the httptest helper doesn't exist yet.

- [ ] **Step 3: Update selfupdate/cosign.go**

Replace the stub implementation. Keep `downloadBundle()` as-is. Replace `VerifyCosignBundle()`:

```go
package selfupdate

import (
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/bernd/vibepit/cosign"
)

// VerifyCosignBundle verifies the cosign bundle for the archive at archivePath.
// Downloads the bundle from bundleURL and verifies against Sigstore's public
// good instance.
//
// Verification checks:
// - Certificate issuer: https://token.actions.githubusercontent.com
// - Certificate SAN (prefix): https://github.com/bernd/vibepit/.github/workflows/build.yml
func VerifyCosignBundle(httpClient *http.Client, archivePath, bundleURL string) error {
	bundlePath, err := downloadBundle(httpClient, bundleURL)
	if err != nil {
		return err
	}
	defer os.Remove(bundlePath)

	if err := cosign.VerifyBlob(archivePath, bundlePath); err != nil {
		return fmt.Errorf("cosign verification failed: %w", err)
	}
	return nil
}

const maxBundleSize = 10 * 1024 * 1024

func downloadBundle(httpClient *http.Client, url string) (string, error) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return "", fmt.Errorf("download cosign bundle: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download cosign bundle: HTTP %d", resp.StatusCode)
	}

	f, err := os.CreateTemp("", ".vibepit-bundle-*")
	if err != nil {
		return "", fmt.Errorf("create temp bundle file: %w", err)
	}

	if _, err := io.Copy(f, io.LimitReader(resp.Body, maxBundleSize)); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", fmt.Errorf("write bundle: %w", err)
	}
	f.Close()
	return f.Name(), nil
}
```

- [ ] **Step 4: Run the tests**

```bash
go test ./selfupdate/ -run TestVerifyCosignBundle -v
```

Expected: Both tests pass (bad URL fails download, empty bundle fails verification).

- [ ] **Step 5: Remove the empty-bundle-URL guard in cmd/update.go**

In `cmd/update.go`, replace the conditional at lines 242-247:

```go
	// Verify cosign bundle.
	if asset.CosignBundleURL == "" {
		return fmt.Errorf("release metadata missing cosign bundle URL for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if err := selfupdate.VerifyCosignBundle(client.HTTPClient, archivePath, asset.CosignBundleURL); err != nil {
		return err
	}
```

- [ ] **Step 6: Run full test suite**

```bash
make test
```

Expected: All tests pass.

- [ ] **Step 7: Commit**

```bash
git add selfupdate/cosign.go selfupdate/cosign_test.go cmd/update.go
git commit -m "Wire cosign blob verification through to sigstore-go

Require cosign bundle URL in release metadata; a missing URL is now a
hard error to prevent verification bypass via metadata tampering."
```

---

### Task 5: Implement OCI registry client (`cosign/registry.go`)

Minimal HTTP client to fetch cosign signature manifests and blobs from an OCI
registry. Follows the OCI distribution spec for `GET /v2/<name>/manifests/<tag>`
and `GET /v2/<name>/blobs/<digest>`. Handles the Bearer token-challenge flow
required by GHCR (even for public images, GHCR returns 401 with a
`WWW-Authenticate: Bearer realm=...,service=...,scope=...` challenge).

**Files:**
- Create: `cosign/registry.go`
- Create: `cosign/registry_test.go`

- [ ] **Step 1: Write the failing tests**

Create `cosign/registry_test.go`:

```go
package cosign

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSigTagFromDigest(t *testing.T) {
	tag := sigTagFromDigest("sha256:abc123def456")
	assert.Equal(t, "sha256-abc123def456.sig", tag)
}

func TestFetchSignatureManifest(t *testing.T) {
	manifest := ociManifest{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.manifest.v1+json",
		Layers: []ociLayer{
			{
				MediaType: "application/vnd.dev.cosign.simplesigning.v1+json",
				Digest:    "sha256:layerdigest",
				Annotations: map[string]string{
					"dev.cosignproject.cosign/signature":  "dGVzdHNpZw==",
					"dev.sigstore.cosign/certificate":     "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
					"dev.sigstore.cosign/bundle":          `{"SignedEntryTimestamp":"dGVzdA==","Payload":{"body":"dGVzdA==","integratedTime":1234,"logIndex":42,"logID":"deadbeef"}}`,
				},
			},
		},
	}
	manifestJSON, err := json.Marshal(manifest)
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/bernd/vibepit/manifests/sha256-abc123.sig":
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.Write(manifestJSON)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	rc := &registryClient{httpClient: srv.Client(), baseURL: srv.URL}
	m, err := rc.fetchSignatureManifest(context.Background(), "bernd/vibepit", "sha256:abc123")
	require.NoError(t, err)
	require.Len(t, m.Layers, 1)
	assert.Equal(t, "sha256:layerdigest", m.Layers[0].Digest)
}

func TestFetchSignatureManifestNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	rc := &registryClient{httpClient: srv.Client(), baseURL: srv.URL}
	_, err := rc.fetchSignatureManifest(context.Background(), "bernd/vibepit", "sha256:abc123")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no cosign signature found")
}

func TestFetchBlob(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/bernd/vibepit/blobs/sha256:blobdigest" {
			w.Write([]byte(`{"critical":{"type":"cosign container image signature"}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	rc := &registryClient{httpClient: srv.Client(), baseURL: srv.URL}
	data, err := rc.fetchBlob(context.Background(), "bernd/vibepit", "sha256:blobdigest")
	require.NoError(t, err)
	assert.Contains(t, string(data), "cosign container image signature")
}

func TestTokenChallenge(t *testing.T) {
	// Simulate GHCR's Bearer token challenge flow.
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "ghcr.io", r.URL.Query().Get("service"))
		assert.Contains(t, r.URL.Query().Get("scope"), "repository:bernd/vibepit:pull")
		w.Write([]byte(`{"token":"test-token-123"}`))
	}))
	defer tokenSrv.Close()

	manifest := ociManifest{SchemaVersion: 2, Layers: []ociLayer{}}
	manifestJSON, _ := json.Marshal(manifest)

	registrySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" {
			w.Header().Set("WWW-Authenticate",
				fmt.Sprintf(`Bearer realm="%s",service="ghcr.io",scope="repository:bernd/vibepit:pull"`, tokenSrv.URL))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		assert.Equal(t, "Bearer test-token-123", auth)
		w.Write(manifestJSON)
	}))
	defer registrySrv.Close()

	rc := &registryClient{httpClient: registrySrv.Client(), baseURL: registrySrv.URL}
	m, err := rc.fetchSignatureManifest(context.Background(), "bernd/vibepit", "sha256:abc123")
	require.NoError(t, err)
	assert.Equal(t, 2, m.SchemaVersion)
}

func TestParseWWWAuthenticate(t *testing.T) {
	tests := []struct {
		name      string
		header    string
		wantRealm string
		wantOK    bool
	}{
		{
			name:      "standard GHCR",
			header:    `Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:bernd/vibepit:pull"`,
			wantRealm: "https://ghcr.io/token?service=ghcr.io&scope=repository%3Abernd%2Fvibepit%3Apull",
			wantOK:    true,
		},
		{
			name:   "missing realm",
			header: `Bearer service="ghcr.io"`,
			wantOK: false,
		},
		{
			name:   "not bearer",
			header: `Basic realm="foo"`,
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, ok := parseWWWAuthenticate(tt.header)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.wantRealm, url)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./cosign/ -run TestSigTag -v
```

Expected: Compilation error — `sigTagFromDigest` and `registryClient` don't exist.

- [ ] **Step 3: Write the implementation**

Create `cosign/registry.go`:

```go
package cosign

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxManifestSize = 4 * 1024 * 1024

type ociManifest struct {
	SchemaVersion int        `json:"schemaVersion"`
	MediaType     string     `json:"mediaType"`
	Layers        []ociLayer `json:"layers"`
}

type ociLayer struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations"`
}

type registryClient struct {
	httpClient *http.Client
	baseURL    string
	token      string
}

func newRegistryClient(registryURL string) *registryClient {
	return &registryClient{
		httpClient: &http.Client{},
		baseURL:    registryURL,
	}
}

// sigTagFromDigest derives the cosign signature tag from an image digest.
// E.g., "sha256:abc123" becomes "sha256-abc123.sig".
func sigTagFromDigest(digest string) string {
	return strings.Replace(digest, ":", "-", 1) + ".sig"
}

// doRegistryRequest performs an HTTP request with Bearer token auth. On a 401
// response with a WWW-Authenticate Bearer challenge, it fetches an anonymous
// token and retries once.
func (rc *registryClient) doRegistryRequest(ctx context.Context, req *http.Request) (*http.Response, error) {
	if rc.token != "" {
		req.Header.Set("Authorization", "Bearer "+rc.token)
	}

	resp, err := rc.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusUnauthorized || rc.token != "" {
		return resp, nil
	}

	// Handle Bearer challenge.
	challenge := resp.Header.Get("WWW-Authenticate")
	resp.Body.Close()

	tokenURL, ok := parseWWWAuthenticate(challenge)
	if !ok {
		return nil, fmt.Errorf("registry returned 401 without a usable Bearer challenge")
	}

	token, err := fetchAnonymousToken(ctx, rc.httpClient, tokenURL)
	if err != nil {
		return nil, fmt.Errorf("fetch registry token: %w", err)
	}
	rc.token = token

	// Retry with token.
	retry, err := http.NewRequestWithContext(ctx, req.Method, req.URL.String(), nil)
	if err != nil {
		return nil, err
	}
	for k, v := range req.Header {
		retry.Header[k] = v
	}
	retry.Header.Set("Authorization", "Bearer "+rc.token)
	return rc.httpClient.Do(retry)
}

// parseWWWAuthenticate parses a WWW-Authenticate Bearer challenge header and
// returns a token endpoint URL with query parameters. Returns false if the
// header is not a valid Bearer challenge with a realm.
func parseWWWAuthenticate(header string) (string, bool) {
	if !strings.HasPrefix(header, "Bearer ") {
		return "", false
	}
	params := header[len("Bearer "):]

	fields := map[string]string{}
	for _, part := range strings.Split(params, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 {
			fields[kv[0]] = strings.Trim(kv[1], `"`)
		}
	}

	realm, ok := fields["realm"]
	if !ok || realm == "" {
		return "", false
	}

	q := url.Values{}
	if svc, ok := fields["service"]; ok {
		q.Set("service", svc)
	}
	if scope, ok := fields["scope"]; ok {
		q.Set("scope", scope)
	}

	if len(q) > 0 {
		return realm + "?" + q.Encode(), true
	}
	return realm, true
}

type tokenResponse struct {
	Token string `json:"token"`
}

func fetchAnonymousToken(ctx context.Context, client *http.Client, tokenURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned HTTP %d", resp.StatusCode)
	}
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	return tr.Token, nil
}

// fetchSignatureManifest fetches the cosign signature manifest for a given
// image digest from the registry.
func (rc *registryClient) fetchSignatureManifest(ctx context.Context, repo, digest string) (*ociManifest, error) {
	sigTag := sigTagFromDigest(digest)
	reqURL := fmt.Sprintf("%s/v2/%s/manifests/%s", rc.baseURL, repo, sigTag)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")

	resp, err := rc.doRegistryRequest(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("fetch cosign signature: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("no cosign signature found for digest %s", digest)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch cosign signature: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestSize))
	if err != nil {
		return nil, fmt.Errorf("read signature manifest: %w", err)
	}

	var m ociManifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("parse signature manifest: %w", err)
	}
	return &m, nil
}

// fetchBlob downloads a blob by digest from the registry.
func (rc *registryClient) fetchBlob(ctx context.Context, repo, digest string) ([]byte, error) {
	reqURL := fmt.Sprintf("%s/v2/%s/blobs/%s", rc.baseURL, repo, digest)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := rc.doRegistryRequest(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("fetch blob: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch blob %s: HTTP %d", digest, resp.StatusCode)
	}

	return io.ReadAll(io.LimitReader(resp.Body, maxManifestSize))
}
```

- [ ] **Step 4: Run the tests**

```bash
go test ./cosign/ -run 'TestSigTag|TestFetchSignature|TestFetchBlob' -v
```

Expected: All tests pass.

- [ ] **Step 5: Commit**

```bash
git add cosign/registry.go cosign/registry_test.go
git commit -m "Add OCI registry client for cosign signature fetching"
```

---

### Task 6: Add ImageDigest method to container client

**Files:**
- Modify: `container/client.go`
- Create: `container/client_digest_test.go`

- [ ] **Step 1: Write the failing test**

Create `container/client_digest_test.go`:

```go
package container

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseRepoDigest(t *testing.T) {
	tests := []struct {
		name       string
		repoDigest string
		wantRepo   string
		wantDigest string
		wantErr    bool
	}{
		{
			name:       "standard digest",
			repoDigest: "ghcr.io/bernd/vibepit@sha256:abc123def",
			wantRepo:   "bernd/vibepit",
			wantDigest: "sha256:abc123def",
		},
		{
			name:       "docker hub",
			repoDigest: "docker.io/library/ubuntu@sha256:abc123",
			wantRepo:   "library/ubuntu",
			wantDigest: "sha256:abc123",
		},
		{
			name:       "invalid format",
			repoDigest: "nope",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, digest, err := ParseRepoDigest(tt.repoDigest)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.wantRepo, repo)
			assert.Equal(t, tt.wantDigest, digest)
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./container/ -run TestParseRepoDigest -v
```

Expected: Compilation error — `ParseRepoDigest` doesn't exist.

- [ ] **Step 3: Write the implementation**

Add to `container/client.go`:

```go
// ImageDigest returns the registry, repository name, and manifest-list digest
// for a locally available image. Matches the RepoDigests entry whose
// repository matches the requested image reference. Returns an error if
// RepoDigests is empty or no entry matches.
func (c *Client) ImageDigest(ctx context.Context, ref string) (registry, repo, digest string, err error) {
	inspect, err := c.docker.ImageInspect(ctx, ref)
	if err != nil {
		return "", "", "", fmt.Errorf("inspect image %s: %w", ref, err)
	}
	if len(inspect.RepoDigests) == 0 {
		return "", "", "", fmt.Errorf("image %s has no repo digests (local-only image?)", ref)
	}

	// Strip tag from ref to get the repo prefix for matching.
	// "ghcr.io/bernd/vibepit:r2-uid-1000-gid-1000" -> "ghcr.io/bernd/vibepit"
	refRepo := ref
	if idx := strings.LastIndex(ref, ":"); idx > 0 {
		// Only strip if it looks like a tag (no slash after colon).
		if !strings.Contains(ref[idx:], "/") {
			refRepo = ref[:idx]
		}
	}

	// Find the RepoDigests entry matching the requested repo.
	for _, rd := range inspect.RepoDigests {
		if !strings.HasPrefix(rd, refRepo+"@") {
			continue
		}
		repo, digest, err = ParseRepoDigest(rd)
		if err != nil {
			return "", "", "", err
		}
		parts := strings.SplitN(rd, "/", 2)
		if len(parts) < 2 {
			return "", "", "", fmt.Errorf("cannot determine registry from %s", rd)
		}
		registry = parts[0]
		return registry, repo, digest, nil
	}
	return "", "", "", fmt.Errorf("no repo digest matching %s in %v", refRepo, inspect.RepoDigests)
}

// ParseRepoDigest splits a repo digest string (e.g.,
// "ghcr.io/bernd/vibepit@sha256:abc") into repository name (without
// registry prefix) and digest.
func ParseRepoDigest(repoDigest string) (repo, digest string, err error) {
	at := strings.LastIndex(repoDigest, "@")
	if at < 0 {
		return "", "", fmt.Errorf("invalid repo digest (no @): %s", repoDigest)
	}
	fullRepo := repoDigest[:at]
	digest = repoDigest[at+1:]

	// Strip registry prefix (first path component containing a dot or colon).
	slash := strings.Index(fullRepo, "/")
	if slash < 0 {
		return "", "", fmt.Errorf("invalid repo digest (no /): %s", repoDigest)
	}
	prefix := fullRepo[:slash]
	if strings.Contains(prefix, ".") || strings.Contains(prefix, ":") {
		repo = fullRepo[slash+1:]
	} else {
		repo = fullRepo
	}
	return repo, digest, nil
}
```

- [ ] **Step 4: Run the tests**

```bash
go test ./container/ -run TestParseRepoDigest -v
```

Expected: All tests pass.

- [ ] **Step 5: Commit**

```bash
git add container/client.go container/client_digest_test.go
git commit -m "Add ImageDigest method and ParseRepoDigest helper"
```

---

### Task 7: Implement image verification (`cosign/image.go`)

Fetches cosign signatures from the OCI registry, constructs a sigstore-go
bundle from the cosign simple signing format, and verifies.

**Files:**
- Create: `cosign/image.go`
- Create: `cosign/image_test.go`

- [ ] **Step 1: Write the failing tests**

Create `cosign/image_test.go`:

```go
package cosign

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateSimpleSigningPayload(t *testing.T) {
	payload := []byte(`{"critical":{"identity":{"docker-reference":"ghcr.io/bernd/vibepit"},"image":{"docker-manifest-digest":"sha256:abc123"},"type":"cosign container image signature"},"optional":null}`)

	err := validateSimpleSigningPayload(payload, "ghcr.io/bernd/vibepit", "sha256:abc123")
	assert.NoError(t, err)
}

func TestValidateSimpleSigningPayloadDigestMismatch(t *testing.T) {
	payload := []byte(`{"critical":{"identity":{"docker-reference":"ghcr.io/bernd/vibepit"},"image":{"docker-manifest-digest":"sha256:wrong"},"type":"cosign container image signature"},"optional":null}`)

	err := validateSimpleSigningPayload(payload, "ghcr.io/bernd/vibepit", "sha256:abc123")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "digest mismatch")
}

func TestValidateSimpleSigningPayloadRefMismatch(t *testing.T) {
	payload := []byte(`{"critical":{"identity":{"docker-reference":"ghcr.io/other/repo"},"image":{"docker-manifest-digest":"sha256:abc123"},"type":"cosign container image signature"},"optional":null}`)

	err := validateSimpleSigningPayload(payload, "ghcr.io/bernd/vibepit", "sha256:abc123")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "reference mismatch")
}

func TestParseCosignRekorBundle(t *testing.T) {
	input := `{"SignedEntryTimestamp":"dGVzdHNldA==","Payload":{"body":"dGVzdGJvZHk=","integratedTime":1700000000,"logIndex":42,"logID":"deadbeef"}}`
	rb, err := parseCosignRekorBundle([]byte(input))
	require.NoError(t, err)
	assert.Equal(t, int64(42), rb.Payload.LogIndex)
	assert.Equal(t, int64(1700000000), rb.Payload.IntegratedTime)
	assert.Equal(t, "deadbeef", rb.Payload.LogID)
}

func TestParseCosignRekorBundleInvalid(t *testing.T) {
	_, err := parseCosignRekorBundle([]byte("not json"))
	assert.Error(t, err)
}

func TestVerifyImageNoSignature(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	err := verifyImageSignature(context.Background(), srv.URL, "bernd/vibepit", "ghcr.io/bernd/vibepit", "sha256:abc123")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no cosign signature found")
}

func TestVerifyImageEmptyLayers(t *testing.T) {
	manifest := ociManifest{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.manifest.v1+json",
		Layers:        []ociLayer{},
	}
	manifestJSON, _ := json.Marshal(manifest)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		w.Write(manifestJSON)
	}))
	defer srv.Close()

	err := verifyImageSignature(context.Background(), srv.URL, "bernd/vibepit", "ghcr.io/bernd/vibepit", "sha256:abc123")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no signature layers")
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./cosign/ -run 'TestBuild|TestParseCosign|TestVerifyImage' -v
```

Expected: Compilation error — functions don't exist.

- [ ] **Step 3: Write the implementation**

Create `cosign/image.go`:

```go
package cosign

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"

	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	protorekor "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// ImageDigestResolver resolves a local image reference to its registry,
// repository, and digest. Implemented by container.Client; mockable in tests.
type ImageDigestResolver interface {
	ImageDigest(ctx context.Context, ref string) (registry, repo, digest string, err error)
}

// VerifyImage verifies the cosign signature for a container image. It resolves
// the image digest via the resolver, fetches the signature from the OCI
// registry, and verifies it against Sigstore's public-good trusted root.
func VerifyImage(ctx context.Context, resolver ImageDigestResolver, imageRef string) error {
	registry, repo, digest, err := resolver.ImageDigest(ctx, imageRef)
	if err != nil {
		return fmt.Errorf("resolve image digest: %w", err)
	}

	registryURL := "https://" + registry
	fullRef := registry + "/" + repo
	return verifyImageSignature(ctx, registryURL, repo, fullRef, digest)
}

func verifyImageSignature(ctx context.Context, registryURL, repo, expectedRef, digest string) error {
	rc := &registryClient{
		httpClient: defaultRegistryHTTPClient(),
		baseURL:    registryURL,
	}

	manifest, err := rc.fetchSignatureManifest(ctx, repo, digest)
	if err != nil {
		return err
	}

	if len(manifest.Layers) == 0 {
		return fmt.Errorf("no signature layers in cosign manifest for %s", digest)
	}

	// Try each signature layer until one verifies.
	var lastErr error
	for _, layer := range manifest.Layers {
		if err := verifySignatureLayer(ctx, rc, repo, expectedRef, digest, &layer); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return fmt.Errorf("cosign verification failed for image %s@%s: %w", repo, digest, lastErr)
}

func verifySignatureLayer(ctx context.Context, rc *registryClient, repo, expectedRef, imageDigest string, layer *ociLayer) error {
	sigB64, ok := layer.Annotations["dev.cosignproject.cosign/signature"]
	if !ok {
		return fmt.Errorf("layer missing signature annotation")
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}

	certPEM, ok := layer.Annotations["dev.sigstore.cosign/certificate"]
	if !ok {
		return fmt.Errorf("layer missing certificate annotation")
	}

	rekorBundleJSON, ok := layer.Annotations["dev.sigstore.cosign/bundle"]
	if !ok {
		return fmt.Errorf("layer missing rekor bundle annotation")
	}
	rekorBundle, err := parseCosignRekorBundle([]byte(rekorBundleJSON))
	if err != nil {
		return fmt.Errorf("parse rekor bundle: %w", err)
	}

	// Fetch the simple signing payload from the layer blob.
	payload, err := rc.fetchBlob(ctx, repo, layer.Digest)
	if err != nil {
		return fmt.Errorf("fetch signature payload: %w", err)
	}

	// Validate that the payload references the expected image.
	if err := validateSimpleSigningPayload(payload, expectedRef, imageDigest); err != nil {
		return fmt.Errorf("payload identity mismatch: %w", err)
	}

	// Build a sigstore-go bundle from the cosign simple signing components.
	b, err := buildBundle(sig, certPEM, payload, rekorBundle)
	if err != nil {
		return fmt.Errorf("build verification bundle: %w", err)
	}

	tm, err := DefaultTrustedMaterial()
	if err != nil {
		return fmt.Errorf("fetch sigstore trusted root: %w", err)
	}

	verifier, err := newVerifier(tm)
	if err != nil {
		return fmt.Errorf("create verifier: %w", err)
	}

	certID, err := verify.NewShortCertificateIdentity(
		OIDCIssuer, "", "", ImageSANRegex,
	)
	if err != nil {
		return fmt.Errorf("create certificate identity: %w", err)
	}

	_, err = verifier.Verify(b, verify.NewPolicy(
		verify.WithArtifact(strings.NewReader(string(payload))),
		verify.WithCertificateIdentity(certID),
	))
	return err
}

// cosignRekorBundle is the JSON structure stored in the
// "dev.sigstore.cosign/bundle" annotation.
type cosignRekorBundle struct {
	SignedEntryTimestamp string             `json:"SignedEntryTimestamp"`
	Payload             cosignRekorPayload `json:"Payload"`
}

type cosignRekorPayload struct {
	Body           string `json:"body"`
	IntegratedTime int64  `json:"integratedTime"`
	LogIndex       int64  `json:"logIndex"`
	LogID          string `json:"logID"`
}

func parseCosignRekorBundle(data []byte) (*cosignRekorBundle, error) {
	var rb cosignRekorBundle
	if err := json.Unmarshal(data, &rb); err != nil {
		return nil, fmt.Errorf("parse cosign rekor bundle: %w", err)
	}
	return &rb, nil
}

// buildBundle constructs a sigstore-go Bundle from cosign simple signing
// components stored in OCI annotations.
func buildBundle(signature []byte, certPEM string, payload []byte, rekor *cosignRekorBundle) (*bundle.Bundle, error) {
	certDER, err := pemToDER(certPEM)
	if err != nil {
		return nil, fmt.Errorf("decode certificate: %w", err)
	}

	set, err := base64.StdEncoding.DecodeString(rekor.SignedEntryTimestamp)
	if err != nil {
		return nil, fmt.Errorf("decode signed entry timestamp: %w", err)
	}

	body, err := base64.StdEncoding.DecodeString(rekor.Payload.Body)
	if err != nil {
		return nil, fmt.Errorf("decode rekor body: %w", err)
	}

	logID, err := hex.DecodeString(rekor.Payload.LogID)
	if err != nil {
		return nil, fmt.Errorf("decode rekor log ID: %w", err)
	}

	pb := &protobundle.Bundle{
		MediaType: "application/vnd.dev.sigstore.bundle+json;version=0.1",
		VerificationMaterial: &protobundle.VerificationMaterial{
			Content: &protobundle.VerificationMaterial_Certificate{
				Certificate: &protocommon.X509Certificate{
					RawBytes: certDER,
				},
			},
			TlogEntries: []*protorekor.TransparencyLogEntry{
				{
					LogIndex: rekor.Payload.LogIndex,
					LogId: &protocommon.LogId{
						KeyId: logID,
					},
					KindVersion: &protorekor.KindVersion{
						Kind:    "hashedrekord",
						Version: "0.0.1",
					},
					IntegratedTime:    rekor.Payload.IntegratedTime,
					CanonicalizedBody: body,
					InclusionPromise: &protorekor.InclusionPromise{
						SignedEntryTimestamp: set,
					},
				},
			},
		},
		Content: &protobundle.Bundle_MessageSignature{
			MessageSignature: &protocommon.MessageSignature{
				MessageDigest: &protocommon.HashOutput{
					Algorithm: protocommon.HashAlgorithm_SHA2_256,
					Digest:    sha256Digest(payload),
				},
				Signature: signature,
			},
		},
	}

	return bundle.NewBundle(pb)
}

func sha256Digest(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}

// simpleSigningPayload is the cosign simple signing JSON structure stored as
// the signature layer blob.
type simpleSigningPayload struct {
	Critical struct {
		Identity struct {
			DockerReference string `json:"docker-reference"`
		} `json:"identity"`
		Image struct {
			DockerManifestDigest string `json:"docker-manifest-digest"`
		} `json:"image"`
		Type string `json:"type"`
	} `json:"critical"`
}

// validateSimpleSigningPayload checks that the payload references the expected
// image digest and repository. Without this, a valid signature over a different
// image from the same workflow would pass cryptographic verification.
func validateSimpleSigningPayload(payload []byte, expectedRef, expectedDigest string) error {
	var p simpleSigningPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("parse simple signing payload: %w", err)
	}
	if p.Critical.Image.DockerManifestDigest != expectedDigest {
		return fmt.Errorf("digest mismatch: payload has %s, expected %s",
			p.Critical.Image.DockerManifestDigest, expectedDigest)
	}
	if p.Critical.Identity.DockerReference != expectedRef {
		return fmt.Errorf("reference mismatch: payload has %s, expected %s",
			p.Critical.Identity.DockerReference, expectedRef)
	}
	return nil
}

func defaultRegistryHTTPClient() *http.Client {
	return &http.Client{}
}
```

- [ ] **Step 4: Add PEM decoding helper**

Add `pemToDER` to `cosign/image.go` (in the import block, add `"encoding/pem"`):

```go
func pemToDER(pemStr string) ([]byte, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM certificate")
	}
	return block.Bytes, nil
}
```

- [ ] **Step 5: Run the tests**

```bash
go test ./cosign/ -run 'TestBuild|TestParseCosign|TestVerifyImage' -v
```

Expected: All tests pass. The `TestVerifyImageNoSignature` test gets a 404 and returns "no cosign signature found". `TestVerifyImageEmptyLayers` returns "no signature layers".

- [ ] **Step 6: Commit**

```bash
git add cosign/image.go cosign/image_test.go
git commit -m "Add cosign image signature verification"
```

---

### Task 8: Integrate image verification into bootstrap and update

Wire `cosign.VerifyImage()` into the two call sites: session startup
(`bootstrap.go`) and image update (`update.go`). Skip when `--local` flag is
set. `*container.Client` already satisfies `cosign.ImageDigestResolver` since
it has the `ImageDigest` method from Task 6.

**Files:**
- Modify: `cmd/bootstrap.go`
- Modify: `cmd/update.go`

- [ ] **Step 1: Add verification to bootstrap.go**

In `cmd/bootstrap.go`, after the `EnsureImage` calls (around line 237), add image verification. The function `startSessionInfra` receives `cmd *cli.Command` which has access to the `localFlag`.

Add the import `"github.com/bernd/vibepit/cosign"` and after the existing `EnsureImage` block:

```go
	if err := client.EnsureImage(ctx, u.Image, false); err != nil {
		return nil, cleanups, fmt.Errorf("image: %w", err)
	}
	if !cmd.Bool(localFlag) {
		tui.Status("Verifying", "image signature for %s", u.Image)
		if err := cosign.VerifyImage(ctx, client, u.Image); err != nil {
			return nil, cleanups, fmt.Errorf("image verification: %w", err)
		}
	}
	if err := client.EnsureImage(ctx, ctr.ProxyImage, false); err != nil {
		return nil, cleanups, fmt.Errorf("proxy image: %w", err)
	}
```

- [ ] **Step 2: Add verification to update.go**

In `cmd/update.go`, in `runImageUpdate()`, after `PullImage` for the vibepit image (around line 276):

Add the import `"github.com/bernd/vibepit/cosign"` and update the function:

```go
func runImageUpdate(ctx context.Context) error {
	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("cannot determine current user: %w", err)
	}

	client, err := ctr.NewClient()
	if err != nil {
		return err
	}
	defer client.Close()

	img := imageName(u)
	if err := client.PullImage(ctx, img, false); err != nil {
		return fmt.Errorf("pull image: %w", err)
	}
	if err := cosign.VerifyImage(ctx, client, img); err != nil {
		return fmt.Errorf("image verification: %w", err)
	}
	if err := client.PullImage(ctx, ctr.ProxyImage, false); err != nil {
		return fmt.Errorf("pull image: %w", err)
	}
	fmt.Println("Container images updated.")
	return nil
}
```

- [ ] **Step 3: Verify compilation**

```bash
go build ./cmd/...
```

Expected: Builds successfully.

- [ ] **Step 4: Run full test suite**

```bash
make test
```

Expected: All tests pass. The image verification code won't be exercised by unit tests (it requires a Docker daemon and registry), but the build and existing tests should not regress.

- [ ] **Step 5: Commit**

```bash
git add cmd/bootstrap.go cmd/update.go
git commit -m "Integrate cosign image verification into run and update commands"
```

---

### Task 9: Final integration test and cleanup

Verify the full build, run the test suite, and ensure no regressions.

**Files:**
- None (verification only)

- [ ] **Step 1: Run the full test suite**

```bash
make test
```

Expected: All tests pass.

- [ ] **Step 2: Run integration tests (if Docker is available)**

```bash
make test-integration
```

Expected: Tests pass (or skip gracefully if no Docker daemon).

- [ ] **Step 3: Verify the build**

```bash
make build
```

Expected: Binary builds successfully.

- [ ] **Step 4: Verify the binary runs**

```bash
go run . --help
```

Expected: Help output shows normally, no panics or import errors.

- [ ] **Step 5: Run go vet**

```bash
go vet ./...
```

Expected: No issues.
