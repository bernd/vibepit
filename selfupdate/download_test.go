package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDownloadArchive(t *testing.T) {
	content := "fake archive content"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		fmt.Fprint(w, content)
	}))
	defer srv.Close()

	dir := t.TempDir()
	path, err := DownloadArchive(srv.URL, dir, false)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, content, string(data))
}

func TestDownloadArchiveTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "300000000") // 300MB > 256MB limit
		fmt.Fprint(w, "data")
	}))
	defer srv.Close()

	dir := t.TempDir()
	_, err := DownloadArchive(srv.URL, dir, false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum")
}

func TestDownloadArchiveStreamCap(t *testing.T) {
	// Use a small cap for testing to avoid transferring large amounts of data.
	origMax := maxArchiveSizeLimit
	maxArchiveSizeLimit = 1024 // 1 KB for test
	t.Cleanup(func() { maxArchiveSizeLimit = origMax })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Don't set Content-Length. Write more than test cap.
		data := strings.Repeat("x", 512)
		for i := 0; i < 10; i++ { // 5 KB > 1 KB test cap
			w.Write([]byte(data))
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	_, err := DownloadArchive(srv.URL, dir, false)
	assert.Error(t, err)
}

func createTestTarball(t *testing.T, dir, filename, content string) string {
	t.Helper()
	tarPath := filepath.Join(dir, "test.tar.gz")
	f, err := os.Create(tarPath)
	require.NoError(t, err)
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	hdr := &tar.Header{
		Name: filename,
		Mode: 0755,
		Size: int64(len(content)),
	}
	require.NoError(t, tw.WriteHeader(hdr))
	_, err = tw.Write([]byte(content))
	require.NoError(t, err)
	return tarPath
}

func TestExtractBinary(t *testing.T) {
	dir := t.TempDir()
	tarPath := createTestTarball(t, dir, "vibepit", "binary content")

	outDir := t.TempDir()
	binPath, err := ExtractBinary(tarPath, outDir, "vibepit")
	require.NoError(t, err)

	content, err := os.ReadFile(binPath)
	require.NoError(t, err)
	assert.Equal(t, "binary content", string(content))
}

func TestExtractBinaryPathTraversal(t *testing.T) {
	dir := t.TempDir()
	tarPath := createTestTarball(t, dir, "../../../etc/malicious", "bad")

	outDir := t.TempDir()
	_, err := ExtractBinary(tarPath, outDir, "vibepit")
	assert.Error(t, err)
}
