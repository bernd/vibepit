package selfupdate

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVerifyCosignBundleBadURL(t *testing.T) {
	err := VerifyCosignBundle(&http.Client{}, "/nonexistent/file", "http://invalid.test/bundle")
	assert.Error(t, err)
}
