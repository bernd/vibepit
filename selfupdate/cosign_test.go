package selfupdate

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVerifyCosignBundleBadURL(t *testing.T) {
	err := VerifyCosignBundle(&http.Client{}, "/nonexistent/file", "http://invalid.test/bundle")
	assert.Error(t, err)
}

func TestVerifyCosignBundleEmptyBundle(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("{}"))
	}))
	defer ts.Close()

	err := VerifyCosignBundle(&http.Client{}, "/nonexistent/artifact", ts.URL)
	assert.Error(t, err)
}
