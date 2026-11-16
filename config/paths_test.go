package config

import (
	"testing"

	"github.com/adrg/xdg"
	"github.com/stretchr/testify/assert"
)

func TestBaseDirs(t *testing.T) {
	origConfig, origState, origCache := xdg.ConfigHome, xdg.StateHome, xdg.CacheHome
	xdg.ConfigHome, xdg.StateHome, xdg.CacheHome = "/xdg/config", "/xdg/state", "/xdg/cache"
	t.Cleanup(func() {
		xdg.ConfigHome, xdg.StateHome, xdg.CacheHome = origConfig, origState, origCache
	})

	assert.Equal(t, "/xdg/config/vibepit/config.yaml", DefaultGlobalPath())
	assert.Equal(t, "/xdg/state/vibepit", StateDir())
	assert.Equal(t, "/xdg/cache/vibepit", CacheDir())
}
