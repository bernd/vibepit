---
description: Fix common Vibepit problems including container runtime errors, connection failures, DNS issues, and image pull errors.
---

# Troubleshoot Common Issues

---

## Container runtime not available

**Symptoms:** Vibepit fails when it tries to create containers or networks. You
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

3. Run Vibepit with `--debug` to see which socket path it connects to and where
   it fails:

    ```bash
    vibepit --debug
    ```

4. Confirm your user can access the socket. On Linux, your user usually needs
   to be in the `docker` group:

    ```bash
    sudo usermod -aG docker "$USER"
    ```

    Log out and back in for the group change to take effect.

5. For rootless Podman, enable lingering so your user services keep running
   after you log out:

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

## `allow-http`, `allow-dns`, or `monitor` cannot connect

**Symptoms:** The command exits with a connection error, or times out while it
tries to reach the control API.

**Cause:** There is no running session, or you are targeting the wrong one.

**Fix:**

1. List active sessions:

    ```bash
    vibepit status
    ```

2. If no sessions are listed, start one with `vibepit run`.

3. If multiple sessions are listed, specify the correct one:

    ```bash
    vibepit allow-http --session <session-id> example.com:443
    ```

---

## DNS resolution fails inside the sandbox

**Symptoms:** Commands inside the sandbox fail with "could not resolve host" or
similar DNS errors, even though the domain works on the host.

**Cause:** The domain is not in the DNS allowlist. The sandbox DNS server only
resolves allowed domains.

**Fix:**

1. Add the domain to the allowlist:

    ```bash
    vibepit allow-dns example.com
    ```

2. Check whether a preset you have not enabled covers the domain. For example,
   the `vcs-github` preset adds several GitHub domains:

    ```bash
    vibepit run -p vcs-github
    ```

3. You can also allow domains from the monitor:

    ```bash
    vibepit monitor
    ```

---

## HTTP requests fail inside the sandbox

**Symptoms:** HTTP or HTTPS requests from inside the sandbox return a proxy
error or are refused, even though DNS resolves correctly.

**Cause:** The domain and port combination is not in the HTTP allowlist.
`example.com` and `*.example.com` are separate entries, so allowing
`example.com` does not allow `api.example.com`.

**Fix:**

1. Add the specific domain and port:

    ```bash
    vibepit allow-http api.example.com:443
    ```

2. If you need to allow all subdomains (one level), use a single-label wildcard:

    ```bash
    vibepit allow-http "*.example.com:443"
    ```

3. If the service uses multi-level subdomains, use `**`:

    ```bash
    vibepit allow-http "**.example.com:443"
    ```

4. Check your existing rules. A common mistake is allowing the apex domain
   when the request goes to a subdomain, or the other way around.

---

## Session will not start

**Symptoms:** `vibepit run` fails with errors about port conflicts, network
creation failures, or references to stale resources.

**Cause:** A previous session did not clean up and left Docker networks or
containers that conflict with the new session.

**Fix:**

1. Check for leftover Vibepit resources:

    ```bash
    docker network ls | grep vibepit
    docker ps -a | grep vibepit
    ```

2. Remove the stale Vibepit network by name (listed in the previous command's
   output):

    ```bash
    docker network rm <network-name>
    ```

3. If a container is stuck, remove it:

    ```bash
    docker rm -f <container-id>
    ```

---

## Config file parse errors

**Symptoms:** Vibepit exits with a configuration error on startup, referencing
YAML parse failures or unexpected values.

**Cause:** The project `.vibepit/network.yaml` file has a syntax error,
typically incorrect indentation or a misplaced key.

**Fix:**

1. Check the file for YAML mistakes. Indentation must use spaces, not tabs.

2. Re-run interactive setup to regenerate a valid config:

    ```bash
    vibepit run --reconfigure
    ```

    The selector walks you through the options and writes a new file.

---

## Sandbox image not found

**Symptoms:** `vibepit run` or `vibepit update` fails with an image pull error
referencing a tag like `r1-uid-1234-gid-1234`.

**Cause:** Vibepit builds sandbox images for specific UID/GID combinations so
file ownership matches between the host and the container. Pre-built images
exist for these combinations:

| Tag | Platform |
|-----|----------|
| `r1-uid-1000-gid-1000` | Linux (default UID/GID) |
| `r1-uid-501-gid-20` | macOS (default UID/GID) |

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

If none of the above fixes your problem, open an issue on
[GitHub](https://github.com/bernd/vibepit/issues). Include the output of
`vibepit --debug` and your container runtime version (`docker --version` or
`podman --version`).

