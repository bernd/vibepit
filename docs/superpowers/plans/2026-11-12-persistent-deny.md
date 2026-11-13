# Persistent Deny Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Enforce persistent HTTP and DNS denies from configuration, CLI commands, and the `N` key.

**Architecture:** Share an enforced `DenyRules` matcher across HTTP, DNS, control API, and holds, separate from session-only `DenySet`. Host-side helpers apply runtime rules then preserve YAML while saving to the session project's config. Both CLI commands and TUI actions use these helpers.

**Tech Stack:** Go, urfave/cli/v3, Bubble Tea v2, existing YAML and testify packages.

**Spec:** `docs/superpowers/specs/2026-11-12-persistent-deny-design.md` (approved).

## Global Constraints

- Denies override all matching allows, including presets and special host allowances.
- Keep `n` and its existing `/deny` API behavior unchanged.
- HTTP and DNS lists remain independent; reuse allow-rule wildcard syntax.
- Saves from CLI and `N` target `.vibepit/network.yaml`, not global config.
- No interactive removal or watcher; manual edits require restarting the session.
- Existing established tunnels are not terminated; denies govern new requests.
- Run `make test` and `make test-integration`; container runtime checks are optional in this nested sandbox.
- Git author identity is currently unset: keep changes locally rather than inventing an identity for commits.

## Review Focus

- A wildcard deny added while holds open must settle every matching hold and avoid a new missed hold (task 2).
- A prior allowed host must immediately appear denied without generating another request (task 3).
- A missing config file must be created, while malformed YAML must remain untouched (task 1).
- An older proxy returning 404 must not turn `N` into a silent success (tasks 2 and 3).
- Save failures must preserve the effective runtime deny and keep the error visible in the prompt (task 3).

## Task 1: Persistent rule representation and config

**Files:** Create `proxy/deny_rules.go`, `proxy/deny_rules_test.go`, `config/deny_test.go`; modify `config/config.go`, `config/setup.go`, `proxy/server.go`.

**Interfaces:**
- `proxy.NewDenyRules(httpEntries, dnsEntries []string) (*DenyRules, error)`.
- `(*DenyRules).AddHTTP(entries []string) error`, `.AddDNS(entries []string) error`, `.Denied(t Target) bool`.
- `config.AppendDenyHTTP(path string, entries []string) error`, `AppendDenyDNS(path string, entries []string) error`.
- `DenyHTTP []string` and `DenyDNS []string` on global, project, merged, and proxy config, tagged `deny-http` / `deny-dns`.

- [x] Write table-driven `TestDenyRules` for exact domains, `*.example.com`, `**.example.com`, port-specific and `*` ports, case-insensitivity, and HTTP/DNS independence. Assert `Denied(Target{Source: SourceProxy, Host: "a.example.com", Port: "443"})` is true for `*.example.com:443` and false for port 80.
- [x] Write `TestDenyConfig` for global/project merge and deduplication, bad entries producing section-specific errors, and merged JSON decoding into `ProxyConfig` with both lists intact.
- [x] Write `TestAppendDeny` for new directories/files, repeated saves, both sections, preservation of existing comments/keys, and malformed YAML returning an error with unchanged bytes.
- [x] Run `go test ./proxy ./config -run 'TestDenyRules|TestDenyConfig|TestAppendDeny'`; confirm failures identify missing feature interfaces.
- [x] Implement `DenyRules` using the existing HTTP/DNS matcher types, including full-batch validation before mutation. Keep nil-rule checks safe for existing constructors/tests.
- [x] Add config fields, validation, merge, and JSON forwarding. Implement deny append helpers using the existing YAML editing functions, creating absent directories/files with private permissions. Avoid changing unrelated allow behavior.
- [x] Run the targeted tests, then `go test ./config ./proxy`; require all tests pass.

## Task 2: Enforce denies and expose runtime operations

**Files:** Modify `proxy/http.go`, `proxy/dns.go`, `proxy/server.go`, `proxy/api.go`, `proxy/hold.go`, `proxy/log.go`, and their `_test.go` files; extend root `integration_test.go` using its existing fixtures and build-tag convention.

**Interfaces:**
- `SetDenyRules(rules *DenyRules)` on `HTTPProxy`, `DNSServer`, `ControlAPI`, and `Holds`, configured before serving requests.
- `(*Holds).EndDenied()` settles active holds matching enforced rules.
- `BlockDenied BlockCode = "denied"`, with reason `"domain in denylist"`.
- `HostStats.Denied bool` tagged `denied,omitempty` reports live enforced denial independently of historical last action.
- `POST /deny-http` and `/deny-dns`: request `{"entries":[...]}`, response `{"added":[...]}`, 400 for malformed/empty/invalid batches.
- `/check` reports `allowed:false, denied:true` for enforced denies; `/hosts` sets live `Denied` and excludes denied targets from `Allowlisted`.

- [x] Write HTTP/DNS tests proving denies beat matching allows and `host.vibepit` handling before upstream I/O. Cover plain HTTP and CONNECT rejection, DNS rejection, and `BlockDenied` logs.
- [x] Write API tests for multiple entries, invalid batches with no partial mutation, wildcard updates, `/check`, and a previously allowed host becoming `Denied` without another log entry.
- [x] Write hold tests where a wildcard deny ends two matching holds but preserves an unrelated hold. Use synchronization barriers to test a deny between initial filtering and hold registration and a concurrent allow release; verify no denied request forwards or remains held.
- [x] Run `go test ./proxy -run 'Deny|Denied'`; confirm the new behavior tests fail before implementing enforcement.
- [x] Initialize shared rules from proxy config in `Server.Run`, set them on all consumers, and check them before allows or special-host paths. Preserve constructor compatibility and the separate session-only `DenySet`.
- [x] Add API routes using the existing entries payload. Publish rules before calling `EndDenied`. Make hold admission check enforced rules before allows; ensure HTTP rechecks filtering after a deny/allow race so final logs use the enforced deny reason.
- [x] Exclude enforced denies from allow-based hold release; add live denied host metadata and effective `/check` reporting. Keep historical action/count data intact.
- [x] Verify `AllowFixes` rejects the new code so prompts skip denied logs. Add a regression assertion on `LogEntry` prompt eligibility.
- [x] Add an integration scenario that saves config, reloads a new proxy, and verifies HTTP/DNS deny behavior persists. Include runtime wildcard deny settlement with event subscription.
- [x] Run `go test ./proxy` and `make test-integration`; require passing output.

## Task 3: CLI commands and prompt/monitor actions

**Files:** Create `cmd/deny.go`, `cmd/deny_test.go`, `cmd/monitor_deny.go`; modify `cmd/control.go`, `cmd/root.go`, `cmd/approve_ui.go`, `cmd/monitor_ui.go`, `cmd/monitor_allow.go`, `cmd/monitor_hosts.go`, and their tests.

**Interfaces:**
- `(*ControlClient).DenyHTTP(entries []string) ([]string, error)` and `.DenyDNS(entries []string) ([]string, error)` call the new endpoints without 404 suppression.
- `DenyHTTPCommand() *cli.Command` and `DenyDNSCommand() *cli.Command` register root subcommands.
- `denyEntries(client *ControlClient, session *SessionInfo, source proxy.Source, entries []string, save bool) (applied bool, err error)` applies runtime rules then saves; a persistence failure returns `true` and an explicit partial-success error.
- Keep lowercase deny handling intact; new uppercase actions carry source screen, target, applied state, and error in asynchronous result messages.

- [x] Write command tests for registration/help, no arguments, invalid input before session discovery, multiple entries, session selection, default save, `--no-save`, and runtime/save failures. Follow existing command test fixtures for session discovery.
- [x] Write client/helper tests with an HTTP test server: 404 must return an error without saving; successful runtime add followed by unwritable or malformed config must return `applied:true` and preserve the rule in the server.
- [x] Write prompt tests for `N`, busy/display guards, success removal, visible partial-save errors surviving subsequent ticks/check results, and unchanged `n` behavior.
- [x] Write monitor tests for `N` in hosts/logs tabs, valid allowed and expired-held targets, no selection, invalid targets, stale results from another screen, partial failures, and updated footer hints. Verify a live deny prevents misleading allow actions/status after polling, while old log entries retain their historical action.
- [x] Run `go test ./cmd -run 'Deny|Denied'`; confirm failures before implementation.
- [x] Implement client methods and shared persistence helper. Mirror existing allow command argument/flag conventions and require a running session. Report applied entries and saved path; `--no-save` still installs enforced rules.
- [x] Wire `N` into the prompt using the existing captured entry and decision result flow. Show `N deny+save`; keep partial-failure errors visible.
- [x] Add monitor deny selection/actions in `monitor_deny.go`. Update the selected host's live denial immediately after runtime success, invalidate older in-flight host polls using the existing mutation-generation mechanism, and respect live `Denied` in status and allow eligibility. Do not rewrite historical log actions or count synthetic requests.
- [x] Update both tabs' footer/inline hints and existing expectation tests. Run `go test ./cmd`; require all tests pass.

## Task 4: Documentation and complete verification

**Files:** Modify `docs/content/how-to/allowlist-and-monitor.md`, `docs/content/how-to/configure-presets.md`, `docs/content/reference/cli.md`, and `AGENTS.md` command reference.

- [x] Document config keys, global/project merge, deny precedence, independent DNS/HTTP scope, wildcard examples, and removal by editing/restarting.
- [x] Document `deny-http`, `deny-dns`, `--session`, `--no-save`, and `N` versus `n`; distinguish runtime-only enforced denial from prompt suppression.
- [x] Run `gofmt` on changed Go files and `git diff --check`.
- [x] Run `make test` and `make test-integration`; inspect exit codes and output before claiming completion. Run targeted `go test -race ./proxy ./cmd ./config` for the new shared runtime mutations if the environment supports the race detector.
- [x] Review the full diff against the approved spec, focusing on deny precedence, concurrency, live UI state, missing files, and partial failure semantics. Address findings and rerun affected checks.
- [x] Report implemented behavior and actual verification results, including any environment limitation. Leave all work local while Git identity is unset.
