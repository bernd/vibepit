# Vibepit

<p align="center">
  <img src="assets/logo-bg.png" alt="Vibepit logo" width="300">
</p>

Run AI coding agents in isolated containers with network filtering.
Vibepit launches a sandboxed dev environment with a filtering proxy that
controls which domains your agent can reach.

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

## Installation

```sh
curl -fsSL https://raw.githubusercontent.com/bernd/vibepit/main/download.sh | bash
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

Add domains to the allowlist of a running session:

```sh
vibepit allow example.com:443
```

Open the interactive monitor to view proxy logs and manage the allowlist for a running session:

```sh
vibepit monitor
```

## Contributing

Vibepit is in early alpha. We welcome bug reports and feedback via [GitHub Issues](https://github.com/bernd/vibepit/issues) but are not accepting pull requests at this time.

## License

Apache 2.0 — see [LICENSE](LICENSE) for details.
