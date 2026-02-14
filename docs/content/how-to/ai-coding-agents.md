# Use with AI Coding Agents

Vibepit works with any AI coding agent that runs in a terminal. The sandbox
provides network isolation by default, and you control which domains each agent
can reach through presets and allowlist entries. This page covers a general
workflow for discovering what your agent needs, followed by notes on common
agents.

## Discovery workflow

Every agent has its own set of backend services, and those services change over
time. Rather than maintaining a static domain list, use the monitor to discover
exactly what your agent needs:

1. Start a session with the `default` preset (included automatically on first
   run):

    ```bash
    vibepit run
    ```

2. Launch your AI coding agent inside the sandbox.

3. In a separate terminal, open the monitor:

    ```bash
    vibepit monitor
    ```

4. Look for blocked requests. The monitor marks each request with `+` (allowed)
   or `x` (blocked).

5. Allow blocked domains directly from the monitor by navigating to a blocked
   entry and pressing **`a`** (session only) or **`A`** (session + save to
   project config).

6. Alternatively, allow domains from the command line:

    ```bash
    vibepit allow-http api.example.com:443
    ```

7. Repeat until your agent operates without blocked requests.

This iterative approach means you always grant the minimum access your agent
actually needs, regardless of how its backend services evolve.

## Claude Code

The `default` preset includes the `anthropic` preset, which covers the domains
Claude Code requires for its core functionality. No additional configuration is
needed for a standard Claude Code session.

If you use MCP servers that fetch remote resources, those servers may need
access to additional domains. Enable the `mcp` preset to cover Model Context
Protocol infrastructure:

```bash
vibepit run --reconfigure
```

Select the `mcp` preset in the interactive selector, then allow any remaining
domains your MCP servers need using the discovery workflow above.

## Cursor

Cursor communicates with its own backend services for AI features. Start with
the `default` preset and use the discovery workflow to identify which domains
Cursor needs. The specific domains depend on your Cursor version and
configuration, so the monitor is the most reliable way to determine them.

```bash
vibepit run
# Start Cursor, then in another terminal:
vibepit monitor
```

Allow blocked domains as they appear. Save entries to your project config
(press **`A`** in the monitor) so they persist across sessions.

## GitHub Copilot

GitHub Copilot requires access to GitHub services and its own API endpoints.
The `default` preset already includes `vcs-github`, which covers core GitHub
domains. Use the discovery workflow to identify any additional Copilot-specific
domains your setup requires.

```bash
vibepit run
# Start your editor with Copilot, then in another terminal:
vibepit monitor
```

As with other agents, allow blocked domains through the monitor and save the
entries you want to keep.

## General advice

Agent backends evolve independently of Vibepit, so any static domain list
becomes outdated. The monitor is the authoritative way to discover what your
agent needs at any point in time. Once you have identified the right set of
domains for your workflow, save them to your project config so they apply to
future sessions automatically.

For full details on allowlist management and the monitor interface, see
[Monitor and Allowlist](allowlist-and-monitor.md).
