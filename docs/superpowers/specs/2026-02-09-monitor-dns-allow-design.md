# Monitor DNS Allow Flow Design

## Status

Proposed -- 2026-02-09

## Problem

The monitor `a` / `A` allow action currently routes every blocked log entry
through the HTTP allow path (`POST /allow` + `allow-http` persistence), even
when the blocked entry came from DNS.

With strict HTTP entry validation, DNS-style domains like `example.com` (no
port) are now rejected by the HTTP allow endpoint. This reveals a product gap:
the monitor has no DNS-specific allow path.

## Goals

- Let monitor add DNS-source blocked entries to runtime `allow-dns`.
- Keep monitor key UX unchanged (`a` temporary allow, `A` allow + save).
- Persist DNS-source entries to project `allow-dns` on `A`.
- Keep strict, type-specific validation (`allow-http` vs `allow-dns`).

## Non-Goals

- No new top-level user command for DNS adds in this change.
- No config schema change.
- No auto-adding both HTTP and DNS from one action.

## Final Decisions

- DNS log entries map to DNS allow only.
- Add dedicated control API endpoint: `POST /allow-dns`.
- `A` on DNS entries persists to project `allow-dns`.

## Current Flow

1. Monitor `a` / `A` action calls `ControlClient.Allow(...)`.
2. Client sends `POST /allow`.
3. Proxy control API updates only `HTTPAllowlist`.
4. `A` path writes to project `allow-http`.

This flow is correct for proxy-source entries but wrong for DNS-source entries.

## Target Flow

### Source-aware monitor routing

- If selected blocked log entry has `SourceProxy`:
  - Runtime: `POST /allow`
  - Save (`A`): append to `allow-http`
- If selected blocked log entry has `SourceDNS`:
  - Runtime: `POST /allow-dns`
  - Save (`A`): append to `allow-dns`

### Runtime API

Add `POST /allow-dns` to control API with request body:

```json
{"entries": ["example.com", "*.example.com"]}
```

Response shape mirrors existing allow endpoint:

```json
{"added": ["example.com", "*.example.com"]}
```

## API and Type Changes

### `proxy/api.go`

- Extend `ControlAPI` to hold both:
  - `*HTTPAllowlist`
  - `*DNSAllowlist`
- Register `POST /allow-dns`.
- Split handlers for clarity:
  - `handleAllowHTTP` for `POST /allow`
  - `handleAllowDNS` for `POST /allow-dns`

### `proxy/server.go`

- Construct both allowlists (already done).
- Pass both to `NewControlAPI(...)`.

### `cmd/control.go`

- Keep `Allow(entries []string)` for HTTP.
- Add `AllowDNS(entries []string)` for DNS.

## Validation Rules

Keep strict boundary validation and make it type-specific:

- `POST /allow` uses `ValidateHTTPEntries` (`domain:port-pattern`).
- `POST /allow-dns` uses new `ValidateDNSEntries` (domain-only patterns).

DNS validation baseline:

- Allowed: `example.com`, `*.example.com`
- Rejected: `example.com:443`, empty strings, wildcard without suffix (`*.`),
  whitespace-containing entries

## Persistence Changes

### `config/setup.go`

Add `AppendAllowDNS(projectConfigPath string, entries []string) error` with the
same behavior pattern as `AppendAllowHTTP`:

- load + dedupe
- preserve comments/formatting
- append to existing `allow-dns` section, or
- convert commented `# allow-dns:` template, or
- create `allow-dns` section if missing

### Monitor save path

In monitor `A` action:

- Proxy source -> `AppendAllowHTTP`
- DNS source -> `AppendAllowDNS`

## UX Behavior

No key changes:

- `a`: temporary allow for current session
- `A`: allow and save

Status rendering stays the same (`a`/`A` markers). The only change is source-
aware backend/persistence routing.

## Error Handling

- Validation errors from control API return `400` with explicit messages.
- Monitor continues to surface errors through existing `allowResultMsg` path.
- Invalid entry does not mutate runtime allowlists or persisted config.

## Testing Plan

### Proxy API tests (`proxy/api_test.go`)

- `POST /allow-dns` adds entries to `DNSAllowlist`.
- malformed DNS entries return `400`.
- ensure DNS entry with port is rejected.

### Control client tests (`cmd/control_test.go`)

- `AllowDNS` success path.
- `AllowDNS` malformed entry returns error.

### Monitor tests (`cmd/monitor_ui_test.go`)

- DNS-source blocked entry routes through DNS allow path.
- proxy-source blocked entry continues HTTP path.
- `A` on DNS source persists via `AppendAllowDNS`.

### Config tests (`config/config_test.go`)

Add `TestAppendAllowDNS` mirroring `TestAppendAllowHTTP`:

- append to existing section
- create from commented template
- dedupe

## Rollout Notes

- Backward compatible for existing config files.
- Existing monitor workflow keeps same keys and status markers.
- Behavior becomes principle-of-least-privilege for DNS events.

## Verification

Run before completion:

```sh
go test ./proxy -run ControlAPI -v
go test ./cmd -run ControlClient -v
go test ./config -run AppendAllow -v
go test ./...
```
