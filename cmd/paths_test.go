package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adrg/xdg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setPathsXDG points the XDG base directories below root for one test.
func setPathsXDG(t *testing.T, root string) {
	t.Helper()
	origConfig, origState, origCache := xdg.ConfigHome, xdg.StateHome, xdg.CacheHome
	xdg.ConfigHome = filepath.Join(root, "config")
	xdg.StateHome = filepath.Join(root, "state")
	xdg.CacheHome = filepath.Join(root, "cache")
	t.Cleanup(func() {
		xdg.ConfigHome, xdg.StateHome, xdg.CacheHome = origConfig, origState, origCache
	})
}

func pathsTestEntries(t *testing.T) (root string, entries []pathEntry) {
	t.Helper()
	root = t.TempDir()
	setPathsXDG(t, root)
	return root, pathEntries(func() (string, error) { return filepath.Join(root, "project"), nil })
}

func TestPathEntries(t *testing.T) {
	root, entries := pathsTestEntries(t)

	got := make(map[string]string, len(entries))
	for _, e := range entries {
		p, err := e.path()
		require.NoError(t, err)
		got[e.name] = p
	}
	assert.Equal(t, map[string]string{
		"global-config":  filepath.Join(root, "config", "vibepit", "config.yaml"),
		"project-config": filepath.Join(root, "project", ".vibepit", "network.yaml"),
		"sessions":       filepath.Join(root, "state", "vibepit", "sessions"),
		"cache":          filepath.Join(root, "cache", "vibepit"),
	}, got)
}

func TestPrintPaths(t *testing.T) {
	root, entries := pathsTestEntries(t)
	globalPath := filepath.Join(root, "config", "vibepit", "config.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(globalPath), 0o700))
	require.NoError(t, os.WriteFile(globalPath, nil, 0o600))

	t.Run("lists every path and marks missing ones", func(t *testing.T) {
		var out bytes.Buffer
		require.NoError(t, printPaths(&out, entries, ""))

		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		require.Len(t, lines, len(entries))
		for i, e := range entries {
			p, err := e.path()
			require.NoError(t, err)
			assert.Contains(t, lines[i], e.label)
			assert.Contains(t, lines[i], p)
		}
		assert.NotContains(t, lines[0], "not found", "global config exists")
		assert.Contains(t, lines[1], "not found", "project config is missing")
	})

	t.Run("prints only the named path", func(t *testing.T) {
		var out bytes.Buffer
		require.NoError(t, printPaths(&out, entries, "global-config"))
		assert.Equal(t, globalPath+"\n", out.String())
	})

	t.Run("rejects an unknown name", func(t *testing.T) {
		var out bytes.Buffer
		err := printPaths(&out, entries, "nope")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "global-config")
		assert.Empty(t, out.String())
	})
}

func TestPrintPaths_ResolvesProjectRootOnlyWhenNeeded(t *testing.T) {
	setPathsXDG(t, t.TempDir())
	errNoRoot := errors.New("no project root")
	entries := pathEntries(func() (string, error) { return "", errNoRoot })

	t.Run("a path that doesn't need it", func(t *testing.T) {
		var out bytes.Buffer
		require.NoError(t, printPaths(&out, entries, "global-config"))
	})

	t.Run("the project config", func(t *testing.T) {
		var out bytes.Buffer
		require.ErrorIs(t, printPaths(&out, entries, "project-config"), errNoRoot)
	})
}

func TestPrintPaths_ShowsStatErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root, entries := pathsTestEntries(t)
	locked := filepath.Join(root, "config", "vibepit")
	require.NoError(t, os.MkdirAll(locked, 0o700))
	require.NoError(t, os.Chmod(locked, 0))
	t.Cleanup(func() { os.Chmod(locked, 0o700) }) //nolint:errcheck

	var out bytes.Buffer
	require.NoError(t, printPaths(&out, entries, ""))

	first := strings.SplitN(out.String(), "\n", 2)[0]
	assert.Contains(t, first, "permission denied")
	assert.NotContains(t, first, "not found")
}

func TestPrintPaths_ShowsUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	root, entries := pathsTestEntries(t)
	globalPath := filepath.Join(root, "config", "vibepit", "config.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(globalPath), 0o700))
	require.NoError(t, os.WriteFile(globalPath, nil, 0))

	var out bytes.Buffer
	require.NoError(t, printPaths(&out, entries, ""))

	first := strings.SplitN(out.String(), "\n", 2)[0]
	assert.Contains(t, first, "permission denied")
}

func TestPathsCommand_ProjectFlag(t *testing.T) {
	setPathsXDG(t, t.TempDir())
	project := t.TempDir()

	var out bytes.Buffer
	root := RootCommand()
	root.Writer = &out
	err := root.Run(t.Context(), []string{"vibepit", "paths", "--project", project, "project-config"})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(project, ".vibepit", "network.yaml")+"\n", out.String())
}

func TestRootCommand_PathsCommand(t *testing.T) {
	names := make(map[string]bool)
	for _, c := range RootCommand().Commands {
		names[c.Name] = true
	}
	assert.Contains(t, names, "paths")
}
