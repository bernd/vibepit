package selfupdate

import "strings"

// packageManagerPrefixes maps path prefixes to package manager names.
var packageManagerPrefixes = []struct {
	prefix  string
	manager string
}{
	{"/opt/homebrew/", "Homebrew"},
	{"/usr/local/Cellar/", "Homebrew"},
	{"/usr/bin/", "system package manager"},
	{"/usr/sbin/", "system package manager"},
	{"/nix/store/", "Nix"},
	{"/snap/", "Snap"},
}

// DetectPackageManager checks if the binary path is inside a known
// package-managed prefix. Returns the manager name and whether it was detected.
func DetectPackageManager(binaryPath string) (string, bool) {
	for _, pm := range packageManagerPrefixes {
		if strings.HasPrefix(binaryPath, pm.prefix) {
			return pm.manager, true
		}
	}
	return "", false
}
