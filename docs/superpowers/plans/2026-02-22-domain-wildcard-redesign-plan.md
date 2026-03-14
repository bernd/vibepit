# Domain Wildcard Redesign Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace prefix-only `*.domain` wildcards with glob-style `*` (one label) and `**` (one-or-more labels) operators, simplify port matching to exact-or-`*`, and wire validation into startup.

**Architecture:** The change is confined to `proxy/allowlist.go` (matching + validation + parsing), `proxy/presets.yaml` (migrate entries), `config/config.go` (startup validation), `proxy/server.go` (startup validation), and six documentation files. Rule structs change from `Domain string + Wildcard bool` to a label slice with an optional `**` index. Port matching drops `portGlobMatch` in favor of exact-or-`*` comparison.

**Tech Stack:** Go, testify, urfave/cli/v3, MkDocs

**Design doc:** `docs/plans/2026-02-21-domain-wildcard-redesign-design.md`

---

### Task 1: Validation — new domain label grammar and port simplification

Rewrite `ValidateHTTPEntry` and `ValidateDNSEntry` to enforce the new rules. This is done first because later tasks depend on correct validation.

**Files:**
- Modify: `proxy/allowlist.go:182-255` (validation functions)
- Test: `proxy/allowlist_test.go:187-247` (validation tests)

**Step 1: Write failing tests for new validation rules**

Replace `TestValidateHTTPEntry` and `TestValidateDNSEntry` with new test tables. Add these to `proxy/allowlist_test.go`, replacing the existing test functions at lines 187-247:

```go
func TestValidateHTTPEntry(t *testing.T) {
	tests := []struct {
		name  string
		entry string
		valid bool
	}{
		// Valid patterns
		{"exact domain exact port", "github.com:443", true},
		{"single-label wildcard prefix", "*.example.com:443", true},
		{"multi-label wildcard prefix", "**.example.com:443", true},
		{"mid-domain single wildcard", "bedrock.*.amazonaws.com:443", true},
		{"mid-domain multi wildcard", "bedrock.**.amazonaws.com:443", true},
		{"wildcard port", "github.com:*", true},
		{"single wildcard with wildcard port", "*.example.com:*", true},
		{"multi wildcard with wildcard port", "**.example.com:*", true},
		{"combined * and **", "*.**.example.com:443", true},

		// Invalid: port patterns
		{"partial port glob trailing", "github.com:80*", false},
		{"partial port glob leading", "github.com:*80", false},
		{"partial port glob infix", "github.com:8*0", false},
		{"non-digit port", "github.com:44a", false},
		{"empty port", "github.com:", false},
		{"missing port", "github.com", false},

		// Invalid: domain label grammar
		{"mixed wildcard label", "a*.example.com:443", false},
		{"mixed wildcard label suffix", "*foo.example.com:443", false},
		{"triple star label", "***.example.com:443", false},
		{"mixed double star label", "foo**.example.com:443", false},
		{"empty label leading dot", ".example.com:443", false},
		{"empty label double dot", "foo..example.com:443", false},
		{"empty label trailing dot", "example.com.:443", false},

		// Invalid: structural rules
		{"bare single wildcard", "*:443", false},
		{"bare double wildcard", "**:443", false},
		{"two double wildcards", "**.**.example.com:443", false},
		{"empty domain", ":443", false},
		{"domain contains colon", "a:b:443", false},
		{"space in domain", "git hub.com:443", false},
		{"space in port", "github.com:44 3", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateHTTPEntry(tt.entry)
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestValidateDNSEntry(t *testing.T) {
	tests := []struct {
		name  string
		entry string
		valid bool
	}{
		// Valid patterns
		{"exact domain", "github.com", true},
		{"single-label wildcard", "*.example.com", true},
		{"multi-label wildcard", "**.example.com", true},
		{"mid-domain wildcard", "bedrock.*.amazonaws.com", true},
		{"combined * and **", "*.**.example.com", true},

		// Invalid patterns
		{"empty string", "", false},
		{"entry with port", "github.com:443", false},
		{"space in domain", "git hub.com", false},
		{"bare single wildcard", "*", false},
		{"bare double wildcard", "**", false},
		{"two double wildcards", "**.**.example.com", false},
		{"mixed label", "a*.example.com", false},
		{"empty label", ".example.com", false},
		{"empty label double dot", "foo..example.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDNSEntry(tt.entry)
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./proxy/ -run 'TestValidateHTTPEntry|TestValidateDNSEntry' -v`
Expected: Multiple FAILs — new valid patterns rejected, old invalid patterns accepted.

**Step 3: Implement new validation**

Replace validation functions in `proxy/allowlist.go:182-255`. Add a shared `validateDomainPattern` helper:

```go
// validateDomainPattern validates a domain pattern string.
// Each label must be "*", "**", or a non-empty literal with no "*".
// At most one "**" label is allowed, and bare "*" or "**" (single label) is rejected.
func validateDomainPattern(domain string) error {
	if domain == "" {
		return fmt.Errorf("domain must not be empty")
	}

	labels := strings.Split(domain, ".")
	doubleStarCount := 0
	for _, label := range labels {
		if label == "" {
			return fmt.Errorf("domain must not contain empty labels")
		}
		if label == "**" {
			doubleStarCount++
			continue
		}
		if label == "*" {
			continue
		}
		if strings.Contains(label, "*") {
			return fmt.Errorf("label %q must not mix '*' with other characters", label)
		}
	}
	if doubleStarCount > 1 {
		return fmt.Errorf("domain must not contain more than one '**' label")
	}
	if len(labels) == 1 && (labels[0] == "*" || labels[0] == "**") {
		return fmt.Errorf("bare %q domain is too broad", labels[0])
	}
	return nil
}

func ValidateHTTPEntry(entry string) error {
	if entry == "" {
		return fmt.Errorf("invalid allow entry: empty string")
	}

	idx := strings.LastIndex(entry, ":")
	if idx <= 0 || idx == len(entry)-1 {
		return fmt.Errorf("invalid allow entry %q: expected domain:port", entry)
	}

	domain := entry[:idx]
	port := entry[idx+1:]

	if strings.Contains(domain, ":") {
		return fmt.Errorf("invalid allow entry %q: domain must not contain ':'", entry)
	}
	if strings.Contains(domain, " ") || strings.Contains(port, " ") {
		return fmt.Errorf("invalid allow entry %q: spaces are not allowed", entry)
	}

	if err := validateDomainPattern(domain); err != nil {
		return fmt.Errorf("invalid allow entry %q: %w", entry, err)
	}

	// Port must be "*" or all digits.
	if port != "*" {
		for _, ch := range port {
			if ch < '0' || ch > '9' {
				return fmt.Errorf("invalid allow entry %q: port must be a number or '*'", entry)
			}
		}
	}

	return nil
}

func ValidateDNSEntry(entry string) error {
	if entry == "" {
		return fmt.Errorf("invalid allow-dns entry: empty string")
	}
	if strings.Contains(entry, ":") {
		return fmt.Errorf("invalid allow-dns entry %q: ports are not allowed", entry)
	}
	if strings.Contains(entry, " ") {
		return fmt.Errorf("invalid allow-dns entry %q: spaces are not allowed", entry)
	}
	if err := validateDomainPattern(entry); err != nil {
		return fmt.Errorf("invalid allow-dns entry %q: %w", entry, err)
	}
	return nil
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./proxy/ -run 'TestValidateHTTPEntry|TestValidateDNSEntry' -v`
Expected: All PASS.

**Step 5: Commit**

```
git add proxy/allowlist.go proxy/allowlist_test.go
git commit -m "Rewrite allowlist validation for new wildcard grammar

Domain labels must be exactly *, **, or a literal with no *.
Port must be an exact number or *. Partial port globs removed."
```

---

### Task 2: Domain matching — label-by-label with `*` and `**`

Replace `domainMatches`, `isSubdomainOf`, and the `Wildcard bool` fields with label-based matching.

**Files:**
- Modify: `proxy/allowlist.go:9-14` (HTTPRule struct)
- Modify: `proxy/allowlist.go:51-64` (parseHTTPRule)
- Modify: `proxy/allowlist.go:66-81` (Allows)
- Modify: `proxy/allowlist.go:83-87` (DNSRule struct)
- Modify: `proxy/allowlist.go:123-132` (parseDNSRule)
- Modify: `proxy/allowlist.go:134-147` (DNSAllowlist.Allows)
- Modify: `proxy/allowlist.go:149-180` (portGlobMatch, domainMatches, isSubdomainOf)
- Test: `proxy/allowlist_test.go`

**Step 1: Write failing tests for new domain matching**

Add a new `TestDomainMatches` test and a new `TestPortMatch` test (replacing `TestPortGlobMatch`). Add these to `proxy/allowlist_test.go`:

```go
func TestDomainMatches(t *testing.T) {
	tests := []struct {
		pattern string
		host    string
		want    bool
	}{
		// Exact match
		{"example.com", "example.com", true},
		{"example.com", "Example.COM", true},
		{"example.com", "other.com", false},
		{"example.com", "sub.example.com", false},

		// Single-label wildcard at prefix
		{"*.example.com", "foo.example.com", true},
		{"*.example.com", "bar.example.com", true},
		{"*.example.com", "example.com", false},
		{"*.example.com", "a.b.example.com", false},

		// Single-label wildcard mid-domain
		{"bedrock.*.amazonaws.com", "bedrock.us-east-1.amazonaws.com", true},
		{"bedrock.*.amazonaws.com", "bedrock.eu-west-2.amazonaws.com", true},
		{"bedrock.*.amazonaws.com", "bedrock.amazonaws.com", false},
		{"bedrock.*.amazonaws.com", "bedrock.a.b.amazonaws.com", false},
		{"bedrock.*.amazonaws.com", "other.us-east-1.amazonaws.com", false},

		// Multi-label wildcard at prefix
		{"**.example.com", "foo.example.com", true},
		{"**.example.com", "a.b.example.com", true},
		{"**.example.com", "a.b.c.example.com", true},
		{"**.example.com", "example.com", false},

		// Multi-label wildcard mid-domain
		{"bedrock.**.amazonaws.com", "bedrock.us-east-1.amazonaws.com", true},
		{"bedrock.**.amazonaws.com", "bedrock.a.b.amazonaws.com", true},
		{"bedrock.**.amazonaws.com", "bedrock.amazonaws.com", false},

		// Multi-label wildcard suffix-adjacent
		{"example.**", "example.com", true},
		{"example.**", "example.co.uk", true},
		{"example.**", "example.a.b.c", true},
		{"example.**", "other.com", false},
		{"example.**", "notexample.com", false},

		// Combined * and **
		{"*.**.example.com", "foo.bar.example.com", true},
		{"*.**.example.com", "foo.bar.baz.example.com", true},
		{"*.**.example.com", "foo.example.com", false},

		// Multiple single-label wildcards
		{"*.*.example.com", "a.b.example.com", true},
		{"*.*.example.com", "a.example.com", false},
		{"*.*.example.com", "a.b.c.example.com", false},

		// Edge cases
		{"", "example.com", false},
		{"example.com", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"/"+tt.host, func(t *testing.T) {
			rule := parseDomainPattern(tt.pattern)
			assert.Equal(t, tt.want, rule.matches(tt.host))
		})
	}
}

func TestPortMatch(t *testing.T) {
	tests := []struct {
		pattern string
		port    string
		want    bool
	}{
		{"443", "443", true},
		{"443", "80", false},
		{"443", "", false},
		{"*", "443", true},
		{"*", "80", true},
		{"*", "12345", true},
		{"*", "", true},
		{"", "", true},
		{"", "80", false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"/"+tt.port, func(t *testing.T) {
			assert.Equal(t, tt.want, portMatches(tt.pattern, tt.port))
		})
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./proxy/ -run 'TestDomainMatches|TestPortMatch' -v`
Expected: FAIL — `parseDomainPattern`, `rule.matches`, `portMatches` don't exist yet.

**Step 3: Implement new matching**

Replace the matching and parsing code in `proxy/allowlist.go`. Remove `portGlobMatch`, `domainMatches`, `isSubdomainOf`. Replace `HTTPRule` and `DNSRule` structs. New code:

```go
// domainPattern holds a parsed domain pattern for matching.
type domainPattern struct {
	labels      []string // split by ".", lowercased
	doubleStarIdx int    // index of "**" label, or -1 if none
}

func parseDomainPattern(pattern string) domainPattern {
	if pattern == "" {
		return domainPattern{doubleStarIdx: -1}
	}
	labels := strings.Split(strings.ToLower(pattern), ".")
	idx := -1
	for i, l := range labels {
		if l == "**" {
			idx = i
			break
		}
	}
	return domainPattern{labels: labels, doubleStarIdx: idx}
}

func (p domainPattern) matches(host string) bool {
	if len(p.labels) == 0 || host == "" {
		return false
	}
	hostLabels := strings.Split(strings.ToLower(host), ".")

	if p.doubleStarIdx >= 0 {
		prefix := p.labels[:p.doubleStarIdx]
		suffix := p.labels[p.doubleStarIdx+1:]
		minLabels := len(prefix) + len(suffix) + 1
		if len(hostLabels) < minLabels {
			return false
		}
		for i, pl := range prefix {
			if !labelMatches(pl, hostLabels[i]) {
				return false
			}
		}
		hostSuffix := hostLabels[len(hostLabels)-len(suffix):]
		for i, pl := range suffix {
			if !labelMatches(pl, hostSuffix[i]) {
				return false
			}
		}
		return true
	}

	if len(p.labels) != len(hostLabels) {
		return false
	}
	for i, pl := range p.labels {
		if !labelMatches(pl, hostLabels[i]) {
			return false
		}
	}
	return true
}

func labelMatches(pattern, label string) bool {
	if pattern == "*" {
		return true
	}
	return pattern == label
}

// portMatches checks if a port matches the pattern.
// Pattern is either "*" (any port) or an exact port string.
func portMatches(pattern, port string) bool {
	if pattern == "*" {
		return true
	}
	return pattern == port
}

// HTTPRule represents a parsed allow-http entry.
type HTTPRule struct {
	Domain domainPattern
	Port   string
}

// DNSRule represents a parsed allow-dns entry.
type DNSRule struct {
	Domain domainPattern
}
```

Update `parseHTTPRule`:

```go
func parseHTTPRule(entry string) HTTPRule {
	var r HTTPRule
	if idx := strings.LastIndex(entry, ":"); idx > 0 {
		r.Port = entry[idx+1:]
		entry = entry[:idx]
	}
	r.Domain = parseDomainPattern(entry)
	return r
}
```

Update `parseDNSRule`:

```go
func parseDNSRule(entry string) DNSRule {
	return DNSRule{Domain: parseDomainPattern(entry)}
}
```

Update `HTTPAllowlist.Allows`:

```go
func (al *HTTPAllowlist) Allows(host, port string) bool {
	if host == "" {
		return false
	}
	rules := *al.rules.Load()
	for _, r := range rules {
		if portMatches(r.Port, port) && r.Domain.matches(host) {
			return true
		}
	}
	return false
}
```

Update `DNSAllowlist.Allows`:

```go
func (al *DNSAllowlist) Allows(host string) bool {
	if host == "" {
		return false
	}
	rules := *al.rules.Load()
	for _, r := range rules {
		if r.Domain.matches(host) {
			return true
		}
	}
	return false
}
```

**Step 4: Run all tests to verify they pass**

Run: `go test ./proxy/ -v`
Expected: `TestDomainMatches`, `TestPortMatch` PASS. `TestHTTPAllowlist`, `TestDNSAllowlist`, `TestPortGlobMatch` FAIL (they use old patterns/semantics — expected, fixed in Task 3).

**Step 5: Commit**

```
git add proxy/allowlist.go proxy/allowlist_test.go
git commit -m "Implement label-by-label domain matching with * and **

* matches exactly one DNS label, ** matches one or more.
Replace portGlobMatch with exact-or-* comparison."
```

---

### Task 3: Update existing allowlist tests for new semantics

Update `TestHTTPAllowlist`, `TestHTTPAllowlistAdd`, `TestDNSAllowlist`, and remove `TestPortGlobMatch`. These tests use old wildcard patterns and port globs that no longer work.

**Files:**
- Modify: `proxy/allowlist_test.go:9-151` (TestPortGlobMatch, TestHTTPAllowlist, TestHTTPAllowlistAdd)
- Modify: `proxy/allowlist_test.go:153-185` (TestDNSAllowlist)
- Modify: `proxy/allowlist_test.go:244-247` (TestValidateDNSEntries)

**Step 1: Rewrite the tests**

Remove `TestPortGlobMatch` entirely (lines 9-64).

Replace `TestHTTPAllowlist` with updated patterns and expectations. Key changes:
- `*.example.com:*` — now single-label only (deep subdomain `a.b.example.com` no longer matches)
- `dev.local:80*` — port glob no longer valid, change to `dev.local:8080` or `dev.local:*`
- `*.cdn.example.com:443` — single-label only
- Add tests for `**` and mid-domain `*`

```go
func TestHTTPAllowlist(t *testing.T) {
	al := NewHTTPAllowlist([]string{
		"github.com:443",
		"*.example.com:*",
		"api.stripe.com:443",
		"*.cdn.example.com:443",
		"dev.local:*",
		"**.amazonaws.com:443",
		"bedrock.*.amazonaws.com:443",
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
		{"subdomain does not match exact", "api.github.com", "443", false},
		{"unrelated domain", "gitlab.com", "443", false},

		// Single-label wildcard domain with wildcard port
		{"wildcard subdomain any port", "foo.example.com", "80", true},
		{"wildcard subdomain https", "foo.example.com", "443", true},
		{"wildcard apex rejected", "example.com", "443", false},
		{"deep subdomain rejected by *", "a.b.example.com", "80", false},

		// Exact domain
		{"port match", "api.stripe.com", "443", true},
		{"port mismatch", "api.stripe.com", "80", false},

		// Single-label wildcard of cdn.example.com
		{"cdn subdomain", "img.cdn.example.com", "443", true},
		{"cdn deep subdomain rejected", "a.b.cdn.example.com", "443", false},

		// Wildcard port
		{"any port 80", "dev.local", "80", true},
		{"any port 443", "dev.local", "443", true},
		{"any port 8080", "dev.local", "8080", true},

		// Multi-label wildcard
		{"** single level", "s3.amazonaws.com", "443", true},
		{"** multi level", "s3.us-east-1.amazonaws.com", "443", true},
		{"** apex rejected", "amazonaws.com", "443", false},

		// Mid-domain single-label wildcard
		{"mid * matches", "bedrock.us-east-1.amazonaws.com", "443", true},
		{"mid * wrong prefix", "other.us-east-1.amazonaws.com", "443", false},
		{"mid * too many labels", "bedrock.a.b.amazonaws.com", "443", false},
		{"mid * too few labels", "bedrock.amazonaws.com", "443", false},

		// Edge cases
		{"empty host", "", "443", false},
		{"empty port with exact port rule", "github.com", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, al.Allows(tt.host, tt.port))
		})
	}

	t.Run("combined exact and wildcard rules", func(t *testing.T) {
		combined := NewHTTPAllowlist([]string{"example.com:443", "*.example.com:443"})
		assert.True(t, combined.Allows("example.com", "443"))
		assert.True(t, combined.Allows("api.example.com", "443"))
		assert.False(t, combined.Allows("a.b.example.com", "443"))
		assert.False(t, combined.Allows("api.example.com", "80"))
	})
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

Replace `TestDNSAllowlist`:

```go
func TestDNSAllowlist(t *testing.T) {
	al := NewDNSAllowlist([]string{
		"github.com",
		"*.example.com",
		"api.openai.com",
		"**.amazonaws.com",
	})

	tests := []struct {
		name string
		host string
		want bool
	}{
		{"exact match", "github.com", true},
		{"subdomain of exact does not match", "api.github.com", false},
		{"wildcard subdomain", "foo.example.com", true},
		{"wildcard apex rejected", "example.com", false},
		{"deep subdomain rejected by *", "a.b.example.com", false},
		{"portless rule", "api.openai.com", true},
		{"not in list", "evil.com", false},
		{"empty host", "", false},
		{"** single level", "s3.amazonaws.com", true},
		{"** multi level", "s3.us-east-1.amazonaws.com", true},
		{"** apex rejected", "amazonaws.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, al.Allows(tt.host))
		})
	}

	t.Run("combined exact and wildcard rules", func(t *testing.T) {
		combined := NewDNSAllowlist([]string{"example.com", "**.example.com"})
		assert.True(t, combined.Allows("example.com"))
		assert.True(t, combined.Allows("api.example.com"))
		assert.True(t, combined.Allows("a.b.example.com"))
	})
}
```

Update `TestValidateDNSEntries` to not use patterns that are now invalid:

```go
func TestValidateDNSEntries(t *testing.T) {
	assert.NoError(t, ValidateDNSEntries([]string{"example.com", "*.svc.local"}))
	assert.Error(t, ValidateDNSEntries([]string{"example.com:443"}))
}
```

(This one is actually unchanged — `*.svc.local` is still valid.)

**Step 2: Run all proxy tests**

Run: `go test ./proxy/ -v`
Expected: All PASS.

**Step 3: Commit**

```
git add proxy/allowlist_test.go
git commit -m "Update allowlist tests for new * and ** wildcard semantics

Remove TestPortGlobMatch, update patterns and expectations for
single-label *, multi-label **, and exact-or-* port matching."
```

---

### Task 4: Startup validation — fail hard on invalid entries

Wire validation into the startup path. The cleanest approach: make `NewHTTPAllowlist` and `NewDNSAllowlist` validate and return errors. Update callers.

**Files:**
- Modify: `proxy/allowlist.go:22-31` (NewHTTPAllowlist)
- Modify: `proxy/allowlist.go:34-49` (Add)
- Modify: `proxy/allowlist.go:95-103` (NewDNSAllowlist)
- Modify: `proxy/allowlist.go:106-121` (DNSAllowlist.Add)
- Modify: `proxy/server.go:56-58` (Server.Run)
- Modify: `config/config.go:101-118` (Config.Merge)
- Test: `proxy/allowlist_test.go` (add construction error tests)
- Modify: various test files that call NewHTTPAllowlist/NewDNSAllowlist

**Step 1: Write failing tests for constructor validation**

Add to `proxy/allowlist_test.go`:

```go
func TestNewHTTPAllowlistValidation(t *testing.T) {
	t.Run("valid entries succeed", func(t *testing.T) {
		_, err := NewHTTPAllowlist([]string{"github.com:443", "*.example.com:*"})
		assert.NoError(t, err)
	})
	t.Run("nil entries succeed", func(t *testing.T) {
		_, err := NewHTTPAllowlist(nil)
		assert.NoError(t, err)
	})
	t.Run("invalid entry returns error", func(t *testing.T) {
		_, err := NewHTTPAllowlist([]string{"github.com:443", "bad:entry:here"})
		assert.Error(t, err)
	})
	t.Run("partial port glob rejected", func(t *testing.T) {
		_, err := NewHTTPAllowlist([]string{"dev.local:80*"})
		assert.Error(t, err)
	})
}

func TestNewDNSAllowlistValidation(t *testing.T) {
	t.Run("valid entries succeed", func(t *testing.T) {
		_, err := NewDNSAllowlist([]string{"github.com", "*.example.com"})
		assert.NoError(t, err)
	})
	t.Run("nil entries succeed", func(t *testing.T) {
		_, err := NewDNSAllowlist(nil)
		assert.NoError(t, err)
	})
	t.Run("invalid entry returns error", func(t *testing.T) {
		_, err := NewDNSAllowlist([]string{"github.com:443"})
		assert.Error(t, err)
	})
}
```

Add a startup-path validation test to `config/config_test.go`:

```go
func TestMergeValidation(t *testing.T) {
	t.Run("invalid allow-http entry fails merge", func(t *testing.T) {
		cfg := &Config{
			Project: ProjectConfig{
				AllowHTTP: []string{"github.com:443", "bad:entry:here"},
			},
		}
		_, err := cfg.Merge(nil, nil)
		assert.Error(t, err)
	})
	t.Run("invalid allow-dns entry fails merge", func(t *testing.T) {
		cfg := &Config{
			Project: ProjectConfig{
				AllowDNS: []string{"github.com:443"},
			},
		}
		_, err := cfg.Merge(nil, nil)
		assert.Error(t, err)
	})
	t.Run("invalid CLI allow entry fails merge", func(t *testing.T) {
		cfg := &Config{}
		_, err := cfg.Merge([]string{"a*.example.com:443"}, nil)
		assert.Error(t, err)
	})
	t.Run("valid entries succeed", func(t *testing.T) {
		cfg := &Config{
			Project: ProjectConfig{
				AllowHTTP: []string{"github.com:443"},
				AllowDNS:  []string{"example.com"},
			},
		}
		_, err := cfg.Merge(nil, nil)
		assert.NoError(t, err)
	})
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./proxy/ -run 'TestNewHTTPAllowlistValidation|TestNewDNSAllowlistValidation' -v && go test ./config/ -run TestMergeValidation -v`
Expected: FAIL — constructors don't return errors yet, Merge doesn't validate.

**Step 3: Change constructors to validate and return errors**

Update `NewHTTPAllowlist` in `proxy/allowlist.go`:

```go
func NewHTTPAllowlist(entries []string) (*HTTPAllowlist, error) {
	if err := ValidateHTTPEntries(entries); err != nil {
		return nil, err
	}
	rules := make([]HTTPRule, 0, len(entries))
	for _, entry := range entries {
		rules = append(rules, parseHTTPRule(entry))
	}
	al := &HTTPAllowlist{}
	al.rules.Store(&rules)
	return al, nil
}
```

Update `NewDNSAllowlist`:

```go
func NewDNSAllowlist(entries []string) (*DNSAllowlist, error) {
	if err := ValidateDNSEntries(entries); err != nil {
		return nil, err
	}
	rules := make([]DNSRule, 0, len(entries))
	for _, entry := range entries {
		rules = append(rules, parseDNSRule(entry))
	}
	al := &DNSAllowlist{}
	al.rules.Store(&rules)
	return al, nil
}
```

Also update `Add` methods to validate:

```go
func (al *HTTPAllowlist) Add(entries []string) error {
	if err := ValidateHTTPEntries(entries); err != nil {
		return err
	}
	// ... rest unchanged
	return nil
}

func (al *DNSAllowlist) Add(entries []string) error {
	if err := ValidateDNSEntries(entries); err != nil {
		return err
	}
	// ... rest unchanged
	return nil
}
```

**Step 4: Fix all callers**

Update `proxy/server.go:56-58`:

```go
allowlist, err := NewHTTPAllowlist(s.config.AllowHTTP)
if err != nil {
    return fmt.Errorf("allow-http: %w", err)
}
dnsAllowlist, err := NewDNSAllowlist(s.config.AllowDNS)
if err != nil {
    return fmt.Errorf("allow-dns: %w", err)
}
```

Update `config/config.go` — `Merge` should return an error:

```go
func (c *Config) Merge(cliAllow []string, cliPresets []string) (MergedConfig, error) {
	allowHTTP := dedup(c.Global.AllowHTTP, c.Project.AllowHTTP, cliAllow)

	reg := proxy.NewPresetRegistry()
	allowHTTP = dedup(allowHTTP, reg.Expand(append(c.Project.Presets, cliPresets...)))

	if err := proxy.ValidateHTTPEntries(allowHTTP); err != nil {
		return MergedConfig{}, fmt.Errorf("allow-http: %w", err)
	}

	allowDNS := dedup(c.Global.AllowDNS, c.Project.AllowDNS)

	if err := proxy.ValidateDNSEntries(allowDNS); err != nil {
		return MergedConfig{}, fmt.Errorf("allow-dns: %w", err)
	}

	return MergedConfig{
		AllowHTTP:      allowHTTP,
		AllowDNS:       allowDNS,
		BlockCIDR:      c.Global.BlockCIDR,
		AllowHostPorts: c.Project.AllowHostPorts,
	}, nil
}
```

Update `cmd/run.go:160` caller of `Merge`:

```go
merged, err := cfg.Merge(cmd.StringSlice(allowFlag), cmd.StringSlice("preset"))
if err != nil {
    return fmt.Errorf("config: %w", err)
}
```

Update `proxy/api.go` handlers for `Add` returning errors (already validates via
API, but now `Add` returns error too).

Fix all test files that call `NewHTTPAllowlist`/`NewDNSAllowlist` — add error
handling. These are in:
- `proxy/allowlist_test.go` — update all `NewHTTPAllowlist(...)` calls
- `proxy/api_test.go:24-25`
- `proxy/http_test.go:20, 61, 81, 102, 129, 163, 185, 205`
- `proxy/dns_test.go:14, 72`
- `cmd/control_test.go` — multiple calls at lines 25, 39, 54, 87, 103, 114, 138, 164
- `cmd/monitor_ui_test.go:330-331`
- `config/config_test.go:42, 53, 74, 92` — calls to `Merge` must handle error return

**Step 5: Run full test suite**

Run: `make test`
Expected: All PASS.

**Step 6: Commit**

```
git add proxy/allowlist.go proxy/allowlist_test.go proxy/server.go proxy/api.go config/config.go cmd/run.go cmd/control_test.go cmd/monitor_ui_test.go config/config_test.go
git commit -m "Validate allowlist entries at construction and startup

NewHTTPAllowlist/NewDNSAllowlist now return errors on invalid entries.
Config.Merge validates merged entries before returning.
Invalid config/preset entries fail startup with a clear error."
```

---

### Task 5: Migrate presets.yaml

Update `proxy/presets.yaml` per the migration table in the design doc.

**Files:**
- Modify: `proxy/presets.yaml`
- Modify: `proxy/presets_test.go` (add validation test)

**Step 1: Add a preset migration test**

Add to `proxy/presets_test.go` inside `TestPresetRegistry`. This test does two
things: validates syntax of all expanded domains, and verifies that the specific
entries from the migration table actually use `**`:

```go
	t.Run("all preset domains pass validation", func(t *testing.T) {
		allNames := make([]string, 0, len(reg.All()))
		for _, p := range reg.All() {
			if len(p.Domains) > 0 {
				allNames = append(allNames, p.Name)
			}
		}
		domains := reg.Expand(allNames)
		_, err := NewHTTPAllowlist(domains)
		require.NoError(t, err, "expanded preset domains must pass validation")
	})

	t.Run("multi-level presets use double-star", func(t *testing.T) {
		mustUseDoubleStar := map[string][]string{
			"cloud":      {"**.amazonaws.com:443", "**.api.aws:443"},
			"monitoring": {"**.sentry.io:443", "**.datadoghq.com:443", "**.datadoghq.eu:443"},
		}
		for presetName, expected := range mustUseDoubleStar {
			p, ok := reg.Get(presetName)
			require.True(t, ok, "preset %q must exist", presetName)
			for _, want := range expected {
				assert.Contains(t, p.Domains, want,
					"preset %q must contain %q (not single-star variant)", presetName, want)
			}
		}
	})
```

Run to confirm the migration subtest fails (no `**` entries yet):

Run: `go test ./proxy/ -run TestPresetRegistry -v`
Expected: "all preset domains pass validation" PASS (syntax is valid),
"multi-level presets use double-star" FAIL (entries still use `*`).

**Step 2: Update entries**

Change to `**` (multi-level subdomains confirmed):
- Line 111: `*.amazonaws.com:443` → `**.amazonaws.com:443`
- Line 112: `*.api.aws:443` → `**.api.aws:443`
- Line 173: `*.sentry.io:443` → `**.sentry.io:443`
- Line 175: `*.datadoghq.com:443` → `**.datadoghq.com:443`
- Line 176: `*.datadoghq.eu:443` → `**.datadoghq.eu:443`

Keep as `*` (single-level only): all other `*.domain` entries. These are already
correct under new semantics.

Keep mid-domain `*` entries as-is (now they work):
- Line 41: `bedrock.*.amazonaws.com:443` (already correct)
- Line 42: `bedrock-runtime.*.amazonaws.com:443` (already correct)

**Step 3: Run preset tests**

Run: `go test ./proxy/ -run TestPresetRegistry -v`
Expected: All subtests PASS now that entries are migrated.

Also run: `make test`
Expected: All PASS.

**Step 4: Commit**

```
git add proxy/presets.yaml proxy/presets_test.go
git commit -m "Migrate presets to new wildcard semantics

Use ** for services with multi-level subdomains (amazonaws.com,
api.aws, sentry.io, datadoghq.com/eu). Keep * for single-level.
Mid-domain bedrock.*.amazonaws.com now works correctly.
Add test that validates all expanded preset domains."
```

---

### Task 6: Update documentation

Update all six documentation files to describe new `*` and `**` semantics and remove partial port glob references.

**Files:**
- Modify: `docs/content/reference/cli.md:106-110` (wildcard semantics sections)
- Modify: `docs/content/reference/cli.md:152-156`
- Modify: `docs/content/reference/presets.md` (preset wildcard entries)
- Modify: `docs/content/how-to/allowlist-and-monitor.md:58-80`
- Modify: `docs/content/how-to/configure-presets.md:69-85`
- Modify: `docs/content/how-to/troubleshooting.md:135-139`
- Modify: `docs/content/explanations/security-model.md:54-68`

**Step 1: Update cli.md**

Replace the `allow-http` wildcard semantics section (lines 106-110):

```markdown
### Wildcard semantics

`*` matches exactly one DNS label. `**` matches one or more labels. Both can
appear in any position but at most one `**` per pattern.

| Pattern | Matches | Does not match |
|---|---|---|
| `*.example.com:443` | `api.example.com` | `example.com`, `a.b.example.com` |
| `**.example.com:443` | `api.example.com`, `a.b.example.com` | `example.com` |
| `bedrock.*.amazonaws.com:443` | `bedrock.us-east-1.amazonaws.com` | `bedrock.a.b.amazonaws.com` |

Ports must be an exact number or `*` for any port.
```

Replace the `allow-dns` wildcard semantics section (lines 152-156) similarly.

Replace the `allow-http` examples (line 119):
```markdown
vibepit allow-http '*.example.com:443'
```
Leave as-is — still valid, just has tighter semantics now.

**Step 2: Update allowlist-and-monitor.md**

Replace wildcard domains section (lines 58-70):

```markdown
### Wildcard domains

`*` matches exactly one subdomain label. `**` matches one or more labels:

| Pattern | Matches | Does not match |
|---|---|---|
| `*.example.com:443` | `api.example.com:443` | `example.com:443`, `a.b.example.com:443` |
| `**.example.com:443` | `api.example.com:443`, `a.b.example.com:443` | `example.com:443` |
| `bedrock.*.amazonaws.com:443` | `bedrock.us-east-1.amazonaws.com:443` | `bedrock.a.b.amazonaws.com:443` |

To allow both the apex and all subdomains, add two entries:

```bash
vibepit allow-http example.com:443 "**.example.com:443"
```
```

Replace port patterns section (lines 72-80):

```markdown
### Port patterns

| Pattern | Effect |
|---|---|
| `443` | Matches port 443 only |
| `*` | Matches any port |
```

Update wildcard reference at line 97:
```markdown
Wildcard semantics are identical to HTTP entries: `*.example.com` matches
exactly one subdomain label, `**.example.com` matches one or more labels.
Neither matches the apex domain.
```

**Step 3: Update configure-presets.md**

Replace lines 72-85:

```yaml
allow-http:
  - api.example.com:443
  - "*.cdn.example.com:443"
  - "**.example.com:443"

allow-dns:
  - "*.internal.example.com"
```

Update description: remove "Port patterns support digits and `*` as a wildcard"
and replace with "Ports must be an exact number or `*` for any port."

**Step 4: Update troubleshooting.md**

Replace lines 135-139:

```markdown
2. If you need to allow all subdomains (one level), use a single-label wildcard:

    ```bash
    vibepit allow-http "*.example.com:443"
    ```

3. If the service uses multi-level subdomains, use `**`:

    ```bash
    vibepit allow-http "**.example.com:443"
    ```
```

**Step 5: Update security-model.md**

Replace line 57:
```markdown
- **Wildcard**: `*.example.com` matches exactly one subdomain label (such as `api.example.com`) but does **not** match the apex domain `example.com` or deeper subdomains like `a.b.example.com`. Use `**.example.com` to match one or more subdomain levels.
```

Replace line 68:
```markdown
- **Port patterns** accept an exact port number or `*` for any port.
```

**Step 6: Update presets.md**

Update any wildcard domain examples or tables in `docs/content/reference/presets.md`
to reflect the migrated entries (e.g. `**.amazonaws.com:443` instead of
`*.amazonaws.com:443`) and describe the `*` vs `**` semantics used in presets.

**Step 7: Commit**

```
git add docs/content/reference/cli.md docs/content/reference/presets.md docs/content/how-to/allowlist-and-monitor.md docs/content/how-to/configure-presets.md docs/content/how-to/troubleshooting.md docs/content/explanations/security-model.md
git commit -m "Update docs for new * and ** wildcard semantics

Describe single-label * and multi-label ** operators. Remove partial
port glob examples. Update all wildcard tables and examples."
```

---

### Task 7: Final verification

**Step 1: Run full test suite**

Run: `make test && make test-integration`
Expected: All PASS.

**Step 2: Verify preset tests pass**

Run: `go test ./proxy/ -run TestPresetRegistry -v`
Expected: All subtests PASS (validation and migration tests added in Task 5).
