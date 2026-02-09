# Simplified Allowlist Design

## Status

Proposed — 2026-02-08

## Problem

The current allowlist has three config knobs that overlap in confusing ways:

- `allow` (domain list) — grants both DNS resolution and HTTP/HTTPS proxy access
- `dns-only` (domain list) — grants DNS resolution only
- `allow-http` (boolean) — global toggle that unlocks plain HTTP for all allowed domains

The implicit DNS grant from `allow` entries is unnecessary because HTTP proxy
clients (curl, etc.) don't resolve DNS locally — the proxy resolves hostnames
itself. The boolean `allow-http` is too coarse — it's all-or-nothing across
every domain.

## Design

Replace all three knobs with two independent lists:

```yaml
allow-http:
  - "github.com:443"
  - "*.npmjs.org:443"
  - "localhost:*"
  - "dev.example.com:80*"

allow-dns:
  - "registry.npmjs.org"
  - "*.googleapis.com"
```

### `allow-http`

Controls what the filtering HTTP proxy will forward. Each entry is
`domain:port-pattern` — the port is mandatory.

**Domain matching**:
- Exact domain (`github.com`) matches only the apex domain.
- Wildcard domain (`*.example.com`) matches subdomains only, not the apex.

**Port matching** uses string glob patterns where `*` matches any sequence of
characters:
- `443` — exact port match
- `*` — any port
- `80*` — ports starting with "80" (80, 800, 8000, 8080, ...)
- `*80` — ports ending with "80" (80, 180, 8080, ...)
- `8*0` — ports starting with "8" and ending with "0" (80, 800, 8000, 8010, ...)

Plain HTTP is no longer a global toggle. A rule like `example.com:80` allows
plain HTTP to that specific domain. A rule like `example.com:*` allows both
HTTP and HTTPS.

**Semantics are purely additive**: if any rule matches the host:port pair,
access is granted. No specificity tiers, no shadowing, no deny rules.

### `allow-dns`

Controls what the filtering DNS server will resolve. Each entry is a bare
domain — no ports (DNS operates before a port is known).

Domain matching is the same as `allow-http` (exact matches apex only, `*.`
matches subdomains only).

### Independence

The two lists are fully independent:

- `allow-http` entries do **not** implicitly grant DNS resolution. When a
  client uses the HTTP proxy, the proxy resolves the hostname itself — the
  client never makes a DNS query.
- `allow-dns` entries do **not** grant proxy access. They only allow the
  filtering DNS server to answer queries for those domains.

### Presets

No changes to `presets.yaml`. Entries already use `domain:443` format and
expand directly into `allow-http` entries.

## Implementation

### New types in `proxy/allowlist.go`

Replace the single `Allowlist` type with two purpose-built types:

**`HTTPAllowlist`** — stores rules with a domain pattern and a port glob.

```go
type HTTPRule struct {
    Domain   string // lowercase domain without wildcard prefix
    Port     string // glob pattern (e.g. "443", "*", "80*")
    Wildcard bool   // true for *.domain entries
}

type HTTPAllowlist struct {
    rules atomic.Pointer[[]HTTPRule]
}

func (al *HTTPAllowlist) Allows(host, port string) bool
func (al *HTTPAllowlist) Add(entries []string)
```

`Allows` iterates all rules. If any rule's domain pattern matches the host
**and** the port glob matches the port, return true.

**`DNSAllowlist`** — stores domain-only rules.

```go
type DNSRule struct {
    Domain   string
    Wildcard bool
}

type DNSAllowlist struct {
    rules atomic.Pointer[[]DNSRule]
}

func (al *DNSAllowlist) Allows(domain string) bool
```

**`portGlobMatch(pattern, port string) bool`** — simple string glob where `*`
matches any sequence of characters. No `?` or character classes.

### Changes by file

| File | Change |
|---|---|
| `proxy/allowlist.go` | Replace `Allowlist` with `HTTPAllowlist` + `DNSAllowlist`, add `portGlobMatch` |
| `proxy/http.go` | Use `HTTPAllowlist`, remove `allowHTTP bool` parameter |
| `proxy/dns.go` | Use `DNSAllowlist`, accept single list instead of two |
| `proxy/server.go` | Update `ProxyConfig` fields (`Allow`/`DNSOnly`/`AllowHTTP` -> `AllowHTTP`/`AllowDNS`), update wiring |
| `proxy/api.go` | Update `ControlAPI` to hold `*HTTPAllowlist` instead of `*Allowlist` |
| `config/config.go` | Rename struct fields: `Allow`+`DNSOnly`+`AllowHTTP(bool)` -> `AllowHTTP`+`AllowDNS` |
| `config/setup.go` | Update config generation (section names, examples), rename `AppendAllow` -> `AppendAllowHTTP` |
| `cmd/allow.go` | Update usage text and function calls |
| All `*_test.go` | Update to new types and field names |

### Config structs

```go
// config/config.go
type GlobalConfig struct {
    AllowHTTP []string `koanf:"allow-http"`
    AllowDNS  []string `koanf:"allow-dns"`
    BlockCIDR []string `koanf:"block-cidr"`
}

type ProjectConfig struct {
    Presets        []string `koanf:"presets"`
    AllowHTTP      []string `koanf:"allow-http"`
    AllowDNS       []string `koanf:"allow-dns"`
    AllowHostPorts []int    `koanf:"allow-host-ports"`
}

type MergedConfig struct {
    AllowHTTP      []string `json:"allow-http"`
    AllowDNS       []string `json:"allow-dns"`
    BlockCIDR      []string `json:"block-cidr"`
    AllowHostPorts []int    `json:"allow-host-ports"`
    // ... remaining fields unchanged
}
```

### host.vibepit handling

The `host.vibepit` logic in `proxy/http.go` currently uses `AllowsPort` (which
requires port-specific rules). Under the new model, `HTTPAllowlist.Allows`
handles this naturally — a rule like `host.vibepit:8080` matches only that
port, while `host.vibepit:*` matches any port. No special method needed.

## Future

`allow-tcp` can be added later for raw TCP proxying, using the same
`domain:port-pattern` format as `allow-http`.
