package runtimeenv

import "strings"

const (
	ProjectDir = "VIBEPIT_PROJECT_DIR"
	WorkingDir = "VIBEPIT_WORKING_DIR"
)

// Lookup returns the last value assigned to name in environ.
func Lookup(environ []string, name string) (string, bool) {
	var value string
	var found bool
	for _, entry := range environ {
		key, candidate, ok := strings.Cut(entry, "=")
		if ok && key == name {
			value = candidate
			found = true
		}
	}
	return value, found
}
