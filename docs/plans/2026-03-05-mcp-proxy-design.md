# MCP Proxy for Tool Call Filtering

**Date:** 2026-03-05
**Status:** Accepted
**Goal:** Intercept MCP tool calls from sandboxed agents to external MCP servers
(e.g., JetBrains IntelliJ) and validate them against a tool allowlist before
forwarding. Prevents agents from using dangerous tools that could escape the
sandbox.

---

## Decisions

| Decision | Choice | Rationale |
|---|---|---|
| MCP server discovery | Explicit config in `.vibepit/network.yaml` | Clear security boundary, per-server allowlists |
| Default policy | Default-deny | Consistent with HTTP/DNS allowlists |
| Allowlist format | Glob patterns on tool names | Flexible, familiar from HTTP allowlist |
| Transports | SSE + streamable HTTP | Both HTTP-based, easy to proxy. Stdio deferred. |
| Parameter validation | Deferred | Experimental phase; tool-level filtering sufficient |
| Runtime updates | Deferred | Static config per session; restart to change |
| Ports | One port per MCP server on proxy | Simpler than path-based multiplexing |
| Host connectivity | Host-side TCP forwarder | Simple, no SSH dependency; upgradeable later |
| Sandbox discovery | Env vars only (`VIBEPIT_MCP_<NAME>=url`) | Agent-agnostic, minimal |
| HTTP library | Plain `net/http` reverse proxy | MCP proxy is reverse proxy, not forward; goproxy wrong fit |

## Configuration

MCP servers declared in `.vibepit/network.yaml`:

```yaml
mcp-servers:
  - name: intellij
    url: http://127.0.0.1:6589
    transport: sse           # sse | streamable-http
    allow-tools:
      - "get_*"
      - "search_*"
      - "find_*"
      - "list_directory_tree"
```

- `url`: MCP server address as seen from the host (127.0.0.1).
- `transport`: Defaults to `sse`. Alternative: `streamable-http`.
- `allow-tools`: Glob patterns. Empty list = all tools blocked.

## Network Architecture

```
+----------------------------------------------------------+
| Host                                                      |
|                                                           |
|  vibepit process              IntelliJ MCP Server         |
|  +-------------------+       +-------------------+        |
|  | TCP forwarder     |------>| 127.0.0.1:6589    |        |
|  | gateway-ip:6589   |       | (SSE)              |       |
|  +-------------------+       +-------------------+        |
|         ^                                                 |
|---------+-------------------------------------------------|
| Isolated Docker Network (gateway-ip = host bridge IP)     |
|         |                                                 |
|  +------+------------+       +-------------------+        |
|  | Proxy container   |       | Sandbox container |        |
|  |                   |<------+                   |        |
|  | MCP proxy :9100   |       | Agent (Claude)    |        |
|  |                   |       | env: VIBEPIT_MCP_ |        |
|  | HTTP proxy :3128  |       |   INTELLIJ=       |        |
|  | DNS server :53    |       |   http://proxy:   |        |
|  | Control API       |       |   9100             |       |
|  +-------------------+       +-------------------+        |
+----------------------------------------------------------+
```

### Request flow (tool call)

1. Agent sends JSON-RPC `tools/call` to `proxy-ip:<mcp-port>`.
2. MCP proxy reads request body, parses JSON-RPC, extracts tool name.
3. Tool name checked against `allow-tools` glob patterns.
4. Blocked: return JSON-RPC error immediately, log to buffer.
5. Allowed: forward request to `gateway-ip:<forwarder-port>`.
6. Host TCP forwarder relays to `127.0.0.1:<mcp-server-port>`.
7. Response streams back unfiltered.

### Port allocation

- One dynamic port per MCP server on the proxy container.
- One listener per MCP server on the host gateway IP (TCP forwarder).

## Filtering Logic

### Filtered messages

- **`tools/call` requests**: Tool name extracted from `params.name`, matched
  against allowlist globs. Blocked calls get a JSON-RPC error response.
- **`tools/list` responses**: Filtered to only include allowed tools, so the
  agent never sees tools it cannot use.

### Pass-through messages

- `initialize` / `initialized` handshake
- `ping` / `pong`
- `resources/*`, `prompts/*`
- All other JSON-RPC messages

### Allowlist matching

Glob patterns on flat tool name strings:

| Pattern | Matches |
|---|---|
| `get_file_text_by_path` | Exact match only |
| `get_*` | `get_file_text_by_path`, `get_symbol_info`, etc. |
| `*` | Everything (not recommended with default-deny) |

### Logging

Tool call attempts logged to existing log buffer:
- Source: `MCP`
- Domain: MCP server name (e.g., `intellij`)
- Action: `Allow` / `Block`
- Reason: tool name + matched pattern or "tool not in allowlist"

Visible in `vibepit monitor`.

## Host TCP Forwarder

The vibepit host process runs a TCP forwarder per MCP server:

- Listens on `gateway-ip:<port>` (the host-side IP of the isolated network).
- Forwards connections to `127.0.0.1:<mcp-server-port>`.
- Forward destination hardcoded to `127.0.0.1` (no other targets).
- Runs for the duration of the session, managed by `cmd/run.go`.

Upgradeable to SSH-based forwarding once host-access part 2 is implemented.

## Sandbox Environment

Per MCP server, one env var injected into the sandbox container:

```
VIBEPIT_MCP_INTELLIJ=http://10.0.0.2:9100
VIBEPIT_MCP_VSCODE=http://10.0.0.2:9101
```

Naming: `VIBEPIT_MCP_<UPPERCASE_NAME>`. The user wires these into their agent's
MCP client config.

## Components

| Component | Location | Description |
|---|---|---|
| MCP server config parsing | `config/` | Parse `mcp-servers` from `network.yaml` |
| MCP tool allowlist | `proxy/` | Glob-based tool name matching |
| MCP proxy service | `proxy/` | `net/http` server per MCP server, JSON-RPC interception, SSE/streamable-HTTP forwarding |
| Host TCP forwarder | `cmd/` | Per-server listener on gateway-ip, forwards to 127.0.0.1 |
| Run command wiring | `cmd/run.go` | Start forwarders, pass config to proxy, inject env vars |
| Log integration | `proxy/` | Tool call attempts logged to existing log buffer |

## Out of Scope

- Parameter validation on tool calls.
- Runtime allowlist updates (`/allow-mcp-tool`).
- Stdio transport support.
- Agent-specific config file generation.
- MCP server auto-discovery.
