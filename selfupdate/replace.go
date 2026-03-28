package selfupdate

import (
	"fmt"
	"os"
	"path/filepath"
)

// CheckWritePermission checks if the directory is writable by attempting to
// create a temporary file.
func CheckWritePermission(dir string) error {
	f, err := os.CreateTemp(dir, ".vibepit-permission-check-*")
	if err != nil {
		return fmt.Errorf("no write permission to %s: try running with sudo or move the binary to a writable location", dir)
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return nil
}

// ReplaceBinary atomically replaces the binary at targetPath with the new
// binary at newPath using os.Rename (atomic on POSIX). Preserves the original
// file permissions.
func ReplaceBinary(targetPath, newPath string) error {
	// Get original permissions.
	info, err := os.Stat(targetPath)
	if err != nil {
		return fmt.Errorf("stat current binary: %w", err)
	}
	origMode := info.Mode().Perm()

	// Set permissions on new binary before rename.
	if err := os.Chmod(newPath, origMode); err != nil {
		return fmt.Errorf("set permissions on new binary: %w", err)
	}

	// Atomic rename.
	if err := os.Rename(newPath, targetPath); err != nil {
		return fmt.Errorf("replace binary: %w", err)
	}
	return nil
}

// ResolveBinaryPath returns the absolute path to the currently running binary,
// resolving any symlinks. It also returns the original (unresolved) path, which
// is useful for package manager detection where symlinks (e.g. /snap/bin/foo ->
// /usr/bin/snap) would cause incorrect prefix matching.
func ResolveBinaryPath() (original string, resolved string, err error) {
	exe, err := os.Executable()
	if err != nil {
		return "", "", fmt.Errorf("resolve executable path: %w", err)
	}
	abs, err := filepath.Abs(exe)
	if err != nil {
		return "", "", fmt.Errorf("resolve absolute path: %w", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", "", fmt.Errorf("resolve symlinks: %w", err)
	}
	return abs, real, nil
}
