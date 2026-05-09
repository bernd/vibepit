package cosign

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrg/xdg"
	"github.com/google/go-containerregistry/pkg/name"
	cosignlib "github.com/sigstore/cosign/v2/pkg/cosign"
)

var cacheFile = filepath.Join(xdg.CacheHome, "vibepit", "verified-image-digests")

// VerifyImage verifies the cosign signature for a container image using
// the Sigstore public-good infrastructure. It checks that the image was
// signed by the expected GitHub Actions workflow and that the signature
// payload references the correct image digest.
//
// The imageRef should be a digest reference (e.g.,
// "ghcr.io/bernd/vibepit@sha256:...") to ensure the verified digest
// matches the locally cached image.
//
// Verified digests are cached in ~/.cache/vibepit/verified-digests to
// avoid repeated network round-trips on subsequent runs.
func VerifyImage(ctx context.Context, imageRef string) error {
	if isDigestVerified(imageRef) {
		return nil
	}

	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return fmt.Errorf("parse image reference: %w", err)
	}

	trustedRoot, err := cosignlib.TrustedRoot()
	if err != nil {
		return fmt.Errorf("fetch sigstore trusted root: %w", err)
	}

	co := &cosignlib.CheckOpts{
		TrustedMaterial: trustedRoot,
		ClaimVerifier:   cosignlib.SimpleClaimVerifier,
		Identities: []cosignlib.Identity{{
			Issuer:        OIDCIssuer,
			SubjectRegExp: ImageSANRegex,
		}},
	}

	_, _, err = cosignlib.VerifyImageSignatures(ctx, ref, co)
	if err != nil {
		return fmt.Errorf("cosign verification failed for image %s: %w", imageRef, err)
	}

	cacheVerifiedDigest(imageRef)
	return nil
}

// VerifyProxyImage verifies the cosign signature for the distroless proxy
// image, signed by Google's keyless identity.
func VerifyProxyImage(ctx context.Context, imageRef string) error {
	if isDigestVerified(imageRef) {
		return nil
	}

	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return fmt.Errorf("parse image reference: %w", err)
	}

	trustedRoot, err := cosignlib.TrustedRoot()
	if err != nil {
		return fmt.Errorf("fetch sigstore trusted root: %w", err)
	}

	co := &cosignlib.CheckOpts{
		TrustedMaterial: trustedRoot,
		ClaimVerifier:   cosignlib.SimpleClaimVerifier,
		Identities: []cosignlib.Identity{{
			Issuer:  ProxyImageOIDCIssuer,
			Subject: ProxyImageSubject,
		}},
	}

	_, _, err = cosignlib.VerifyImageSignatures(ctx, ref, co)
	if err != nil {
		return fmt.Errorf("cosign verification failed for proxy image %s: %w", imageRef, err)
	}

	cacheVerifiedDigest(imageRef)
	return nil
}

func isDigestVerified(digestRef string) bool {
	f, err := os.Open(cacheFile)
	if err != nil {
		return false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == digestRef {
			return true
		}
	}
	return false
}

func cacheVerifiedDigest(digestRef string) {
	if err := os.MkdirAll(filepath.Dir(cacheFile), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(cacheFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, digestRef)
}
