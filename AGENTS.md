## Project

Vibepit runs development agents inside isolated Docker/Podman containers with
network isolation via a filtering proxy. A single `vibepit` command launches a
proxy container and a sandbox container on an isolated network, with a persistent
home volume, the project directory mounted in, and a security-hardened runtime
(read-only root filesystem, dropped capabilities, no-new-privileges).

## Prerequisites

- Go installed locally (project currently uses Go 1.26.x in CI/dev).
- Docker or Podman installed and running (host development only).
- Your user can access the container runtime socket (host development only).
- For release tasks: `gh` CLI authenticated and a valid git tag.

## Nested Sandbox Context (Vibepit-in-Vibepit)

When you use Vibepit to develop Vibepit itself, expect these constraints:

- No Docker/Podman socket or API access from inside the sandbox.
- No direct internet access except explicitly allowlisted destinations.
- `vibepit` runtime commands that need container/network control are expected to
  fail in this environment.

In this context, prefer code-level validation (`make test`,
`make test-integration`, targeted `go test`) over attempting runtime session
operations.

## Build, Run, and Test

Use `go run` for local execution to avoid leaving build artifacts. In nested
sandbox development, treat runtime command execution as optional/manual host
verification.

```bash
go run .                     # default command: run
go run . -L                  # use local image instead of published one
go run . -a example.com:443  # allow additional domain:port
go run . -p vcs-github       # enable additional network preset
go run . --prompt            # prompt to allow blocked connections
go run . --reconfigure       # re-run interactive setup
go run . up                  # start in daemon mode with SSH server
go run . connect             # connect to running daemon-mode session
go run . exec ls -la         # execute command in the sandbox
go run . status              # show session status
go run . down                # stop daemon-mode session
```

Use Make targets for reproducible build/test workflows:

```bash
make build             # build vibepit binary for current platform
make test              # run unit tests
make test-integration  # run integration tests (60s timeout)
make test-bats         # run BATS tests for image/entrypoint scripts
make clean             # remove binary and dist/ artifacts
make ghostty-wasm      # rebuild libghostty-vt .wasm and its Go translation (needs Zig 0.16)
```

## CLI Command Reference

Current root commands are defined in `cmd/root.go` and include:

- `run` (default) -- launch or attach to a sandbox session.
- `up` -- start sandbox and proxy containers in daemon mode with SSH server.
- `down` -- stop and remove sandbox and proxy containers.
- `connect` -- connect to a running sandbox (interactive shell).
- `exec` -- execute a command in the sandbox (non-interactive).
- `status` -- show session status including container uptime and SSH address.
- `allow-http` -- add HTTP(S) allowlist entries at runtime.
- `allow-dns` -- add DNS allowlist entries at runtime.
- `proxy` -- internal command used inside the proxy container.
- `vibed` -- internal SSH daemon (runs inside sandbox container, hidden).
- `monitor` -- interactive TUI for logs and allowlist/admin actions.
- `update` -- update binary and pull latest runtime images.

When docs and behavior differ, treat `cmd/root.go` and command files under
`cmd/` as the source of truth.

## Architecture

### Go CLI (`cmd/`)

Built with `urfave/cli/v3`.

- `run` creates an isolated network, starts proxy + sandbox containers, and
  manages persistent `vibepit-home` volume and per-session networking. With
  `--prompt` it polls the control API for blocked connections and shows the
  approve screen over the session through `overlay`.
- `up` creates the same infrastructure as `run` but in daemon mode — containers
  run in the background with an SSH server. Returns immediately after startup.
- `down` stops and removes all containers for a session and cleans up
  credentials.
- `connect` connects to a running daemon-mode session via SSH with ephemeral
  Ed25519 keys. Opens an interactive shell.
- `exec` executes a command in the sandbox via SSH and returns its exit code.
- `status` shows session info including container uptime and published ports.
- `allow-http` / `allow-dns` call the control API and can persist to project
  config.
- `proxy` runs the proxy server inside the proxy container.
- `vibed` runs the SSH daemon inside the sandbox container (internal).
- `monitor` provides interactive control.
- `update` refreshes local runtime images.

### Proxy (`proxy/`)

Network isolation layer running three services in the proxy container:

1. HTTP proxy (dynamic port) filtering HTTP/HTTPS via allowlist rules.
2. DNS server (port 53) filtering DNS queries via allowlist rules.
3. mTLS-secured control API (dynamic port) for runtime admin and logs.

Key files:
- `proxy/server.go` -- service orchestration.
- `proxy/http.go` -- HTTP filtering.
- `proxy/dns.go` -- DNS filtering.
- `proxy/allowlist.go` -- domain/port matching.
- `proxy/cidr.go` -- IP range blocking.
- `proxy/api.go` -- control API.
- `proxy/mtls.go` -- cert generation/validation.
- `proxy/log.go` -- request log buffer.
- `proxy/presets.go` / `proxy/presets.yaml` -- network presets.

### Container client (`container/`)

Docker/Podman client abstractions for networks, containers, attach/exec, and
volumes. Terminal I/O handling lives in `container/terminal.go`.

### Configuration (`config/`)

YAML project config via `knadh/koanf`, including first-run/reconfigure setup UI
and runtime detection helpers.

### TUI (`tui/`)

Bubble Tea-based terminal UI primitives for window framing, header/cursor, and
screen abstraction.

### SSH daemon (`sshd/`)

SSH server implementation using `charmbracelet/ssh`. Handles public key
authentication, interactive PTY sessions with a BubbleTea session selector, and
non-interactive command execution. Used by the `vibed` command inside the
sandbox container.

### Session management (`session/`)

Persistent PTY session manager. Manages shell processes that survive SSH
disconnects, supports multiple attached clients per session, handles scrollback
buffers, and enforces concurrent session limits.

### SSH key generation (`keygen/`)

Ed25519 SSH keypair generation. Produces PEM-encoded private keys and
OpenSSH-format authorized keys. Used by `up` to create ephemeral per-session
keypairs.

### Terminal emulator (`vt/`)

Shadow terminal emulator: parses a byte stream, reports terminal state, and
serializes it back to VT sequences. Backed by libghostty-vt compiled to
WebAssembly and translated to Go by wasm2go, so builds stay
`CGO_ENABLED=0` and need no WebAssembly runtime.
`vt/internal/ghostty/internal/wasmvt` holds the `.wasm` and the generated
`ghostty_vt.go` (never edit it); `vt/internal/ghostty` holds the C-ABI
bindings and the feature tests that pin libghostty behaviour; only `vt` may
import it. See `vt/internal/ghostty/README.md` before upgrading the module
or wasm2go.

### Overlay (`overlay/`)

Shows a Bubble Tea program over a `run` session in any terminal. A shadow
`vt.Terminal` sees every container byte but never sits between the
container and the screen. `Terminal.Show` stops forwarding output at a byte
where the shadow's parser is at ground, hands stdin to the prompt at the
terminal's reply to a DSR 5n barrier query, and restores the screen by
replaying the output logged meanwhile or from the shadow. `inputMux` is the
only reader of stdin. `NewFilter` passes only the prompt output the restore
can undo. Used by `container.runTTYSession`; `cmd/prompt.go` shows the
approve screen through it. Only `overlay` imports `vt` on the host side.

### Embedded proxy binary (`embed/`)

Used during release builds to embed the Linux arm64 proxy binary for macOS
runtime compatibility via Go embed.

### Container image (`image/`)

- `image/Dockerfile` -- Ubuntu base + dev tools, non-root `code` user
  (`CODE_UID`/`CODE_GID` build args).
- `image/entrypoint.sh` -- initializes home template and starts shell.
- `image/entrypoint-lib.sh` -- shared shell library sourced by `entrypoint.sh`.
- `image/lib.sh` -- additional shell library.
- `image/config/` -- shell and tool configs: `profile`, `bashrc`,
  `bashrc.tail`, `tmux.conf`.
- `image/bin/*` -- runtime installers (`brew`, `claude`, `ccusage`, `cxusage`,
  `yoloclaude`) and a `skills` helper script, run by users inside container,
  not during image build.
- `image/tests/` -- BATS tests for entrypoint scripts (run via `make test-bats`).

Runtime installers persist in the home volume and are intentionally not baked
into the base image.

## Development Guidelines

### Go

- Format code with `gofmt`.
- Keep comments focused on why, not what.
- Prefer table-driven tests with subtests.
- Use `github.com/stretchr/testify` for assertions/requirements.
- Use `any` instead of `interface{}`.
- Use `go doc foo.Bar` or `go doc -all foo` for API docs.
- For dependency source inspection, run `go mod download -json MODULE` and read
  from the returned `Dir`.

### Verification Matrix

Run the smallest set that proves correctness for your change:

| Change type | Minimum verification |
|---|---|
| Docs-only changes | Lint/spell check if applicable |
| Go logic in one package | `make test` (or targeted `go test` for that package during iteration, then `make test`) |
| Proxy/network/container behavior | `make test` + `make test-integration` |
| Entrypoint/shell script changes | `make test-bats` |
| CLI flags/command wiring | Validate with command/unit tests; run `go run . --help` only when runtime access is available |
| Release/build pipeline changes | `make release-build` (and release flow checks as needed) |

## Common Failure Modes

- Container runtime unavailable:
  expected inside nested Vibepit sandbox; otherwise start Docker/Podman and
  verify socket permissions.
- `allow-http` / `allow-dns` / `monitor` fail to connect:
  ensure a matching session is running (`go run . status`).
- Image pull/update failures:
  expected under network isolation unless registry domains are allowlisted.

## CI and Release Notes

Five CI workflows under `.github/workflows/`:

- `docker-publish.yml` -- publishes multi-arch images (`amd64`, `arm64`) to
  `ghcr.io/bernd/vibepit` when files under `image/` change on `main`. Images
  are tagged by revision and uid/gid (e.g. `:r1-uid-1000-gid-1000` for Linux,
  `:r1-uid-501-gid-20` for macOS).
- `build.yml` -- runs on PRs, pushes to `main`, and version tags. Runs
  `make test`, `make test-integration`, `make test-bats`, `make release-build`,
  and on tags: `make release-archive release-publish`.
- `pages.yml` -- deploys MkDocs documentation to GitHub Pages when docs content
  or config changes on `main`.
- `ghostty-wasm.yml` -- weekly and on demand: rebuilds the libghostty-vt
  `.wasm` at a newer ghostty commit, regenerates its Go translation, runs
  the feature tests, and opens or updates a PR on `ghostty-wasm-update`.
  Never pushes to `main`.
- `release-metadata.yml` -- runs when a release is published: downloads the
  release's `checksums.txt`, generates release metadata under
  `docs/content/releases/` with `.github/scripts/generate-release-metadata.py`,
  and commits/pushes it to `main`.

Releases are driven by Make targets:
- `make release-build` builds Linux and macOS artifacts and embeds the Linux
  arm64 proxy binary for macOS runtime compatibility.
- `make release-archive` creates tarballs and checksums.
- `make release-publish` creates a draft prerelease on GitHub.

## Documentation (`docs/`)

MkDocs-based documentation site with content under `docs/content/`. Build and
serve locally with `make docs-install && make docs-serve`.
