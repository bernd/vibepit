package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// maxArchiveSizeLimit is the maximum allowed archive size in bytes.
// It is a var (not const) so tests can override it.
var maxArchiveSizeLimit int64 = 256 * 1024 * 1024 // 256 MB

// DownloadArchive downloads a file from url to a temp file in dir.
// Returns the path to the downloaded file. If isTTY is true, displays a
// progress bar; otherwise uses line-based progress.
// Checks Content-Length header and caps streaming at maxArchiveSizeLimit.
func DownloadArchive(url, dir string, isTTY bool) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("download archive: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download archive: HTTP %d", resp.StatusCode)
	}

	// Check Content-Length if present.
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		size, err := strconv.ParseInt(cl, 10, 64)
		if err == nil && size > maxArchiveSizeLimit {
			return "", fmt.Errorf("archive size %d bytes exceeds maximum %d bytes", size, maxArchiveSizeLimit)
		}
	}

	f, err := os.CreateTemp(dir, ".vibepit-download-*")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	path := f.Name()

	// Cap the reader at maxArchiveSize as defense-in-depth.
	reader := io.LimitReader(resp.Body, maxArchiveSizeLimit+1)

	// TODO: Add progress bar (isTTY) or line-based progress (!isTTY).
	// For now, just copy.
	n, err := io.Copy(f, reader)
	f.Close()
	if err != nil {
		os.Remove(path)
		return "", fmt.Errorf("write archive: %w", err)
	}
	if n > maxArchiveSizeLimit {
		os.Remove(path)
		return "", fmt.Errorf("archive size exceeds maximum %d bytes", maxArchiveSizeLimit)
	}

	return path, nil
}

// ExtractBinary extracts the named binary from a .tar.gz archive to a temp file
// in outDir. Validates that the extracted path contains no separators or
// traversal components.
func ExtractBinary(archivePath, outDir, binaryName string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read tar: %w", err)
		}

		// Reject path traversal first.
		if strings.Contains(hdr.Name, "..") {
			return "", fmt.Errorf("archive contains path traversal: %s", hdr.Name)
		}

		// Get the base name and match.
		name := filepath.Base(hdr.Name)
		if name != binaryName {
			continue
		}

		outPath, err := os.CreateTemp(outDir, ".vibepit-extract-*")
		if err != nil {
			return "", fmt.Errorf("create temp file: %w", err)
		}

		if _, err := io.Copy(outPath, io.LimitReader(tr, maxArchiveSizeLimit)); err != nil {
			outPath.Close()
			os.Remove(outPath.Name())
			return "", fmt.Errorf("extract binary: %w", err)
		}
		outPath.Close()
		return outPath.Name(), nil
	}

	return "", fmt.Errorf("binary %q not found in archive", binaryName)
}
