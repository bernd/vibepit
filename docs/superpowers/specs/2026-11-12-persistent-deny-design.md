# Persistent deny rules

## Intent and scope

Persist unwanted network targets so users can press `N` in the prompt or
monitor once and avoid further prompts, including after restarting a session.
Users can also edit deny rules directly in global or project configuration.
The user confirmed that denies override matching allows, including presets,
and chose separate `deny-http` and `deny-dns` lists. Matching CLI subcommands
are also required, mirroring the existing allow commands.

## Approaches

- **Separate protocol lists (selected):** mirror existing allow rules, retaining
  HTTP port matching and independent DNS decisions. Reuse the rule parser and
  matcher; enforce denies before allows.
- **One hostname list:** simpler configuration, but denies both protocols and
  every port even when the user selected one specific request.
- Persisting today's prompt suppression alone cannot satisfy deny precedence:
  the existing `DenySet` does not enforce network filtering.

## Configuration and precedence

Both global `config.yaml` and project `.vibepit/network.yaml` accept:

```yaml
deny-http:
  - telemetry.example.com:443
  - '*.tracking.example.com:*'
deny-dns:
  - telemetry.example.com
  - '**.tracking.example.com'
```

Merge and deduplicate global and project denies. Validate entries before
starting containers, using the corresponding allow-rule syntax and wildcard
semantics. Carry both lists through the merged JSON configuration to the proxy.
Every matching deny takes precedence over any allow, CLI allow, preset, or
special `host.vibepit` allowance. HTTP denies do not themselves deny DNS, and
DNS denies govern the sandbox DNS service, not the HTTP proxy's own upstream
resolution; this mirrors the existing separation of allow rules.

## Prompt and monitor

- Lowercase `n` retains today's session-only prompt suppression behavior.
- Uppercase `N` installs an enforced runtime deny immediately, ends matching
  held requests, and saves the exact target to the project config. HTTP targets
  retain their selected port; DNS targets are bare domains.
- The prompt applies `N` to its displayed target using the existing busy and
  stale-selection guards.
- The monitor offers `N` on selected valid HTTP/DNS targets in both tabs,
  including allowed hosts and requests whose holds have expired. Lowercase
  `n` remains limited to held requests in the monitor.
- Footer hints show `N deny+save`. Success confirms the deny and save. A save
  failure explicitly reports that the runtime deny remains active but was not
  saved. A runtime failure must not be presented as success.
- Explicitly denied targets show a deny reason and cannot be made accessible
  with `a` or `A`. Host status must reflect the live deny even before another
  request arrives; old log entries remain historical records.

These UI and persistence details are proposed implementation defaults.

## CLI commands

Add root commands mirroring `allow-http` and `allow-dns`:

```bash
vibepit deny-http telemetry.example.com:443 '*.tracking.example.com:*'
vibepit deny-dns telemetry.example.com '**.tracking.example.com'
```

Both accept multiple entries, validate before discovering the session, require
a running proxy, and support the same `--session` selector. With no entries,
show subcommand help. Apply enforced runtime denies through the same API used
by `N`, then persist to the selected session's project config by default.
`--no-save` skips persistence but still enforces the deny for the lifetime of
the running proxy; it is stronger than lowercase `n` prompt suppression.

Report each applied rule and the save destination. A runtime failure returns
an error without saving; a save failure returns an error that explicitly says
the runtime deny remains active. Wildcard rules immediately release all
matching held targets as denied. Register both commands in `cmd/root.go` and
document their flags and examples alongside the allow commands.

## Enforcement and API

Maintain enforced deny rules separately from the existing session-only
`DenySet`. Share the enforced rules between HTTP, DNS, and the control API.
Reuse existing domain/port matching rather than introduce a second syntax.
Check enforced denies before allows, special host handling, or upstream I/O.
Use a distinct block code and reason so denies never enter the prompt queue or
offer ineffective allow actions.

Provide authenticated runtime operations for adding HTTP and DNS deny rules.
The host-side client owns config persistence, as it does for allow-and-save.
Use a distinct API operation so an older proxy cannot silently interpret `N`
as ordinary prompt suppression. Report unsupported operations as errors.
Keep `/check`, `/hosts`, and hold admission consistent with deny precedence.
Concurrent allow/deny decisions must never let a matching allow bypass an
enforced deny. Publish rules before settling affected holds and recheck
filtering before forwarding requests released by an allow.

## Persistence and limitations

Append without duplicates while preserving unrelated YAML keys and comments.
Create the project config directory/file if absent; malformed or unwritable
configuration produces a visible save error without undoing the runtime deny.
Manual edits take effect when the session is restarted, as with existing
startup configuration. Remove a saved deny by editing the file and restarting.
Existing established tunnels are not terminated; denies govern new requests.

No interactive deny removal, new preset format, or config file watcher are
included. Exact internal types, API route names, and screen
layout details are left to the builder. Keep Go and the existing TUI stack.

## Verification

Test config loading, merging, validation, JSON forwarding, YAML preservation,
and missing-file persistence. Test HTTP and DNS deny precedence, wildcard and
port matching, special-host precedence, immediate runtime updates, absence of
holds/prompts, and allow/deny races. Test both UI keys, selection guards,
success and partial-failure reporting, and live monitor status. Include a
save/reload scenario proving a deny survives a new proxy instance.
Test command registration, argument validation, multi-entry requests, session
selection, default persistence, `--no-save`, partial failures, and wildcard
denies settling multiple held targets.

Run `make test` and `make test-integration`. Update config reference and the
prompt/monitor and CLI documentation with precedence, key bindings, and removal.
