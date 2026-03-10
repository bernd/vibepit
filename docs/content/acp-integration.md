# IDE Integration via the Agent Client Protocol (ACP)

This document analyzes how IDEs and editors that speak the
[Agent Client Protocol](https://agentclientprotocol.com/) can connect to coding
agents running inside a Vibepit sandbox.

## Goal

An IDE on the host connects to an ACP endpoint exposed by the Vibepit proxy.
The proxy bridges messages to an agent process running inside the sandbox
container. The agent executes tools (file edits, terminal commands, etc.)
against the sandboxed project directory while the IDE renders streaming updates,
plans, and permission prompts.

```
┌──────────────────────────────────────────────────────────────────────┐
│ Host                                                                │
│                                                                     │
│  ┌──────────┐   JSON-RPC / newline-delimited   ┌────────────────┐  │
│  │   IDE    │ ──────────────────────────────────▸│ Proxy container│  │
│  │ (client) │◂──────────────────────────────────│                │  │
│  └──────────┘   WebSocket or Streamable HTTP    │  ┌──────────┐ │  │
│                 mTLS on 127.0.0.1:<port>        │  │ ACP      │ │  │
│                                                 │  │ bridge   │ │  │
│                                                 │  └────┬─────┘ │  │
│                                                 │       │ stdin/ │  │
│                                                 │       │ stdout │  │
│                                                 │       │ (exec) │  │
│                                                 └───────┼────────┘  │
│                                        isolated network │           │
│                                                 ┌───────┼────────┐  │
│                                                 │ Sandbox        │  │
│                                                 │  ┌─────▼─────┐ │  │
│                                                 │  │   Agent    │ │  │
│                                                 │  │ (claude,   │ │  │
│                                                 │  │  codex, …) │ │  │
│                                                 │  └───────────┘ │  │
│                                                 └────────────────┘  │
└──────────────────────────────────────────────────────────────────────┘
```

## ACP Protocol Summary

ACP is a JSON-RPC 2.0 protocol (inspired by LSP) that standardises how editors
talk to coding agents. Key characteristics:

| Aspect | Detail |
|--------|--------|
| **Transport** | stdio (primary, newline-delimited JSON-RPC). Streamable HTTP is draft. |
| **Lifecycle** | `initialize` → `session/new` → `session/prompt` ↔ `session/update` loop |
| **Streaming** | Agent sends `session/update` notifications (plan, text chunks, tool calls) |
| **Permissions** | Agent requests permission via `session/request_permission`; client approves/denies |
| **File access** | Agent can call client-side `fs/read_text_file`, `fs/write_text_file` |
| **Terminals** | Agent can request `terminal/create`, `terminal/output`, etc. |
| **Sessions** | Identified by opaque session IDs; optionally resumable via `session/load` |

## Design

### Where the ACP endpoint lives

The proxy container already runs three services (HTTP proxy, DNS server, control
API). The ACP bridge is a fourth service on a dedicated port, also mTLS-secured
and published to `127.0.0.1` on the host.

Why the proxy, not the sandbox:

1. **Network boundary** — The sandbox has no published ports and lives on an
   isolated network. The proxy is the only component with both host-facing and
   sandbox-facing connectivity.
2. **Security** — mTLS on the control plane is already implemented. The ACP
   endpoint reuses the same CA/cert infrastructure.
3. **No image changes** — The sandbox image does not need to bundle an HTTP
   server or expose ports. Agents use their native stdio transport inside the
   container.

### Transport: host ↔ proxy

ACP's primary transport is stdio, designed for local subprocesses. Since the
agent runs in a remote container, we need a network transport. Two options:

| Option | Mechanism | Fit |
|--------|-----------|-----|
| **WebSocket** | Full-duplex, natural mapping to bidirectional JSON-RPC | Best fit — low latency, simple framing, wide IDE support |
| **Streamable HTTP** | ACP draft spec for HTTP-based transport | Future option once spec stabilises |

**Recommendation:** Start with WebSocket. Each WebSocket connection maps to one
agent session. Messages are newline-delimited JSON-RPC, identical to the stdio
framing that ACP already defines.

### Transport: proxy ↔ sandbox agent

The proxy starts the agent inside the sandbox via `docker exec` (or equivalent)
and communicates over the exec session's stdin/stdout — exactly the stdio
transport ACP was designed for. This is the simplest and most compatible
approach: every ACP-compliant agent works unmodified.

```
IDE ──ws──▸ proxy ──docker exec stdin/stdout──▸ agent
IDE ◂──ws── proxy ◂──docker exec stdin/stdout── agent
```

### Session lifecycle

```
IDE                         Proxy (ACP bridge)              Sandbox
 │                               │                              │
 │── WS connect ────────────────▸│                              │
 │   (mTLS handshake)           │                              │
 │                               │── docker exec agent ────────▸│
 │                               │   (stdin/stdout attached)    │
 │                               │                              │
 │── initialize ────────────────▸│── initialize ───────────────▸│
 │◂── initialize result ────────│◂── initialize result ────────│
 │                               │                              │
 │── session/new ───────────────▸│── session/new ──────────────▸│
 │◂── session/new result ───────│◂── session/new result ───────│
 │                               │                              │
 │── session/prompt ────────────▸│── session/prompt ───────────▸│
 │◂── session/update (plan) ────│◂── session/update (plan) ────│
 │◂── session/update (chunks) ──│◂── session/update (chunks) ──│
 │◂── session/update (tool) ────│◂── session/update (tool) ────│
 │                               │                              │
 │   (agent calls fs/read…)     │                              │
 │◂── fs/read_text_file ────────│◂── fs/read_text_file ────────│
 │── fs/read result ────────────▸│── fs/read result ───────────▸│
 │                               │                              │
 │◂── session/prompt result ────│◂── session/prompt result ────│
 │                               │                              │
 │── WS close ──────────────────▸│── signal / close stdin ─────▸│
 │                               │                              │
```

The proxy is a transparent bidirectional relay — it forwards JSON-RPC messages
in both directions without interpreting their contents, with two exceptions:

1. **`fs/read_text_file` and `fs/write_text_file`** — When the agent calls
   these client methods, the proxy can intercept and serve them directly from
   the sandbox filesystem (via `docker exec cat` or similar) instead of
   forwarding to the IDE. This keeps file I/O inside the sandbox and avoids
   exposing host paths. Alternatively, these can be forwarded to the IDE if the
   use case requires host-side file access.

2. **`terminal/*` methods** — Similarly, terminal operations can be handled
   inside the sandbox rather than forwarded to the IDE.

### Configuration

Extend `ProxyConfig` with ACP settings:

```go
type ProxyConfig struct {
    // ... existing fields ...
    ACPPort    int    `json:"acp-port"`    // 0 = disabled
    ACPAgent   string `json:"acp-agent"`   // agent command, e.g. "claude"
    ACPAgentArgs []string `json:"acp-agent-args"` // e.g. ["--acp"]
}
```

The `run` command gains flags:

```
vibepit run --acp                    # enable ACP endpoint
vibepit run --acp-agent claude       # specify which agent to launch
vibepit run --acp-port 9444          # override default port
```

When `--acp` is set, the proxy container publishes the ACP port to
`127.0.0.1`, and the CLI prints connection details:

```
ACP endpoint: wss://127.0.0.1:9444  (mTLS)
```

### Authentication

Reuse the existing mTLS infrastructure:

- The proxy already generates an ephemeral CA + server/client cert pair per
  session.
- The IDE needs the CA cert and client cert/key to connect.
- These are already written to `$XDG_RUNTIME_DIR/vibepit/<sessionID>/`.

For IDEs that don't support mTLS natively, offer a local plaintext fallback:

```
vibepit run --acp --acp-no-tls     # listen on 127.0.0.1 without TLS
```

Since the port is bound to localhost only, the security boundary is the host
OS's process isolation (same trust model as LSP servers).

### Handling client-side capabilities

ACP agents may request capabilities that normally live on the client side:

| Capability | Strategy |
|------------|----------|
| `fs/read_text_file` | Intercept in proxy, read from sandbox via exec. The agent already has direct filesystem access in the sandbox so this is mainly for protocol compliance. |
| `fs/write_text_file` | Same — proxy writes to sandbox filesystem. |
| `terminal/*` | Proxy creates exec sessions in the sandbox container. |
| `session/request_permission` | Forward to IDE — the user must approve tool use. |

This means the proxy can advertise full filesystem and terminal capabilities
during `initialize`, even though these are handled server-side rather than by
the IDE client.

### Multiple agents / sessions

- Each WebSocket connection corresponds to one agent process.
- Multiple connections can run concurrently (multiple IDE windows, different
  agents).
- The proxy manages a map of WebSocket connection → exec session.
- When a WebSocket closes, the proxy signals the agent process to exit.

### IDE configuration

For an IDE to connect, it needs:

1. The ACP endpoint URL (printed by `vibepit run`).
2. mTLS credentials (or no-TLS mode).
3. The agent's advertised capabilities (discovered via `initialize`).

A `vibepit ide-config` command could emit a JSON config suitable for IDE
plugins:

```json
{
  "transport": "websocket",
  "url": "wss://127.0.0.1:9444",
  "tls": {
    "ca": "/run/user/1000/vibepit/abc123/ca.pem",
    "cert": "/run/user/1000/vibepit/abc123/client-cert.pem",
    "key": "/run/user/1000/vibepit/abc123/client-key.pem"
  }
}
```

## Implementation Plan

### Phase 1: Transparent relay

1. Add `proxy/acp.go` — WebSocket server that accepts connections, spawns an
   agent via `docker exec` in the sandbox, and relays JSON-RPC messages
   bidirectionally between the WebSocket and the exec stdin/stdout.
2. Wire the ACP service into `proxy/server.go` as a fourth goroutine.
3. Add `acp-port`, `acp-agent` to `ProxyConfig`.
4. Add `--acp` flags to the `run` command.
5. Publish the ACP port to `127.0.0.1` like the control API port.

This gets basic IDE → agent connectivity working. All ACP messages pass through
unmodified.

### Phase 2: Sandbox-local file and terminal handling

1. Intercept `fs/read_text_file` and `fs/write_text_file` in the proxy,
   fulfilling them from the sandbox filesystem.
2. Intercept `terminal/*` methods, creating exec sessions in the sandbox.
3. Advertise these capabilities during `initialize` so agents can use them
   without requiring IDE-side implementations.

### Phase 3: IDE integration polish

1. `vibepit ide-config` command to emit connection details.
2. Editor plugin examples (VS Code extension, Neovim plugin) showing how to
   connect.
3. `--acp-no-tls` for IDEs that don't support mTLS.
4. Session resume support (`session/load`).

### Phase 4: Streamable HTTP transport

Once the ACP streamable HTTP spec stabilises, add it as an alternative to
WebSocket for environments where WebSocket is inconvenient (corporate proxies,
etc.).

## Key Decisions and Trade-offs

| Decision | Rationale |
|----------|-----------|
| Bridge in proxy, not sandbox | Proxy already has host-facing ports and mTLS. Sandbox stays image-agnostic. |
| WebSocket over HTTP | Full-duplex maps naturally to bidirectional JSON-RPC. Lower latency than polling. |
| Transparent relay first | Gets working fast, no protocol interpretation needed, any ACP agent works. |
| Reuse mTLS | No new auth mechanism. Same security model as control API. |
| `docker exec` for agent spawn | Uses existing container client code. Agent gets stdio transport for free. |
| Intercept fs/terminal later | Phase 1 works without it (agents can use the sandbox filesystem directly). |

## Open Questions

1. **Agent discovery** — Should the proxy auto-detect which agents are installed
   in the sandbox, or require explicit configuration?
2. **Multiple agents per session** — Should one WebSocket support switching
   between agents, or one connection = one agent?
3. **Streamable HTTP priority** — If major IDEs adopt streamable HTTP before
   WebSocket, should we skip WebSocket and go straight to HTTP?
4. **Host file access** — Should `fs/*` ever reach the IDE for host-side files,
   or always stay sandboxed?
