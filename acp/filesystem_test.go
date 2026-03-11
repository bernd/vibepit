package acp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	require.NoError(t, os.WriteFile(path, []byte("hello world"), 0644))

	result, err := ReadFile(FSReadParams{Path: path})
	require.NoError(t, err)
	assert.Equal(t, "hello world", result.Content)
}

func TestReadFileNotFound(t *testing.T) {
	_, err := ReadFile(FSReadParams{Path: "/nonexistent/file.txt"})
	assert.Error(t, err)
}

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output.txt")

	result, err := WriteFile(FSWriteParams{Path: path, Content: "test content"})
	require.NoError(t, err)
	assert.Equal(t, len("test content"), result.BytesWritten)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "test content", string(data))
}

func TestWriteFileOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output.txt")

	require.NoError(t, os.WriteFile(path, []byte("old"), 0644))

	_, err := WriteFile(FSWriteParams{Path: path, Content: "new"})
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "new", string(data))
}
