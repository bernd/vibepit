package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// mk creates entries below base: a trailing slash means a directory,
// "link->target" a symlink to base/target, anything else an empty file.
func mk(t *testing.T, base string, entries ...string) {
	t.Helper()
	for _, e := range entries {
		if link, target, ok := strings.Cut(e, "->"); ok {
			require.NoError(t, os.Symlink(filepath.Join(base, target), filepath.Join(base, link)))
			continue
		}
		p := filepath.Join(base, e)
		if strings.HasSuffix(e, "/") {
			require.NoError(t, os.MkdirAll(p, 0o755))
			continue
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, nil, 0o600))
	}
}

func TestFindProjectRoot(t *testing.T) {
	tests := []struct {
		name     string
		entries  []string
		recorded []string
		start    string
		want     string
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
			name:     "recorded root without git",
			entries:  []string{"proj/src/"},
			recorded: []string{"proj"},
			start:    "proj/src",
			want:     "proj",
		},
		{
			// The sandbox can remove the project's .git, not the record.
			name:     "recorded root stops the walk before an outer repository",
			entries:  []string{"outer/.git/", "outer/proj/src/"},
			recorded: []string{"outer/proj"},
			start:    "outer/proj/src",
			want:     "outer/proj",
		},
		{
			// The sandbox can write a project config in any subdirectory.
			name:    "a project config is not a marker",
			entries: []string{"outer/.git/", "outer/proj/.vibepit/network.yaml", "outer/proj/src/"},
			start:   "outer/proj/src",
			want:    "outer",
		},
		{
			name:    "a project config without git gives the path itself",
			entries: []string{"proj/.vibepit/network.yaml", "proj/src/"},
			start:   "proj/src",
			want:    "proj/src",
		},
		{
			// A planted sub/.git only narrows the root; sub isn't recorded,
			// so its project config isn't loaded without asking.
			name:     "git below a recorded root starts a nested project",
			entries:  []string{"proj/sub/.git/", "proj/sub/dir/"},
			recorded: []string{"proj"},
			start:    "proj/sub/dir",
			want:     "proj/sub",
		},
		{
			name:     "a recorded root below the start is ignored",
			entries:  []string{"proj/.git/", "proj/sub/"},
			recorded: []string{"proj/sub"},
			start:    "proj",
			want:     "proj",
		},
		{
			name:    "symlinks are resolved",
			entries: []string{"proj/.git/", "proj/src/", "link->proj"},
			start:   "link/src",
			want:    "proj",
		},
		{
			// The session key must not change once the first run records
			// the root.
			name:    "symlinks are resolved without a marker",
			entries: []string{"proj/", "link->proj"},
			start:   "link",
			want:    "proj",
		},
		{
			// The caller reports it as missing; an enclosing project
			// must not stand in for it.
			name:    "a missing path comes back as given",
			entries: []string{"proj/.git/"},
			start:   "proj/typo",
			want:    "proj/typo",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The temp dir may sit behind a symlink (macOS /var).
			base, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			skipIfInsideProject(t, base)
			mk(t, base, tt.entries...)
			records := testRecords(t)
			for _, r := range tt.recorded {
				require.NoError(t, records.add(filepath.Join(base, r)))
			}

			got, err := findProjectRoot(filepath.Join(base, tt.start), records)
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
		cmd := exec.Command("git", append([]string{"-C", proj}, args...)...)
		// A GIT_DIR or GIT_WORK_TREE inherited from a hook would point
		// these commands, and the core.worktree write, at the real repo.
		cmd.Env = []string{}
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "GIT_") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return string(out)
	}
	git("init", "-q")
	git("config", "core.worktree", base)
	// What the lookup used to return.
	require.Contains(t, git("rev-parse", "--show-toplevel"), base+"\n")

	got, err := findProjectRoot(proj, testRecords(t))
	require.NoError(t, err)
	require.Equal(t, proj, got)
}

// A .git another user owns must not become the root: one planted in a
// shared parent like /tmp would capture the project.
func TestGitMarkerOwnership(t *testing.T) {
	uid := os.Geteuid()
	tests := []struct {
		name  string
		entry string
		uid   int
		want  markerKind
	}{
		{"own git directory", ".git/", uid, markerOwned},
		{"foreign git directory", ".git/", uid + 1, markerForeign},
		{"own git file", ".git", uid, markerOwned},
		{"foreign git file", ".git", uid + 1, markerForeign},
		{"no marker", "other/", uid, markerNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			mk(t, dir, tt.entry)
			require.Equal(t, tt.want, gitMarker(dir, tt.uid))
		})
	}
}

func TestProjectRecords(t *testing.T) {
	records := projectRecords(filepath.Join(t.TempDir(), "state", "projects"))

	ok, err := records.has("/home/u/proj")
	require.NoError(t, err)
	require.False(t, ok, "nothing recorded, and the dir doesn't exist yet")

	require.NoError(t, records.add("/home/u/proj"))
	require.NoError(t, records.add("/home/u/proj"), "recording twice is fine")

	for root, want := range map[string]bool{
		"/home/u/proj":  true,
		"/home/u/proj/": false,
		"/home/u":       false,
		"/home/u/other": false,
	} {
		ok, err := records.has(root)
		require.NoError(t, err)
		require.Equal(t, want, ok, root)
	}

	fi, err := os.Stat(string(records))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), fi.Mode().Perm())
}

// The project config the user reviewed is what gets loaded, not whatever
// the file holds by then.
func TestLoadWithProjectData(t *testing.T) {
	dir := t.TempDir()
	globalPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(globalPath, []byte("allow-http:\n  - global.example:443\n"), 0o600))

	cfg, err := LoadWithProjectData(globalPath, []byte("presets:\n  - default\nallow-http:\n  - reviewed.example:443\n"))
	require.NoError(t, err)
	require.Equal(t, []string{"global.example:443"}, cfg.Global.AllowHTTP)
	require.Equal(t, []string{"default"}, cfg.Project.Presets)
	require.Equal(t, []string{"reviewed.example:443"}, cfg.Project.AllowHTTP)

	_, err = LoadWithProjectData(globalPath, []byte("allow-http: [\n"))
	require.Error(t, err)
}

// testRecords returns an empty records dir, so the user's own records
// don't affect the test.
func testRecords(t *testing.T) projectRecords {
	t.Helper()
	return projectRecords(filepath.Join(t.TempDir(), "projects"))
}

// skipIfInsideProject skips when a directory above the temp dir holds a
// marker (a dotfiles repo in $HOME with TMPDIR below it), since cases that
// expect no marker would then find that one.
func skipIfInsideProject(t *testing.T, base string) {
	t.Helper()
	root, err := findProjectRoot(base, testRecords(t))
	require.NoError(t, err)
	if root != base {
		t.Skipf("temp dir %s is inside project %s", base, root)
	}
}
