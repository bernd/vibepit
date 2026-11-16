package config

import (
	"path/filepath"

	"github.com/adrg/xdg"
)

// The xdg package resolves its base directories from the environment or the
// platform defaults, so they are never empty.

// DefaultGlobalPath returns the path of the global config file.
func DefaultGlobalPath() string {
	return filepath.Join(xdg.ConfigHome, ConfigDirName, "config.yaml")
}

// DefaultProjectPath returns the path of the project config file.
func DefaultProjectPath(projectRoot string) string {
	return filepath.Join(projectRoot, ProjectConfigDirName, "network.yaml")
}

// StateDir returns the directory for per-session state such as credentials.
func StateDir() string {
	return filepath.Join(xdg.StateHome, RuntimeDirName)
}

// CacheDir returns the directory for data vibepit can recreate, such as the
// extracted proxy binary and verified image digests.
func CacheDir() string {
	return filepath.Join(xdg.CacheHome, CacheDirName)
}
