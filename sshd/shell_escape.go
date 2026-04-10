package sshd

import "strings"

// ShellEscape quotes a string for safe transmission over SSH wire protocol.
func ShellEscape(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') &&
			c != '-' && c != '_' && c != '.' && c != '/' && c != ':' && c != ',' && c != '+' && c != '=' {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
