package proxy

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProxyBinary_NoEmbed(t *testing.T) {
	// Without a build that places a file in embed/proxy/vibepit,
	// the embedded FS is empty.
	data, ok := ProxyBinary()
	assert.False(t, ok)
	assert.Nil(t, data)
}

func TestCachedBinary(t *testing.T) {
	data := []byte("fake-binary-content")
	hash := sha256.Sum256(data)
	wantName := fmt.Sprintf("vibepit-%x", hash[:6])

	t.Run("creates file on first call", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "vibepit")

		path, err := cachedBinary(data, dir)
		require.NoError(t, err)

		assert.Equal(t, filepath.Join(dir, wantName), path)

		got, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, data, got)

		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())
	})

	t.Run("reuses cached file", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "vibepit")

		path1, err := cachedBinary(data, dir)
		require.NoError(t, err)

		path2, err := cachedBinary(data, dir)
		require.NoError(t, err)

		assert.Equal(t, path1, path2)
	})

	t.Run("different content gets different path", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "vibepit")

		path1, err := cachedBinary([]byte("version-1"), dir)
		require.NoError(t, err)

		path2, err := cachedBinary([]byte("version-2"), dir)
		require.NoError(t, err)

		assert.NotEqual(t, path1, path2)
	})
}
