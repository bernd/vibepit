package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/adrg/xdg"
	"github.com/bernd/vibepit/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionBaseDirUsesStateHome(t *testing.T) {
	origStateHome := xdg.StateHome
	xdg.StateHome = "/tmp/test-vibepit-state"
	t.Cleanup(func() { xdg.StateHome = origStateHome })

	assert.Equal(t, "/tmp/test-vibepit-state/vibepit/sessions", sessionBaseDir())
}

func TestWriteSessionCredentials(t *testing.T) {
	tmpDir := t.TempDir()
	origStateHome := xdg.StateHome
	xdg.StateHome = tmpDir
	t.Cleanup(func() { xdg.StateHome = origStateHome })

	sessionID := "test-session-abc"
	creds, err := proxy.GenerateMTLSCredentials(24 * time.Hour)
	require.NoError(t, err)

	dir, err := WriteSessionCredentials(sessionID, creds)
	require.NoError(t, err)

	expected := filepath.Join(tmpDir, "vibepit", "sessions", sessionID)
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
	origStateHome := xdg.StateHome
	xdg.StateHome = tmpDir
	t.Cleanup(func() { xdg.StateHome = origStateHome })

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

func TestWriteSSHCredentials(t *testing.T) {
	tmpDir := t.TempDir()
	origStateHome := xdg.StateHome
	xdg.StateHome = tmpDir
	t.Cleanup(func() { xdg.StateHome = origStateHome })

	sessionID := "test-session-id"
	clientPriv := []byte("fake-client-private-key")
	clientPub := []byte("fake-client-public-key")
	hostPriv := []byte("fake-host-private-key")
	hostPub := []byte("fake-host-public-key")

	err := WriteSSHCredentials(sessionID, clientPriv, clientPub, hostPriv, hostPub)
	require.NoError(t, err)

	sessDir := sessionDir(sessionID)

	data, err := os.ReadFile(filepath.Join(sessDir, SSHClientPrivFile))
	require.NoError(t, err)
	assert.Equal(t, clientPriv, data)

	info, _ := os.Stat(filepath.Join(sessDir, SSHClientPrivFile))
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	data, _ = os.ReadFile(filepath.Join(sessDir, SSHClientPubFile))
	assert.Equal(t, clientPub, data)

	data, _ = os.ReadFile(filepath.Join(sessDir, SSHHostPrivFile))
	assert.Equal(t, hostPriv, data)

	info, _ = os.Stat(filepath.Join(sessDir, SSHHostPrivFile))
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	data, _ = os.ReadFile(filepath.Join(sessDir, SSHHostPubFile))
	assert.Equal(t, hostPub, data)
}

func TestCleanupSessionCredentials(t *testing.T) {
	tmpDir := t.TempDir()
	origStateHome := xdg.StateHome
	xdg.StateHome = tmpDir
	t.Cleanup(func() { xdg.StateHome = origStateHome })

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

func TestCleanupSessionCredentialsRejectsInvalidIDs(t *testing.T) {
	origStateHome, origRuntimeDir := xdg.StateHome, xdg.RuntimeDir
	xdg.StateHome, xdg.RuntimeDir = t.TempDir(), t.TempDir()
	t.Cleanup(func() { xdg.StateHome, xdg.RuntimeDir = origStateHome, origRuntimeDir })

	// Something that must survive: another session's credentials and a file
	// next to the sessions directory.
	other := filepath.Join(sessionBaseDir(), "other")
	require.NoError(t, os.MkdirAll(other, 0o700))
	outside := filepath.Join(xdg.StateHome, "keep")
	require.NoError(t, os.WriteFile(outside, nil, 0o600))

	tests := []struct {
		id    string
		valid bool
	}{
		{id: "d7fk6l9vl3elh5nbtqn0", valid: true},
		{id: ""},
		{id: "."},
		{id: ".."},
		{id: "../.."},
		{id: "a/b"},
		{id: `a\b`},
		{id: "/abs"},
		{id: "a\x00b"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.id), func(t *testing.T) {
			assert.Equal(t, tt.valid, validSessionID(tt.id))
			err := CleanupSessionCredentials(tt.id)
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, "invalid session ID")
			}
			assert.DirExists(t, other)
			assert.FileExists(t, outside)
		})
	}
}
