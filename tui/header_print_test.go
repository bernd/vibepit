package tui

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWriteBanner(t *testing.T) {
	var buf bytes.Buffer
	writeBanner(&buf, 90, 30)

	assert.Equal(t, RenderBanner(90, 30)+"\n\n", buf.String())
}

func TestNormalizeBannerSize(t *testing.T) {
	t.Run("uses terminal size when available", func(t *testing.T) {
		width, height := normalizeBannerSize(120, 40, nil)
		assert.Equal(t, 120, width)
		assert.Equal(t, 40, height)
	})

	t.Run("falls back when size detection fails", func(t *testing.T) {
		width, height := normalizeBannerSize(0, 0, errors.New("no tty"))
		assert.Equal(t, defaultBannerWidth, width)
		assert.Equal(t, 0, height)
	})
}
