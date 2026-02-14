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
sudo mv vibepit /usr/local/bin/
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

### First-run experience

On the first run inside a project, Vibepit presents a preset selector. Presets
define which network destinations (domains and ports) the sandbox is allowed to
reach.

- Vibepit auto-detects language presets from project files. For example, a
  `go.mod` file triggers the `pkg-go` preset.
- The `default` preset is always pre-selected. It includes common destinations
  such as package registries and GitHub.

Select the presets you need and press Enter. The choices are saved to
`.vibepit/network.yaml` in the project directory.

For details on creating and managing presets, see
[Configure Network Presets](../how-to/configure-presets.md).

## 4. What happens under the hood

When the sandbox starts, Vibepit:

1. Creates an **isolated Docker network** with no external connectivity.
2. Starts a **proxy container** on that network. The proxy runs a DNS server and
   an HTTP/HTTPS filtering proxy. Only allowlisted domains are resolvable and
   reachable.
3. Starts a **sandbox container** on the same network. The container runs with a
   read-only root filesystem, dropped capabilities, and `no-new-privileges`. Your
   project directory is bind-mounted in, and a persistent home volume preserves
   installed tools between sessions.

For a deeper look, see [Architecture](../explanations/architecture.md).

## 5. Work inside the sandbox

Once the sandbox shell appears, you are inside the sandbox container. A few things
to note:

- Your project is mounted at its **original absolute path** (e.g.,
  `/home/you/Code/myproject` on the host is available at the same path inside the
  sandbox). This keeps file references and tooling consistent.
- Your home directory inside the sandbox is `/home/code`, not your host home
  directory. This is a persistent volume that survives across sessions.
- Only allowlisted domains are reachable. Requests to any other destination are
  blocked by the proxy.
- You can install additional language runtimes and tools with Homebrew. See
  [Install Development Tools](../how-to/install-tools.md) for details.

For the full list of environment variables, mounts, and hardening settings, see
the [Sandbox Environment](../reference/sandbox.md) reference.

## 6. Open the monitor

The monitor provides a live view of proxy logs and lets you manage allowlist
entries interactively. From a separate terminal on the host:

```bash
vibepit monitor
```

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

## 8. Check active sessions

From a separate terminal on the host, list running sessions:

```bash
vibepit sessions
```

This shows each active session along with its project directory and network
details.

## 9. Open additional terminals

While a session is running, you can open additional shells inside the same
sandbox. Run `vibepit` again from the same project directory in a separate
terminal:

```bash
vibepit
```

Vibepit detects the existing session and opens a new shell inside it instead of
creating a new one.

!!! note "Important: Session lifecycle"
    The session is tied to the **first** `vibepit` process. When that process
    exits normally, Vibepit cleans up the session — containers and the network
    are removed —
    even if other shells are still open. Additional shells opened with
    `vibepit` are exec sessions inside the existing container and do not keep
    the session alive on their own.
