package selfupdate

import (
	"regexp"

	"golang.org/x/mod/semver"
)

// gitDescribeSuffix matches git describe suffixes like "-3-gabcdef" appended
// to a semver pre-release tag. These are produced by `git describe --tags`
// when there are commits after the last tag.
var gitDescribeSuffix = regexp.MustCompile(`-\d+-g[0-9a-f]+$`)

// addV prepends "v" to a bare version string for use with golang.org/x/mod/semver,
// which requires the "v" prefix for all operations.
func addV(version string) string {
	return "v" + version
}

// IsDevBuild returns true if the version is not a valid semver string,
// or is a non-canonical short version like "0.0", or has a git describe
// suffix like "0.1.0-alpha.7-3-gabcdef".
func IsDevBuild(version string) bool {
	v := addV(version)
	if !semver.IsValid(v) {
		return true
	}
	// Short versions like "0.0" canonicalize to "0.0.0".
	if v != semver.Canonical(v) {
		return true
	}
	// Git describe outputs like "0.1.0-alpha.7-3-gabcdef".
	if gitDescribeSuffix.MatchString(version) {
		return true
	}
	return false
}

// IsPrerelease returns true if the version is a valid semver string
// with a prerelease suffix (e.g., "0.1.0-alpha.7").
func IsPrerelease(version string) bool {
	v := addV(version)
	return semver.IsValid(v) && semver.Prerelease(v) != ""
}

// ShouldUpdate returns true if the binary should be updated from current to
// latest. If crossChannel is true, the update is always offered (the user
// explicitly chose to switch channels). Dev builds always get offered updates.
func ShouldUpdate(current, latest string, crossChannel bool) bool {
	if IsDevBuild(current) || crossChannel {
		return true
	}
	return semver.Compare(addV(current), addV(latest)) < 0
}
