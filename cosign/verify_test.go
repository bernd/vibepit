package cosign

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyBlobInvalidBundle(t *testing.T) {
	dir := t.TempDir()

	artifactPath := filepath.Join(dir, "artifact")
	require.NoError(t, os.WriteFile(artifactPath, []byte("test artifact content"), 0o600))

	bundlePath := filepath.Join(dir, "bundle.json")
	require.NoError(t, os.WriteFile(bundlePath, []byte("{}"), 0o600))

	err := VerifyBlob(artifactPath, bundlePath)
	assert.Error(t, err)
}

func TestVerifyBlobMissingArtifact(t *testing.T) {
	dir := t.TempDir()
	bundlePath := filepath.Join(dir, "bundle.json")
	require.NoError(t, os.WriteFile(bundlePath, []byte("{}"), 0o600))

	err := VerifyBlob("/nonexistent/artifact", bundlePath)
	assert.Error(t, err)
}

func TestVerifyBlobMissingBundle(t *testing.T) {
	dir := t.TempDir()
	artifactPath := filepath.Join(dir, "artifact")
	require.NoError(t, os.WriteFile(artifactPath, nil, 0o600))

	err := VerifyBlob(artifactPath, "/nonexistent/bundle.json")
	assert.Error(t, err)
}

func TestIdentityConstants(t *testing.T) {
	assert.Equal(t, "https://token.actions.githubusercontent.com", OIDCIssuer)
	assert.Contains(t, BinarySANRegex, `build\.yml`)
	assert.Contains(t, ImageSANRegex, `docker-publish\.yml`)
}
