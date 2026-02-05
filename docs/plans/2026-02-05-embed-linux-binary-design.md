# Embed Linux Binary for macOS Docker Support

## Problem

Vibepit bind-mounts its own binary into the proxy container at `/vibepit`. On
macOS, Docker Desktop runs a Linux VM, so the mounted darwin/arm64 binary can't
execute inside the Linux container. This makes vibepit unusable on macOS.

## Solution

Embed a pre-built linux/arm64 binary into the darwin/arm64 build using
`//go:embed`. At runtime on macOS, extract the embedded binary to a cache
directory and mount that instead.

## Decisions

- **Two-stage Makefile build** — `make build-darwin` first cross-compiles the
  linux/arm64 binary, then builds the darwin/arm64 binary with the Linux binary
  embedded.
- **Cache directory** — The extracted binary is stored in
  `~/.cache/vibepit/vibepit-<hash[:12]>` and reused across runs. A SHA-256 hash
  ensures it gets replaced when the embedded binary changes.
- **Build tags for conditional embed** — A `//go:build darwin` file embeds the
  binary; a `//go:build !darwin` file provides a no-op. Uses `//go:embed vibepit*`
  wildcard so the build succeeds even when the file is absent.
- **Fail fast at runtime** — If the embedded binary is missing (plain `go build`
  on macOS without the two-stage process), print a clear error when the proxy
  container is about to start, not at build time.

## Research

The embed approach is uncommon — most tools (kind, k3d, Telepresence, Portainer)
bake the binary into a dedicated container image instead. We chose embed + bind
mount for simplicity and to avoid managing an additional container image for now.
This can be revisited later.

## Files

### New: `embed/proxy/proxy_darwin.go`

```go
//go:build darwin

package proxy

import "embed"

//go:embed vibepit*
var proxyFS embed.FS
```

### New: `embed/proxy/proxy_other.go`

```go
//go:build !darwin

package proxy

import "embed"

var proxyFS embed.FS
```

### New: `embed/proxy/proxy.go`

Shared logic for both platforms:

```go
package proxy

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

func ProxyBinary() ([]byte, bool) {
	data, err := proxyFS.ReadFile("vibepit")
	if err != nil {
		return nil, false
	}
	return data, true
}

func CachedProxyBinary() (string, error) {
	data, ok := ProxyBinary()
	if !ok {
		return "", fmt.Errorf(
			"no embedded Linux binary — run 'make build-darwin' to build with Docker support",
		)
	}

	hash := sha256.Sum256(data)
	name := fmt.Sprintf("vibepit-%x", hash[:6])

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("cache dir: %w", err)
	}

	dir := filepath.Join(cacheDir, "vibepit")
	path := filepath.Join(dir, name)

	if _, err := os.Stat(path); err == nil {
		return path, nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create cache dir: %w", err)
	}

	if err := os.WriteFile(path, data, 0o755); err != nil {
		return "", fmt.Errorf("write cached binary: %w", err)
	}

	return path, nil
}
```

### Modified: `cmd/run.go`

After the existing `selfBinary` resolution (lines 180-184):

```go
if runtime.GOOS == "darwin" {
	proxyBinary, err := embeddedproxy.CachedProxyBinary()
	if err != nil {
		return fmt.Errorf("macOS Docker support: %w", err)
	}
	selfBinary = proxyBinary
}
```

### Modified: `Makefile`

```makefile
build-linux-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/vibepit-linux-arm64 .

build-darwin: build-linux-arm64
	mkdir -p embed/proxy
	cp dist/vibepit-linux-arm64 embed/proxy/vibepit
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/vibepit-darwin-arm64 .
	rm embed/proxy/vibepit
```

### Modified: `.gitignore`

Add `dist/` and `embed/proxy/vibepit`.
