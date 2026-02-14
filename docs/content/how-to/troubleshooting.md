# Troubleshoot Common Issues

---

## Container Runtime Not Available

**Symptoms:** Vibepit fails when trying to create containers or networks. You
see errors referencing the Docker or Podman socket.

**Cause:** The container runtime is not running, or your user does not have
permission to access its socket.

**Fix:**

1. Verify the runtime is running:

    ```bash
    docker info
    # or
    podman info
    ```

2. If the command fails, start the Docker or Podman daemon.

3. Run Vibepit with `--debug` for more detail on what socket path it is trying
   to connect to and where it fails:

    ```bash
    vibepit --debug
    ```

4. Confirm your user can access the socket. On Linux, your user typically needs
   to be in the `docker` group:

    ```bash
    sudo usermod -aG docker "$USER"
    ```

    Log out and back in for the group change to take effect.

5. For rootless Podman, ensure your user session is set up correctly:

    ```bash
    loginctl enable-linger "$USER"
    ```

6. If the error references `XDG_RUNTIME_DIR`, confirm the variable is set and
   the directory is accessible:

    ```bash
    echo "$XDG_RUNTIME_DIR"
    ls -la "$XDG_RUNTIME_DIR"
    ```

---

## `allow-http`, `allow-dns`, or `monitor` Cannot Connect

**Symptoms:** The command exits with a connection error or times out when trying
to reach the control API.

**Cause:** There is no running session, or you are targeting the wrong one.

**Fix:**

1. List active sessions:

    ```bash
    vibepit sessions
    ```

2. If no sessions are listed, start one with `vibepit run`.

3. If multiple sessions are listed, specify the correct one:

    ```bash
    vibepit allow-http --session <name> example.com:443
    ```

---

## DNS Resolution Failures Inside the Sandbox

**Symptoms:** Commands inside the sandbox fail with "could not resolve host" or
similar DNS errors, even though the domain works on the host.

**Cause:** The domain is not in the DNS allowlist. The sandbox DNS server only
resolves domains that have been explicitly allowed.

**Fix:**

1. Add the domain to the allowlist:

    ```bash
    vibepit allow-dns example.com
    ```

2. Check whether the domain is covered by a preset you have not enabled. For
   example, enabling the `vcs-github` preset adds several GitHub-related
   domains:

    ```bash
    vibepit run -p vcs-github
    ```

3. You can also add domains interactively through the monitor TUI:

    ```bash
    vibepit monitor
    ```

---

## HTTP Requests Failing Inside the Sandbox

**Symptoms:** HTTP or HTTPS requests from inside the sandbox return a proxy
error or are refused, even though DNS resolves correctly.

**Cause:** The domain and port combination is not in the HTTP allowlist. Note
that `example.com` and `*.example.com` are separate entries — allowing
`example.com` does not automatically allow `api.example.com`.

**Fix:**

1. Add the specific domain and port:

    ```bash
    vibepit allow-http api.example.com:443
    ```

2. If you need to allow all subdomains, use a wildcard:

    ```bash
    vibepit allow-http "*.example.com:443"
    ```

3. Double-check your existing rules. A common mistake is allowing the apex
   domain when the request targets a subdomain, or vice versa.

---

## Session Will Not Start

**Symptoms:** `vibepit run` fails with errors about port conflicts, network
creation failures, or references to stale resources.

**Cause:** A previous session may not have cleaned up properly, leaving behind
Docker networks or containers that conflict with the new session.

**Fix:**

1. Check for leftover Vibepit resources:

    ```bash
    docker network ls | grep vibepit
    docker ps -a | grep vibepit
    ```

2. Remove stale networks (only after confirming no other containers depend on
   them):

    ```bash
    docker network prune
    ```

3. If a specific container is stuck, remove it manually:

    ```bash
    docker rm -f <container-id>
    ```

---

## Config File Parse Errors

**Symptoms:** Vibepit exits with a configuration error on startup, referencing
YAML parse failures or unexpected values.

**Cause:** The project `.vibepit/network.yaml` file has a syntax error,
typically incorrect indentation or a misplaced key.

**Fix:**

1. Check the file for obvious YAML issues — indentation must use spaces, not
   tabs.

2. Re-run interactive setup to regenerate a valid config:

    ```bash
    vibepit run --reconfigure
    ```

    This walks you through the configuration options and writes a clean file.

---

## Sandbox Image Not Found

**Symptoms:** `vibepit run` or `vibepit update` fails with an image pull error
referencing a tag like `main-uid-1234-gid-1234`.

**Cause:** Vibepit builds sandbox images for specific UID/GID combinations to
match file ownership between the host and the container. Pre-built images are
published for these combinations:

| Tag | Platform |
|-----|----------|
| `main-uid-1000-gid-1000` | Linux (default UID/GID) |
| `main-uid-501-gid-20` | macOS (default UID/GID) |

If your user has a different UID or GID, no pre-built image exists.

**Fix:**

1. Check your UID and GID:

    ```bash
    id
    ```

2. If no pre-built image exists for your UID/GID, clone the repository and
   build the image locally:

    ```bash
    git clone https://github.com/bernd/vibepit.git
    cd vibepit
    docker build --build-arg CODE_UID=$(id -u) --build-arg CODE_GID=$(id -g) \
      -t vibepit:latest image/
    ```

3. Run Vibepit with the `--local` flag to use your locally built image:

    ```bash
    vibepit run --local
    ```

---

## Still stuck?

If none of the above resolves your problem, open an issue on
[GitHub](https://github.com/bernd/vibepit/issues). Include the output of
`vibepit --debug` and your container runtime version (`docker --version` or
`podman --version`).

