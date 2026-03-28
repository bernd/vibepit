package selfupdate

import (
	"fmt"
	"io"
	"net/http"
	"os"
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

	// TODO: Implement sigstore-go verification.
	// The implementer should:
	// 1. Load bundle with sigstore-go's bundle package
	// 2. Get trusted root from sigstore TUF
	// 3. Create verifier with CertificateIdentity policy
	// 4. Verify the artifact against the bundle
	//
	// See: https://pkg.go.dev/github.com/sigstore/sigstore-go
	// Example pattern:
	//   root, _ := root.FetchTrustedRoot()
	//   verifierConfig := verify.VerifierConfig{...}
	//   verifier, _ := verify.NewSignedEntityVerifier(root, verifierConfig)
	//   policy := verify.NewPolicy(verify.WithCertificateIdentity(...))
	//   result, _ := verifier.Verify(entity, policy)

	return fmt.Errorf("cosign verification not yet implemented")
}

const maxBundleSize = 10 * 1024 * 1024 // 10 MB — bundles are typically a few KB

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
