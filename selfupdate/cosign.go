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
