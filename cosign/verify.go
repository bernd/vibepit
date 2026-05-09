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
	BinarySANRegex = `^https://github\.com/bernd/vibepit/\.github/workflows/build\.yml@refs/tags/v.*`
	ImageSANRegex  = `^https://github\.com/bernd/vibepit/\.github/workflows/docker-publish\.yml@refs/heads/main$`

	ProxyImageOIDCIssuer = "https://accounts.google.com"
	ProxyImageSubject    = "keyless@distroless.iam.gserviceaccount.com"
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
