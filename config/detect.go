package config

import (
	"os"
	"path/filepath"

	"github.com/bernd/vibepit/proxy"
)

// DetectPresets scans the project root for marker files and returns the names
// of presets that match. Only presets with Matchers are considered.
func DetectPresets(projectDir string) []string {
	reg := proxy.NewPresetRegistry()
	var detected []string

	for _, p := range reg.All() {
		if len(p.Matchers) == 0 {
			continue
		}
		if matchesAny(projectDir, p.Matchers) {
			detected = append(detected, p.Name)
		}
	}

	return detected
}

// matchesAny returns true if any of the patterns match a file in dir.
func matchesAny(dir string, patterns []string) bool {
	for _, pattern := range patterns {
		if containsGlob(pattern) {
			matches, _ := filepath.Glob(filepath.Join(dir, pattern))
			if len(matches) > 0 {
				return true
			}
		} else {
			if _, err := os.Stat(filepath.Join(dir, pattern)); err == nil {
				return true
			}
		}
	}
	return false
}

func containsGlob(pattern string) bool {
	for _, c := range pattern {
		if c == '*' || c == '?' || c == '[' {
			return true
		}
	}
	return false
}
