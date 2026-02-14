<p align="center">
  <img src="assets/logo.png" alt="Vibepit" width="300">
</p>

Run AI coding agents in isolated containers with network filtering.
Vibepit launches a sandboxed dev environment with a filtering proxy that
controls which domains your agent can reach.

> [!CAUTION]
> Vibepit is still in alpha and under heavy development. Expect breaking changes.

> [!WARNING]
> Container isolation is not as strong as VM isolation. Kernel vulnerabilities,
> misconfigurations, or container runtime bugs can weaken the sandbox. Vibepit
> adds defense-in-depth (read-only rootfs, dropped capabilities, network
> filtering) but should not be treated as a hard security boundary.

## Features

- Isolated containers with read-only root filesystem and dropped capabilities
- DNS and HTTP/HTTPS filtering proxy with domain allowlist
- Network presets for common services (GitHub, Anthropic, package managers, etc.)
- Runtime allowlist management via CLI or interactive TUI monitor
- Persistent home volume across sessions
- Works with Docker and Podman

## Requirements

- Linux or macOS
- [Docker](https://docs.docker.com/get-docker/) or [Podman](https://podman.io/)

## Installation

```sh
curl -fsSL https://vibepit.dev/download.sh | bash
```

This downloads the latest release for your platform. Move the binary to somewhere in your `PATH`:

```sh
sudo mv vibepit /usr/local/bin/
```

## Usage

```sh
cd your/project/dir
vibepit
```

Start a session with additional domains or network presets:

```sh
vibepit -a example.com:443
vibepit -p github
```

Add entries to the HTTP or DNS allowlist of a running session:

```sh
vibepit allow-http example.com:443
vibepit allow-dns internal.example.com
```

Open the interactive monitor to view proxy logs and manage the allowlist for a running session:

```sh
vibepit monitor
```

## Documentation Site

Build or serve the MkDocs site locally:

```sh
make docs-install
make docs-build
make docs-serve
```

These targets use [`uv`](https://docs.astral.sh/uv/) to manage Python dependencies.
`make docs-build` outputs the MkDocs site into `site/`.

## Contributing

Vibepit is in early alpha. We welcome bug reports and feedback via [GitHub Issues](https://github.com/bernd/vibepit/issues) but are not accepting pull requests at this time.

## AI Transparency

Vibepit uses AI coding agents to assist with planning, code, tests, and
documentation. We review all AI-assisted changes before merge. A change is
accepted only after tests pass and we verify the behavior. AI output can be
incorrect, so we remain responsible for all released code and documentation.

## License

Apache 2.0 — see [LICENSE](LICENSE) for details.
