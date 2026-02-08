# Simplified Allowlist Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace the three overlapping allowlist config knobs (`allow`, `dns-only`, `allow-http` bool) with two independent lists (`allow-http` domain:port-glob, `allow-dns` domain-only).

**Architecture:** Two new types (`HTTPAllowlist`, `DNSAllowlist`) replace the single `Allowlist`. Port matching uses string glob patterns. Semantics are purely additive (any match = allowed). The HTTP proxy no longer needs a global `allowHTTP` bool toggle.

**Tech Stack:** Go, testify, koanf (YAML config), goproxy (HTTP proxy), miekg/dns (DNS server)

**Design doc:** `docs/plans/2026-02-08-simplified-allowlist-design.md`

---

### Task 1: `portGlobMatch` function

**Files:**
- Modify: `proxy/allowlist.go`
- Modify: `proxy/allowlist_test.go`

This is a standalone utility with no dependencies on the rest of the refactor.

**Step 1: Write the failing test**

Add to `proxy/allowlist_test.go`:

```go
func TestPortGlobMatch(t *testing.T) {
	tests := []struct {
		pattern string
		port    string
		want    bool
	}{
		// Exact match
		{"443", "443", true},
		{"443", "80", false},
		{"443", "", false},

		// Full wildcard
		{"*", "443", true},
		{"*", "80", true},
		{"*", "12345", true},
		{"*", "", true},

		// Trailing wildcard (prefix)
		{"80*", "80", true},
		{"80*", "800", true},
		{"80*", "8080", true},
		{"80*", "8000", true},
		{"80*", "443", false},
		{"80*", "180", false},

		// Leading wildcard (suffix)
		{"*80", "80", true},
		{"*80", "180", true},
		{"*80", "8080", true},
		{"*80", "443", false},
		{"*80", "801", false},

		// Infix wildcard
		{"8*0", "80", true},
		{"8*0", "800", true},
		{"8*0", "8000", true},
		{"8*0", "8010", true},
		{"8*0", "81", false},
		{"8*0", "443", false},

		// Multiple wildcards
		{"*4*", "443", true},
		{"*4*", "8443", true},
		{"*4*", "80", false},

		// Empty pattern
		{"", "", true},
		{"", "80", false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"/"+tt.port, func(t *testing.T) {
			got := portGlobMatch(tt.pattern, tt.port)
			if got != tt.want {
				t.Errorf("portGlobMatch(%q, %q) = %v, want %v", tt.pattern, tt.port, got, tt.want)
			}
		})
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./proxy/ -run TestPortGlobMatch -v`
Expected: FAIL — `portGlobMatch` is undefined.

**Step 3: Write minimal implementation**

Add to `proxy/allowlist.go`:

```go
// portGlobMatch reports whether port matches the glob pattern.
// The only special character is '*', which matches any sequence of characters.
func portGlobMatch(pattern, port string) bool {
	// Simple recursive glob with O(n*m) worst case, fine for short port strings.
	for len(pattern) > 0 {
		if pattern[0] == '*' {
			// Try matching '*' against 0..len(port) characters.
			pattern = pattern[1:]
			for i := 0; i <= len(port); i++ {
				if portGlobMatch(pattern, port[i:]) {
					return true
				}
			}
			return false
		}
		if len(port) == 0 || pattern[0] != port[0] {
			return false
		}
		pattern = pattern[1:]
		port = port[1:]
	}
	return len(port) == 0
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./proxy/ -run TestPortGlobMatch -v`
Expected: PASS

**Step 5: Commit**

```
feat(proxy): add portGlobMatch for port pattern matching
```

---

### Task 2: `HTTPAllowlist` type

**Files:**
- Modify: `proxy/allowlist.go`
- Modify: `proxy/allowlist_test.go`

Replace the HTTP-related logic from `Allowlist` with a new `HTTPAllowlist` type. Keep the old `Allowlist` type intact for now — it will be deleted in a later task.

**Step 1: Write the failing test**

Replace `TestAllowlist`, `TestAllowlistAdd`, and `TestAllowsPort` in `proxy/allowlist_test.go` with:

```go
func TestHTTPAllowlist(t *testing.T) {
	al := NewHTTPAllowlist([]string{
		"github.com:443",
		"*.example.com:*",
		"api.stripe.com:443",
		"*.cdn.example.com:443",
		"dev.local:80*",
	})

	tests := []struct {
		name string
		host string
		port string
		want bool
	}{
		// Exact domain with exact port
		{"exact match", "github.com", "443", true},
		{"exact port mismatch", "github.com", "80", false},
		{"subdomain match", "api.github.com", "443", true},
		{"deep subdomain", "raw.api.github.com", "443", true},
		{"unrelated domain", "gitlab.com", "443", false},

		// Wildcard domain with full port wildcard
		{"wildcard subdomain any port", "foo.example.com", "80", true},
		{"wildcard subdomain https", "foo.example.com", "443", true},
		{"wildcard apex rejected", "example.com", "443", false},
		{"wildcard deep subdomain", "a.b.example.com", "80", true},

		// Exact domain with port-specific
		{"port match", "api.stripe.com", "443", true},
		{"port mismatch", "api.stripe.com", "80", false},
		// exact domain rules do NOT match subdomains when port-specific
		// (exact domain = domain + subdomains for domain matching)
		{"subdomain of exact", "foo.api.stripe.com", "443", true},

		// Wildcard domain with exact port
		{"wildcard port match", "img.cdn.example.com", "443", true},
		{"wildcard port mismatch", "img.cdn.example.com", "80", false},
		{"wildcard port apex rejected", "cdn.example.com", "443", false},

		// Port glob
		{"port glob 80", "dev.local", "80", true},
		{"port glob 800", "dev.local", "800", true},
		{"port glob 8080", "dev.local", "8080", true},
		{"port glob no match", "dev.local", "443", false},

		// Edge cases
		{"empty host", "", "443", false},
		{"empty port with exact port rule", "github.com", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := al.Allows(tt.host, tt.port)
			if got != tt.want {
				t.Errorf("Allows(%q, %q) = %v, want %v", tt.host, tt.port, got, tt.want)
			}
		})
	}
}

func TestHTTPAllowlistAdd(t *testing.T) {
	al := NewHTTPAllowlist([]string{"github.com:443"})

	assert.True(t, al.Allows("github.com", "443"))
	assert.False(t, al.Allows("bun.sh", "443"))

	al.Add([]string{"bun.sh:443", "esm.sh:*"})

	assert.True(t, al.Allows("bun.sh", "443"), "added port-specific entry should match")
	assert.False(t, al.Allows("bun.sh", "80"), "port mismatch should be rejected")
	assert.True(t, al.Allows("esm.sh", "443"), "wildcard port entry should match any port")
	assert.True(t, al.Allows("esm.sh", "80"), "wildcard port entry should match any port")
	assert.True(t, al.Allows("github.com", "443"), "original entries should still work")
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./proxy/ -run 'TestHTTPAllowlist' -v`
Expected: FAIL — `NewHTTPAllowlist` is undefined.

**Step 3: Write the implementation**

Add to `proxy/allowlist.go`:

```go
// HTTPRule represents a parsed allow-http entry with a domain pattern and port glob.
type HTTPRule struct {
	Domain   string // lowercase domain without wildcard prefix
	Port     string // glob pattern (e.g. "443", "*", "80*")
	Wildcard bool   // true for *.domain entries
}

// HTTPAllowlist holds parsed HTTP allow rules. Safe for concurrent use.
type HTTPAllowlist struct {
	rules atomic.Pointer[[]HTTPRule]
}

// NewHTTPAllowlist parses allow-http entries into an HTTPAllowlist.
// Each entry must be "domain:port-pattern" (e.g. "github.com:443", "*.example.com:80*").
func NewHTTPAllowlist(entries []string) *HTTPAllowlist {
	rules := make([]HTTPRule, 0, len(entries))
	for _, entry := range entries {
		rules = append(rules, parseHTTPRule(entry))
	}
	al := &HTTPAllowlist{}
	al.rules.Store(&rules)
	return al
}

// Add parses new entries and appends them atomically.
func (al *HTTPAllowlist) Add(entries []string) {
	newRules := make([]HTTPRule, 0, len(entries))
	for _, entry := range entries {
		newRules = append(newRules, parseHTTPRule(entry))
	}

	for {
		current := al.rules.Load()
		merged := make([]HTTPRule, len(*current), len(*current)+len(newRules))
		copy(merged, *current)
		merged = append(merged, newRules...)
		if al.rules.CompareAndSwap(current, &merged) {
			return
		}
	}
}

func parseHTTPRule(entry string) HTTPRule {
	var r HTTPRule
	// Split domain:port — port is everything after the last colon.
	if idx := strings.LastIndex(entry, ":"); idx > 0 {
		r.Port = entry[idx+1:]
		entry = entry[:idx]
	}
	if strings.HasPrefix(entry, "*.") {
		r.Wildcard = true
		r.Domain = strings.ToLower(entry[2:])
	} else {
		r.Domain = strings.ToLower(entry)
	}
	return r
}

// Allows checks whether a host:port pair is permitted. Purely additive —
// if any rule matches both domain and port glob, returns true.
func (al *HTTPAllowlist) Allows(host, port string) bool {
	if host == "" {
		return false
	}
	host = strings.ToLower(host)

	rules := *al.rules.Load()
	for _, r := range rules {
		if !portGlobMatch(r.Port, port) {
			continue
		}
		if r.Wildcard {
			if isSubdomainOf(host, r.Domain) {
				return true
			}
		} else {
			if host == r.Domain || isSubdomainOf(host, r.Domain) {
				return true
			}
		}
	}
	return false
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./proxy/ -run 'TestHTTPAllowlist' -v`
Expected: PASS

**Step 5: Commit**

```
feat(proxy): add HTTPAllowlist with port glob matching
```

---

### Task 3: `DNSAllowlist` type

**Files:**
- Modify: `proxy/allowlist.go`
- Modify: `proxy/allowlist_test.go`

**Step 1: Write the failing test**

Replace `TestAllowlistDNS` in `proxy/allowlist_test.go` with:

```go
func TestDNSAllowlist(t *testing.T) {
	al := NewDNSAllowlist([]string{
		"github.com",
		"*.example.com",
		"api.openai.com",
	})

	tests := []struct {
		name string
		host string
		want bool
	}{
		{"exact match", "github.com", true},
		{"subdomain of exact", "api.github.com", true},
		{"wildcard subdomain", "foo.example.com", true},
		{"wildcard apex rejected", "example.com", false},
		{"portless rule", "api.openai.com", true},
		{"not in list", "evil.com", false},
		{"empty host", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := al.Allows(tt.host)
			if got != tt.want {
				t.Errorf("Allows(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./proxy/ -run TestDNSAllowlist -v`
Expected: FAIL — `NewDNSAllowlist` is undefined.

**Step 3: Write the implementation**

Add to `proxy/allowlist.go`:

```go
// DNSRule represents a parsed allow-dns entry with a domain pattern.
type DNSRule struct {
	Domain   string
	Wildcard bool
}

// DNSAllowlist holds parsed DNS allow rules. Safe for concurrent use.
type DNSAllowlist struct {
	rules atomic.Pointer[[]DNSRule]
}

// NewDNSAllowlist parses allow-dns entries (bare domains, no ports).
func NewDNSAllowlist(entries []string) *DNSAllowlist {
	rules := make([]DNSRule, 0, len(entries))
	for _, entry := range entries {
		rules = append(rules, parseDNSRule(entry))
	}
	al := &DNSAllowlist{}
	al.rules.Store(&rules)
	return al
}

func parseDNSRule(entry string) DNSRule {
	var r DNSRule
	if strings.HasPrefix(entry, "*.") {
		r.Wildcard = true
		r.Domain = strings.ToLower(entry[2:])
	} else {
		r.Domain = strings.ToLower(entry)
	}
	return r
}

// Allows checks whether a domain is permitted for DNS resolution.
func (al *DNSAllowlist) Allows(host string) bool {
	if host == "" {
		return false
	}
	host = strings.ToLower(host)
	rules := *al.rules.Load()
	for _, r := range rules {
		if r.Wildcard {
			if isSubdomainOf(host, r.Domain) {
				return true
			}
		} else {
			if host == r.Domain || isSubdomainOf(host, r.Domain) {
				return true
			}
		}
	}
	return false
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./proxy/ -run TestDNSAllowlist -v`
Expected: PASS

**Step 5: Commit**

```
feat(proxy): add DNSAllowlist for DNS-only domain matching
```

---

### Task 4: Update `proxy/http.go` to use `HTTPAllowlist`

**Files:**
- Modify: `proxy/http.go`
- Modify: `proxy/http_test.go`

**Step 1: Update the implementation**

In `proxy/http.go`:

1. Change the `HTTPProxy` struct field `allowlist *Allowlist` → `allowlist *HTTPAllowlist`
2. Change `NewHTTPProxy` signature: remove `allowHTTP bool` parameter, change `allowlist` type to `*HTTPAllowlist`
3. Remove the plain-HTTP-blocking `if !allowHTTP` block (lines 90-97). Plain HTTP is now controlled by whether the domain:port matches a rule.
4. In the `host.vibepit` handling (both CONNECT and plain HTTP handlers), replace `p.allowlist.AllowsPort(hostname, port)` with `p.allowlist.Allows(hostname, port)`. The `AllowsPort` method is no longer needed since all `HTTPAllowlist` entries require a port pattern — a rule like `host.vibepit:*` explicitly allows all ports, while `host.vibepit:8080` only allows that port.

The updated `NewHTTPProxy` signature:

```go
func NewHTTPProxy(allowlist *HTTPAllowlist, cidr *CIDRBlocker, log *LogBuffer) *HTTPProxy {
```

**Step 2: Update the tests**

In `proxy/http_test.go`:

1. Replace `NewAllowlist(...)` calls with `NewHTTPAllowlist(...)`. All entries must now include a port pattern.
2. Remove `allowHTTP bool` argument from `NewHTTPProxy(...)` calls.
3. The "blocks plain HTTP by default" test should change: plain HTTP is now blocked because the allowlist entry `httpbin.org:443` doesn't match port `80`. The reason message changes from "plain HTTP blocked" to "domain not in allowlist".
4. The "allows plain HTTP when allowHTTP is true" test: change the allowlist entry to include port wildcard (e.g. the `host` variable already contains `host:port`, so use that directly).
5. The "blocks disallowed domain even with allowHTTP" test: remove `true` arg, use `NewHTTPAllowlist([]string{"allowed.example.com:443"})`.
6. The "logs blocked request" test: use `NewHTTPAllowlist([]string{"httpbin.org:443"})`, remove `false` arg.
7. In `TestHTTPProxyHostVibepit`: replace `NewAllowlist` with `NewHTTPAllowlist`, remove `allowHTTP` arg. The "portless host.vibepit allowlist entry" test should be removed since `HTTPAllowlist` entries always have a port pattern. Update the "allowed via allowlist" test to use `NewHTTPAllowlist([]string{"host.vibepit:" + backendPortStr})`.

**Step 3: Run tests to verify**

Run: `go test ./proxy/ -run TestHTTPProxy -v`
Expected: PASS

**Step 4: Commit**

```
refactor(proxy): update HTTPProxy to use HTTPAllowlist
```

---

### Task 5: Update `proxy/dns.go` to use `DNSAllowlist`

**Files:**
- Modify: `proxy/dns.go`
- Modify: `proxy/dns_test.go`

**Step 1: Update the implementation**

In `proxy/dns.go`:

1. Change `DNSServer` struct: remove `dnsOnly *Allowlist`, change `allowlist *Allowlist` → `allowlist *DNSAllowlist`
2. Change `NewDNSServer` signature: accept single `*DNSAllowlist` instead of two `*Allowlist` params:
   ```go
   func NewDNSServer(allowlist *DNSAllowlist, cidr *CIDRBlocker, log *LogBuffer, upstream string) *DNSServer {
   ```
3. Update the DNS check on line 75 from:
   ```go
   if !s.allowlist.AllowsDNS(domain) && !s.dnsOnly.AllowsDNS(domain) {
   ```
   to:
   ```go
   if !s.allowlist.Allows(domain) {
   ```

**Step 2: Update the tests**

In `proxy/dns_test.go`:

1. `TestDNSServer`: merge the two allowlist entries into a single `NewDNSAllowlist`. Update `NewDNSServer` call to pass a single list. The "allows domain in dns-only list" test should use the combined list (the domain is just another entry now).
2. `TestDNSHostVibepit`: use `NewDNSAllowlist(nil)` and update the `NewDNSServer` call.

**Step 3: Run tests to verify**

Run: `go test ./proxy/ -run TestDNS -v`
Expected: PASS

**Step 4: Commit**

```
refactor(proxy): update DNSServer to use DNSAllowlist
```

---

### Task 6: Update `proxy/api.go` and `proxy/server.go`

**Files:**
- Modify: `proxy/api.go`
- Modify: `proxy/api_test.go`
- Modify: `proxy/server.go`

**Step 1: Update `proxy/api.go`**

1. Change `ControlAPI` struct: `allowlist *Allowlist` → `allowlist *HTTPAllowlist`
2. Change `NewControlAPI` signature:
   ```go
   func NewControlAPI(log *LogBuffer, config any, allowlist *HTTPAllowlist) *ControlAPI {
   ```
3. The `handleAllow` method calls `a.allowlist.Add(req.Entries)` — this already works with `HTTPAllowlist.Add`.

**Step 2: Update `proxy/server.go`**

1. Change `ProxyConfig` fields:
   ```go
   type ProxyConfig struct {
       AllowHTTP      []string `json:"allow-http"`
       AllowDNS       []string `json:"allow-dns"`
       BlockCIDR      []string `json:"block-cidr"`
       Upstream       string   `json:"upstream"`
       AllowHostPorts []int    `json:"allow-host-ports"`
       ProxyIP        string   `json:"proxy-ip"`
       HostGateway    string   `json:"host-gateway"`
       ProxyPort      int      `json:"proxy-port"`
       ControlAPIPort int      `json:"control-api-port"`
   }
   ```
2. Update `Server.Run()`:
   ```go
   func (s *Server) Run(ctx context.Context) error {
       allowlist := NewHTTPAllowlist(s.config.AllowHTTP)
       dnsAllowlist := NewDNSAllowlist(s.config.AllowDNS)
       cidr := NewCIDRBlocker(s.config.BlockCIDR)
       log := NewLogBuffer(LogBufferCapacity)

       httpProxy := NewHTTPProxy(allowlist, cidr, log)
       dnsServer := NewDNSServer(dnsAllowlist, cidr, log, s.config.Upstream)
       controlAPI := NewControlAPI(log, s.config, allowlist)
       // ... rest unchanged
   ```

**Step 3: Update `proxy/api_test.go`**

1. Replace `NewAllowlist(...)` with `NewHTTPAllowlist(...)` throughout. All entries must include a port.
2. Update the `mergedConfig` map: change `"allow"` → `"allow-http"`, `"dns-only"` → `"allow-dns"`.
3. The POST /allow test entries already have ports (`bun.sh:443`) or need wildcard ports added (`esm.sh` → `esm.sh:*`).
4. Update assertions: `allowlist.Allows("esm.sh", "80")` — with `esm.sh:*` entry, this should return true.

**Step 4: Run all proxy tests**

Run: `go test ./proxy/ -v`
Expected: PASS

**Step 5: Commit**

```
refactor(proxy): update ControlAPI and Server to new allowlist types
```

---

### Task 7: Delete old `Allowlist` type

**Files:**
- Modify: `proxy/allowlist.go`
- Modify: `proxy/allowlist_test.go`

**Step 1: Delete old code**

Remove from `proxy/allowlist.go`:
- `Rule` struct
- `Allowlist` struct
- `NewAllowlist`
- `Allowlist.Add`
- `Allowlist.Allows`
- `Allowlist.AllowsPort`
- `Allowlist.AllowsDNS`
- `parseRule`
- `isNumeric`

Keep: `isSubdomainOf`, `portGlobMatch`, `HTTPRule`, `HTTPAllowlist`, `DNSRule`, `DNSAllowlist`, and all their methods.

**Step 2: Delete old tests**

Remove from `proxy/allowlist_test.go` any remaining tests that reference `Allowlist` (as opposed to `HTTPAllowlist` / `DNSAllowlist`). These should already have been replaced in Tasks 2-3, but verify nothing references the old type.

**Step 3: Run all proxy tests**

Run: `go test ./proxy/ -v`
Expected: PASS — no remaining references to old type.

**Step 4: Commit**

```
refactor(proxy): remove old Allowlist type
```

---

### Task 8: Update `config/config.go`

**Files:**
- Modify: `config/config.go`
- Modify: `config/config_test.go`

**Step 1: Update the structs and merge logic**

In `config/config.go`:

1. Update `GlobalConfig`:
   ```go
   type GlobalConfig struct {
       AllowHTTP []string `koanf:"allow-http"`
       AllowDNS  []string `koanf:"allow-dns"`
       BlockCIDR []string `koanf:"block-cidr"`
   }
   ```

2. Update `ProjectConfig`:
   ```go
   type ProjectConfig struct {
       Presets        []string `koanf:"presets"`
       AllowHTTP      []string `koanf:"allow-http"`
       AllowDNS       []string `koanf:"allow-dns"`
       AllowHostPorts []int    `koanf:"allow-host-ports"`
   }
   ```

3. Update `MergedConfig`:
   ```go
   type MergedConfig struct {
       AllowHTTP      []string `json:"allow-http"`
       AllowDNS       []string `json:"allow-dns"`
       BlockCIDR      []string `json:"block-cidr"`
       AllowHostPorts []int    `json:"allow-host-ports"`
       ProxyIP        string   `json:"proxy-ip,omitempty"`
       HostGateway    string   `json:"host-gateway,omitempty"`
       ProxyPort      int      `json:"proxy-port,omitempty"`
       ControlAPIPort int      `json:"control-api-port,omitempty"`
   }
   ```

4. Update `Merge` method:
   ```go
   func (c *Config) Merge(cliAllow []string, cliPresets []string) MergedConfig {
       allowHTTP := dedup(c.Global.AllowHTTP, c.Project.AllowHTTP, cliAllow)

       allPresets := append(c.Project.Presets, cliPresets...)
       reg := proxy.NewPresetRegistry()
       allowHTTP = dedup(allowHTTP, reg.Expand(allPresets))

       allowDNS := dedup(c.Global.AllowDNS, c.Project.AllowDNS)

       return MergedConfig{
           AllowHTTP:      allowHTTP,
           AllowDNS:       allowDNS,
           BlockCIDR:      c.Global.BlockCIDR,
           AllowHostPorts: c.Project.AllowHostPorts,
       }
   }
   ```

**Step 2: Update the tests**

In `config/config_test.go`:

1. "merges global and project configs": update YAML content to use `allow-http:` and `allow-dns:` keys. Entries need ports:
   ```yaml
   allow-http:
     - github.com:443
   allow-dns:
     - internal.example.com
   block-cidr:
     - 203.0.113.0/24
   ```
   and project:
   ```yaml
   presets:
     - pkg-go
   allow-http:
     - api.anthropic.com:443
   ```
   Update assertions: `merged.Allow` → `merged.AllowHTTP`, `merged.DNSOnly` → `merged.AllowDNS`.

2. "CLI overrides add to merged config": update to use port entries:
   ```go
   merged := cfg.Merge([]string{"extra.com:443"}, []string{"pkg-node"})
   ```
   Update assertions: `merged.Allow` → `merged.AllowHTTP`.

3. "missing files are not errors": update `merged.Allow` → `merged.AllowHTTP`.

**Step 3: Run tests**

Run: `go test ./config/ -v`
Expected: PASS

**Step 4: Commit**

```
refactor(config): rename Allow/DNSOnly/AllowHTTP to AllowHTTP/AllowDNS
```

---

### Task 9: Update `config/setup.go`

**Files:**
- Modify: `config/setup.go`
- Modify: `config/config_test.go` (the `TestAppendAllow` tests)

**Step 1: Update setup.go**

1. In `RunReconfigure` (line 49): change `cfg.Allow, cfg.DNSOnly` → `cfg.AllowHTTP, cfg.AllowDNS`.

2. In `writeReconfiguredProjectConfig` (line 59): update parameter names from `allow []string, dnsOnly []string` to `allowHTTP []string, allowDNS []string`.

3. Update the `writeYAMLListSection` calls (lines 67-74):
   ```go
   writeYAMLListSection(&sb,
       "# Domains and ports to allow through the HTTP proxy.",
       "allow-http", allowHTTP,
       []string{"api.openai.com:443", "api.anthropic.com:443"})
   writeYAMLListSection(&sb,
       "# Domains that only need DNS resolution (no HTTP proxy).",
       "allow-dns", allowDNS,
       []string{"internal.corp.example.com"})
   ```

4. Rename `AppendAllow` → `AppendAllowHTTP`. Update all internal references from `cfg.Allow` → `cfg.AllowHTTP` and the YAML section key from `"allow:"` → `"allow-http:"` (lines 131-214):
   - `containsLine(content, "allow:")` → `containsLine(content, "allow-http:")`
   - `containsLine(content, "# allow:")` → `containsLine(content, "# allow-http:")`
   - `trimmed == "# allow:"` → `trimmed == "# allow-http:"`
   - `"#   - "` prefix stays the same
   - `sb.WriteString("allow:\n")` → `sb.WriteString("allow-http:\n")`

**Step 2: Update tests**

In `config/config_test.go`, update `TestAppendAllow`:

1. Rename to `TestAppendAllowHTTP`.
2. "adds to existing allow section": change YAML content `allow:` → `allow-http:`, entries need ports. Call `AppendAllowHTTP` instead of `AppendAllow`. Assert on `cfg.AllowHTTP`.
3. "creates allow section from commented template": change `# allow:` → `# allow-http:`, update example entries. Call `AppendAllowHTTP`.
4. "deduplicates existing entries": same pattern.

**Step 3: Run tests**

Run: `go test ./config/ -v`
Expected: PASS

**Step 4: Commit**

```
refactor(config): update setup.go for allow-http/allow-dns config keys
```

---

### Task 10: Update `cmd/allow.go` and `cmd/control_test.go`

**Files:**
- Modify: `cmd/allow.go`
- Modify: `cmd/control_test.go`

**Step 1: Update `cmd/allow.go`**

1. Update usage text: `ArgsUsage: "<domain:port-pattern>..."` (line 16).
2. Update the `config.AppendAllow` call → `config.AppendAllowHTTP` (line 53).

**Step 2: Update `cmd/control_test.go`**

1. `TestControlClient_Config` (line 96-113): update the `MergedConfig` literal:
   ```go
   merged := config.MergedConfig{
       AllowHTTP: []string{"a.com:443", "b.com:443"},
       AllowDNS:  []string{"c.com"},
       BlockCIDR: []string{"10.0.0.0/8"},
   }
   ```
   Update assertions: `cfg.Allow` → `cfg.AllowHTTP`, `cfg.DNSOnly` → `cfg.AllowDNS`, remove `cfg.AllowHTTP` bool assertion.

2. `TestControlClient_Allow` (line 115-130): use `NewHTTPAllowlist` instead of `NewAllowlist`. Update entries to include ports:
   ```go
   allowlist := proxy.NewHTTPAllowlist([]string{"existing.com:443"})
   ```
   Update the test entries and assertions.

**Step 3: Run tests**

Run: `go test ./cmd/ -v`
Expected: PASS

**Step 4: Commit**

```
refactor(cmd): update allow command and control client tests
```

---

### Task 11: Update `integration_test.go` and `cmd/run.go`

**Files:**
- Modify: `integration_test.go`
- Modify: `cmd/run.go`

**Step 1: Update `integration_test.go`**

Update the `ProxyConfig` literal (line 39-43):
```go
cfg := proxy.ProxyConfig{
    AllowHTTP: []string{"httpbin.org:443", "example.com:443"},
    AllowDNS:  []string{"dns-only.example.com"},
    Upstream:  "8.8.8.8:53",
}
```

**Step 2: Update `cmd/run.go`**

The `allowFlag` const (line 29) and its usage in `Merge` (line 158) pass CLI `--allow` values into `cliAllow`. These now need to be `allow-http` entries with ports. Update the flag:

1. Change the flag name and usage (line 52-55):
   ```go
   &cli.StringSliceFlag{
       Name:    allowFlag,
       Aliases: []string{"a"},
       Usage:   "Additional domain:port to allow (e.g. api.example.com:443)",
   },
   ```

   The `allowFlag` const value can stay as `"allow"` for the CLI flag name — it's the CLI flag, not the config key. Or rename to `"allow-http"` for consistency. Keep it as `"allow"` for brevity since it's a CLI flag.

**Step 3: Run compilation check**

Run: `go build ./...`
Expected: compiles successfully.

**Step 4: Commit**

```
refactor: update integration test and run command for new config fields
```

---

### Task 12: Final verification

**Step 1: Run all tests**

Run: `go test ./... -v`
Expected: All PASS.

**Step 2: Run vet and build**

Run: `go vet ./... && go build ./...`
Expected: clean.

**Step 3: Commit (if any fixups needed)**

```
fix: address any remaining issues from allowlist refactor
```
