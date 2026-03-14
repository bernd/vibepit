# Domain Wildcard Redesign

## Problem

The allowlist only supports `*` as a `*.domain` prefix, meaning "any subdomain
at any depth." Mid-domain wildcards like `bedrock.*.amazonaws.com` are silently
parsed as exact-match literals that never match. The `aws-bedrock` preset ships
broken entries because of this.

## Design

Replace the current prefix-only wildcard with two glob-style operators that work
in any label position:

| Operator | Meaning | Matches |
|----------|---------|---------|
| `*` | Exactly one DNS label | `bedrock.*.amazonaws.com` matches `bedrock.us-east-1.amazonaws.com` |
| `**` | One or more DNS labels | `**.amazonaws.com` matches `s3.amazonaws.com` and `s3.us-east-1.amazonaws.com` |

### Matching algorithm

Per-label comparison: a pattern label of `*` matches any single host label. A
literal pattern label matches case-insensitively. This per-label comparison is
used in all steps below.

1. Split pattern and host by `.` into label arrays.
2. If the pattern contains a `**` label, split the pattern into prefix labels
   (before `**`) and suffix labels (after `**`). The host must have at least
   `len(prefix) + len(suffix) + 1` labels. The prefix labels must match the
   start of the host and the suffix labels must match the end, both using
   per-label comparison. (This means `*` labels in the prefix/suffix are
   wildcard-matched, e.g. `*.**.example.com` matches `foo.bar.baz.example.com`.)
3. If no `**`, the pattern and host must have the same number of labels. Compare
   using per-label comparison.

### Validation rules

**Domain label grammar**: each label in the pattern must be one of:
- `*` (single-label wildcard)
- `**` (multi-label wildcard)
- A non-empty literal containing no `*` characters

Reject any label that mixes `*` with other characters (e.g. `a*`, `*foo`,
`***`, `foo**`). Reject empty labels (caused by leading/trailing dots or `..`).

**Structural rules**:
- At most one `**` per pattern. Reject patterns with two or more `**` labels.
- `**` alone (without other labels) is rejected -- too broad.
- `*` alone (without other labels) is rejected -- too broad.
- Port patterns must be an exact port number or `*` (any port). Partial port
  globs like `80*` or `8*0` are removed -- `*` means whole-unit-only, consistent
  with domain label semantics. Numeric ranges or comma lists may be added later
  as a separate concept.

### Breaking changes

Two breaking changes, shipped together as one migration:

1. `*.domain` changes from "any depth subdomain" to "exactly one subdomain
   label." Users needing multi-level matching must switch to `**.domain`.
2. Partial port globs (`80*`, `8*0`) are removed. Port must be an exact number
   or `*`. No known presets or docs use partial globs in real configs, but user
   configs may.

Hard break, no auto-migration. Invalid entries in config or presets fail startup
with a clear error pointing to the offending entry.

### Preset migration

Entries that need `**` (confirmed multi-level subdomains in practice):

| Current | New | Reason |
|---------|-----|--------|
| `*.amazonaws.com:443` | `**.amazonaws.com:443` | `s3.us-east-1.amazonaws.com` |
| `*.api.aws:443` | `**.api.aws:443` | `ec2.us-east-1.api.aws` |
| `*.sentry.io:443` | `**.sentry.io:443` | `o123.ingest.sentry.io` |
| `*.datadoghq.com:443` | `**.datadoghq.com:443` | `trace.agent.datadoghq.com` |
| `*.datadoghq.eu:443` | `**.datadoghq.eu:443` | Same pattern as .com |

Entries that stay as `*` (single-level subdomains only):

- `*.gcr.io:443` -- `us.gcr.io`, `eu.gcr.io`
- `*.googleapis.com:443` -- `storage.googleapis.com`
- `*.microsoftonline.com:443` -- `login.microsoftonline.com`
- `*.data.mcr.microsoft.com:443` -- single-level CDN subdomains
- `*.ubuntu.com:443` -- `archive.ubuntu.com`
- `*.sourceforge.net:443`, `*.packagecloud.io:443` -- single-level
- `*.modelcontextprotocol.io:443` -- single-level

Entries that become mid-domain `*` (fixing the existing bug):

| Current (broken) | New (working) |
|-------------------|---------------|
| `bedrock.*.amazonaws.com:443` | `bedrock.*.amazonaws.com:443` (now works) |
| `bedrock-runtime.*.amazonaws.com:443` | `bedrock-runtime.*.amazonaws.com:443` (now works) |

### Internal changes

**`HTTPRule` / `DNSRule` structs**: Replace `Domain string` + `Wildcard bool`
with a parsed label slice and a flag/index for `**` position.

**`domainMatches`**: New label-by-label matching function implementing the
algorithm above.

**`parseHTTPRule` / `parseDNSRule`**: Split domain into labels, detect `*` and
`**` labels.

**Validation**: Update `ValidateHTTPEntry` / `ValidateDNSEntry` to enforce the
new rules (max one `**`, no bare `*` or `**`, port exact or `*` only).

**Startup validation**: Wire validation into the startup path so that config and
preset entries are validated before allowlist construction. Currently the path
`config.Load` -> `config.Merge` -> `proxy.NewHTTPAllowlist`/`NewDNSAllowlist`
does not call validators (`config/config.go`, `proxy/server.go`). Either:
- `NewHTTPAllowlist`/`NewDNSAllowlist` validate on construction and return an
  error, or
- callers validate before construction.
Invalid entries must fail startup hard with a clear error.

**Tests**: Update all existing wildcard tests to use new semantics. Required
test cases:
- Mid-domain `*` matching (single label).
- `**` in various positions (prefix, mid, suffix-adjacent).
- `*` and `**` combined in one pattern.
- Rejection of legacy partial port globs (`80*`, `8*0`, `*80`).
- Rejection of invalid domain labels (`a*`, `*foo`, `***`, empty labels).
- Startup-path validation: config/preset entries with invalid patterns cause
  startup failure (not just runtime `allow-http`/`allow-dns` API validation).

**Port matching**: Remove `portGlobMatch`. Replace with exact match or `*` for
any port. Simplifies validation to: port must be all digits or `*`.

### Documentation updates

The following docs reference the current `*.` wildcard semantics and must be
updated to describe `*` (single-label) and `**` (multi-label):

- `docs/content/reference/cli.md` -- CLI flag descriptions for `-a` / allowlist
- `docs/content/reference/presets.md` -- preset reference with wildcard entries
- `docs/content/how-to/allowlist-and-monitor.md` -- allowlist how-to guide
- `docs/content/how-to/configure-presets.md` -- preset configuration examples
- `docs/content/how-to/troubleshooting.md` -- troubleshooting guidance
- `docs/content/explanations/security-model.md` -- security model explanation
