# Agent Sessions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** vibed supervises Claude, Codex, and ACP agent processes, and UIs attach, detach, and reattach over a `vibepit-agent` SSH subsystem.

**Architecture:** New `agentsession` package with a manager, a home-volume store, and two attach modes. Broker mode covers the claude and acp drivers: vibed owns stdio and keeps a seq log, replay, pending requests, and ID rewriting. Passthrough mode covers the codex driver: app-server listens on a Unix socket, and vibed splices channels to it and observes it. The new `agentclient` package is the host-side client. The desktop experiment moves onto it.

**Tech Stack:** Go, `golang.org/x/crypto/ssh`, charmbracelet/ssh (existing sshd), a WebSocket client library for the Codex observer and the desktop, testify.

**Spec:** `docs/superpowers/specs/2026-11-15-agent-sessions-design.md` (draft, pending approval).

## Global Constraints

- Agent processes run as `code` inside the sandbox; no new privileges, listeners on TCP, or image runtimes (no Node for the claude driver).
- The subsystem is reachable only through the authenticated SSH connection.
- `create` accepts per-driver option allowlists only; never free-form argv.
- Agent stdout is never blocked by a slow client.
- Existing PTY sessions, `exec`, and forwards keep their behavior.
- Validate with `make test` and `make test-integration`; real agent binaries are manual verification only.

## Review Focus

- A client that disconnects mid-turn and reattaches from its log position sees every frame exactly once and the same pending requests (tasks 2, 3).
- Two clients answering one approval: exactly one response reaches the agent, and both see `resolved` with the winning client and outcome (tasks 2, 4, 5).
- A prompt sent by one client appears in every other client's stream and in replay, as an inbound frame in its original form (task 2).
- An answer racing the agent's cancel is never forwarded, and `resolved` reports `cancelled` (tasks 2, 4, 5).
- ID rewriting restores numeric and string JSON-RPC IDs byte-for-byte (tasks 2, 5).
- ACP cancel answers all pending permission requests with `cancelled` (task 5).
- Passthrough switch-over loses no buffered bytes (task 6).
- vibed restart lists previous sessions as `exited` and resumes them (tasks 1, 4, 5, 6).

## Task 0: Spikes (no production code)

Record findings in the spec's "Risks to resolve first" section and adjust tasks before starting task 1.

- [ ] Codex: run `codex app-server --listen unix://…` in a sandbox. Connect two WebSocket clients to one thread and check deltas, approvals, answering from either client (first answer wins), `serverRequest/resolved`, whether one client's input reaches the other as `userMessage` items, and mid-turn `thread/resume` content. Check which `sandbox` modes work in the hardened container, and whether an observer subscription keeps the thread loaded.
- [ ] Claude: interrupt a stream-json session during a turn and during a pending `can_use_tool`, then `--resume`; record the first frames and what was lost.
- [ ] ACP: run `gemini --acp`, `copilot --acp --stdio`, and `opencode acp` in a sandbox. Record `initialize` capabilities (`loadSession`, `sessionCapabilities.resume`), auth methods, and permission request shapes.

## Task 1: Core manager, store, and state

**Files:** Create `agentsession/manager.go`, `agentsession/store.go`, `agentsession/state.go`, `agentsession/process.go`, with tests.

**Interfaces:**
- `NewManager(cfg ManagerConfig) (*Manager, error)`, where the config holds the limit, store root, state file path, and driver registry.
- `(*Manager).Create(ctx, driver string, opts CreateOptions) (*Session, error)`, plus `.Get(id)`, `.List() []Info`, `.Restart(ctx, id)`, `.Kill(id)`, and `.Close()`.
- `State{Status, Busy, Pending []Pending, Model, Resumable, AgentRef, Clients []ClientInfo}` (clients are filled in by task 2).
- Store layout `<root>/<project>/<id>/meta.json` (driver, options, agent reference) and `log.jsonl`, scoped per project with a `flock`ed lock file because the home volume is shared by all sandboxes; a vibed that cannot take the lock reports the agent service unavailable and retries the lock on every new channel. Restoring on start marks sessions `exited`, drops a partial last log line, and gives each restored log a new epoch (restore also runs when the lock is taken after start), with `seq` continuing from the last complete line. No log position or client ID is stored.
- Process start with `setsid`. Shutdown is the driver's orderly path, then a process-group kill after a grace period.

- [ ] Write table-driven tests against a fake driver, using a helper process re-executed from the test binary: limits, list, kill, exited retention, restore after manager restart (partial last line dropped, a new epoch, `seq` continuing from the last complete line), a second manager on the same project store reporting the service unavailable until the first releases the lock, and state-file contents.
- [ ] Implement the manager and store.

## Task 2: Broker engine

**Files:** Create `agentsession/broker.go`, `agentsession/log.go`, `agentsession/driver.go`, with tests.

**Interfaces:**
- The `BrokerDriver` and `Frame` types from the spec.
- `(*Session).Attach(client, label string, pos Position) (*Client, Welcome, error)` with `Position{Epoch string; Since uint64}`, plus `(*Client).Send(frame json.RawMessage) error`, `.Recv() (Envelope, error)`, and `.Detach()`. The client ID and label come from the client's `hello` and appear in `state.clients`. Attaching with an ID that is still attached closes the older channel with `error: superseded`, and the new attachment receives the responses addressed to that ID.
- Multi-client rules from the spec's "Multiple clients" section: turn and settings serialization via `CheckSend`, first answer wins, cancel wins over a late answer, interrupt always allowed.
- Bounded per-client queues; overflow sends `error: lagging` and closes the client.
- The log assigns `seq`s from 1 within an epoch, drops `Superseded` lines from the file (rewritten atomically, survivors keep their `seq`), and replays from a position: every entry above `since` when the epoch matches, otherwise a full replay with `replay: full` (epoch mismatch, or `since` above the newest entry). `Coalesce` merges complete runs that lie entirely above `since`, at replay time only; the file keeps every line. A replaying client joins the live fan-out only after catching up with the log, under the log's lock.
- Client request responses are fanned out to every client with `for` naming the requester and the original ID restored.

- [ ] Test against a fake line-protocol driver: replay correctness and exactly-once delivery across reattach, including frames the agent writes while a long replay is in progress and a dropping rename during a replay; full replay on an epoch mismatch (including after `restart`) and on `since` above the newest entry; replay-time merging of complete runs above `since`, with a run that `since` falls inside, or that is still growing, sent line by line; replay from inside a dropped range delivering the superseding line; responses delivered to every client with `for`, and on replay to a client that reattached with the same ID; inbound frames logged with `dir` and `by`, before ID rewriting; first-response-wins for agent requests, with `resolved` broadcast carrying `by` and `outcome`; a late answer after an agent cancel; presence in `state.clients` on attach and detach; reattaching with the same client ID routes pending responses to the new channel and supersedes a still-attached old channel; ID rewrite round-trip for numeric and string IDs; lagging client eviction without blocking the agent; stderr capture.
- [ ] Implement.

## Task 3: Subsystem and wire protocol in vibed

**Files:** Modify `sshd/rawsession.go`, `sshd/server.go`, `cmd/vibed.go`. Create `agentsession/wire.go`, `sshd/agent.go`, and `agentclient/client.go`, with tests.

**Interfaces:**
- `sshd.Config.Agents *agentsession.Manager`.
- The `handleSessionChannel` loop accepts `subsystem` with payload `vibepit-agent` before dispatch, marks the channel dispatched (later `pty-req`, `env`, `shell`, `exec`, and `subsystem` requests are rejected), and starts `handleAgentChannel`. It rejects other subsystems as before.
- Envelope types shared by server and client in `agentsession/wire.go`.
- `agentclient.List`, `Create`, `Attach`, `Restart`, `Kill`, and `Watch`, each taking an `*ssh.Client`.
- The `watch` operation: `sessions` once, then on every create, exit, kill, and `state` change of any session in the project, plus every session's `resolved` envelopes.

- [ ] Add an in-process sshd test, modeled on `cmd/connect_test.go`: create, attach, receive frames, disconnect the SSH connection mid-stream, reconnect, attach with the same client ID and the last `(epoch, since)` position, then kill and list. A `watch` channel sees the create, the state changes, a `resolved` envelope with its `session`, the exit, and the kill. Requests after the subsystem on the same channel are rejected, and so is `attach` without a well-formed client ID.
- [ ] Wire `agentsession.NewManager` into `cmd/vibed.go` next to the PTY session manager.
- [ ] Implement.

## Task 4: claude driver

**Files:** Create `agentsession/claude/driver.go`, with tests and synthetic fixtures under `agentsession/claude/testdata/`. Source the process and protocol logic from `experiments/desktop/backend/agent.go`; the `agent_config.go` handshake moves to `@vibepit/agent-ui` in task 7.

- [ ] Test with recorded synthetic frames: classification (`can_use_tool`, `control_cancel_request`, `control_response`, `result`); `Outcome` for allow and deny; turn and config policy in `CheckSend`; reference from `system/init`; superseded `stream_event` lines; resume argv.
- [ ] Implement, including `$SHELL -c 'exec …'` command execution with validated, shell-quoted option values, and orderly shutdown by closing stdin.

## Task 5: acp driver

**Files:** Create `agentsession/acp/driver.go`, `agentsession/acp/agents.go` (the known-agent command table), with tests.

- [ ] Test the handshake (no fs or terminal capabilities); `Coalesce` of message and thought chunks; the choice between `session/new`, `session/resume`, and `session/load`; rejected client methods; generic agent-request classification, including an unknown vendor method; `Outcome` from the selected option's kind, cancellation, and a vendor request answered with a result; prompt turn start and end; cancel answering pending permissions; and the `authenticate` pass-through window.
- [ ] Implement, with a `session/load` replay replacing the log under a new epoch, `seq` starting at 1.

## Task 6: codex driver (passthrough)

**Files:** Create `agentsession/passthrough.go`, `agentsession/codex/driver.go`, `agentsession/codex/observer.go`, with tests.

- [ ] Test the splice with a fake Unix-socket WebSocket server: no lost buffered bytes at switch-over; an observer maps `thread/status/changed`, server requests, and `serverRequest/resolved` into `state`; the thread ID is recorded as the reference; spliced channels appear in `state.clients` and the observer does not; `resolved` outcomes from the following `item/completed` status; restart resumes the thread.
- [ ] Implement, using the socket path and directory permissions from the spec.

## Task 7: Desktop experiment on agentclient

**Files:** Modify `experiments/desktop/backend/*`, `experiments/desktop/frontend/src/*`.

**Gate:** do not start before the UI design gate ("UI design gate" in `docs/superpowers/specs/2026-11-15-desktop-architecture-design.md`) is complete and its findings are applied to both specs and this plan. Tasks 1 to 6 do not depend on it.

Driver UIs go into `packages/agent-ui` (`@vibepit/agent-ui`; see "Agent UI package" in `docs/superpowers/specs/2026-11-15-desktop-architecture-design.md`). The desktop keeps only the session flows and the adapter from vibed envelopes to `AgentTransport`.

- [ ] Replace the Claude process handling with `agentclient`. Move the existing Claude UI, debug view, and the `agent_config.go` settings handshake into the package's claude controller and components (debug records now come from broker frames through `onDiagnostic`). Add a session picker that lists and reattaches to existing sessions.
- [ ] Add the ACP controller and view: render `session/update` kinds, permission options, and config-option selectors. Add a generic fallback for unknown agent requests that shows the raw request and offers a reply.
- [ ] Add the diff viewer to the package ("Diff viewer" in the desktop architecture spec): the `FilePatch` model with per-driver normalization in `core/`, inline and per-turn display, and the diff in file-edit approvals.
- [ ] Add the Codex controller and view: a JSON-RPC client over the relayed passthrough WebSocket, `thread/resume` with `excludeTurns`, paginated history, item rendering, approvals, `turn/interrupt`, and per-turn model and effort.
- [ ] Back demo mode with the package's fake transports for all three drivers.

## Task 8: Status and docs

**Files:** Modify `cmd/status.go`, `AGENTS.md` (architecture section), and `docs/content/` (a reference page for agent sessions).

- [ ] Show agent sessions in `vibepit status` by reading them over SSH (`list`), since the vibed state file lives inside the sandbox.
- [ ] Document the subsystem, drivers, persistence, and the threat-model statement.
- [ ] Run `make test`, `make test-integration`, and `make test-bats`.
