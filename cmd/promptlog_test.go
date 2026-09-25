package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPromptLog(t *testing.T) {
	dir := t.TempDir()
	old := promptLogDir
	promptLogDir = func() string { return dir }
	t.Cleanup(func() { promptLogDir = old })

	stale := filepath.Join(dir, "stale.log")
	require.NoError(t, os.WriteFile(stale, []byte("x"), 0o600))
	past := time.Now().Add(-promptLogMaxAge - time.Hour)
	require.NoError(t, os.Chtimes(stale, past, past))
	fresh := filepath.Join(dir, "fresh.log")
	require.NoError(t, os.WriteFile(fresh, []byte("x"), 0o600))

	logger, path := openPromptLog("sess1")
	assert.Equal(t, filepath.Join(dir, "sess1.log"), path)
	assert.NoFileExists(t, stale, "logs untouched for a week are removed")
	assert.FileExists(t, fresh)

	logger.Print("first")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "first")
	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())

	require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte("x"), maxPromptLog-10), 0o600))
	logger.Print("does not fit")
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Len(t, data, maxPromptLog-10, "past the cap, lines are dropped")
}
