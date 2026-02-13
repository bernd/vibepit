# Security Model

Vibepit reduces risk for agent-driven development by combining container isolation with strict network controls.

## Layered Controls

Vibepit applies several controls together:

- Read-only container root filesystem
- Dropped Linux capabilities
- `no-new-privileges`
- Isolated per-session network
- DNS and HTTP(S) filtering proxy with allowlist rules

## Proxy Responsibilities

The proxy container enforces outbound policy across:

- HTTP/HTTPS filtering
- DNS filtering
- mTLS-protected control API for runtime admin operations

## What This Is Not

Vibepit is not a hard VM boundary.

Container escapes, kernel vulnerabilities, runtime bugs, and host misconfiguration can weaken isolation. Treat Vibepit as defense in depth, not absolute containment.
