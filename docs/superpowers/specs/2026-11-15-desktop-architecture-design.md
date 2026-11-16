# Desktop system architecture

## Intent and scope

Define the components and boundaries of a Vibepit desktop application, so
that features can be built independently without the desktop becoming a second
copy of the CLI. The Wails experiment in `experiments/desktop` proved the user
experience. It also shows the coupling to remove:

- It imports CLI internals: `cmd.SessionInfo`, `cmd.NewControlClient`,
  `cmd.SSHClientPrivFile`, and `cmd.SSHHostPubFile`.
- It re-implements credential path resolution, including the legacy runtime
  directory fallback in `cmd/session.go`.
- It runs Claude as a process that dies with the SSH channel.
- It broadcasts every terminal byte to every window.

This document places each concern on one side of a boundary. Agent sessions
are one component, specified in
`docs/superpowers/specs/2026-11-15-agent-sessions-design.md`.

Out of scope: a remote container host (sandboxes run on the user's machine),
a common event schema across agents, file editing, browsing the working tree
or its version-control diff, and replacing the CLI. Viewing the patches agents
report is in scope (see "Diff viewer").

## Principles

1. **Security decisions go from the host to the proxy, never through the
   sandbox.** Network approvals, allow and deny changes, and anything that
   widens isolation use the mTLS control API with credentials that exist only
   on the host. The sandbox must have no path, direct or relayed, to change
   policy. Agent approvals are user-experience controls inside the sandbox
   (see the agent-sessions threat model) and may go through vibed.
2. **Durable state lives where the work runs.**
   - Terminal and agent sessions live in vibed.
   - Network policy, holds, and logs live in the proxy.
   - Project configuration lives in the project directory.

   The desktop holds only caches and preferences, and rebuilds its view after
   any reconnect, restart, or second instance.
3. **One host library, two front ends.** The CLI and the desktop share a
   host-side library for discovery, lifecycle, credentials, and connections.
   The desktop never imports `cmd`.
4. **The desktop backend transports agent content but does not interpret
   it.** Agent-native frames are rendered by per-driver frontend code. The
   backend relies on normalized state from vibed for anything it must act on,
   such as notifications and the tray.
5. **Every sandbox-facing protocol is versioned.** vibed and the proxy run
   the host binary mounted at `up` time (`cfg.DaemonBinaryPath` and
   `cfg.BinaryPath` in `container/client.go`). A desktop build therefore talks
   to whichever version started the sandbox.

## Components

```
┌──────────────────────────────── host ────────────────────────────────────────┐
│ Desktop app (separate Go module + frontend)                                  │
│  ┌ Frontend (Svelte) ─────────────────────────────────────────────────────┐  │
│  │ sandbox list · terminal (wterm) · agent views (claude/codex/acp)       │  │
│  │ network monitor · diff viewer · attention list · settings              │  │
│  └──────────── commands ▲ per-window events/streams ──────────────────────┘  │
│  ┌ Backend ─────────────────────────────────────────────────────────────────┐ │
│  │ Workspaces · Connection supervisor · Attention center · Stream relay     │ │
│  │ Preferences · Diagnostics recorder                                       │ │
│  └──────────────┬──────────────────────────────────────────────────────────┘ │
│  ┌ Native shell (Wails): windows, tray, menus, notifications, clipboard ┐    │
│                 │ Go API                                                     │
│ Host library (root module): discovery · lifecycle · credentials · connect   │
│  ├─ agentclient  ├─ terminalclient  ├─ control client (proxy)                │
└────┬───────────────────────┬──────────────────────┬──────────────────────────┘
     │ Docker/Podman API     │ SSH (pinned host key, │ HTTPS mTLS
     │                       │  per-session key)     │ (per-session certs)
     ▼                       ▼                       ▼
 container runtime    ┌ sandbox: vibed ─────────┐  ┌ proxy ─────────────────┐
                      │ terminal service (PTY)  │  │ policy · holds · logs  │
                      │ agent service           │  │ control API · /events  │
                      │ exec · forwards         │  │ HTTP proxy · DNS       │
                      └─────────────────────────┘  └────────────────────────┘
                         sandbox traffic ──────────────► (data plane only)
```

### 1. Host library (root module)

New package `host`, extracted from `cmd`. The CLI commands become thin layers
over it.

- **Discovery**: `List(ctx) ([]Sandbox, error)` returns the session ID,
  project directory, status, and published SSH and control ports. Today this
  is `ListProxySessions`, `FindRunningSession`, and `FindPublishedPort` usage
  spread across `cmd/session.go` and `cmd/bootstrap_ssh.go`.
- **Credentials**: the session directory with its legacy fallback, SSH
  keys, the pinned host key, mTLS material, and cleanup. Today:
  `cmd/session.go`.
- **Connect**: `Connect(ctx, sandbox) (*Conn, error)` returns a handle that
  owns the `*ssh.Client` (keepalive, reconnect with backoff, connection state
  callbacks) and the proxy control client. Service clients take a `*Conn`.
- **Lifecycle**: `Up(ctx, Options)`, `Down(ctx, id)`, `Status`. These are
  non-interactive: the first-run preset setup returns
  `ErrSetupRequired{Detected…}` instead of running the TUI, and each front
  end renders its own setup. Today this is `startSessionInfra`, which takes a
  `*cli.Command`, and `config.RunReconfigure` called inside bootstrap.
- **Control client**: today's `cmd.ControlClient` moves here unchanged. The
  NATS control plane spec (`2026-05-24-nats-control-plane-design.md`) would
  swap its transport behind the same surface.

### 2. Sandbox services (vibed)

vibed's SSH server is the single entry point into the sandbox. Each service
owns its channel types and an optional control subsystem. Each control
subsystem starts with `{"op":"hello","version":N}`, and vibed answers with its
version and capabilities, so an older vibed fails clearly instead of
misbehaving.

| Service | Data channel | Control subsystem | State owner |
|---|---|---|---|
| Terminal | PTY session channel (existing) | `vibepit-terminal`: `list`, `kill`, `rename`, `watch` | `session.Manager` |
| Agent | Subsystem channel per attach | `vibepit-agent` (agent-sessions spec) | `agentsession.Manager` |
| Exec | Exec session channel (existing) | none | none |
| Forwards | `direct-tcpip` (existing) | none | Host (`connect --forward`) |

**Terminal service change.** Today a PTY request lands in the
Bubble Tea session selector whenever detached sessions exist
(`handlePTYSession`). A graphical client needs to pick sessions itself.
Add an `env` request `VIBEPIT_SESSION=new|<id>` before `shell`: `new` always
creates a session, and an ID attaches to that session or fails. Without it,
today's behavior is unchanged. `vibepit connect` can adopt the same mechanism
for a host-side picker later.

**Watch.** `watch` in both control subsystems streams list changes and state
changes: a session created, attached, detached, or exited; agent status; agent
pending requests. This is the sandbox half of the attention center.

**Later: port discovery.** vibed reports listening sockets (from
`/proc/net/tcp*`) through a `vibepit-ports` subsystem, so the desktop can offer
"forward and open in browser". It reuses the existing forward path; nothing
listens on the host until the user asks.

### 3. Network control (proxy)

Unchanged boundary. The desktop uses the host control client for:

- the `/events` stream, holds, and logs;
- `Hosts` (per-host statistics) and `Config` (the effective merged
  configuration);
- `AllowHTTP` and `AllowDNS` (runtime);
- `Deny`, `DenyTarget`, `DenyHTTP`, and `DenyDNS`;
- saving to `.vibepit/network.yaml` through the same `config` helpers as the
  CLI's `A` and `N` keys.

Several clients can subscribe at once (desktop windows, `vibepit monitor`,
`connect --prompt`). The proxy resolves each hold once and broadcasts
`hold-end`, so clients stay consistent without coordinating.

Holding is enabled while any client subscribes. A desktop window watching a
sandbox therefore turns on holds for that sandbox, as `--prompt` does. The
network monitor must make that visible.

**Network decision logic moves into the host library.** `vibepit monitor`
keeps more than presentation in `cmd`:

- which allow entry to offer for a held request or a host (`allows.forHeld`,
  `allows.forHost` in `cmd/monitor_allow.go`);
- the deny-and-save target (`cmd/monitor_deny.go`);
- whether a host is allowable or explicitly denied;
- the host statistics model behind the hosts tab.

That logic moves to a `host/network` package used by both `vibepit monitor`
and the desktop's network feature. The desktop never re-implements these rules
in TypeScript.

### 4. Desktop backend

- **Workspaces**: one window per sandbox. Opening a sandbox that already has
  a window focuses that window instead of creating a second one, so each
  sandbox has exactly one connection, one attention list, and one set of
  notifications in this desktop instance. Other clients (the CLI, a second
  desktop instance) may still attach concurrently; vibed and the proxy are
  multi-client. Hiding a window keeps its connection and subscriptions;
  closing the sandbox (an explicit action) disconnects it; quitting closes all
  connections but leaves sandboxes, terminal sessions, and agent sessions
  running.
- **Connection supervisor**: owns `host.Conn` per sandbox and its
  reconnection. When the connection returns after sleep or a network change,
  it reattaches open terminals (by session ID; vibed replays scrollback),
  agent sessions (by `since`), and network watches (fresh `/events`
  subscription plus reset). The frontend sees connection states (`connecting`,
  `connected`, `reconnecting`, `lost`) instead of errors.
- **Attention center**: one list per sandbox of items needing the user. Each
  item has a source, kind, summary, deadline, and a resolve or withdraw
  signal. Sources:
  - proxy holds, from `/events`;
  - agent pending requests and finished turns, from `vibepit-agent watch`;
  - exited terminal sessions, from `vibepit-terminal watch`.

  It drives the tray, badges, and native notifications under today's rules:
  notifications focus a window and never approve, and they are withdrawn on
  resolution, expiry, or disconnect. Command completion in a visible terminal
  stays a frontend concern (OSC 133 through wterm).
- **Stream relay**: carries terminal bytes, agent envelopes, and Codex
  WebSocket messages between host clients and one window's frontend. It
  replaces the global `"vibepit"` broadcast with per-window delivery. Terminal
  output needs binary-safe delivery with backpressure. Whether Wails
  window-scoped events are enough, or a backend-served local WebSocket per
  window (with a per-window token) is needed, is a spike.
- **Preferences**: windows, notification policy, and per-sandbox UI state.
  This is host-only and never synced into the sandbox.
- **Diagnostics recorder**: opt-in local recordings of streams, generalizing
  the experiment's Claude stream log. Recordings stay outside the project
  directory, because the sandbox can read the project directory.

### 5. Desktop frontend

A projection of backend state plus protocol renderers:

- **Sandbox list and lifecycle**: attach to sandboxes first, with up and down
  added once the host library offers them.
- **Terminal**: wterm with the Ghostty WASM core, plus session tabs from
  `vibepit-terminal list`.
- **Agent views**: one renderer per driver.
  - Claude: `claude-stream.ts`.
  - ACP: `session/update` kinds plus a fallback for unknown agent requests.
  - Codex: a JSON-RPC client over the relayed WebSocket, with
    `thread/resume` and paginated history.
- **Diff viewer** for patches agents report (see "Diff viewer").
- **Network monitor**: the desktop counterpart of `vibepit monitor` (see
  "Network feature").
- **Attention list**.

### 6. Native shell

Wails-specific code for windows, tray, menus, notifications, clipboard, and
activation, as `shell_*.go` is today. It depends on the backend's interfaces,
not the reverse. The browser preview stays demo-only.

## Desktop internal structure

The desktop module is split into three layers plus a composition root. Features
are vertical slices: one Go package and one frontend folder each, meeting
through generated bindings. Everything runs in one process. Durable state lives
in vibed and the proxy, so a host daemon would add nothing.

```
┌ Features ───────────────────────────────────────────────────────────────────┐
│ sandboxes   terminal   agents (drivers: claude | codex | acp)   network     │
│ later: ports                                                                │
└──────┬─────────────┬───────────────────────────────┬────────────────────────┘
       │ uses        │ puts items into                │ uses
┌ Workspace core ────▼────────────────────────────────▼────────────────────────┐
│ workspace   connection supervisor   attention   prefs   diagnostics   bridge │
└──────┬───────────────────────────────────────────────┬───────────────────────┘
       │ host ports                                    │ platform ports
┌ Host adapters ─────▼──────────────────┐   ┌──────────▼──────────────────────────┐
│ live: host, terminalclient,           │   │ shell: Wails windows, tray, menus,  │
│ agentclient, control client           │   │ notifications, clipboard            │
│ demo: fake sandbox                    │   │ preview: browser stubs              │
└───────────────────────────────────────┘   └─────────────────────────────────────┘
                 app: the composition root wiring adapters → core → features
```

### Dependency rules

- Features depend on the workspace core and on host-port interfaces only.
  They never import each other, the shell, or a concrete host adapter.
- The core depends on host-port and platform-port interfaces only.
- Adapters implement ports and import no feature and no core package except
  the port definitions.
- `app` is the only package that knows every concrete type. It replaces
  today's `main.go` wiring.

These rules are enforced by an import check in the desktop module's tests,
not by convention.

### Feature lifecycle

There is one feature instance per workspace (window). The connection
supervisor drives it:

```go
type Feature interface {
	Name() string
	Open(ws *workspace.Workspace) (Instance, error)
}

type Instance interface {
	Connected(conn HostConn) // first connect and every reconnect
	Disconnected(err error)
	Close() error
}
```

`Workspace` provides the sandbox description, the attention sink, feature
preferences, and the bridge topics and streams scoped to its window. Each
feature reattaches its own resources in `Connected`:

- **terminal**: reattaches sessions by ID;
- **agents**: broker sessions reattach with `since`, Codex sessions with
  `thread/resume`;
- **network**: opens a fresh `/events` subscription and resets its view;
- **sandboxes**: has no workspace-scoped resources, and also serves the
  window-less sandbox picker.

### Agents feature

One feature for all drivers. It shares the session picker, create, kill, and
restart flows, attention items from `vibepit-agent watch`, and lifecycle.
Drivers differ only in transport and rendering:

- **Backend**: a broker transport relays `agentclient` envelopes (claude,
  acp); a passthrough transport terminates the Codex WebSocket and relays
  its messages. The backend never parses agent-native frames.
- **Frontend**: the driver UIs live in the `@vibepit/agent-ui` package
  (below). `features/agents/` holds only the vibepit-specific parts: the
  session picker, create, kill, and restart flows, and the adapter from vibed
  envelopes to the package's transport interface.

### Agent UI package

The Claude, Codex, and ACP user interfaces form a separate in-repo package,
`@vibepit/agent-ui`, with no dependency on Wails, vibed, or the desktop app,
so other projects can reuse them.

```
packages/agent-ui/
  src/core/            framework-agnostic TypeScript, no Svelte imports
    transport.ts       interfaces implemented by the host application
    claude/ codex/ acp/  protocol state and session controllers
  src/svelte/          Svelte 5 components
    shared/            Markdown, Composer, ToolActivity, PermissionPrompt
    claude/ codex/ acp/  per-driver views
  playground/          Vite app running the components against fake transports
```

- **Entry points**: `@vibepit/agent-ui/core` must stay usable from any
  framework, a CLI, or tests, and its build fails on Svelte imports.
  `@vibepit/agent-ui/svelte` contains the components, which are the only UI
  bindings for now. Custom elements or other framework bindings are added when
  a consumer needs them.
- **Native protocols, not vibed envelopes**: the transport carries each
  agent's own messages: Claude stream-json frames, and JSON-RPC messages for
  ACP and Codex. The desktop's `features/agents/` adapter turns vibed's
  `welcome`, replay, `frame`, and `resolved` envelopes into native messages
  and the optional resolution signal. Another project can connect the
  components directly to `claude -p`, `codex app-server`, or an ACP agent.

  ```ts
  interface AgentTransport {
    send(message: unknown): Promise<void>;
    onMessage(cb: (message: unknown) => void): () => void;
    onResolved?(cb: (requestId: string) => void): () => void;
    onDiagnostic?(cb: (d: { stream: string; raw: string }) => void): () => void;
  }

  interface UIHost {
    openExternal(url: string): Promise<void>;
    writeClipboard?(text: string): Promise<void>;
  }
  ```

  `onResolved` exists for multi-client hosts, where another client may answer
  a request. `onDiagnostic` feeds the optional debug view.
- **Session controllers** in `core/` own protocol logic that the experiment
  keeps in Go or `App.svelte`. That includes Claude's model and effort
  `control_request` handshake (validation, acknowledgement matching,
  timeouts) from `agent_config.go`, which the agent-sessions design no longer
  runs in a backend.
- **Theming** uses documented CSS custom properties; the desktop maps its
  tokens onto them.
- **Dependencies**: Markdown rendering (marked, DOMPurify) moves into the
  package. Link handling goes through `UIHost.openExternal`, replacing the
  experiment's direct `Browser.OpenURL` call.
- **Tests**: `core/` is tested headlessly against synthetic protocol fixtures,
  like today's `claude-stream.test.mjs`. The playground's fake transports
  double as the desktop demo mode's agent simulation.
- **Distribution**: consumed through an npm workspace in this repository.
  Publishing to npm is deferred until a second project needs it.

### Diff viewer

A read-only view of the patches agents report, part of `@vibepit/agent-ui`.
It shows what the agent says it changed, without reading the working tree.
Each driver reports changes in its own form, so `core/` normalizes them into
one model and `svelte/shared/` renders it:

```ts
interface FilePatch {
  path: string;
  change: 'add' | 'delete' | 'modify' | 'rename';
  oldPath?: string;          // renames
  hunks?: Hunk[];            // when the agent sent a unified diff
  oldText?: string;          // when the agent sent before and after text
  newText?: string;
  binary?: boolean;
}
```

| Driver | Source | Normalization |
|---|---|---|
| Claude | Edit, MultiEdit, and Write tool calls and their results | the tool input's replaced and replacement snippets (Edit, MultiEdit) or new content (Write); the structured patch in the tool result when present |
| Codex | `fileChange` items (`changes: [{path, kind, diff}]`) and `turn/diff/updated` | unified diff, parsed into hunks |
| ACP | tool call content of type `diff` (`path`, `oldText`, `newText`) | before and after text |

- **Placement**: inline under the tool call or file-change item in the
  transcript (collapsed by default), and as an aggregate per turn. Codex
  provides the aggregate (`turn/diff/updated`). For Claude and ACP, core
  combines the turn's patches per file.
- **Approvals show the patch.** When an agent asks to edit a file (Claude
  `can_use_tool` for Edit or Write, Codex `item/fileChange/requestApproval`,
  ACP permission requests whose tool call carries diff content), the approval
  prompt includes the diff viewer. Approving an edit without seeing it is the
  case this feature exists for.
- **Rendering**: unified and side-by-side modes, line numbers, and
  word-level highlighting within changed lines. Large patches are virtualized
  or truncated with an explicit "show all". Syntax highlighting is optional
  and added later. Text from agents is untrusted and rendered as text, never
  as HTML.
- **Dependencies**: a diff algorithm library in `core/` turns before and after
  text into hunks. The choice is left to implementation; it must have no
  framework or DOM dependency.
- **Fidelity differs by driver and moment.** A Claude edit approval carries
  only the replaced and replacement snippets, without line numbers or
  surrounding context, and a Write over an existing file carries only the new
  content. The viewer labels such partial patches instead of presenting them
  as full file diffs. After the tool runs, the result's structured patch, when
  present, replaces the partial view.
- **Scope limits**: patches are shown as reported. vibepit does not verify
  them against the files on disk, and the viewer does not apply, revert, or
  edit anything.

### Network feature

The desktop counterpart of `vibepit monitor`, scoped to the window's sandbox.
It uses the host control client and `host/network`, never re-implementing
decision rules (see "Network control").

- **Held requests**: pending holds with their deadline, approved for the
  session (`a`), approved and saved (`A`), rejected for the session (`n`), or
  denied and saved (`N`), the same choices as the CLI prompt and monitor.
  Holds feed attention.
- **Requests log**: the live `/events` stream with history, HTTP and DNS,
  with filters by source, action (allowed, blocked, held, denied), and text.
  Allow and deny actions are available on log entries as in the monitor's
  logs tab.
- **Hosts view**: per-host statistics from `Hosts`, sortable, with an
  expandable detail row. It shows what `proxy.HostStats` reports today:
  allowed and blocked counts, first and last seen, the last block reason,
  and whether the host is allowlisted, explicitly denied, or rejected for the
  session.
- **Rules view**: the effective allow and deny configuration from `Config`.
- **Known gaps for the UI design gate**: the proxy does not report which rule
  admitted or denied a host, and `MergedConfig` does not keep where an entry
  came from (global, project, preset, or a runtime allow). Explaining "why is
  this host allowed" or "what did my session-only approval not save" needs a
  control API addition. The proxy remains the owner of that information; the
  desktop must not reconstruct it.
- **Holding indicator**: a visible marker that this window's subscription
  enables holding for the sandbox.
- **Feedback**: a save failure reports that the runtime decision remains
  active but was not saved, as the monitor does today.

### Terminal UI package

The terminal user interface is a separate in-repo package,
`@vibepit/terminal-ui`, built like the agent UI package. wterm
(`@wterm/dom` with the `@wterm/ghostty` core) remains the emulator. The
package holds what vibepit adds on top of it and keeps wterm replaceable.

```
packages/terminal-ui/
  src/core/            framework-agnostic TypeScript, no Svelte imports
    transport.ts       TerminalTransport, implemented by the host application
    engine.ts          TerminalEngine, implemented by engine adapters
    session.ts         input and resize ordering, open and exit handling
    shell.ts           shell-state and working-directory interpretation
  src/engines/wterm/   the wterm adapter (the only code importing wterm)
  src/svelte/          Terminal component, find bar, status line
  playground/          fake transports: loopback, recorded VT streams, slow acks
  tests/conformance/   behavior every engine adapter must pass
```

- **Transport**: a byte-level contract. The component encodes wterm's string
  input as UTF-8, so there is one write path. Base64 and Wails details belong
  to the desktop adapter, which maps the stream relay onto it.

  ```ts
  interface TerminalTransport {
    open(size: { cols: number; rows: number }): Promise<void>;
    write(data: Uint8Array): Promise<void>;
    resize(cols: number, rows: number): Promise<void>;
    onOutput(cb: (data: Uint8Array) => void): () => void;
    onExit(cb: (reason: string) => void): () => void;
  }
  ```

- **Ordering lives in the package**: input and resize are serialized after
  open, sizes below the PTY minimum are dropped, a resize that happened while
  opening is sent afterwards, and nothing is written after exit. The
  experiment learned these rules the hard way (`Terminal.svelte`); every host
  gets them for free.
- **Engine independence**:
  1. No wterm type crosses the package's public API. The package defines its
     own `ShellState`, `SearchState`, and `TerminalEditor` handle (today
     `terminal-tools.ts` exports wterm's `ShellIntegrationState`).
  2. The component talks to an internal `TerminalEngine` interface covering
     only what it uses: mount, write, input, resize, title, working
     directory, shell state, search, selection, bracketed-paste mode,
     rendering pause, focus, and dispose. The wterm adapter is a thin
     mapping, and wterm-specific workarounds (such as capturing the selection
     before wterm's pointer handler clears it) stay inside it.
  3. A conformance suite runs recorded VT streams and playground scenarios
     against an engine adapter. It checks title, directory, shell state,
     search, selection, paste framing, and resize ordering. Replacing wterm
     means writing an adapter and passing this suite; the desktop does not
     change. Emulation and rendering details (Unicode width, reflow, fonts)
     are covered by the suite as regressions, not made identical.
- **Wasm loading**: the host passes the Ghostty wasm location, defaulting to
  an asset the package exports. The experiment's Vite-only
  `ghostty-vt.wasm?url` import does not leak to consumers.
- **App-specific actions** go through slots and callbacks. "Send selection to
  Claude" becomes a host-provided action using an `onSelection` callback.
  The package knows nothing about agents.
- **Stays in the desktop's `features/terminal/`**: session tabs and
  attach-by-ID, the stream relay adapter, native menu wiring through the
  `TerminalEditor` handle, and enabling shell integration
  (`VIBEPIT_SHELL_INTEGRATION`).
- **Distribution, theming, tests**: same rules as `@vibepit/agent-ui`: an
  in-repo npm workspace member, CSS custom properties (wterm's `--term-*`
  variables are mapped, not exposed), and the playground's fake transports
  doubling as the desktop demo's terminal.

### Future: OSC 7501 program status

The [Program Status Protocol](https://www.superlogical.com/rex/docs/build/program-status)
(OSC 7501, draft revision 0.4) lets a program report `idle`, `working`,
`done`, `blocked` (with `kind` = `permission`, `question`, or `auth`), or
`error`, with an optional `progress`, `app`, `title`, and `msg`, as a set
of records per terminal. It covers what OSC 133 cannot: what a program is
doing and whether it waits on the user. Coding agents running in a terminal
tab are the obvious producers. Support is planned, not part of the first
delivery. The intended design:

- **vibed is the terminal for this protocol.** Records belong to the PTY,
  and vibed's terminal sessions outlive any attached client. vibed scans each
  session's output for `OSC 7501` (and `OSC 133 ; A` for lifetime), with a
  byte-level scanner that handles sequences split across reads, independent
  of the scrollback emulator. It keeps the record set per session, following
  the spec's rules:
  - each report replaces its record completely;
  - `clear` removes the addressed record and every record below it;
  - `working` and `blocked` are dropped on process exit and the next prompt,
    while `done` and `error` survive;
  - the size limits apply, and reports with invalid base64 or control
    characters are discarded whole.

  `vibepit-terminal` `list` and `watch` expose the records, so status
  survives detaching and reaches the desktop without an open window.
- **One reply to feature detection.** vibed answers `OSC 7501 ; ?` itself
  by writing the reply into the PTY input, and removes the query from the
  output it forwards. Several attached clients therefore never produce
  duplicate replies. This includes a host terminal behind `vibepit connect`
  that supports the protocol. Reports themselves are forwarded unchanged, so
  such a host terminal also shows the status.
- **Attention.** The terminal feature turns records into attention items:
  - `blocked` becomes an approval, question, or login item, depending on
    `kind`;
  - `done` and `error` become finished items;
  - a removed record withdraws its item.

  Items name the sandbox and terminal session they came from, as the spec
  asks. Attention rate-limits the notifications they cause.
- **Display.** `@vibepit/terminal-ui` gets an optional `programStatus`
  input carrying records from the host, shown in the status line and tab
  badges. `msg` and `title` are untrusted. They are rendered as plain text
  with bidirectional and invisible formatting characters disarmed. Engine
  adapters must swallow `OSC 7501` queries rather than answer them. A
  built-in TypeScript record store in `core/` is added only if a host without
  a server-side store needs one.
- **Coverage**: OSC 133 `A` comes from vibepit's shell integration, which is
  enabled only for desktop sessions. In other sessions, `working` and
  `blocked` records last until the program replaces them or exits.

### Attention component

A core component separate from workspaces, because notification policy,
deadlines, withdrawal, and the tray badge span all windows. Features put
items in and withdraw them:

```go
type Item struct {
	ID       string // stable per source, e.g. "network/hold/123"
	Source   string // feature name
	Kind     string // approval, question, finished, exited
	Summary  string
	Deadline time.Time // zero if none
	Focus    FocusTarget // what to show when the user clicks
}

type Sink interface {
	Put(Item)
	Withdraw(id string)
}
```

Attention keeps today's rules: a notification focuses its window and never
approves; items are withdrawn on resolution, expiry, or disconnect; duplicates
from repeated events collapse by ID. It drives the tray and native
notifications through platform ports, so it is testable without Wails.

### Bridge and bindings

- **Calls**: each feature registers its own Wails service (`TerminalService`,
  `AgentService`, `NetworkService`, `SandboxService`), plus core services for
  workspace and attention. Methods take `context.Context`. The bridge resolves
  the workspace from the calling window (`ctx.Value(application.WindowKey)`)
  so the frontend never passes workspace IDs and cannot address another
  window's workspace.
- **Bindings** are generated with `wails3 generate bindings` into
  `frontend/src/bindings/` and committed. CI regenerates them and fails on
  any difference. Feature contracts are typed on both sides; hand-written
  `Call.ByName` strings go away.
- **Events and streams**: Wails events are broadcast to every window (even
  `WebviewWindow.EmitEvent` only sets `Sender`). The bridge therefore owns
  per-window delivery. Low-rate topics (state, attention) may use events
  filtered by the bridge's frontend half. Terminal output and agent streams
  use a per-window channel with binary payloads and backpressure. That
  mechanism is chosen by the stream relay spike: a local WebSocket or a
  streaming asset-handler endpoint, either authenticated by a per-window
  token.

### Frontend layout

- `src/app/`: window bootstrap, layout composition, routing between
  feature panels.
- `src/platform/bridge/`: generated bindings re-exported per feature, topic
  subscriptions, and stream readers.
- `src/features/<feature>/`: a store factory per workspace plus components.
  Features import only `platform/`, their own folder, and their UI package
  (`@vibepit/agent-ui` for agents, `@vibepit/terminal-ui` for terminal).

### Demo mode and tests

Demo mode is the fake host adapter set: a simulated sandbox with terminal,
agent drivers, and network holds. It replaces the `if s.demo` branches in
today's `service.go` and `agent.go`. The same fakes back feature tests in Go
and the frontend's tests, and the browser preview runs the app with fake host
adapters and stub platform adapters.

## State ownership

| State | Owner | Survives desktop quit | Survives container restart |
|---|---|---|---|
| Terminal sessions, scrollback | vibed `session.Manager` | yes | no |
| Agent sessions, logs, agent references | vibed `agentsession` (home volume) | yes | yes (as resumable) |
| Network holds, logs, runtime allows and denies | proxy | yes | no |
| Persistent allows and denies | `.vibepit/network.yaml`, global config | yes | yes |
| Session credentials | host state directory | yes | until `down` |
| Window layout, notification preferences | desktop preferences | yes | n/a |
| Recordings | desktop diagnostics directory | yes | n/a |

The project directory is mounted writable into the sandbox, but vibepit hides
`<project>/.vibepit` behind an empty read-only directory
(`container/client.go`). Project network configuration is therefore host-only:
the agent can neither read nor edit it. The desktop relies on this, and must
keep host-side state it writes into the project inside `.vibepit`, or outside
the project entirely.

## Binaries and version skew

vibed and the proxy are the `vibepit` binary of whoever ran `up`. Two
consequences:

- **Lifecycle from the desktop requires a `vibepit` Linux binary to mount.**
  The desktop bundles one: on Linux the `vibepit` binary for the host
  architecture, on macOS the Linux arm64 binary that the CLI release already
  embeds (`embed/`).
  Attach-only operation needs no binary.
- **The desktop must work with older sandboxes.** The `hello` versioning on
  every subsystem covers this. A missing subsystem (an old vibed rejects it as
  an unknown subsystem) means "feature unavailable for this sandbox; restart
  it with a newer vibepit", shown per feature rather than as a failed
  connection.

## Modules and packaging

- **Root module**: `host`, `agentsession`, `agentclient`, `terminalclient`,
  shared wire types, `proxy` types. No Wails dependency.
- **Desktop module**: today `experiments/desktop`, moving to `desktop/` once
  boundaries 1 to 4 exist. It depends on the root module at the same commit.
- **UI packages**: `packages/agent-ui` and `packages/terminal-ui`, npm
  workspace members the desktop frontend depends on by path. Not published.
- **Release**: the desktop has its own build and release track. The CLI
  release pipeline is unchanged until the desktop leaves experiment status.

## Migration from the experiment

| Experiment code | Destination |
|---|---|
| `backend/service.go` discovery, credentials, dial | `host` (Discovery, Credentials, Connect) |
| `backend/service.go` terminal open, write, resize | `terminalclient`; reattach by session ID |
| `backend/service.go` network watch and decisions | Host control client plus the desktop attention center |
| `backend/agent.go`, `agent_config.go` protocol handling | vibed claude driver (agent-sessions spec) |
| `backend/workspaces.go` notices and summaries | Attention center fed by `watch` and `/events` |
| `backend/stream_log.go` | Diagnostics recorder |
| Network panel in `App.svelte` | Network feature (held requests, log, hosts, rules) |
| Decision logic in `cmd/monitor_allow.go`, `cmd/monitor_deny.go`, `cmd/monitor_hosts.go` | `host/network`, shared by `vibepit monitor` and the desktop |
| `main.go` global `"vibepit"` emit | Stream relay with per-window delivery |
| `main.go` wiring | `app` composition root |
| `if s.demo` branches | Fake host adapters |
| `frontend/src/bridge.ts` (`Call.ByName`) | Generated bindings under `platform/bridge` |
| `claude-stream.ts`, `Claude*.svelte`, `ToolActivity`, `TurnDetails`, `Markdown` | `@vibepit/agent-ui` (core and svelte) |
| Claude session glue in `App.svelte`, `agent_config.go` handshake | Claude session controller in `@vibepit/agent-ui/core` |
| `Terminal.svelte`, `terminal-input.ts`, `terminal-tools.ts` (shell state, directory) | `@vibepit/terminal-ui` (core, wterm adapter, svelte) |
| `appendTerminalSelection` and the "Send selection to Claude" button | Desktop action bound through `onSelection` |
| `shell_*.go` | Shell platform adapter, unchanged in role |

## UI design gate

Before the frontend is restructured, a low to medium fidelity UI design
checks that the boundaries hold up against real screens. Its deliverable is a
UI design spec with:

1. **A screen inventory**:
   - the sandbox picker, with and without a window;
   - the workspace window and its panels;
   - agent session views per driver, including the diff viewer;
   - the network monitor with its held requests, log, hosts, and rules
     views;
   - the attention list, tray menu, notifications, and settings.
2. **Walkthroughs of the key flows**:
   - first launch → open a sandbox → start an agent → approve a file edit
     with its diff → a network request is held mid-turn;
   - close the window → an approval notification arrives → reopen with state
     intact;
   - laptop sleep and reconnect during a turn;
   - desktop and CLI attached to one agent session, including an answer from
     the other client winning;
   - terminal tabs, find, send selection to an agent, and OSC 7501 status from
     an agent running in the terminal;
   - investigating a blocked host in the network monitor and saving a rule;
   - an older sandbox with features unavailable.
3. **A boundary check table**: each UI element and interaction mapped to its
   feature, data source (vibed subsystem, proxy, host), protocol message, and
   package. An element needing two features, data no boundary provides, or
   protocol knowledge in the backend is a finding, fed back into this spec and
   the agent-sessions spec.

Wireframes suffice, except for the agent view: streaming, approvals, and
answers from other clients interact enough to justify a clickable prototype
on the experiment's demo mode.

Questions the design is expected to settle:

- **Network holds during agent turns**: a separate monitor only, or a
  workspace-level link point that shows a hold next to the agent turn. The
  proxy cannot attribute requests to processes, so a link would be a guess
  based on timing.
- **Codex's own network approvals** (`networkApprovalContext`) next to
  vibepit's holds: whether Codex's are disabled inside vibepit, so one request
  never prompts twice.
- **Cross-feature actions**: send selection to an agent, open a path from
  agent output, run a suggested command in the terminal. Each must be a host
  action through slots and callbacks, or the feature split is wrong.
- **Layout**: terminal and agent as tabs or side by side, several agent
  sessions per window, attention per window or in the tray, and where the
  monitor and the diff viewer live.

## Delivery order

0. **UI design gate** (above). It runs in parallel with step 1 and the
   agent-sessions task 0 spikes, whose results feed it. It must finish before
   step 4, the creation of the UI packages, and agent-sessions task 7. Steps 2
   and 3 may proceed; operations the design adds there are additive under
   `hello` versioning.
1. **Host library extraction**, including `host/network` (the monitor's
   decision logic): no behavior change; CLI tests prove parity. This unblocks
   everything else.
2. **Terminal service additions** (`VIBEPIT_SESSION`, `vibepit-terminal`
   with `hello`, `list`, `watch`) and **stream relay spike**.
3. **Agent sessions**, following the agent-sessions plan. Its task 0 spikes
   can run in parallel with steps 1 and 2.
4. **Desktop restructuring** into the internal structure above: the `app`
   composition root, ports and adapters (live and demo), workspace,
   connection supervisor, attention, generated bindings, per-window streams,
   then the features moved over one at a time.
5. **Lifecycle from the desktop** (deferred: up and down, graphical setup,
   bundled binary).
6. **Port discovery and forwarding**.
7. **OSC 7501 program status** in vibed's terminal service, the terminal
   feature, and `@vibepit/terminal-ui`.

## Decisions

1. **Attach-only first.** The desktop attaches to sandboxes started with
   `vibepit up`. Up, down, and graphical setup (delivery step 5) come later.
   The host library still exposes lifecycle so that step is additive.
2. **Per-service control subsystems** (`vibepit-terminal`, `vibepit-agent`,
   later `vibepit-ports`), each with its own `hello` version, because
   services version and ship independently.
3. **One sandbox per window.** Reopening an open sandbox focuses its window.
4. **The desktop bundles the `vibepit` binary**, built from the same commit as
   its host library. Sandboxes the desktop starts mount this binary, keeping
   vibed and the proxy at the desktop's protocol versions. Sandboxes started
   by a separately installed CLI are still attachable, with `hello`
   versioning covering the skew.
5. **One agents feature** with driver plugins, not one feature per agent.
6. **Attention is a separate core component**, not part of workspace.
7. **Generated bindings** (`wails3 generate bindings`), committed and checked
   in CI.
8. **Agent UIs in `@vibepit/agent-ui`**, independent of Wails and vibed. Only
   Svelte components for now; `@vibepit/agent-ui/core` stays framework
   agnostic.
9. **The agent UI package is consumed in-repo** through an npm workspace and
   not published for now.
10. **Terminal UI in `@vibepit/terminal-ui`**, structured like the agent UI
    package, with no wterm types in its public API and an internal engine
    adapter plus conformance suite so that wterm can be replaced.
11. **OSC 7501 program status is planned**, with vibed as the
    authoritative record store and the only responder to feature detection.
12. **A UI design gate precedes frontend restructuring**, with a boundary
    check table as its main output.
13. **A network monitor** equivalent to `vibepit monitor` is part of the
    network feature, sharing decision logic with the CLI through
    `host/network`.
14. **A diff viewer for agent-reported patches** is part of
    `@vibepit/agent-ui`. It is read-only, and is shown inline, per turn, and in
    edit approvals.
