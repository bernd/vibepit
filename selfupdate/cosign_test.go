package selfupdate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVerifyCosignBundleBadURL(t *testing.T) {
	err := VerifyCosignBundle("/nonexistent/file", "http://invalid.test/bundle")
	assert.Error(t, err)
}
