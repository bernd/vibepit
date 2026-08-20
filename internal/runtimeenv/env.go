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

// Set returns a copy of environ with name assigned exactly once.
func Set(environ []string, name, value string) []string {
	result := make([]string, 0, len(environ)+1)
	for _, entry := range environ {
		key, _, ok := strings.Cut(entry, "=")
		if ok && key == name {
			continue
		}
		result = append(result, entry)
	}
	return append(result, name+"="+value)
}
