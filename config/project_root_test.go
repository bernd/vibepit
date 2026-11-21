package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFindProjectRoot(t *testing.T) {
	// mk creates dirs and files below base; a trailing slash means directory.
	mk := func(t *testing.T, base string, entries ...string) {
		t.Helper()
		for _, e := range entries {
			p := filepath.Join(base, e)
			if e[len(e)-1] == '/' {
				require.NoError(t, os.MkdirAll(p, 0o755))
				continue
			}
			require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
			require.NoError(t, os.WriteFile(p, nil, 0o600))
		}
	}

	tests := []struct {
		name    string
		entries []string
		start   string
		want    string
	}{
		{
			name:    "git directory at the root, started below it",
			entries: []string{"proj/.git/", "proj/src/pkg/"},
			start:   "proj/src/pkg",
			want:    "proj",
		},
		{
			name:    "git file marks a worktree or submodule root",
			entries: []string{"proj/.git/", "proj/sub/.git", "proj/sub/dir/"},
			start:   "proj/sub/dir",
			want:    "proj/sub",
		},
		{
			name:    "no marker gives the path itself",
			entries: []string{"plain/dir/"},
			start:   "plain/dir",
			want:    "plain/dir",
		},
		{
			name:    "project config marks a root without git",
			entries: []string{"proj/.vibepit/network.yaml", "proj/src/"},
			start:   "proj/src",
			want:    "proj",
		},
		{
			// The sandbox can remove the project's .git, not its config.
			name:    "project config stops the walk before an outer repository",
			entries: []string{"outer/.git/", "outer/proj/.vibepit/network.yaml", "outer/proj/src/"},
			start:   "outer/proj/src",
			want:    "outer/proj",
		},
		{
			name:    "an empty .vibepit directory is not a marker",
			entries: []string{"outer/.git/", "outer/proj/.vibepit/", "outer/proj/src/"},
			start:   "outer/proj/src",
			want:    "outer",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The temp dir may sit behind a symlink (macOS /var).
			base, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			mk(t, base, tt.entries...)

			got, err := FindProjectRoot(filepath.Join(base, tt.start))
			require.NoError(t, err)
			require.Equal(t, filepath.Join(base, tt.want), got)
		})
	}
}

// The sandbox can write the project's .git/config. A core.worktree that
// names a parent directory must not move the project root there.
func TestFindProjectRootIgnoresGitConfig(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	proj := filepath.Join(base, "outer", "proj")
	require.NoError(t, os.MkdirAll(proj, 0o755))

	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", proj}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
		return string(out)
	}
	git("init", "-q")
	git("config", "core.worktree", base)
	// What the lookup used to return.
	require.Contains(t, git("rev-parse", "--show-toplevel"), base+"\n")

	got, err := FindProjectRoot(proj)
	require.NoError(t, err)
	require.Equal(t, proj, got)
}

func TestFindProjectRootResolvesSymlinks(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	proj := filepath.Join(base, "proj")
	require.NoError(t, os.MkdirAll(filepath.Join(proj, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(proj, "src"), 0o755))
	link := filepath.Join(base, "link")
	require.NoError(t, os.Symlink(proj, link))

	got, err := FindProjectRoot(filepath.Join(link, "src"))
	require.NoError(t, err)
	require.Equal(t, proj, got)
}
