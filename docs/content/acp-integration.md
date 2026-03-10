# IDE Integration via the Agent Client Protocol (ACP)

This document analyzes how IDEs and editors that speak the
[Agent Client Protocol](https://agentclientprotocol.com/) can connect to coding
agents running inside a Vibepit sandbox.

## Goal

An IDE on the host launches `vibepit acp` as a local subprocess. This command
execs into the running sandbox to start the agent, and acts as a protocol-aware
bridge between the IDE and the sandboxed agent. The agent works against the
sandboxed project directory while the IDE renders streaming updates, plans, and
permission prompts.

```
┌─────────────────────────────────────────────────────────────────┐
│ Host                                                            │
│                                                                 │
│  ┌──────────┐   stdio        ┌──────────────┐                  │
│  │   IDE    │ ──────────────▸│ vibepit acp  │                  │
│  │ (client) │◂──────────────│ (bridge)      │                  │
│  └──────────┘   JSON-RPC    │              │                  │
│                              │  intercepts: │                  │
│                              │  • terminal/* │                  │
│                              │  • fs/*       │                  │
│                              └──────┬───────┘                  │
│                                     │ docker exec              │
│                                     │ stdin/stdout             │
│                              ┌──────┼───────┐                  │
│                              │ Sandbox      │                  │
│                              │  ┌───▼─────┐ │                  │
│                              │  │  Agent  │ │                  │
│                              │  │ (claude,│ │                  │
│                              │  │ codex,…)│ │                  │
│                              │  └─────────┘ │                  │
│                              └──────────────┘                  │
└─────────────────────────────────────────────────────────────────┘
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
| **File access** | Agent calls client-side `fs/read_text_file`, `fs/write_text_file` |
| **Terminals** | Agent calls client-side `terminal/create`, `terminal/output`, etc. |
| **Sessions** | Identified by opaque session IDs; optionally resumable via `session/load` |

### Why not a transparent pipe

ACP defines `terminal/*` and `fs/*` as **client-side methods** — the agent
sends requests to the IDE asking it to execute commands and read/write files.
In a naive stdio pipe, these requests would reach the IDE and execute on the
**host**, outside the sandbox. This defeats Vibepit's isolation model.

The `vibepit acp` bridge must intercept these methods and fulfill them inside
the sandbox container instead of forwarding them to the IDE.

## Design

### Architecture: CLI command, not a proxy service

The bridge is a `vibepit acp` CLI command, not a new service in the proxy
container. Rationale:

1. **ACP's primary transport is stdio** — IDEs already know how to spawn a
   subprocess and talk JSON-RPC over stdin/stdout. No WebSocket, no mTLS, no
   new ports.
2. **Simpler** — A single command that execs into the sandbox. No proxy
   changes, no container image changes.
3. **Standard** — The IDE configures it exactly like any other local ACP agent.

The proxy-based approach (WebSocket, mTLS) would only be needed for remote IDE
access, which is a separate feature.

### Message routing

The bridge parses each JSON-RPC message and routes it:

| Direction | Method | Action |
|-----------|--------|--------|
| IDE → Agent | `initialize`, `session/*`, etc. | Forward to agent stdin |
| Agent → IDE | `session/update`, `session/request_permission` | Forward to IDE stdout |
| Agent → IDE | `terminal/create` | **Intercept**: run command in sandbox via `docker exec` |
| Agent → IDE | `terminal/output` | **Intercept**: return buffered output from sandbox exec |
| Agent → IDE | `terminal/wait_for_exit` | **Intercept**: wait for sandbox exec to complete |
| Agent → IDE | `terminal/kill` | **Intercept**: signal the sandbox exec process |
| Agent → IDE | `terminal/release` | **Intercept**: clean up sandbox exec resources |
| Agent → IDE | `fs/read_text_file` | **Intercept**: read file from sandbox filesystem |
| Agent → IDE | `fs/write_text_file` | **Intercept**: write file in sandbox filesystem |

Everything else passes through unmodified. The bridge is transparent for the
core conversation flow (`session/prompt`, `session/update`, etc.) and only
interposes on sandbox-sensitive operations.

### Initialize handshake

During `initialize`, the bridge must modify the response to advertise
client capabilities for `terminal` and `fs`, since it handles these itself:

```
IDE ── initialize ──▸ bridge ── initialize ──▸ agent
IDE ◂── result ────── bridge ◂── result ────── agent
                        │
                        └─ patches clientCapabilities in the
                           initialize request to include
                           terminal: true, fs: true
```

The agent sees a client that supports terminals and filesystem access. The IDE
does not need to implement these capabilities.

### Session lifecycle

```
IDE                      vibepit acp (bridge)              Sandbox
 │                               │                              │
 │   (IDE spawns subprocess)     │                              │
 │                               │── docker exec agent ────────▸│
 │                               │   (stdin/stdout attached)    │
 │                               │                              │
 │── initialize ────────────────▸│── initialize ───────────────▸│
 │                               │   (patches clientCapabilities│
 │                               │    to add terminal + fs)     │
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
 │   (agent wants to run cmd)   │                              │
 │                               │◂── terminal/create ─────────│
 │                               │── docker exec <cmd> ────────▸│
 │                               │── terminal/create result ───▸│
 │                               │                              │
 │                               │◂── terminal/wait_for_exit ──│
 │                               │   (waits for exec to finish) │
 │                               │── wait result ──────────────▸│
 │                               │                              │
 │◂── session/prompt result ────│◂── session/prompt result ────│
 │                               │                              │
 │   (IDE closes stdin)          │── signal agent ─────────────▸│
 │                               │                              │
```

### Terminal handling in detail

The bridge maintains a map of `terminalId` → exec session:

```go
type terminalState struct {
    execID     string       // docker exec ID
    output     bytes.Buffer // captured output
    exitCode   *int         // nil while running
    cancel     func()       // cancel context to kill
}
```

- **`terminal/create`**: Starts `docker exec` in the sandbox with the
  requested command, args, env, and cwd. Returns a `terminalId` immediately.
  Output is captured in background.
- **`terminal/output`**: Returns buffered output and truncation status.
- **`terminal/wait_for_exit`**: Blocks on the exec process. Returns exit code.
- **`terminal/kill`**: Sends signal to the exec process.
- **`terminal/release`**: Kills process if still running, cleans up state.

### Filesystem handling in detail

- **`fs/read_text_file`**: Reads from the sandbox filesystem via
  `docker exec cat <path>` or the container client's exec API.
- **`fs/write_text_file`**: Writes via `docker exec tee <path>` or equivalent.

All paths are resolved within the sandbox — the bridge never accesses host
filesystem paths.

### Configuration

The IDE configures the bridge as a standard ACP agent subprocess:

```json
{
  "command": "vibepit",
  "args": ["acp", "--agent", "claude"]
}
```

CLI flags for `vibepit acp`:

```
vibepit acp                          # auto-detect agent and session
vibepit acp --agent claude           # specify agent command
vibepit acp --agent-args "--acp"     # pass args to agent
vibepit acp --session <id>           # target specific session
```

Session discovery reuses the existing `ListProxySessions()` from
`cmd/session.go`. If only one session is running, it is selected automatically.

## Implementation Plan

### Phase 1: Protocol-aware bridge

1. Add `cmd/acp.go` — the `vibepit acp` command.
2. Session discovery: find the running sandbox container.
3. Agent launch: `docker exec -i <sandbox> <agent>` with stdin/stdout.
4. Message parsing: line-delimited JSON-RPC router that classifies messages
   by method.
5. Forwarding: pass-through for all non-intercepted methods.
6. `terminal/*` interception: manage exec sessions in the sandbox.
7. `fs/*` interception: read/write files in the sandbox.
8. `initialize` patching: inject `terminal` and `fs` client capabilities.

### Phase 2: Polish

1. Multiple concurrent terminals (map of terminalId → exec session).
2. Output byte limiting and truncation per ACP spec.
3. Graceful shutdown: clean up exec sessions when IDE disconnects.
4. `session/load` support for resuming agent sessions.
5. Error handling: surface container exec failures as JSON-RPC errors.

### Phase 3: Remote IDE support (optional)

If remote IDE access becomes a requirement, add a WebSocket/mTLS endpoint in
the proxy container that speaks the same protocol. The proxy would run the
same bridge logic, but over a network transport instead of stdio. This is
additive — the CLI command continues to work for local IDEs.

## Key Decisions

| Decision | Rationale |
|----------|-----------|
| CLI command, not proxy service | stdio is ACP's native transport. No new infrastructure needed. |
| Intercept `terminal/*` and `fs/*` | These are client-side methods that would escape the sandbox if forwarded to the IDE. |
| Forward `session/request_permission` to IDE | The user must approve tool use — this stays in the IDE where the user is. |
| `docker exec` for both agent and terminals | Reuses existing container client code. Consistent execution model. |
| Patch `initialize` capabilities | Agent sees full client support without requiring IDE-side implementations. |

## Open Questions

1. **Agent discovery** — Should `vibepit acp` auto-detect installed agents in
   the sandbox, or always require `--agent`?
2. **Permission forwarding** — Should `session/request_permission` be
   auto-approved (the sandbox is already isolated) or always forwarded to the
   IDE for user confirmation?
3. **Output streaming** — Should `terminal/output` stream incrementally or
   only return on poll? The ACP spec uses polling, but streaming would be more
   responsive.
