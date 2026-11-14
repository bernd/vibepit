---
description: Install Vibepit, launch an isolated sandbox for a project, and verify that network filtering is active.
---

# First Sandbox

Install Vibepit, launch an isolated sandbox for a project, and verify that
network filtering is working.

## Prerequisites

- **Linux or macOS** (amd64 or arm64)
- **Docker or Podman** installed and running
- Your user can access the container runtime socket (e.g., you can run
  `docker ps` or `podman ps` without errors)

## 1. Install Vibepit

Download the latest release and place the binary on your `PATH`:

```bash
curl -fsSL https://vibepit.dev/download.sh | bash
mv vibepit "$HOME/.local/bin/"
```

Confirm the installation:

```bash
vibepit --help
```

## 2. Move into a project directory

Vibepit mounts your current working directory into the sandbox so the code is
available inside the container at the same path.

```bash
cd ~/projects/my-app
```

!!! tip
    Vibepit refuses to run if your working directory is your home directory.
    Always `cd` into a specific project first.

## 3. Launch the sandbox

Run `vibepit` with no arguments. The default command is `run`.

```bash
vibepit
```

### First run

On the first run inside a project, Vibepit shows a preset selector. Presets
define which network destinations (domains and ports) the sandbox can reach.

- Vibepit detects language presets from project files. For example, a
  `go.mod` file triggers the `pkg-go` preset.
- The `default` preset is always pre-selected. It includes common destinations
  such as package registries and GitHub.

Select the presets you need and press Enter. The choices are saved to
`.vibepit/network.yaml` in the project directory.

For details on creating and managing presets, see
[Configure Network Presets](../how-to/configure-presets.md).

## 4. What Vibepit starts

When the sandbox starts, Vibepit:

1. Creates an **isolated Docker network** with no external connectivity.
2. Starts a **proxy container** on that network. The proxy runs a DNS server and
   an HTTP/HTTPS filtering proxy. Only allowlisted domains resolve and accept
   connections.
3. Starts a **sandbox container** on the same network. The container runs with a
   read-only root filesystem, dropped capabilities, and `no-new-privileges`.
   Vibepit bind-mounts your project directory, and two persistent volumes (home
   and linuxbrew) keep shell state and installed tools between sessions.

For details, see [Architecture](../explanations/architecture.md).

## 5. Work inside the sandbox

Once the sandbox shell appears, you are inside the sandbox container. Keep in
mind:

- Your project is mounted at its **original absolute path** (e.g.,
  `/home/you/Code/myproject` on the host is available at the same path inside the
  sandbox). Absolute paths in files and tool output work in both places.
- Your home directory inside the sandbox is `/home/code`, not your host home
  directory. This is a persistent volume that survives across sessions.
- Only allowlisted domains are reachable. The proxy blocks requests to any other
  destination.
- You can install additional language runtimes and tools with Homebrew. See
  [Install Development Tools](../how-to/install-tools.md) for details.

For the full list of environment variables, mounts, and hardening settings, see
the [Sandbox Environment](../reference/sandbox.md) reference.

## 6. Open the monitor

The monitor shows proxy logs as they arrive and lets you allow or deny hosts
with a keypress. From a separate terminal on the host:

```bash
vibepit monitor
```

<div class="vp-cast" data-cast="../../assets/casts/monitor.cast" data-poster="npt:0:13"></div>

For details on using the monitor and managing the allowlist at runtime, see
[Monitor and Allowlist](../how-to/allowlist-and-monitor.md).

## 7. Allow extra domains at startup

If you know ahead of time that you need access to additional domains, pass them
on the command line:

```bash
vibepit -a example.com:443
```

You can also enable an entire preset:

```bash
vibepit -p vcs-github
```

Both flags can be repeated. See the [CLI Reference](../reference/cli.md) for the
full list of flags.

## 8. Check the session status

From a separate terminal on the host, in the same project directory, run:

```bash
vibepit status
```

This shows the session ID, the project directory, the state and uptime of the
sandbox and proxy containers, and the control API address. Run it outside a
project directory to see every running session.

## 9. Open additional terminals

While a session is running, you can open additional shells inside the same
sandbox. Run `vibepit` again from the same project directory in a separate
terminal:

```bash
vibepit
```

Vibepit detects the existing session and opens a new shell inside it instead of
creating a new one.

!!! note "Important: session lifecycle"
    The session lasts as long as the **first** `vibepit` process. When that
    process exits normally, Vibepit removes the containers and the network,
    even if other shells are still open. Additional shells opened with
    `vibepit` are exec sessions inside the existing container and do not keep
    the session alive on their own.

## Alternative: daemon mode

Instead of attaching your terminal directly, you can run the sandbox in the
background. Use this when you want sessions that persist across disconnects or
when an AI agent connects over SSH.

### Start the session

```bash
vibepit up
```

This starts the sandbox and proxy containers in the background and prints the
SSH address when ready.

### Connect to the sandbox

```bash
vibepit connect
```

Each connection opens a shell session inside the sandbox. If you disconnect,
for example by closing the terminal or losing the network, the shell session
keeps running. When
you reconnect, the server shows a selector where you can reattach to the
previous session or start a new one.

<div class="vp-cast" data-cast="../../assets/casts/start.cast" data-poster="npt:0:28"></div>

You can also run a single command without an interactive shell:

```bash
vibepit exec ls -la
```

### Check status

```bash
vibepit status
```

Shows container status, uptime, and published ports (control API and SSH).

### Stop the session

```bash
vibepit down
```

This stops and removes all containers and cleans up credentials. Unlike `run`
mode, the session keeps running until you stop it.

For full details on all commands and flags, see the
[CLI Reference](../reference/cli.md).
