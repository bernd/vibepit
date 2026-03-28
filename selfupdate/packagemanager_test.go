package selfupdate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDetectPackageManager(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		want    string
		managed bool
	}{
		{"homebrew arm", "/opt/homebrew/bin/vibepit", "Homebrew", true},
		{"homebrew intel cellar", "/usr/local/Cellar/vibepit/0.1.0/bin/vibepit", "Homebrew", true},
		{"linuxbrew cellar", "/home/linuxbrew/.linuxbrew/Cellar/vibepit/0.1.0/bin/vibepit", "Homebrew", true},
		{"linuxbrew bin", "/home/linuxbrew/.linuxbrew/bin/vibepit", "Homebrew", true},
		{"system usr bin", "/usr/bin/vibepit", "system package manager", true},
		{"system usr sbin", "/usr/sbin/vibepit", "system package manager", true},
		{"nix", "/nix/store/abc123-vibepit/bin/vibepit", "Nix", true},
		{"snap bin", "/snap/bin/vibepit", "Snap", true},
		{"snap resolved", "/snap/vibepit/123/vibepit", "Snap", true},
		{"user local", "/usr/local/bin/vibepit", "", false},
		{"home dir", "/home/user/bin/vibepit", "", false},
		{"current dir", "./vibepit", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, managed := DetectPackageManager(tt.path)
			assert.Equal(t, tt.managed, managed)
			if managed {
				assert.Equal(t, tt.want, manager)
			}
		})
	}
}
