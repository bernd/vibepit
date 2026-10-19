---
description: Watch Vibepit start a sandbox, block an unknown host, and allow it live from the monitor.
hide:
  - toc
---

# Demo

## Start a sandbox and connect

`vibepit up` starts the proxy and sandbox containers in the background.
`vibepit connect` opens a shell inside the sandbox. Requests to allowlisted
hosts go through; everything else is blocked by the proxy. The session keeps
running after you disconnect.

<div class="vp-cast" data-cast="../../assets/casts/start.cast" data-autoplay="true" data-loop="true"></div>

## Watch and allow traffic with the monitor

`vibepit monitor` shows every host the sandbox talks to. Select a blocked host
and press `a` to allow it for the current session, or `A` to also save it to
the project config.

<div class="vp-cast" data-cast="../../assets/casts/monitor.cast" data-poster="npt:0:13"></div>

## Next steps

- [First Sandbox](first-sandbox.md): install Vibepit and launch your
  first session.
- [Monitor and Allowlist](../how-to/allowlist-and-monitor.md): manage network
  access while a session runs.
