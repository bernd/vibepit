# IDE Integration via the Agent Client Protocol (ACP)

This document analyzes how IDEs and editors that speak the
[Agent Client Protocol](https://agentclientprotocol.com/) can connect to coding
agents running inside a Vibepit sandbox.

## Goal

An IDE on the host launches `vibepit acp` as a local subprocess. This command
does a single `docker exec` into the sandbox to run `vibepit acp-intercept`,
which starts the agent and handles all intercepted operations (terminals,
filesystem) directly inside the sandbox — no per-command `docker exec` needed.
The IDE renders streaming updates, plans, and permission prompts.

```
┌──────────────────────────────────────────────────────────────────┐
│ Host                                                              │
│                                                                   │
│  ┌──────────┐  stdio   ┌──────────────┐                          │
│  │   IDE    │ ────────▸│ vibepit acp  │                          │
│  │ (client) │◂────────│ (host shim)  │                          │
│  └──────────┘  JSON-RPC└──────┬───────┘                          │
│                               │ single docker exec               │
│                               │ stdin/stdout                     │
│                        ┌──────┼──────────────────────────┐       │
│                        │ Sandbox                         │       │
│                        │  ┌───▼────────────────────────┐ │       │
│                        │  │ vibepit acp-intercept      │ │       │
│                        │  │                            │ │       │
│                        │  │  intercepts:               │ │       │
│                        │  │  • terminal/* → os/exec    │ │       │
│                        │  │  • fs/*       → os file I/O│ │       │
│                        │  │                            │ │       │
│                        │  │  ┌────────────────────┐    │ │       │
│                        │  │  │  Agent (claude, …) │    │ │       │
│                        │  │  │  stdin/stdout pipe │    │ │       │
│                        │  │  └────────────────────┘    │ │       │
│                        │  └────────────────────────────┘ │       │
│                        └─────────────────────────────────┘       │
└──────────────────────────────────────────────────────────────────┘
```

The vibepit binary is bind-mounted into the sandbox container at startup
(read-only), so `acp-intercept` is available without modifying the sandbox
image.

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

The interceptor running inside the sandbox handles these methods directly using
native system calls (`os/exec` for terminals, `os` file I/O for filesystem),
keeping all tool execution sandboxed.

## Design

### Architecture: two-part CLI command

The bridge has two halves:

1. **`vibepit acp`** (runs on host) — Thin shim that the IDE spawns as a
   subprocess. It does a single `docker exec` into the sandbox to start
   `vibepit acp-intercept`, then relays stdio between the IDE and the exec
   session. It has no protocol awareness — it is a byte pipe.

2. **`vibepit acp-intercept`** (runs inside sandbox) — The protocol-aware
   interceptor. It starts the agent as a child process, parses JSON-RPC
   messages, and intercepts `terminal/*` and `fs/*` methods. Intercepted
   operations run directly inside the sandbox using native Go APIs (`os/exec`,
   `os` file I/O). Everything else is forwarded between the IDE and the agent.

Rationale:

1. **ACP's primary transport is stdio** — IDEs spawn a subprocess and talk
   JSON-RPC over stdin/stdout. No WebSocket, no mTLS, no new ports.
2. **No per-command docker exec** — Terminals and file operations run natively
   inside the sandbox. Only one `docker exec` is needed for the entire session.
3. **No image changes** — The vibepit binary is bind-mounted into the sandbox
   at startup (read-only). No new dependencies in the sandbox image.
4. **Standard** — The IDE configures it exactly like any other local ACP agent.

The proxy-based approach (WebSocket, mTLS) would only be needed for remote IDE
access, which is a separate feature.

### Message routing

The bridge parses each JSON-RPC message and routes it:

| Direction | Method | Action |
|-----------|--------|--------|
| IDE → Agent | `initialize`, `session/*`, etc. | Forward to agent stdin |
| Agent → IDE | `session/update`, `session/request_permission` | Forward to IDE stdout |
| Agent → IDE | `terminal/create` | **Intercept**: `os/exec.Command()` in sandbox |
| Agent → IDE | `terminal/output` | **Intercept**: return buffered output |
| Agent → IDE | `terminal/wait_for_exit` | **Intercept**: `cmd.Wait()` |
| Agent → IDE | `terminal/kill` | **Intercept**: `cmd.Process.Signal()` |
| Agent → IDE | `terminal/release` | **Intercept**: kill + clean up |
| Agent → IDE | `fs/read_text_file` | **Intercept**: `os.ReadFile()` |
| Agent → IDE | `fs/write_text_file` | **Intercept**: `os.WriteFile()` |

Everything else passes through unmodified. The interceptor is transparent for
the core conversation flow (`session/prompt`, `session/update`, etc.) and only
interposes on sandbox-sensitive operations.

### Initialize handshake

During `initialize`, the interceptor modifies the request to advertise
client capabilities for `terminal` and `fs`, since it handles these itself:

```
IDE ── initialize ──▸ interceptor ── initialize ──▸ agent
IDE ◂── result ────── interceptor ◂── result ────── agent
                           │
                           └─ patches clientCapabilities in the
                              initialize request to include
                              terminal: true, fs: true
```

The agent sees a client that supports terminals and filesystem access. The IDE
does not need to implement these capabilities.

### Session lifecycle

```
IDE              vibepit acp          vibepit acp-intercept (in sandbox)
 │               (host shim)                    │
 │                    │                         │
 │  (IDE spawns       │                         │
 │   subprocess)      │── docker exec ─────────▸│
 │                    │   vibepit acp-intercept  │
 │                    │                         │── starts agent
 │                    │                         │   (child process)
 │                    │                         │
 │── initialize ─────▸│─── relay ──────────────▸│── initialize ──▸ agent
 │                    │                         │   (patches caps)
 │◂── result ────────│◂── relay ───────────────│◂── result ────── agent
 │                    │                         │
 │── session/prompt ─▸│─── relay ──────────────▸│── forward ─────▸ agent
 │◂── session/update─│◂── relay ───────────────│◂── forward ───── agent
 │                    │                         │
 │                    │   (agent wants to       │
 │                    │    run a command)        │
 │                    │                         │◂── terminal/create
 │                    │                         │    agent
 │                    │                         │── os/exec <cmd>
 │                    │                         │── result ──────▸ agent
 │                    │                         │
 │◂── prompt result──│◂── relay ───────────────│◂── prompt result  agent
 │                    │                         │
 │  (IDE closes       │                         │
 │   stdin)           │── EOF ─────────────────▸│── signal agent
 │                    │                         │
```

Note how the host shim (`vibepit acp`) is a simple byte relay — all protocol
logic lives in `acp-intercept` inside the sandbox.

### Terminal handling in detail

The interceptor maintains a map of `terminalId` → process:

```go
type terminalState struct {
    cmd      *exec.Cmd
    output   bytes.Buffer // captured stdout+stderr
    exitCode *int         // nil while running
    cancel   func()       // cancel context to kill
}
```

Since the interceptor runs inside the sandbox, terminal operations are native:

- **`terminal/create`**: Starts the command via `os/exec.CommandContext()` with
  the requested command, args, env, and cwd. Returns a `terminalId`
  immediately. Output is captured in background goroutine.
- **`terminal/output`**: Returns buffered output and truncation status.
- **`terminal/wait_for_exit`**: Calls `cmd.Wait()`. Returns exit code.
- **`terminal/kill`**: Calls `cmd.Process.Signal()`.
- **`terminal/release`**: Kills process if still running, cleans up state.

No `docker exec` per command — processes are direct children of the
interceptor.

### Filesystem handling in detail

- **`fs/read_text_file`**: `os.ReadFile(path)` — direct filesystem access.
- **`fs/write_text_file`**: `os.WriteFile(path, data, perm)` — direct write.

All paths are naturally sandboxed because the interceptor runs inside the
container. No path translation or escaping is needed.

### Binary mounting

The `run` command bind-mounts the vibepit binary into the sandbox container at
a well-known path (e.g. `/usr/local/bin/vibepit`) as read-only. This makes
`acp-intercept` available inside the sandbox without modifying the image.

The binary is the same `vibepit` binary running on the host. On Linux hosts
this works directly. On macOS hosts, the existing cross-compiled Linux binary
(used for the proxy container) is mounted instead.

### Configuration

The IDE configures the bridge as a standard ACP agent subprocess:

```json
{
  "command": "vibepit",
  "args": ["acp", "--agent", "claude"]
}
```

CLI flags for `vibepit acp` (host side):

```
vibepit acp                          # auto-detect agent and session
vibepit acp --agent claude           # specify agent command
vibepit acp --agent-args "--acp"     # pass args to agent
vibepit acp --session <id>           # target specific session
```

`vibepit acp` discovers the running session, then runs:

```
docker exec -i <sandbox> vibepit acp-intercept --agent <agent> [--agent-args ...]
```

`vibepit acp-intercept` (sandbox side) is an internal command not intended for
direct use. It starts the agent, runs the JSON-RPC interceptor, and exits when
stdin closes.

Session discovery reuses the existing `ListProxySessions()` from
`cmd/session.go`. If only one session is running, it is selected automatically.

## Implementation Plan

### Phase 1: Protocol-aware interceptor

1. Add `cmd/acp.go` — the `vibepit acp` host shim. Discovers session, runs
   `docker exec -i <sandbox> vibepit acp-intercept ...`, relays stdio.
2. Add `cmd/acp_intercept.go` — the `vibepit acp-intercept` command that runs
   inside the sandbox.
3. Bind-mount the vibepit binary into the sandbox in `cmd/run.go`.
4. Agent launch: start the agent as a child process with piped stdin/stdout.
5. Message parsing: line-delimited JSON-RPC router that classifies messages
   by method.
6. Forwarding: pass-through for all non-intercepted methods.
7. `terminal/*` interception: manage child processes via `os/exec`.
8. `fs/*` interception: read/write files via `os` package.
9. `initialize` patching: inject `terminal` and `fs` client capabilities.

### Phase 2: Polish

1. Multiple concurrent terminals (map of terminalId → `*exec.Cmd`).
2. Output byte limiting and truncation per ACP spec.
3. Graceful shutdown: kill child processes when stdin closes.
4. `session/load` support for resuming agent sessions.
5. Error handling: surface process failures as JSON-RPC errors.

### Phase 3: Remote IDE support (optional)

If remote IDE access becomes a requirement, add a WebSocket/mTLS endpoint in
the proxy container that speaks the same protocol. The proxy would run the
same interceptor logic, but over a network transport instead of stdio. This is
additive — the CLI command continues to work for local IDEs.

## Key Decisions

| Decision | Rationale |
|----------|-----------|
| CLI command, not proxy service | stdio is ACP's native transport. No new infrastructure needed. |
| Interceptor inside sandbox | Terminal and fs operations are native (no per-command docker exec). Only one docker exec for the entire session. |
| Bind-mount vibepit binary | No sandbox image changes needed. Same binary, read-only mount. |
| Two-part command (shim + interceptor) | Host shim is trivial (byte pipe). All protocol logic runs sandboxed. |
| Intercept `terminal/*` and `fs/*` | These are client-side methods that would escape the sandbox if forwarded to the IDE. |
| Forward `session/request_permission` to IDE | The user must approve tool use — this stays in the IDE where the user is. |
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
