# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

Vibepit runs development agents inside isolated Docker/Podman containers with
network isolation via a filtering proxy. A single `vibepit` command launches a
proxy container and a dev container on an isolated network, with a persistent
home volume, the project directory mounted in, and a security-hardened runtime
(read-only root filesystem, dropped capabilities, no-new-privileges).

## Build & Run

Build the Go CLI:

```
make build        # build for current platform
make test         # run unit tests
make test-integration  # run integration tests (60s timeout)
```

Run with the Go CLI:

```
go run .                     # default: launches sandbox
go run . -L                  # use local image instead of published one
go run . -a example.com:443  # allow additional domain
go run . -p github           # enable a network preset
go run . --reconfigure       # re-run interactive setup
```

CI publishes multi-arch images (amd64 + arm64) to `ghcr.io/bernd/vibepit:main`
via `.github/workflows/docker-publish.yml`. The workflow triggers on pushes to
main that change files under `image/`.

GoReleaser (`.goreleaser.yaml`) handles release builds for Linux, macOS, and
Windows across amd64/arm64. macOS builds embed the Linux arm64 proxy binary for
Docker.

## Architecture

### Go CLI (`cmd/`)

Built with `urfave/cli/v3`. Commands:

- **`run`** (default) -- Launches the sandbox: creates an isolated Docker
  network, starts a proxy container, then starts the dev container proxied
  through it. Manages persistent `vibepit-home` volume and per-session networks.
- **`allow`** -- Add domains to the proxy allowlist at runtime via the control
  API. Persists to project config.
- **`proxy`** -- Internal command: runs the proxy server inside the proxy
  container.
- **`sessions`** -- List and connect to running sessions.
- **`monitor`** -- Interactive TUI for viewing proxy logs, managing allowlist
  entries, and switching between sessions.
- **`update`** -- Update the vibepit binary and pull the latest container image.

### Proxy (`proxy/`)

Network isolation layer that runs three services in the proxy container:

1. **HTTP proxy** (dynamic port) -- Filters HTTP/HTTPS requests against a domain
   allowlist. Uses `elazarl/goproxy`.
2. **DNS server** (port 53) -- Filters DNS queries against an allowlist. Uses
   `miekg/dns`.
3. **Control API** (dynamic port) -- mTLS-secured API for runtime allowlist
   management, log streaming, and admin.

Key files: `server.go` (orchestration), `http.go` (HTTP filtering),
`dns.go` (DNS filtering), `allowlist.go` (domain/port matching), `cidr.go`
(IP range blocking), `api.go` (control API), `mtls.go` (TLS certs),
`log.go` (request log buffer), `presets.go`/`presets.yaml` (network presets
like anthropic, github, homebrew, package-managers, etc.).

### Container client (`container/`)

Docker/Podman SDK client (`client.go`) for creating networks, starting
containers, attaching terminals, and managing volumes. Terminal I/O handling
in `terminal.go`.

### Configuration (`config/`)

YAML-based project config via `knadh/koanf`. Interactive setup wizard
(`setup.go`, `setup_ui.go`) for first-run configuration. Runtime detection
(`detect.go`) for discovering installed tools.

### TUI (`tui/`)

Terminal UI framework built on Charmbracelet Bubbletea. Provides window
framing (`window.go`), header display (`header.go`), cursor navigation
(`cursor.go`), and a screen abstraction (`screen.go`).

### Container image (`image/`)

- `Dockerfile` -- Ubuntu 25.10 base, 40+ dev packages, non-root `code` user
  (UID/GID configurable via `CODE_UID`/`CODE_GID` build args)
- `entrypoint.sh` -- Initializes home from template on first run, launches
  interactive shell
- `config/profile` -- Homebrew env, `cdp` alias to jump to project dir
- `bin/` -- Runtime installers (`brew`, `claude`, `ccusage`, `yoloclaude`).
  Run by the user inside the container, not during image build.

Runtime installers are intentionally not baked into the image. They run on
first use and persist in the home volume.

## Guidelines

### Golang

- Format with `gofmt`/`goimports`
- Comments explain **why**, not **what**
- Table-driven tests with subtests
- To see source files from a dependency, or to answer questions about a dependency, run `go mod download -json MODULE` and use the returned `Dir` path to read the files.
- Use `go doc foo.Bar` or `go doc -all foo` to read documentation for packages, types, functions, etc.
- Use `go run .` or `go run ./cmd/foo` instead of `go build` to run programs, to avoid leaving behind build artifacts.
- Use `any` instead of `interface{}`.
- Use `github.com/stretchr/testify` for testing.
