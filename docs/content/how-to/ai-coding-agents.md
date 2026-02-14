# Use with AI Coding Agents

Vibepit works with any AI coding agent that runs in a terminal. The sandbox
provides network isolation by default, and you control which domains each agent
can reach through presets and allowlist entries.

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
    vibepit allow-http --save api.example.com:443
    ```

7. Repeat until your agent operates without blocked requests.

This iterative approach means you always grant the minimum access your agent
actually needs, regardless of how its backend services evolve.

## Agent-specific notes

The `default` preset (pre-selected on first run) bundles presets for several
common agents. In most cases, no extra configuration is needed beyond the
discovery workflow above.

| Agent | Included preset | Covers |
|---|---|---|
| Claude Code | `anthropic` | Anthropic API domains |
| OpenAI Codex | `openai` | OpenAI API domains |
| GitHub Copilot | `vcs-github` | Core GitHub domains |

If you use **MCP servers** that fetch remote resources (common with Claude
Code), enable the `mcp` preset via `vibepit run --reconfigure` and then use the
discovery workflow to allow any remaining domains your servers need.

For any agent not listed here, the discovery workflow works the same way — start
a session, run the monitor, and allow what gets blocked. Save entries to your
project config so they persist across sessions.

For full details on allowlist management and the monitor interface, see
[Monitor and Allowlist](allowlist-and-monitor.md).
