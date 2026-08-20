package cmd

import (
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
	require.NoError(t, os.MkdirAll(nestedDir, 0o755))
	require.NoError(t, os.Mkdir(explicitDir, 0o755))
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
