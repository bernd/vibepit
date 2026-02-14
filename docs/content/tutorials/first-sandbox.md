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

!!! warning
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

## 5. Allow extra domains at startup

If you know ahead of time that you need access to additional domains, pass them
on the command line:

```bash
vibepit -a example.com:443
```

You can also enable an entire preset:

```bash
vibepit -p github
```

Both flags can be repeated. See the [CLI Reference](../reference/cli.md) for the
full list of flags.

## 6. Work inside the sandbox

Once the sandbox shell appears, you are inside the sandbox container. A few things
to note:

- Your project is available at the **same path** as on the host.
- `HTTP_PROXY` and `HTTPS_PROXY` are set automatically. Tools that respect these
  variables (curl, pip, npm, and most language package managers) route traffic
  through the filtering proxy.
- Only allowlisted domains are reachable. Requests to any other destination are
  blocked by the proxy.

## 7. Check active sessions

From a separate terminal on the host, list running sessions:

```bash
vibepit sessions
```

This shows each active session along with its project directory and network
details.

## 8. Open the monitor

The monitor provides a live view of proxy logs and lets you manage allowlist
entries interactively:

```bash
vibepit monitor
```

For details on using the monitor and managing the allowlist at runtime, see
[Manage Allowlist and Monitor](../how-to/allowlist-and-monitor.md).

## 9. Reattach to a session

When you exit the sandbox shell, the session keeps running in the background.
To reattach, run `vibepit` again from the same project directory:

```bash
vibepit
```

If a session is still active for that project, Vibepit reattaches to it
automatically instead of creating a new one. There is no explicit detach
command — just exit the shell.
