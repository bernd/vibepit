# Troubleshoot Common Issues

## Container Runtime Not Available

Symptoms:

- `vibepit` fails when creating containers or networks.

Checks:

```bash
docker info
# or
podman info
```

In nested Vibepit development sandboxes, this is expected because runtime socket access is unavailable.

## `allow-http`, `allow-dns`, or `monitor` Cannot Connect

Symptoms:

- Command fails to connect to a session.

Checks:

```bash
vibepit sessions
```

Ensure you target the right session with `--session`.

## Image Pull or Update Failures

Symptoms:

- Runtime image pull fails.

Cause:

- Network filtering blocks registry access.

Action:

- Allow required registries, then retry, or run in an environment with outbound access.
