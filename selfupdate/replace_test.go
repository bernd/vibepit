package selfupdate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplaceBinary(t *testing.T) {
	dir := t.TempDir()
	oldBin := filepath.Join(dir, "vibepit")
	require.NoError(t, os.WriteFile(oldBin, []byte("old"), 0755))

	newBin := filepath.Join(dir, "vibepit-new")
	require.NoError(t, os.WriteFile(newBin, []byte("new"), 0755))

	err := ReplaceBinary(oldBin, newBin)
	require.NoError(t, err)

	content, err := os.ReadFile(oldBin)
	require.NoError(t, err)
	assert.Equal(t, "new", string(content))

	// New temp file should be cleaned up.
	_, err = os.Stat(newBin)
	assert.True(t, os.IsNotExist(err))

	// Permissions should be preserved.
	info, err := os.Stat(oldBin)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm())
}

func TestReplaceBinaryReadOnlyDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test as root")
	}
	dir := t.TempDir()
	oldBin := filepath.Join(dir, "vibepit")
	require.NoError(t, os.WriteFile(oldBin, []byte("old"), 0755))

	newBin := filepath.Join(dir, "vibepit-new")
	require.NoError(t, os.WriteFile(newBin, []byte("new"), 0755))

	// Remove write permission from directory after writing both files.
	require.NoError(t, os.Chmod(dir, 0555))
	t.Cleanup(func() { os.Chmod(dir, 0755) })

	err := ReplaceBinary(oldBin, newBin)
	assert.Error(t, err)
}

func TestCheckWritePermission(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test as root")
	}

	writable := t.TempDir()
	assert.NoError(t, CheckWritePermission(writable))

	readOnly := t.TempDir()
	require.NoError(t, os.Chmod(readOnly, 0555))
	t.Cleanup(func() { os.Chmod(readOnly, 0755) })
	assert.Error(t, CheckWritePermission(readOnly))
}
