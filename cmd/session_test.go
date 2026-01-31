package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bernd/vibepit/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteSessionCredentials(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	sessionID := "test-session-abc"
	creds, err := proxy.GenerateMTLSCredentials(24 * time.Hour)
	require.NoError(t, err)

	dir, err := WriteSessionCredentials(sessionID, creds)
	require.NoError(t, err)

	expected := filepath.Join(tmpDir, "vibepit", sessionID)
	assert.Equal(t, expected, dir)

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), info.Mode().Perm())

	for _, name := range []string{"ca.pem", "client-key.pem", "client-cert.pem"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err, "reading %s", name)
		assert.NotEmpty(t, data)

		info, err := os.Stat(filepath.Join(dir, name))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	}
}

func TestReadSessionCredentials(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	sessionID := "test-session-read"
	creds, err := proxy.GenerateMTLSCredentials(24 * time.Hour)
	require.NoError(t, err)

	_, err = WriteSessionCredentials(sessionID, creds)
	require.NoError(t, err)

	tlsCfg, err := LoadSessionTLSConfig(sessionID)
	require.NoError(t, err)
	require.NotNil(t, tlsCfg)
	assert.NotEmpty(t, tlsCfg.Certificates)
	assert.NotNil(t, tlsCfg.RootCAs)
}

func TestCleanupSessionCredentials(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	sessionID := "test-session-cleanup"
	creds, err := proxy.GenerateMTLSCredentials(24 * time.Hour)
	require.NoError(t, err)

	dir, err := WriteSessionCredentials(sessionID, creds)
	require.NoError(t, err)

	err = CleanupSessionCredentials(sessionID)
	require.NoError(t, err)

	_, err = os.Stat(dir)
	assert.True(t, os.IsNotExist(err))
}
