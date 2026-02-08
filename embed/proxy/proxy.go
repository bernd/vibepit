package proxy

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"github.com/adrg/xdg"
	"github.com/bernd/vibepit/config"
)

// ProxyBinary returns the embedded Linux proxy binary, if present.
func ProxyBinary() ([]byte, bool) {
	data, err := proxyFS.ReadFile("vibepit")
	if err != nil {
		return nil, false
	}
	return data, true
}

// CachedProxyBinary extracts the embedded binary to a cache directory and
// returns the path. Subsequent calls reuse the cached file if the content
// hash matches.
func CachedProxyBinary() (string, error) {
	data, ok := ProxyBinary()
	if !ok {
		return "", fmt.Errorf("no embedded Linux binary found")
	}

	cacheDir := xdg.CacheHome
	if cacheDir == "" {
		userCacheDir, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("cache dir: %w", err)
		}
		cacheDir = userCacheDir
	}

	return cachedBinary(data, filepath.Join(cacheDir, config.CacheDirName, "bin"))
}

// cachedBinary writes data to dir/<name> where name is derived from a content
// hash. Returns the path to the cached file, reusing an existing one if
// present.
func cachedBinary(data []byte, dir string) (string, error) {
	hash := sha256.Sum256(data)
	name := fmt.Sprintf("vibepit-%x", hash[:6])
	path := filepath.Join(dir, name)

	if _, err := os.Stat(path); err == nil {
		return path, nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create cache dir: %w", err)
	}

	if err := os.WriteFile(path, data, 0o755); err != nil {
		return "", fmt.Errorf("write cached binary: %w", err)
	}

	return path, nil
}
