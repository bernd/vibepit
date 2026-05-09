package selfupdate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsDevBuild(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    bool
	}{
		{"default version", "0.0", true},
		{"empty string", "", true},
		{"git describe suffix", "0.1.0-alpha.7-3-gabcdef", true},
		{"stable release", "0.2.0", false},
		{"prerelease", "0.1.0-alpha.7", false},
		{"prerelease rc", "0.2.0-rc.1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsDevBuild(tt.version))
		})
	}
}

func TestIsPrerelease(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    bool
	}{
		{"stable", "0.2.0", false},
		{"alpha", "0.1.0-alpha.7", true},
		{"rc", "0.2.0-rc.1", true},
		{"beta", "0.3.0-beta.2", true},
		{"dev build", "0.0", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsPrerelease(tt.version))
		})
	}
}

func TestShouldUpdate(t *testing.T) {
	tests := []struct {
		name         string
		current      string
		latest       string
		crossChannel bool
		want         bool
	}{
		{"newer available", "0.1.0", "0.2.0", false, true},
		{"already up to date", "0.2.0", "0.2.0", false, false},
		{"ahead of latest", "0.3.0", "0.2.0", false, false},
		{"dev build always updates", "0.0", "0.1.0", false, true},
		{"cross-channel always updates", "0.3.0-alpha.1", "0.2.0", true, true},
		{"cross-channel lower to higher", "0.1.0", "0.2.0-alpha.1", true, true},
		{"prerelease to newer prerelease", "0.1.0-alpha.1", "0.1.0-alpha.7", false, true},
		{"prerelease at latest", "0.1.0-alpha.7", "0.1.0-alpha.7", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ShouldUpdate(tt.current, tt.latest, tt.crossChannel))
		})
	}
}
