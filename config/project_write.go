package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// projectConfigDirMode keeps a config directory this package creates private.
const projectConfigDirMode = 0o700

// withProjectConfigLock runs update with an exclusive lock on the config's
// directory, so concurrent read-modify-write saves don't lose entries. The
// directory is locked, not the file: atomic replacement swaps the file's
// inode, and a sidecar lock file would litter the user's project. With
// create, a missing config directory is created; otherwise it must already
// exist. Symlinks are followed, so a .vibepit or network.yaml the
// user keeps elsewhere works; the sandbox can't plant one, since it only
// sees an empty read-only mount at .vibepit.
func withProjectConfigLock(path string, create bool, update func(string) error) error {
	if create {
		if err := os.MkdirAll(filepath.Dir(path), projectConfigDirMode); err != nil {
			return err
		}
	}
	// Follow symlinks as WriteFile did, and share the lock with callers
	// using the real project path.
	resolved, err := resolveConfigPath(path)
	if err != nil {
		return err
	}
	lock, err := os.Open(filepath.Dir(resolved))
	if err != nil {
		return err
	}
	defer lock.Close()
	for {
		err = unix.Flock(int(lock.Fd()), unix.LOCK_EX)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil {
		return err
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	return update(resolved)
}

// resolveConfigPath follows symlinks to the file a save reads and writes.
// A dangling link resolves to its missing target, so the save creates the
// target and keeps the link, as os.WriteFile did, instead of renaming a
// regular file over the link.
func resolveConfigPath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	target := path
	for range 40 {
		info, err := os.Lstat(target)
		if err != nil || info.Mode()&fs.ModeSymlink == 0 {
			break // missing: the file to create
		}
		link, err := os.Readlink(target)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(link) {
			link = filepath.Join(filepath.Dir(target), link)
		}
		target = link
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil {
		return "", fmt.Errorf("read project config: %w", err)
	}
	return filepath.Join(dir, filepath.Base(target)), nil
}

// writeProjectConfigFile saves data to path, which the caller resolved and
// holds the config lock for. It writes a temporary file and renames it, so a
// full disk or a crash can't leave a truncated config whose saved denies
// silently stop applying. It rewrites the file in place only where a rename
// would break something the user set up: a hard link, a file owned by
// someone else, a group the replacement can't keep, or a directory we can't
// write to.
func writeProjectConfigFile(path string, data []byte) error {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return replaceProjectConfig(path, data, 0o600, -1)
	}
	if err != nil {
		return err
	}
	// A rename would replace even a read-only file; honor its permissions
	// as an in-place write does.
	if err := unix.Access(path, unix.W_OK); err != nil {
		return &fs.PathError{Op: "open", Path: path, Err: err}
	}
	gid := -1
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		gid = int(st.Gid)
	}
	if !rewriteInPlace(path, info) {
		err := replaceProjectConfig(path, data, info.Mode().Perm(), gid)
		if !errors.Is(err, errKeepGroup) {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

func rewriteInPlace(path string, info fs.FileInfo) bool {
	if st, ok := info.Sys().(*syscall.Stat_t); ok && (st.Nlink > 1 || int(st.Uid) != os.Geteuid()) {
		return true
	}
	return unix.Access(filepath.Dir(path), unix.W_OK) != nil
}

// errKeepGroup means the replacement couldn't take the old file's group, so
// the save falls back to rewriting the file in place.
var errKeepGroup = errors.New("replacement can't keep the config's group")

// replaceProjectConfig atomically replaces path with data. gid is the group
// the old file had, or -1 for none: a new file's group depends on the
// directory (setgid, BSD semantics), so it is compared, not assumed.
func replaceProjectConfig(path string, data []byte, mode fs.FileMode, gid int) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".network-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if gid >= 0 {
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Gid) != gid {
			if err := file.Chown(-1, gid); err != nil {
				return errKeepGroup
			}
		}
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
