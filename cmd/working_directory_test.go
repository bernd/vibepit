package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveProjectWorkingDirectory(t *testing.T) {
	projectRoot := t.TempDir()
	nestedDir := filepath.Join(projectRoot, "nested", "project")
	explicitDir := filepath.Join(projectRoot, "explicit")
	configSubDir := filepath.Join(projectRoot, ".vibepit", "subdir")
	require.NoError(t, os.MkdirAll(nestedDir, 0o755))
	require.NoError(t, os.Mkdir(explicitDir, 0o755))
	require.NoError(t, os.MkdirAll(configSubDir, 0o755))
	linkToRoot := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(projectRoot, linkToRoot))
	t.Chdir(nestedDir)

	tests := []struct {
		name         string
		requested    string
		wantDir      string
		wantRelative string
		wantErr      bool
	}{
		{
			name:         "current directory",
			wantDir:      nestedDir,
			wantRelative: filepath.Join("nested", "project"),
		},
		{
			name:         "project root",
			requested:    projectRoot,
			wantDir:      projectRoot,
			wantRelative: ".",
		},
		{
			name:         "explicit relative directory",
			requested:    filepath.Join("..", "..", "explicit"),
			wantDir:      explicitDir,
			wantRelative: "explicit",
		},
		{
			name:         "symlinked path into project",
			requested:    filepath.Join(linkToRoot, "nested", "project"),
			wantDir:      nestedDir,
			wantRelative: filepath.Join("nested", "project"),
		},
		{
			name:         "config directory falls back to project root",
			requested:    filepath.Join(projectRoot, ".vibepit"),
			wantDir:      projectRoot,
			wantRelative: ".",
		},
		{
			name:         "config subdirectory falls back to project root",
			requested:    configSubDir,
			wantDir:      projectRoot,
			wantRelative: ".",
		},
		{
			name:      "missing directory",
			requested: filepath.Join(projectRoot, "does-not-exist"),
			wantErr:   true,
		},
		{
			name:      "parent directory",
			requested: filepath.Dir(projectRoot),
			wantErr:   true,
		},
		{
			name:      "sibling with project prefix",
			requested: projectRoot + "-other",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workingDir, relativeDir, err := resolveProjectWorkingDirectory(projectRoot, tt.requested)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantDir, workingDir)
			assert.Equal(t, tt.wantRelative, relativeDir)
		})
	}
}

func TestRemoteWorkingDirectory(t *testing.T) {
	t.Run("inside project", func(t *testing.T) {
		projectRoot := t.TempDir()
		nestedDir := filepath.Join(projectRoot, "nested")
		require.NoError(t, os.Mkdir(nestedDir, 0o755))
		t.Chdir(nestedDir)

		var stderr bytes.Buffer
		relativeDir, ok := remoteWorkingDirectory(projectRoot, &stderr)

		require.True(t, ok)
		assert.Equal(t, "nested", relativeDir)
		assert.Empty(t, stderr.String())
	})

	t.Run("outside project warns and falls back", func(t *testing.T) {
		projectRoot := t.TempDir()
		t.Chdir(t.TempDir())

		var stderr bytes.Buffer
		relativeDir, ok := remoteWorkingDirectory(projectRoot, &stderr)

		require.False(t, ok)
		assert.Empty(t, relativeDir)
		assert.Contains(t, stderr.String(), "project root")
	})
}
