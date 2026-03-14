# Apex-Only Domain Matching Design

## Status

Proposed -- 2026-02-09

## Problem

The current allowlist matcher treats exact domains as "apex + all subdomains."
For example, `chatgpt.com:443` allows both `chatgpt.com:443` and
`ab.chatgpt.com:443`.

This is broader than expected and makes it easy to over-allow traffic by
mistake.

## Goals

- Make exact domain entries apex-only.
- Keep wildcard entries (`*.`) as subdomain-only.
- Keep matching semantics consistent across `allow-http` and `allow-dns`.
- Preserve existing port glob behavior.

## Non-Goals

- No compatibility mode or version flag.
- No new domain pattern syntax.
- No change to config keys, CLI flags, or control API structure.

## Final Decisions

- Applies to both `allow-http` and `allow-dns`.
- Hard break: exact entries become apex-only immediately.
- No new syntax for "apex + subdomains."
- Built-in presets will be proactively updated with explicit `*.` entries where
  subdomains are likely required.

## Semantics

### `allow-http` (`domain:port-pattern`)

- Exact domain rule: `example.com:443` matches only `example.com:443`.
- Wildcard domain rule: `*.example.com:443` matches subdomains only.
- Wildcard does not match apex.
- Port matching remains unchanged (`443`, `*`, `80*`, `*80`, `8*0`, etc.).

To allow both apex and subdomains, users must define both rules explicitly:

```yaml
allow-http:
  - "example.com:443"
  - "*.example.com:443"
```

### `allow-dns` (`domain`)

- Exact domain rule: `example.com` matches only `example.com`.
- Wildcard domain rule: `*.example.com` matches subdomains only.
- Wildcard does not match apex.

To allow DNS for both apex and subdomains, users must define both rules:

```yaml
allow-dns:
  - "example.com"
  - "*.example.com"
```

## Implementation Plan

### 1. Matcher change in `proxy/allowlist.go`

Current behavior for non-wildcard rules:

- `host == domain || isSubdomainOf(host, domain)`

New behavior for non-wildcard rules:

- `host == domain`

`domainMatches` should become:

- `wildcard == true` -> `isSubdomainOf(host, domain)` (unchanged)
- `wildcard == false` -> exact equality only

`isSubdomainOf` and parsing code (`parseHTTPRule`, `parseDNSRule`) stay
unchanged.

### 2. Preset updates in `proxy/presets.yaml`

Because this is a hard break, preset subdomain access must become explicit.
Presets keep their existing wildcard entries and add a small curated set of new
wildcards for AI web domains where subdomain traffic is expected.

Curated additions:

- `*.chatgpt.com:443`
- `*.claude.ai:443`

### 3. Documentation updates

Update design and user-facing docs to remove language that says exact entries
implicitly include subdomains. Replace with explicit apex-only semantics and
paired-rule examples.

## Data Flow and System Impact

No wiring changes are required.

- HTTP proxy path remains `checkRequest -> HTTPAllowlist.Allows`.
- DNS server path remains `DNSAllowlist.Allows`.
- Runtime `vibepit allow` additions continue through `HTTPAllowlist.Add` and
  automatically use the new matching behavior.

Config format remains unchanged; only matching behavior changes.

## Testing Plan

### Unit tests (`proxy/allowlist_test.go`)

Update tests that currently assert implicit subdomain matching for exact rules:

- HTTP: exact-domain subdomain cases should become `false`.
- DNS: exact-domain subdomain cases should become `false`.

Add/keep tests for:

- Exact rule matches apex only.
- Wildcard rule matches subdomains only.
- Wildcard does not match apex.
- Combined exact + wildcard rules allow both apex and subdomains.
- Port glob behavior remains unchanged.

### Integration checks

- Validate preset-backed workflows still function where subdomains are expected,
  after explicit wildcard entries are added.

## Rollout and Migration

This is a behavior-breaking change by design.

- Existing configs remain syntactically valid.
- Existing exact entries no longer imply subdomain access.
- Users must add explicit wildcard rules for required subdomains.

No compatibility fallback is provided.

## Verification

Run before completion:

```sh
go test ./proxy -run 'Allowlist|DNS' -v
go test ./...
```

Optionally spot-check key preset domains known to use subdomains.
