# First Sandbox

This tutorial gets you from zero to a running Vibepit session.

## Prerequisites

- Linux or macOS
- Docker or Podman installed and running
- Access to your container runtime socket

## 1. Move Into a Project Directory

```bash
cd your/project/dir
```

Vibepit binds your current project into the sandbox.

## 2. Start the Sandbox

```bash
vibepit
```

By default, `vibepit` runs the `run` command.

## 3. Allow Extra Network Destinations

You can add explicit domains or presets at startup:

```bash
vibepit -a example.com:443
vibepit -p github
```

## 4. Inspect Active Sessions

```bash
vibepit sessions
```

## 5. Open the Monitor

```bash
vibepit monitor
```

The monitor shows live proxy logs and lets you manage allowlist entries.
