package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/bernd/vibepit/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDenyConfig(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "global.yaml")
	project := filepath.Join(dir, "project.yaml")
	require.NoError(t, os.WriteFile(global, []byte("deny-http: [a.com:443]\ndeny-dns: [a.com]\n"), 0600))
	require.NoError(t, os.WriteFile(project, []byte("deny-http: [a.com:443, b.com:80]\ndeny-dns: [a.com, b.com]\n"), 0600))
	cfg, err := Load(global, project)
	require.NoError(t, err)
	merged, err := cfg.Merge(nil, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"a.com:443", "b.com:80"}, merged.DenyHTTP)
	assert.Equal(t, []string{"a.com", "b.com"}, merged.DenyDNS)
	data, err := json.Marshal(merged)
	require.NoError(t, err)
	var forwarded proxy.ProxyConfig
	require.NoError(t, json.Unmarshal(data, &forwarded))
	assert.Equal(t, merged.DenyHTTP, forwarded.DenyHTTP)
	assert.Equal(t, merged.DenyDNS, forwarded.DenyDNS)
	for _, tt := range []struct{ section, entry string }{{"deny-http", "bad"}, {"deny-dns", "bad:443"}} {
		t.Run(tt.section, func(t *testing.T) {
			require.NoError(t, os.WriteFile(project, []byte(tt.section+": ["+tt.entry+"]\n"), 0600))
			cfg, err := Load(global, project)
			require.NoError(t, err)
			_, err = cfg.Merge(nil, nil)
			require.ErrorContains(t, err, tt.section)
		})
	}
}

func TestAppendDeny(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "nested", "network.yaml")
	require.NoError(t, AppendDenyHTTP(path, []string{"a.com:443", "a.com:443"}))
	require.NoError(t, AppendDenyDNS(path, []string{"a.com"}))
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, AppendDenyHTTP(path, []string{"a.com:443"}))
	require.NoError(t, AppendDenyHTTP(path, []string{"A.COM:443"}), "rules match case-insensitively")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	info, err = os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), info.Mode().Perm())
	cfg, err := Load("", path)
	require.NoError(t, err)
	assert.Equal(t, []string{"a.com:443"}, cfg.Project.DenyHTTP)
	assert.Equal(t, []string{"a.com"}, cfg.Project.DenyDNS)
	original := []byte("# header\npresets: [default] # keep\n\ndeny-http:\n  - a.com:443 # entry\n")
	require.NoError(t, os.WriteFile(path, original, 0600))
	require.NoError(t, AppendDenyHTTP(path, []string{"b.com:443"}))
	after, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(after), string(original))
	assert.Contains(t, string(after), "b.com:443")
	for _, bad := range []string{"deny-http: [", "deny-http: scalar", "- not-a-mapping"} {
		require.NoError(t, os.WriteFile(path, []byte(bad), 0600))
		require.Error(t, AppendDenyHTTP(path, []string{"a.com:443"}))
		after, err = os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, []byte(bad), after)
	}
	require.NoError(t, os.WriteFile(path, original, 0600))
	require.Error(t, AppendDenyHTTP(path, []string{"bad"}))
	require.Error(t, AppendDenyDNS(path, []string{"bad:443"}))
	after, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, after)
}

func TestConcurrentProjectConfigSaves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.yaml")
	require.NoError(t, os.WriteFile(path, []byte("# preserved\npresets: [default]\nmemory: 2g\n"), 0640))
	const n = 40
	start := make(chan struct{})
	errs := make(chan error, n)
	for i := range n {
		go func() {
			<-start
			host := fmt.Sprintf("host%d.example.com", i)
			switch i % 4 {
			case 0:
				errs <- AppendDenyHTTP(path, []string{host + ":443"})
			case 1:
				errs <- AppendDenyDNS(path, []string{host})
			case 2:
				errs <- AppendAllowHTTP(path, []string{host + ":443"})
			default:
				errs <- AppendAllowDNS(path, []string{host})
			}
		}()
	}
	close(start)
	for range n {
		require.NoError(t, <-errs)
	}
	cfg, err := Load("", path)
	require.NoError(t, err)
	assert.Len(t, cfg.Project.DenyHTTP, n/4)
	assert.Len(t, cfg.Project.DenyDNS, n/4)
	assert.Len(t, cfg.Project.AllowHTTP, n/4)
	assert.Len(t, cfg.Project.AllowDNS, n/4)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "# preserved\npresets: [default]\nmemory: 2g\n")
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0640), info.Mode().Perm())
	// Only the config may be left in the user's project: no lock or temp files.
	files, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	require.Len(t, files, 1)
}

// Run the real public writers in separate CLI-like processes; a mutex alone
// cannot protect this transaction.
func TestProjectConfigProcessWriter(t *testing.T) {
	path := os.Getenv("VIBEPIT_TEST_CONFIG_PATH")
	if path == "" {
		return
	}
	id := os.Getenv("VIBEPIT_TEST_CONFIG_ID")
	for i := range 5 {
		host := fmt.Sprintf("p%s-%d.example.com", id, i)
		require.NoError(t, AppendDenyHTTP(path, []string{host + ":443"}))
		require.NoError(t, AppendDenyDNS(path, []string{host}))
		require.NoError(t, AppendAllowHTTP(path, []string{host + ":443"}))
		require.NoError(t, AppendAllowDNS(path, []string{host}))
		require.NoError(t, saveProjectPresets(path, []string{"default"}))
	}
}

func TestConcurrentProjectConfigProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.yaml")
	require.NoError(t, os.WriteFile(path, []byte("# preserved\npresets: [default]\nmemory: 2g\n"), 0640))
	executable, err := os.Executable()
	require.NoError(t, err)
	const n = 8
	commands := make([]*exec.Cmd, n)
	outputs := make([]bytes.Buffer, n)
	for i := range n {
		command := exec.Command(executable, "-test.run=^TestProjectConfigProcessWriter$")
		command.Env = append(os.Environ(), "VIBEPIT_TEST_CONFIG_PATH="+path, fmt.Sprintf("VIBEPIT_TEST_CONFIG_ID=%d", i))
		command.Stdout = &outputs[i]
		command.Stderr = &outputs[i]
		require.NoError(t, command.Start())
		commands[i] = command
	}
	for i, command := range commands {
		require.NoError(t, command.Wait(), outputs[i].String())
	}
	cfg, err := Load("", path)
	require.NoError(t, err)
	assert.Len(t, cfg.Project.DenyHTTP, n*5)
	assert.Len(t, cfg.Project.DenyDNS, n*5)
	assert.Len(t, cfg.Project.AllowHTTP, n*5)
	assert.Len(t, cfg.Project.AllowDNS, n*5)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "# preserved")
	assert.Contains(t, string(data), "memory: 2g")
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0640), info.Mode().Perm())
}

func TestAppendDenyReadOnlyConfig(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write read-only files")
	}
	path := filepath.Join(t.TempDir(), "network.yaml")
	original := []byte("# keep\npresets: [default]\n")
	require.NoError(t, os.WriteFile(path, original, 0400))
	require.Error(t, AppendDenyDNS(path, []string{"blocked.example.com"}))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, data)
}

// Bootstrap skips setup once network.yaml exists, so a deny save that
// creates it must leave the presets setup would preselect: default plus
// the detected ones.
func TestAppendDenyCreatesConfigWithInitialPresets(t *testing.T) {
	project := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(project, "go.mod"), []byte("module x\n"), 0o600))
	path := DefaultProjectPath(project)
	require.NoError(t, AppendDenyHTTP(path, []string{"evil.example.com:443"}))
	require.NoError(t, AppendDenyDNS(path, []string{"evil.example.com"}))
	cfg, err := Load("", path)
	require.NoError(t, err)
	assert.Equal(t, initialPresets(DetectPresets(project)), cfg.Project.Presets)
	assert.Contains(t, cfg.Project.Presets, "default")
	assert.Contains(t, cfg.Project.Presets, "pkg-go")
	assert.Equal(t, []string{"evil.example.com:443"}, cfg.Project.DenyHTTP)
	assert.Equal(t, []string{"evil.example.com"}, cfg.Project.DenyDNS)
	assert.Empty(t, cfg.Project.AllowHTTP, "template placeholders stay commented out")
}

// Setup runs before the sandbox exists, so a .vibepit the user symlinked
// into e.g. a dotfiles repo keeps working there.
func TestSetupWritesThroughSymlinkedConfigDir(t *testing.T) {
	project, outside := t.TempDir(), t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(project, ".vibepit")))
	require.NoError(t, saveProjectPresets(DefaultProjectPath(project), []string{"default"}))
	require.FileExists(t, filepath.Join(outside, "network.yaml"))
}

// Saves rewrite an existing config in place, as they always did: hard links
// keep seeing the change and a read-only directory is no obstacle.
func TestSaveRewritesExistingConfigInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "network.yaml")
	require.NoError(t, os.WriteFile(path, []byte("presets: [default]\n"), 0o640))
	link := filepath.Join(t.TempDir(), "linked.yaml")
	require.NoError(t, os.Link(path, link))
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	require.NoError(t, AppendAllowHTTP(path, []string{"a.example.com:443"}))
	require.NoError(t, AppendDenyHTTP(path, []string{"b.example.com:443"}))

	data, err := os.ReadFile(link)
	require.NoError(t, err)
	assert.Contains(t, string(data), "a.example.com:443")
	assert.Contains(t, string(data), "b.example.com:443")
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
}

func TestAllowSaveMissingConfigDirNamesConfig(t *testing.T) {
	err := AppendAllowHTTP(DefaultProjectPath(t.TempDir()), []string{"a.example.com:443"})
	require.ErrorContains(t, err, "read project config")
}

// Normally a save replaces the file whole, so a failed write can't leave a
// truncated config; the mode carries over.
func TestSaveReplacesConfigAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.yaml")
	require.NoError(t, os.WriteFile(path, []byte("presets: [default]\n"), 0o640))
	before, err := os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, AppendDenyHTTP(path, []string{"b.example.com:443"}))
	after, err := os.Stat(path)
	require.NoError(t, err)
	assert.False(t, os.SameFile(before, after), "replaced, not rewritten in place")
	assert.Equal(t, os.FileMode(0o640), after.Mode().Perm())
	files, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	assert.Len(t, files, 1, "no temporary file left")
}

// Reconfigure runs under the user's control and keeps a network.yaml the
// user linked elsewhere: the linked file changes, the link stays.
func TestReconfigureFollowsLinkedConfig(t *testing.T) {
	project := t.TempDir()
	target := filepath.Join(t.TempDir(), "dotfiles-network.yaml")
	require.NoError(t, os.WriteFile(target, []byte("presets: [default]\n"), 0o600))
	path := DefaultProjectPath(project)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.Symlink(target, path))
	require.NoError(t, saveProjectPresets(path, []string{"default", "pkg-go"}))
	info, err := os.Lstat(path)
	require.NoError(t, err)
	require.Equal(t, os.ModeSymlink, info.Mode()&os.ModeType, "the link stays")
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Contains(t, string(data), "pkg-go")
}

// Saves follow symlinks, so a .vibepit or network.yaml the user keeps
// elsewhere, e.g. in a dotfiles repo, keeps working for allows and denies.
func TestSavesFollowSymlinks(t *testing.T) {
	t.Run("linked dir, missing config", func(t *testing.T) {
		project, outside := t.TempDir(), t.TempDir()
		require.NoError(t, os.Symlink(outside, filepath.Join(project, ".vibepit")))
		require.NoError(t, AppendDenyHTTP(DefaultProjectPath(project), []string{"evil.example.com:443"}))
		data, err := os.ReadFile(filepath.Join(outside, "network.yaml"))
		require.NoError(t, err)
		require.Contains(t, string(data), "evil.example.com:443")
	})
	t.Run("linked file", func(t *testing.T) {
		project := t.TempDir()
		target := filepath.Join(t.TempDir(), "dotfiles-network.yaml")
		require.NoError(t, os.WriteFile(target, []byte("presets: [default]\n"), 0o600))
		path := DefaultProjectPath(project)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.Symlink(target, path))
		require.NoError(t, AppendAllowHTTP(path, []string{"a.example.com:443"}))
		require.NoError(t, AppendDenyHTTP(path, []string{"b.example.com:443"}))
		info, err := os.Lstat(path)
		require.NoError(t, err)
		require.Equal(t, os.ModeSymlink, info.Mode()&os.ModeType, "the link stays")
		data, err := os.ReadFile(target)
		require.NoError(t, err)
		require.Contains(t, string(data), "a.example.com:443")
		require.Contains(t, string(data), "b.example.com:443")
	})
}

// Saved entries can be hostnames the sandbox chose; ones that start with a
// YAML indicator must read back unchanged instead of corrupting the config.
func TestAppendDenySavesYAMLIndicatorEntries(t *testing.T) {
	for _, entry := range []string{"#x.com", "&a.com", "[x.com", "!x.com", "|x.com", "%x.com", "123", "true", "*.x.com"} {
		t.Run(entry, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".vibepit", "network.yaml")
			require.NoError(t, AppendDenyDNS(path, []string{entry}))
			require.NoError(t, AppendDenyDNS(path, []string{"later.com"}))
			cfg, err := Load(filepath.Join(t.TempDir(), "missing.yaml"), path)
			require.NoError(t, err)
			assert.Equal(t, []string{entry, "later.com"}, cfg.Project.DenyDNS)
		})
	}
}

// YAML can't represent invalid UTF-8, so such an entry is refused rather
// than saved as a different string that would never match.
func TestAppendDenyRefusesInvalidUTF8(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".vibepit", "network.yaml")
	err := AppendDenyDNS(path, []string{"a\xffb.com"})
	require.ErrorContains(t, err, "not valid UTF-8")
	_, statErr := os.Stat(path)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

// Saving skips an entry that's another spelling of one already saved:
// rules ignore case and port zero padding.
func TestSaveSkipsEquivalentEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.yaml")
	require.NoError(t, os.WriteFile(path, []byte("presets: [default]\n"), 0o600))
	require.NoError(t, AppendDenyHTTP(path, []string{"example.com:443"}))
	require.NoError(t, AppendDenyHTTP(path, []string{"Example.com:0443", "example.com:00443"}))
	require.NoError(t, AppendAllowHTTP(path, []string{"a.example.com:080"}))
	require.NoError(t, AppendAllowHTTP(path, []string{"a.example.com:80"}))
	cfg, err := Load("", path)
	require.NoError(t, err)
	assert.Equal(t, []string{"example.com:443"}, cfg.Project.DenyHTTP)
	assert.Equal(t, []string{"a.example.com:080"}, cfg.Project.AllowHTTP)
}

// A network.yaml linked to a file that doesn't exist yet gets that file
// created, and the link stays.
func TestSaveFollowsDanglingLink(t *testing.T) {
	project, dotfiles := t.TempDir(), t.TempDir()
	path := DefaultProjectPath(project)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	// A relative link through a second link, as dotfile managers make them.
	require.NoError(t, os.Symlink(filepath.Join(dotfiles, "proj.yaml"), filepath.Join(project, "hop.yaml")))
	require.NoError(t, os.Symlink("../hop.yaml", path))

	require.NoError(t, AppendDenyHTTP(path, []string{"evil.example.com:443"}))
	info, err := os.Lstat(path)
	require.NoError(t, err)
	require.Equal(t, os.ModeSymlink, info.Mode()&os.ModeType, "the link stays")
	data, err := os.ReadFile(filepath.Join(dotfiles, "proj.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(data), "evil.example.com:443")

	// A target whose directory doesn't exist fails and names the config.
	other := DefaultProjectPath(t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Dir(other), 0o700))
	require.NoError(t, os.Symlink(filepath.Join(dotfiles, "missing", "proj.yaml"), other))
	require.ErrorContains(t, AppendDenyHTTP(other, []string{"evil.example.com:443"}), "read project config")
}
