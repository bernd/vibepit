# Manage Allowlist and Monitor

Use these commands to update network permissions for a running session.

## Add HTTP(S) Allowlist Entries

```bash
vibepit allow-http api.example.com:443
```

Add multiple entries in one command:

```bash
vibepit allow-http api.example.com:443 registry.npmjs.org:443
```

## Add DNS Allowlist Entries

```bash
vibepit allow-dns internal.example.com
```

## Target a Specific Session

When multiple sessions are running, pass `--session`:

```bash
vibepit allow-http --session <session-id> api.example.com:443
vibepit allow-dns --session <session-id> internal.example.com
vibepit monitor --session <session-id>
```

## Open the Interactive Monitor

```bash
vibepit monitor
```

Use the monitor to inspect request logs and apply allowlist changes interactively.
