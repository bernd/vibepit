---
description: Configure Vibepit for Claude Code, OpenAI Codex, GitHub Copilot, and other AI coding agents with the right network presets.
---

# Use with AI Coding Agents

Vibepit works with any AI coding agent that runs in a terminal. The sandbox
starts with no network access, and you choose which domains each agent can
reach through presets and allowlist entries.

## Choosing a session mode

Vibepit has two ways to run a sandbox:

- **`vibepit run`** (default) attaches your terminal directly. The session
  ends when you exit. Use it for short interactive work.
- **`vibepit up` + `vibepit connect`** runs the sandbox in the background with
  an SSH server. Sessions persist across disconnects, and you reconnect with
  `vibepit connect`. Stop it with `vibepit down`. Use it for long-running
  agents or agents that connect over SSH.

Both modes use the same network isolation, allowlist rules, and container
hardening. The examples below use `vibepit run`. For daemon mode, run
`vibepit up` and then `vibepit connect` instead.

## Discovery workflow

Each agent talks to its own set of backend services, and that set changes over
time. Instead of maintaining a fixed domain list, use the monitor to find the
domains your agent needs:

1. Start a session with the `default` preset (selected automatically on first
   run):

    ```bash
    vibepit run
    ```

2. Launch your AI coding agent inside the sandbox.

3. In a separate terminal, open the monitor:

    ```bash
    vibepit monitor
    ```

4. Look for blocked hosts. The monitor's `HOSTS` tab lists every host the
   agent contacted, most recently active first, with a status of `blocked` or
   `allowed`.

5. Allow a blocked host from the monitor by selecting it and pressing **`a`**
   (this session only) or **`A`** (this session, and save to the project
   config).

6. Or allow domains from the command line:

    ```bash
    vibepit allow-http api.example.com:443
    ```

7. Repeat until the agent runs without blocked requests.

You end up allowing only the domains the agent requested, and you can repeat
the workflow when its backend changes.

## Agent-specific notes

The `default` preset (pre-selected on first run) includes presets for several
common agents. For these agents, the discovery workflow above is usually all
the configuration you need.

| Agent | Included preset | Covers |
|---|---|---|
| Claude Code | `anthropic` | Anthropic API domains |
| OpenAI Codex | `openai` | OpenAI API domains |
| GitHub Copilot | `vcs-github` | Core GitHub domains |

If you use MCP servers that fetch remote resources, enable the `mcp` preset
with `vibepit run --reconfigure`, then use the discovery workflow to allow any
other domains your servers need.

For agents not listed here, the discovery workflow is the same. Start a
session, run the monitor, and allow what gets blocked. Save entries to your
project config so they persist across sessions.

For full details on allowlist management and the monitor interface, see
[Monitor and Allowlist](allowlist-and-monitor.md).
