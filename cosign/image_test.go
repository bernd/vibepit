package cosign

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	ociremote "github.com/sigstore/cosign/v3/pkg/oci/remote"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// staleCredentialsFixture sets up a fake OCI registry that records every
// Authorization header it receives and seeds the user's Docker keychain
// (via DOCKER_CONFIG) with stale credentials scoped to that registry. If
// any code under test forwards the keychain credentials, they will be
// visible in the captured headers.
type staleCredentialsFixture struct {
	// host is the host:port of the fake registry, suitable for embedding in
	// an OCI reference.
	host string
	// staleCreds is the base64 blob the keychain will surface — assertions
	// fail if it appears in any captured Authorization header.
	staleCreds string
	// authHeaders returns all Authorization headers the registry has
	// observed so far. Safe to call after the registry has been exercised.
	authHeaders func() []string
}

func newStaleCredentialsFixture(t *testing.T) *staleCredentialsFixture {
	t.Helper()

	var (
		mu      sync.Mutex
		headers []string
	)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headers = append(headers, r.Header.Get("Authorization"))
		mu.Unlock()

		// Unauthenticated requests get a Basic challenge — this forces ggcr
		// to resolve credentials, exposing whatever the configured
		// authenticator returns. Basic is used rather than Bearer because
		// ggcr refuses Bearer realms pointing at loopback addresses, which
		// makes httptest unusable with a Bearer flow.
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// After auth, the signature manifest doesn't exist. That's fine —
		// the assertion is about what landed on the wire, not whether a
		// signature was found.
		w.WriteHeader(http.StatusNotFound)
	})

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)
	host := serverURL.Host

	// Plant stale credentials in a fake DOCKER_CONFIG scoped to the test
	// server's host. authn.DefaultKeychain — cosign's default — would
	// surface these and Basic-encode them onto every authenticated request.
	configDir := t.TempDir()
	staleCreds := base64.StdEncoding.EncodeToString([]byte("staleuser:stalepass"))
	dockerConfig := fmt.Sprintf(`{"auths":{%q:{"auth":%q}}}`, host, staleCreds)
	require.NoError(t, os.WriteFile(
		filepath.Join(configDir, "config.json"),
		[]byte(dockerConfig),
		0o600,
	))
	t.Setenv("DOCKER_CONFIG", configDir)

	// Sanity check: with these settings, the Docker keychain really does
	// surface the stale credentials. If this ever fails the rest of the
	// assertions become meaningless.
	registry, err := name.NewRegistry(host)
	require.NoError(t, err)
	keychainAuth, err := authn.DefaultKeychain.Resolve(registry)
	require.NoError(t, err)
	keychainCfg, err := keychainAuth.Authorization()
	require.NoError(t, err)
	require.Equal(t, "staleuser", keychainCfg.Username,
		"test fixture: DOCKER_CONFIG must inject stale credentials so assertions are meaningful")

	return &staleCredentialsFixture{
		host:       host,
		staleCreds: staleCreds,
		authHeaders: func() []string {
			mu.Lock()
			defer mu.Unlock()
			out := make([]string, len(headers))
			copy(out, headers)
			return out
		},
	}
}

// assertNoCredentialLeak runs the standard post-conditions: at least one
// retry happened (so the auth resolution path was actually exercised) and
// no captured Authorization header carried the stale credentials.
func assertNoCredentialLeak(t *testing.T, fx *staleCredentialsFixture) {
	t.Helper()
	headers := fx.authHeaders()

	// The /v2/ challenge must have triggered a retry — otherwise the
	// credential-resolution path was never exercised and the test is
	// vacuous. With DefaultKeychain the retry carries Basic creds; with
	// Anonymous it carries an empty Authorization. Either way there should
	// be at least two requests.
	require.GreaterOrEqual(t, len(headers), 2,
		"expected at least one retry after the Basic challenge; got %d request(s)", len(headers))

	for _, h := range headers {
		assert.NotContains(t, h, fx.staleCreds,
			"registry received stale Docker keychain credentials in Authorization: %q", h)
	}
}

// TestAnonymousRegistryOptsBypassesDockerKeychain locks in the fix for a
// real-world bug: a stale or revoked ghcr.io PAT in the user's Docker
// keychain caused GHCR's token endpoint to return "DENIED: denied" during
// `vibepit update`, instead of falling back to anonymous access. Vibepit's
// images and their cosign signatures are public, so signature verification
// must never forward user credentials to the registry.
//
// This is the narrow unit test for the anonymousRegistryOpts value itself.
// TestVerifyImageFunctionsDoNotForwardDockerCredentials covers the wiring
// into VerifyImage and VerifyProxyImage.
func TestAnonymousRegistryOptsBypassesDockerKeychain(t *testing.T) {
	fx := newStaleCredentialsFixture(t)

	ref, err := name.ParseReference(
		fmt.Sprintf("%s/foo/bar@sha256:%s", fx.host, strings.Repeat("a", 64)),
	)
	require.NoError(t, err)
	sigTag, err := ociremote.SignatureTag(ref, anonymousRegistryOpts...)
	require.NoError(t, err)
	_, _ = ociremote.Signatures(sigTag, anonymousRegistryOpts...)

	assertNoCredentialLeak(t, fx)
}

// TestVerifyImageFunctionsDoNotForwardDockerCredentials exercises the full
// VerifyImage / VerifyProxyImage flows against a fake registry while stale
// credentials sit in DOCKER_CONFIG. This catches not just changes to
// anonymousRegistryOpts but also a silent removal of the
// RegistryClientOpts: anonymousRegistryOpts wiring from either CheckOpts.
func TestVerifyImageFunctionsDoNotForwardDockerCredentials(t *testing.T) {
	// Replace the live TUF round-trip with an empty trusted material. Cosign
	// only requires it to be non-nil before reaching the registry call; we
	// never get far enough to actually verify any signatures (the fake
	// registry 404s the lookup), so the empty material is sufficient.
	originalFetch := fetchTrustedRoot
	fetchTrustedRoot = func() (root.TrustedMaterial, error) {
		return &root.BaseTrustedMaterial{}, nil
	}
	t.Cleanup(func() { fetchTrustedRoot = originalFetch })

	tests := []struct {
		name   string
		verify func(ctx context.Context, ref string) error
	}{
		{"VerifyImage", VerifyImage},
		{"VerifyProxyImage", VerifyProxyImage},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fx := newStaleCredentialsFixture(t)

			// The digest must be unique per subtest so it doesn't collide
			// with anything in ~/.cache/vibepit/verified-image-digests.
			// Embedding the test name in the digest is enough; pad to 64
			// hex chars.
			digestHex := fmt.Sprintf("%064x", []byte(tc.name))[:64]
			imageRef := fmt.Sprintf("%s/foo/bar@sha256:%s", fx.host, digestHex)

			// VerifyImage is expected to return an error here (no signature
			// exists on our fake registry). We only care about whether
			// stale credentials were leaked to the wire.
			err := tc.verify(context.Background(), imageRef)
			require.Error(t, err,
				"verification must fail against a registry with no signature; otherwise the test is vacuous")

			assertNoCredentialLeak(t, fx)
		})
	}
}
