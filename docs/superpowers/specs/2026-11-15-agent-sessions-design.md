# Agent sessions in vibed

## Intent and scope

Coding agents started from a UI (today: the desktop experiment's Claude tab)
must outlive the UI connection. vibed supervises the agent process inside the
sandbox, and UIs attach to it over the existing SSH connection, detach, and
reattach without losing the conversation, pending approvals, or a running
turn.

Three drivers are in scope:

- **claude**: Claude Code's native stream-json control protocol.
- **codex**: Codex `app-server`, which already serves multiple clients.
- **acp**: the Agent Client Protocol, covering Gemini CLI, Copilot CLI,
  Cursor, OpenCode, Goose, Pi (via pi-acp), and other ACP agents.

Out of scope: a common event schema across agents (UIs render each agent's
native frames), editor integration (`vibepit agent attach --acp`; see Later),
and replacing PTY sessions.

## Threat model

Approvals are a user-experience control, not a security boundary. The agent
runs as `code`, the same user as vibed and every agent process. A process
running as `code` can reach a Unix socket owned by `code`, and can usually open
`/proc/<pid>/fd` pipes of processes owned by `code`. The security boundary
remains the container hardening and the network proxy. Brokering must not
weaken SSH authentication: attaching is only possible through an authenticated
SSH connection.

## Architecture

```
UI (desktop, later CLI)                       sandbox
  ssh.Client ── session channel ──► vibed: subsystem "vibepit-agent"
                 subsystem request          │
                                            ▼
                                     agentsession.Manager
                                     ├─ Store (home volume: meta + log)
                                     ├─ broker mode ── claude driver ── claude -p … (stdio)
                                     │               └ acp driver ──── <agent> acp (stdio)
                                     └─ passthrough ── codex driver ── codex app-server
                                                                        --listen unix://…
```

New package `agentsession` (root module). It does not reuse `session.Manager`,
which is built around PTYs and the vt emulator. It does copy that manager's
patterns: a limit, a list, and a state file.

### Session model

An agent session has a vibed ID, a driver, creation options, an agent
reference, a status, and a log.

- **Agent reference**: the agent-native identifier used to resume. That is
  Claude's `session_id`, the Codex thread ID, or the ACP `sessionId`.
- **Status**: `starting`, `idle`, `running`, `waiting` (a pending request
  exists), `exited`, or `failed`.
- **One process per session** for all three drivers. This keeps lifecycle,
  limits, and cleanup uniform.
- **Processes** start with `setsid`. Shutdown uses the driver's orderly path
  first (closing stdin, or SIGTERM), then kills the process group after a grace
  period. This reuses `session/cleanup.go` behavior.
- **Exited sessions** stay listed with their log and agent reference until
  killed. `restart` resumes them.

### Persistence

The session metadata and log live under
`~/.local/state/vibed/agents/<project>/<id>/` in the home volume, so they
survive container restarts. `<project>` is derived from the project directory
(for example a hash of its path). The `vibepit-home` volume is shared by every
sandbox on the host, including concurrently running ones for other projects,
so the store must be scoped per project: vibed lists, restores, and allocates
IDs only within its own project directory, and holds an advisory `flock` on a
lock file there so two sandboxes for the same project never supervise or
resume the same agent session. The kernel drops the lock when vibed dies, so a
crashed container leaves no stale lock. A vibed that cannot take the lock
reports the agent service as unavailable (`hello` capabilities and every
operation answer `error: store locked by another sandbox`) rather than
reading a store it does not own. A summary state file in `/tmp`, like `ctr.SessionStatePath`, is
written for diagnostics. `vibepit status` runs on the host and cannot read it
directly; showing agent sessions there needs a read over SSH (`list` or an
exec of `cat`). On vibed start, sessions found in its project's store are
listed as `exited` and can be resumed. A restored session's `seq` continues
from the highest `seq` ever assigned, which `meta.json` records next to the
log, so a client's `since` stays valid across vibed and container restarts.
A truncated last line, left by a crash mid-write, is dropped on restore. `seq`
starts at 1, so `since: 0` always means "nothing seen". `seq` never goes
backwards: when the log is rewritten (compaction, or an ACP `session/load`
replay replacing it), the new entries get fresh `seq`s above the old maximum,
and an attach whose `since` refers to entries no longer in the log gets a
full replay (`"replay":"full"`).

### Wire protocol

The SSH client opens a session channel and requests subsystem
`vibepit-agent`. vibed handles this in the `handleSessionChannel` request loop,
before dispatch, alongside the existing `env` and forwards requests. Accepting
the subsystem counts as dispatch, like `shell` and `exec`: later `pty-req`,
`env`, `shell`, `exec`, or `subsystem` requests on that channel are rejected,
and a subsystem request after dispatch is rejected too. Every line
is an envelope object. **One channel carries one operation.** `list`, `create`,
`kill`, and `restart` reply and close. `attach` and `watch` keep the channel
open. `watch` sends `sessions` once, then a `sessions` envelope whenever a
session is created, exits, or changes `state` (status, pending requests, turn
end). The desktop's attention center depends on it.

Every channel starts with `{"op":"hello","version":N}`. vibed answers with its
version and capabilities (see "Binaries and version skew" in
`2026-11-15-desktop-architecture-design.md`). Client operations:

```jsonc
{"op":"list"}
{"op":"create","driver":"claude"|"codex"|"acp","options":{...}}
{"op":"attach","session":"a1","since":4182}   // since: last seq seen (seqs start at 1), 0 for full replay
{"op":"restart","session":"a1"}
{"op":"kill","session":"a1"}
{"op":"watch"}                                // streams state changes of all sessions
```

`create` options are a per-driver allowlist, never free-form argv:

- **All drivers**: `cwd` (must be inside the project or home directory) and
  `resume` (an agent reference).
- **claude**: `yolo`.
- **codex**: `approvalPolicy`, `sandbox`, `model`.
- **acp**: `agent`, a key into a vibed-side table of known ACP agent commands
  (for example `gemini` maps to `gemini --acp`). It also takes `mode` and
  `model`.

Server envelopes:

```jsonc
{"op":"welcome","session":"a1","driver":"claude","mode":"broker","seq":4182,
 "client":"c7","init":{...cached handshake result...},"state":{...}}
{"op":"frame","seq":4183,"dir":"out","frame":{...native frame from the agent...}}
{"op":"frame","seq":4184,"dir":"in","by":"c3","frame":{...native frame a client sent...}}
{"op":"frame","seq":4185,"dir":"out","for":"c3","frame":{...response to c3's request...}}
{"op":"frame","seq":4186,"stream":"stderr","raw":"…"}
{"op":"state","state":{...}}
{"op":"resolved","request":"…","by":"c3","outcome":"allowed"|"denied"|"answered"|"cancelled"}
{"op":"exited","code":0}
{"op":"error","message":"…"}
{"op":"sessions","sessions":[{"id":"a1","driver":"codex","state":{...}}]}
```

The client's `hello` carries a display label (`{"op":"hello","version":N,
"label":"desktop"}`). `welcome` returns the ID vibed assigned to this
attachment (`client`). A reattaching client passes its previous ID
(`"client":"c7"` in `hello`) to reclaim it. Otherwise responses to requests it
sent before the disconnect, which are routed by client ID, would reach no one,
and a settings change it started would stay unacknowledged.

Two live channels never share an ID. If the reclaimed ID is still attached,
vibed closes the older channel with `error: superseded` before the new one
receives `welcome`. The newest claim wins rather than being refused, because
the usual holder is the same client's dead connection: after sleep or a
network change, vibed still counts it as attached until keepalives time out.
Refusing would block exactly the reconnect that reclaiming exists for. All
clients authenticate with the same session key, so this transfers no
privilege. An ID vibed never assigned in this session is ignored, and the
client gets a fresh one.

`state` is the only normalized structure:
`{status, busy, pending:[{id, kind, summary}], model, resumable, agentRef,
clients:[{id, label, attachedAt}]}`. `list`, notifications, and
`vibepit status` use it without understanding any agent protocol. `clients`
is presence information for UIs ("also attached: vibepit CLI"). It is
advisory and grants nothing.

After `attach`, a broker-mode channel sends `welcome`, then replays from
`since`, then live `frame`s. A client writes `{"op":"send","frame":{...}}` and
`{"op":"detach"}`. A passthrough-mode channel sends `welcome` and then switches
to a raw byte stream spliced to the agent's socket; vibed flushes any bytes it
had already buffered. The client speaks the agent's own protocol from then on.
No envelope reaches a spliced channel after `welcome`, so a passthrough client
gets `state` and `resolved` only from Codex itself (`thread/status/changed`,
`serverRequest/resolved`), and other observers of the session get vibed's
view of them through `watch`.

## Broker mode (claude, acp)

vibed owns the agent's stdio. It is the agent's only client and multiplexes
any number of UIs.

- **Log**: every stdout line (`dir: "out"`) and every frame vibed forwards to
  the agent's stdin on a client's behalf (`dir: "in"`, with `by`) gets a
  monotonic `seq`. vibed appends it to the log file and fans it out. Inbound
  frames are logged in their original form, before ID rewriting. Logging
  inbound frames is what lets every client show prompts sent from another
  client: Claude's stream-json output does not echo stdin input, and ACP sends
  `user_message_chunk` only when replaying history. Each client has a bounded
  queue for live frames. Overflow disconnects that client with
  `error: lagging`, and the client reattaches with `since`. Replay is read
  from the log file under the channel's own flow control and never goes
  through the bounded queue, so a large replay cannot cause another `lagging`
  and a reattach loop. A replaying client is not yet subscribed to live
  frames: it keeps reading the log until it reaches the newest `seq`, then,
  under the log's lock, joins the live fan-out at exactly the next `seq`.
  Frames that arrive during replay are therefore read from the log, never
  queued, and none is skipped or sent twice. Agent stdout is never blocked by
  a client.
- **Compaction**: the driver marks lines the log may drop once superseded,
  such as streaming deltas of a completed message. Replay of a compacted range
  sends the final frames only. If `since` falls inside a compacted range, the
  client receives a full replay, and `welcome` says so (`"replay":"full"`) so
  the client discards its view first instead of showing frames twice.
- **Handshake**: vibed runs it at process start and caches the result for
  `welcome`. It rejects client attempts to repeat it.
- **Session lifecycle is vibed's**: clients cannot create, load, or resume
  agent sessions through `send`. That would bypass the vibed session model.
- **Request IDs**: vibed rewrites the ID of each client request to
  `"<client>:<original raw JSON>"`. The response is logged and fanned out to
  every client like any other stdout line, as a `frame` with `for` naming the
  requesting client and the ID restored to the exact original. The requesting
  client matches it to its pending request; other clients use it as an
  observation (an ACP `session/prompt` response is how they learn the turn
  ended and why) and never as an answer to a request of their own. Replay
  delivers logged responses the same way, so a client that reclaimed its ID
  receives the responses it missed.
- **Agent requests** (approvals, questions) become pending entries in `state`,
  and `welcome` carries them. Resolution follows the multi-client rules
  below. Entries are also cleared when the agent cancels them or the turn
  ends.
- **Turn policy** is per driver (see below), enforced by vibed because
  several clients can write.
- **Stderr** goes to the log as a diagnostic stream and to clients as
  `frame` records with a `stream` field (see the server envelopes), mirroring
  the experiment's debug tab.

### Multiple clients

Any number of UIs may attach to one agent session, and any of them may send
anything the driver allows. The expected case is one person with several
screens (the desktop, the CLI, later a phone), not several people. vibed
settles each conflict per message:

| Conflict | Rule |
|---|---|
| Two prompts at once | Turn policy: the second is rejected with `turn in progress`. A driver whose agent queues input during a turn may accept it as steering instead. |
| Two answers to one agent request | The first is forwarded to the agent. Later answers get `error: already resolved`. Every client receives `resolved` with `by` and `outcome`, so the losing client shows what actually happened. |
| An answer racing the agent's own cancel | The cancel wins. The late answer gets `already resolved`, and `resolved` carries `outcome: "cancelled"`. |
| Overlapping settings changes | Serialized: a change is rejected while another awaits the agent's acknowledgement. |

Interrupting is allowed from every client at any time; being able to stop the
agent from anywhere matters more than avoiding conflicts.

This is chosen over a single writer with takeover because:

- answering an approval from whichever screen is at hand is the primary use;
- a writer whose connection silently died (a sleeping laptop, until
  keepalives time out) would block every other client;
- Codex in passthrough mode cannot have a single writer enforced, because
  vibed does not see its messages, and its own model is multi-client;
- all clients authenticate with the same session key, so takeover would add no
  security.

If conflicting prompts become a real problem, an advisory composer lease
("being written on desktop") can be added without a protocol break.
Approvals and interrupts would stay open to every client.

**Terminal sessions deliberately differ.** PTY sessions stay single-writer
(`session.Attach`, `WriteInput`): keystrokes are a raw byte stream that two
typists would interleave mid-sequence, a PTY has one window size, and only one
client's replies to terminal queries may reach the program. None of this
applies to agent protocols, whose messages are discrete JSON objects with IDs.

### Driver interface

```go
type BrokerDriver interface {
	Command(opts CreateOptions) (argv, env []string, err error)
	// Start runs the handshake and creates or resumes the agent session.
	Start(ctx context.Context, rw LineRW, opts CreateOptions) (init json.RawMessage, ref string, err error)
	Classify(line []byte) (Frame, error)
	RewriteID(line []byte, rewrite func(json.RawMessage) json.RawMessage) ([]byte, error)
	CheckSend(st State, line []byte) error
	// Outcome derives the resolved outcome from a client's answer to an
	// agent request ("allowed", "denied", "answered", or "cancelled").
	Outcome(request, answer []byte) string
	Superseded(line []byte, ctx CompactionContext) bool
	Shutdown(rw LineRW, p *os.Process) error
}

type Frame struct {
	Kind FrameKind // Event, AgentRequest, AgentCancel, Response, TurnStarted, TurnEnded
	ID   json.RawMessage
	// Optional state updates extracted from the line.
	Model, Ref string
	Summary    string // human-readable pending-request summary
}
```

### claude driver

- **Command**: `claude -p --verbose --input-format stream-json
  --output-format stream-json --include-partial-messages
  --permission-prompt-tool stdio`, plus `--permission-mode default` or
  `--dangerously-skip-permissions`, plus `--resume <ref>` when resuming. The
  `create` handler passes the command to `$SHELL -c` prefixed with `exec`, as
  the experiment does through vibed's exec handler, so `claude` gets the same
  environment and PATH as `vibepit exec` (a non-login `-c` shell; anything
  the claude installer needs must be on that PATH), and the supervised
  process is `claude` itself rather than the shell. Every option value placed in the
  command string (such as the `resume` reference) is validated against the
  driver's expected format and shell-quoted; an allowlist of option names does
  not stop injection through their values.
- **Handshake**: `control_request{subtype:"initialize"}`. The cached response
  carries models, the permission mode, and commands.
- **Agent requests**: `control_request` with `can_use_tool`. Other subtypes
  get an error response, as the experiment does today.
  `control_cancel_request` clears the entry.
- **Outcome**: `response.behavior` of the client's `control_response`:
  `allow` becomes `allowed`, and `deny` becomes `denied`.
- **Responses**: `control_response.response.request_id`. Client control
  requests (`set_model`, `apply_flag_settings`, `interrupt`) are ID-rewritten.
- **Turns**: a `user` message from a client starts a turn, and `result` ends
  it. Prompts and configuration changes are rejected while a turn runs or a
  configuration change is unacknowledged.
- **Reference**: `session_id` from `system/init`.
- **Superseded**: `stream_event` lines of a message whose `assistant` frame
  has arrived.

The process and protocol handling in `experiments/desktop/backend/agent.go`
moves here. The model and effort handshake in `agent_config.go` (validation,
acknowledgement matching, timeouts) moves to the Claude session controller in
`@vibepit/agent-ui/core` (see the desktop architecture spec); vibed only
tracks which configuration request is unacknowledged, for the turn policy.

### acp driver

- **Command**: from the known-agent table (`gemini --acp`,
  `copilot --acp --stdio`, `agent acp`, `opencode acp`, `pi-acp`, …).
- **Handshake**: `initialize` at protocol version 1 with **no** `fs` or
  `terminal` capabilities, so agents use their own tools. Then `session/new`,
  or `session/resume` or `session/load` when resuming, chosen by advertised
  capabilities.
- **Agent requests**: any JSON-RPC request from the agent. This covers
  `session/request_permission` and vendor extensions such as
  `cursor/ask_question` without vibed knowing them. The pending summary comes
  from `toolCall.title` when present, otherwise the method name.
- **Outcome**: for `session/request_permission`, the selected `optionId` is
  looked up in the request's options. An `allow_*` kind becomes `allowed`, a
  `reject_*` kind becomes `denied`, and `{"outcome":{"outcome":"cancelled"}}`
  becomes `cancelled`. Any other request answered with a result becomes `answered`.
- **Client-request rules**: requests are ID-rewritten. `initialize`,
  `session/new`, `session/load`, and `session/resume` are always rejected.
  `authenticate` is forwarded only while the session is `starting` because
  `session/new` failed with an auth error. `welcome` exposes the advertised
  auth methods, so a UI can show a terminal login command or call
  `authenticate`. After it succeeds, vibed retries session creation.
- **Turns**: a `session/prompt` request starts a turn, and its response
  (with stop reason) ends it. A second prompt during a turn is rejected.
- **Cancel**: after forwarding `session/cancel`, vibed answers every pending
  `session/request_permission` with `{"outcome":{"outcome":"cancelled"}}`.
  The spec requires this from the client, and vibed is the client.
- **Resume**: prefer `session/resume` when the log is intact. Use
  `session/load` when the log is lost, or when it is intact but the agent
  supports only `session/load`. Its replay replaces the log in both cases, so
  history is never logged twice. Agents advertising neither are
  `resumable: false`.
- **Superseded**: none initially. ACP updates are chunks without a final
  aggregate frame, so an ACP log grows by one line per chunk and a full
  replay resends every chunk. Until a driver-side aggregation exists, vibed
  coalesces consecutive `agent_message_chunk` and `agent_thought_chunk`
  updates of one message into a single logged frame once the message is
  followed by any other update, and replays the coalesced form.

## Passthrough mode (codex)

Codex app-server already multiplexes clients. It does per-connection
`initialize`, thread subscriptions, server-side pending requests replayed on
`thread/resume`, `serverRequest/resolved`, and `thread/status/changed`. vibed
supervises it and splices client channels to it. It keeps no log and rewrites
nothing.

- **Command**:
  `codex app-server --listen unix://$XDG_RUNTIME_DIR/vibed/agents/<id>.sock`
  (fallback: `/tmp/vibed-agents/<id>.sock`), with the directory at mode 0700.
- **Observer**: vibed keeps one WebSocket connection of its own on the
  socket. It initializes, then runs `thread/start` with the create options, or
  `thread/resume <ref>` when resuming. It records the thread ID as the
  reference and stays subscribed, which also keeps the thread loaded past the
  60-second unload delay. `thread/status/changed` (for example `activeFlags:
  ["waitingOnApproval"]`), server requests (counted from the observer's view,
  never answered by it), and `serverRequest/resolved` feed `state`.
- **Attach**: `welcome` carries the thread ID and `state`. The UI then
  connects over the spliced stream as a WebSocket client, performs its own
  `initialize`, and calls `thread/resume` with `excludeTurns: true`. It loads
  history with `thread/turns/list` and `thread/items/list`.
- **Multiple clients**: app-server settles conflicts itself. vibed's
  multi-client rules (see Broker mode) are the expected behavior, and the
  task 0 spike checks that app-server matches them. Specifically: user input
  from one connection reaches the others as `userMessage` items; the first
  answer wins; and `serverRequest/resolved` reaches every subscriber. vibed
  still tracks presence: it knows each spliced channel and its `hello` label,
  and lists it in `state.clients`. vibed's observer connection is not listed.
  The `outcome` of a resolution comes from the `item/completed` status that
  follows (`completed` or `failed` means the work ran and maps to `allowed`;
  `declined` maps to `denied`), or `cancelled` when the request was cleared by
  turn end or interrupt. vibed does not read spliced traffic, so it cannot
  name the answering client: the resolution reaches `watch` and `state`
  without `by`, and spliced clients learn it from Codex's own
  `serverRequest/resolved`.
- **Model and effort**: fields on `turn/start`. Mid-turn input uses
  `turn/steer`.
- **Codex's own sandbox**: inside vibepit the container is the boundary.
  The default `sandbox` option is the least restrictive mode that works in the
  hardened container. The spike determines that mode.
- **Bonus**: `codex --remote unix://…` in a sandbox shell attaches the Codex
  terminal UI to the same session.

## Host-side client

Package `agentclient` (root module) provides `List`, `Create`, `Attach`,
`Restart`, `Kill`, and `Watch` over an `*ssh.Client`. `Attach` returns either a broker
stream (send, receive envelopes) or a passthrough `net.Conn`. The desktop backend
uses it in place of its own process handling. A later
`vibepit agent list|kill` CLI uses it too.

## Approaches considered

- **Brokering Codex over stdio like ACP**: one client protocol for all
  drivers. It duplicates what app-server already does (multi-client, pending
  replay, resolution) and loses `codex --remote`. Rejected.
- **claude-agent-acp for Claude**: one fewer driver, but it adds a Node runtime
  that is not in the image (about 41k lines of compiled JS to depend on). It
  also drops rate-limit windows the desktop shows. Rejected in favor of the
  native driver.
- **Extending `session.Manager`**: its scrollback, resize, and vt handling do
  not apply to agents. Rejected.
- **Free-form agent argv in `create`**: maximum flexibility, but it widens the
  exec surface reachable through the subsystem. Rejected for per-driver
  allowlists.

## Risks to resolve first

1. **Codex multi-client behavior on one thread**: whether two connections
   both receive item deltas, approval requests, and each other's user input
   (`userMessage` items); whether either can answer, with the first answer
   winning; and what a connection that resumes mid-turn sees of the
   in-progress turn. Also whether app-server sends a server request to every
   subscribed connection or only to one (such as the first subscriber or the
   turn's originator). If only one, the observer, which subscribes first and
   never answers, could receive an approval no UI sees and stall the turn.
2. **Codex sandbox modes** inside the hardened container (read-only root,
   dropped capabilities, no-new-privileges).
3. **Claude `--resume`** with stream-json input: whether an interrupted turn or
   pending approval is lost, and what the first frames after resume look like.
4. **ACP capabilities** of the agents users want first (Gemini, Copilot,
   OpenCode): `loadSession`, `sessionCapabilities.resume`, and auth methods in
   a headless sandbox.

## Later

- `vibepit agent attach --acp <session>`: an ACP stdio bridge for editors
  (Zed custom agent server) to ACP-driver sessions, with `_vibepit/resolved`
  as an extension notification.
- Notifications on the host for detached sessions waiting on approval, driven
  by `watch`, for a host without the desktop (for example the CLI).
- ACP v2 once it is stable.
