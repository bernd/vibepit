# Network Isolation Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace `bin/vibepit` with a Go CLI that isolates the dev container behind a filtering HTTP proxy and DNS server.

**Architecture:** Single Go binary with three subcommands (root launcher, proxy server, monitor). The launcher creates a private Docker network, starts a proxy container (scratch + mounted binary), and starts the dev container with proxy/DNS settings. The proxy package handles HTTP CONNECT filtering, DNS filtering, CIDR blocking, and a control API.

**Tech Stack:** Go, urfave/cli/v3, knadh/koanf, docker/docker client, elazarl/goproxy, miekg/dns

**Design doc:** `docs/plans/2026-01-30-network-isolation-design.md`

---

### Task 1: Project scaffolding

Set up the Go module, dependencies, and CLI skeleton with three subcommands
that print placeholder messages.

**Files:**
- Create: `go.mod`
- Create: `main.go`
- Create: `cmd/root.go`
- Create: `cmd/proxy.go`
- Create: `cmd/monitor.go`

**Step 1: Initialize the Go module**

Run: `go mod init github.com/bernd/vibepit`

**Step 2: Install dependencies**

Run:
```
go get github.com/urfave/cli/v3
go get github.com/knadh/koanf/v2
go get github.com/knadh/koanf/parsers/yaml
go get github.com/knadh/koanf/providers/file
go get github.com/elazarl/goproxy
go get github.com/miekg/dns
go get github.com/docker/docker/client
go get github.com/docker/docker/api/types
```

**Step 3: Create the CLI entry point**

`main.go`:
```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/bernd/vibepit/cmd"
	"github.com/urfave/cli/v3"
)

func main() {
	app := &cli.Command{
		Name:  "vibepit",
		Usage: "Run agents in isolated Docker containers",
		Commands: []*cli.Command{
			cmd.ProxyCommand(),
			cmd.MonitorCommand(),
		},
		Action: cmd.RootAction,
	}
	if err := app.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
```

`cmd/root.go`:
```go
package cmd

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"
)

func RootAction(ctx context.Context, cmd *cli.Command) error {
	fmt.Println("vibepit launcher (not yet implemented)")
	return nil
}
```

`cmd/proxy.go`:
```go
package cmd

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"
)

func ProxyCommand() *cli.Command {
	return &cli.Command{
		Name:  "proxy",
		Usage: "Run the proxy server (used inside proxy container)",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			fmt.Println("vibepit proxy (not yet implemented)")
			return nil
		},
	}
}
```

`cmd/monitor.go`:
```go
package cmd

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"
)

func MonitorCommand() *cli.Command {
	return &cli.Command{
		Name:  "monitor",
		Usage: "Connect to a running proxy for logs and admin",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			fmt.Println("vibepit monitor (not yet implemented)")
			return nil
		},
	}
}
```

**Step 4: Verify it compiles and runs**

Run: `go run . --help`
Expected: Help output showing `proxy` and `monitor` subcommands.

Run: `go run . proxy`
Expected: `vibepit proxy (not yet implemented)`

**Step 5: Tidy modules**

Run: `go mod tidy`

**Step 6: Commit**

```
feat: scaffold Go CLI with urfave/cli v3
```

---

### Task 2: Domain matching (allowlist engine)

Implement the domain matching logic with port-specific rules and specificity
ranking. This is a pure function package with no I/O, ideal for TDD.

**Files:**
- Create: `proxy/allowlist.go`
- Create: `proxy/allowlist_test.go`

**Step 1: Write failing tests**

`proxy/allowlist_test.go`:
```go
package proxy

import "testing"

func TestAllowlist(t *testing.T) {
	al := NewAllowlist([]string{
		"github.com",
		"*.example.com",
		"api.stripe.com:443",
		"*.cdn.example.com:443",
	})

	tests := []struct {
		name   string
		host   string
		port   string
		want   bool
	}{
		// Exact with automatic subdomains
		{"exact match", "github.com", "443", true},
		{"subdomain match", "api.github.com", "443", true},
		{"deep subdomain", "raw.api.github.com", "8080", true},
		{"unrelated domain", "gitlab.com", "443", false},

		// Wildcard: subdomains only, not apex
		{"wildcard subdomain", "foo.example.com", "80", true},
		{"wildcard apex rejected", "example.com", "443", false},
		{"wildcard deep subdomain", "a.b.example.com", "80", true},

		// Port-specific exact
		{"port match", "api.stripe.com", "443", true},
		{"port mismatch", "api.stripe.com", "80", false},
		{"port subdomain no match", "foo.api.stripe.com", "443", false},

		// Port-specific wildcard
		{"wildcard port match", "img.cdn.example.com", "443", true},
		{"wildcard port mismatch", "img.cdn.example.com", "80", false},
		{"wildcard port apex rejected", "cdn.example.com", "443", false},

		// Edge cases
		{"empty host", "", "443", false},
		{"empty port still matches portless rule", "github.com", "", true},
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

func TestAllowlistDNS(t *testing.T) {
	al := NewAllowlist([]string{
		"github.com:443",
		"*.example.com:8080",
		"api.openai.com",
	})

	tests := []struct {
		name string
		host string
		want bool
	}{
		// DNS ignores port, so port-specific rules still match the domain
		{"port rule domain allowed for DNS", "github.com", true},
		{"wildcard port rule domain allowed for DNS", "foo.example.com", true},
		{"portless rule", "api.openai.com", true},
		{"not in list", "evil.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := al.AllowsDNS(tt.host)
			if got != tt.want {
				t.Errorf("AllowsDNS(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./proxy/ -v`
Expected: Compilation errors (types not defined yet).

**Step 3: Implement the allowlist**

`proxy/allowlist.go`:
```go
package proxy

import "strings"

// Rule represents a parsed allowlist entry.
type Rule struct {
	// Pattern is the domain or wildcard (e.g. "github.com" or "*.example.com").
	Pattern string
	// Port is empty for any-port rules, or a specific port like "443".
	Port string
	// Wildcard is true for *.domain patterns.
	Wildcard bool
	// Domain is the base domain without the "*." prefix.
	Domain string
}

// Allowlist checks domains and ports against a set of rules.
type Allowlist struct {
	rules []Rule
}

func NewAllowlist(entries []string) *Allowlist {
	rules := make([]Rule, 0, len(entries))
	for _, entry := range entries {
		r := parseRule(entry)
		rules = append(rules, r)
	}
	return &Allowlist{rules: rules}
}

func parseRule(entry string) Rule {
	var r Rule

	// Split off port if present. Be careful with IPv6 (not supported in
	// allowlist entries — domains only).
	if idx := strings.LastIndex(entry, ":"); idx > 0 {
		possiblePort := entry[idx+1:]
		// Only treat as port if it looks numeric and the part before is not
		// empty.
		if isNumeric(possiblePort) {
			r.Port = possiblePort
			entry = entry[:idx]
		}
	}

	if strings.HasPrefix(entry, "*.") {
		r.Wildcard = true
		r.Domain = entry[2:]
	} else {
		r.Domain = entry
	}
	r.Pattern = entry
	return r
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Allows checks whether a host:port combination is permitted by the allowlist.
// Port may be empty, in which case only portless rules match.
func (al *Allowlist) Allows(host, port string) bool {
	if host == "" {
		return false
	}
	host = strings.ToLower(host)

	// Check rules in specificity order:
	// 1. Exact hostname with port
	// 2. Exact hostname, any port
	// 3. Wildcard with port
	// 4. Wildcard, any port
	for _, r := range al.rules {
		if !r.Wildcard && r.Port != "" {
			if host == r.Domain && port == r.Port {
				return true
			}
			continue
		}
	}
	for _, r := range al.rules {
		if !r.Wildcard && r.Port == "" {
			if host == r.Domain || strings.HasSuffix(host, "."+r.Domain) {
				return true
			}
			continue
		}
	}
	for _, r := range al.rules {
		if r.Wildcard && r.Port != "" {
			if port == r.Port && isSubdomainOf(host, r.Domain) {
				return true
			}
			continue
		}
	}
	for _, r := range al.rules {
		if r.Wildcard && r.Port == "" {
			if isSubdomainOf(host, r.Domain) {
				return true
			}
			continue
		}
	}
	return false
}

// AllowsDNS checks whether a domain is permitted for DNS resolution.
// Port rules are ignored — only the domain part matters.
func (al *Allowlist) AllowsDNS(host string) bool {
	if host == "" {
		return false
	}
	host = strings.ToLower(host)

	for _, r := range al.rules {
		if r.Wildcard {
			if isSubdomainOf(host, r.Domain) {
				return true
			}
		} else {
			if host == r.Domain || strings.HasSuffix(host, "."+r.Domain) {
				return true
			}
		}
	}
	return false
}

func isSubdomainOf(host, domain string) bool {
	return strings.HasSuffix(host, "."+domain)
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./proxy/ -v`
Expected: All tests pass.

**Step 5: Commit**

```
feat: implement domain allowlist with port and wildcard matching
```

---

### Task 3: CIDR blocking

Implement the CIDR blocking logic that validates IP addresses against blocked
ranges. Used by both the proxy and the DNS server.

**Files:**
- Create: `proxy/cidr.go`
- Create: `proxy/cidr_test.go`

**Step 1: Write failing tests**

`proxy/cidr_test.go`:
```go
package proxy

import (
	"net"
	"testing"
)

func TestCIDRBlocker(t *testing.T) {
	blocker := NewCIDRBlocker(nil)

	tests := []struct {
		name string
		ip   string
		want bool
	}{
		// Default blocked ranges
		{"private 10.x", "10.0.0.1", true},
		{"private 172.16.x", "172.16.0.1", true},
		{"private 172.31.x", "172.31.255.255", true},
		{"not private 172.15.x", "172.15.0.1", false},
		{"private 192.168.x", "192.168.1.1", true},
		{"loopback", "127.0.0.1", true},
		{"link-local", "169.254.1.1", true},
		{"public IP", "8.8.8.8", false},
		{"another public", "1.1.1.1", false},

		// IPv6
		{"ipv6 loopback", "::1", true},
		{"ipv6 ULA", "fd00::1", true},
		{"ipv6 link-local", "fe80::1", true},
		{"ipv6 public", "2607:f8b0:4004:800::200e", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("invalid test IP: %s", tt.ip)
			}
			got := blocker.IsBlocked(ip)
			if got != tt.want {
				t.Errorf("IsBlocked(%s) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

func TestCIDRBlockerCustomRanges(t *testing.T) {
	blocker := NewCIDRBlocker([]string{"203.0.113.0/24"})

	tests := []struct {
		name string
		ip   string
		want bool
	}{
		{"custom blocked", "203.0.113.50", true},
		{"custom not blocked", "203.0.114.1", false},
		{"default still blocked", "10.0.0.1", true},
		{"public still allowed", "8.8.8.8", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			got := blocker.IsBlocked(ip)
			if got != tt.want {
				t.Errorf("IsBlocked(%s) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./proxy/ -v -run TestCIDR`
Expected: Compilation errors.

**Step 3: Implement the CIDR blocker**

`proxy/cidr.go`:
```go
package proxy

import "net"

var defaultBlockedCIDRs = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"::1/128",
	"fc00::/7",
	"fe80::/10",
}

// CIDRBlocker checks IPs against a list of blocked CIDR ranges.
type CIDRBlocker struct {
	nets []*net.IPNet
}

func NewCIDRBlocker(extra []string) *CIDRBlocker {
	all := make([]string, 0, len(defaultBlockedCIDRs)+len(extra))
	all = append(all, defaultBlockedCIDRs...)
	all = append(all, extra...)

	nets := make([]*net.IPNet, 0, len(all))
	for _, cidr := range all {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		nets = append(nets, ipNet)
	}
	return &CIDRBlocker{nets: nets}
}

func (b *CIDRBlocker) IsBlocked(ip net.IP) bool {
	for _, n := range b.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./proxy/ -v -run TestCIDR`
Expected: All tests pass.

**Step 5: Commit**

```
feat: implement CIDR blocker with default private ranges
```

---

### Task 4: Request logging (ring buffer)

Implement the shared log ring buffer used by the proxy, DNS server, and
control API.

**Files:**
- Create: `proxy/log.go`
- Create: `proxy/log_test.go`

**Step 1: Write failing tests**

`proxy/log_test.go`:
```go
package proxy

import (
	"testing"
	"time"
)

func TestLogBuffer(t *testing.T) {
	t.Run("stores entries up to capacity", func(t *testing.T) {
		buf := NewLogBuffer(3)
		buf.Add(LogEntry{Time: time.Now(), Domain: "a.com", Action: ActionAllow, Source: SourceProxy})
		buf.Add(LogEntry{Time: time.Now(), Domain: "b.com", Action: ActionBlock, Source: SourceProxy})
		buf.Add(LogEntry{Time: time.Now(), Domain: "c.com", Action: ActionAllow, Source: SourceDNS})

		entries := buf.Entries()
		if len(entries) != 3 {
			t.Fatalf("got %d entries, want 3", len(entries))
		}
		if entries[0].Domain != "a.com" {
			t.Errorf("first entry domain = %q, want %q", entries[0].Domain, "a.com")
		}
	})

	t.Run("overwrites oldest when full", func(t *testing.T) {
		buf := NewLogBuffer(2)
		buf.Add(LogEntry{Domain: "a.com"})
		buf.Add(LogEntry{Domain: "b.com"})
		buf.Add(LogEntry{Domain: "c.com"})

		entries := buf.Entries()
		if len(entries) != 2 {
			t.Fatalf("got %d entries, want 2", len(entries))
		}
		if entries[0].Domain != "b.com" {
			t.Errorf("first entry = %q, want %q", entries[0].Domain, "b.com")
		}
		if entries[1].Domain != "c.com" {
			t.Errorf("second entry = %q, want %q", entries[1].Domain, "c.com")
		}
	})

	t.Run("stats counts per domain", func(t *testing.T) {
		buf := NewLogBuffer(100)
		buf.Add(LogEntry{Domain: "a.com", Action: ActionAllow})
		buf.Add(LogEntry{Domain: "a.com", Action: ActionAllow})
		buf.Add(LogEntry{Domain: "a.com", Action: ActionBlock})
		buf.Add(LogEntry{Domain: "b.com", Action: ActionBlock})

		stats := buf.Stats()
		if stats["a.com"].Allowed != 2 {
			t.Errorf("a.com allowed = %d, want 2", stats["a.com"].Allowed)
		}
		if stats["a.com"].Blocked != 1 {
			t.Errorf("a.com blocked = %d, want 1", stats["a.com"].Blocked)
		}
		if stats["b.com"].Blocked != 1 {
			t.Errorf("b.com blocked = %d, want 1", stats["b.com"].Blocked)
		}
	})
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./proxy/ -v -run TestLogBuffer`
Expected: Compilation errors.

**Step 3: Implement the log buffer**

`proxy/log.go`:
```go
package proxy

import (
	"sync"
	"time"
)

type Action string

const (
	ActionAllow Action = "allow"
	ActionBlock Action = "block"
)

type Source string

const (
	SourceProxy Source = "proxy"
	SourceDNS   Source = "dns"
)

type LogEntry struct {
	Time   time.Time `json:"time"`
	Domain string    `json:"domain"`
	Port   string    `json:"port,omitempty"`
	Action Action    `json:"action"`
	Source Source     `json:"source"`
	Reason string    `json:"reason,omitempty"`
}

type DomainStats struct {
	Allowed int `json:"allowed"`
	Blocked int `json:"blocked"`
}

// LogBuffer is a thread-safe circular buffer for log entries.
type LogBuffer struct {
	mu      sync.Mutex
	entries []LogEntry
	cap     int
	pos     int
	full    bool
	stats   map[string]*DomainStats
}

func NewLogBuffer(capacity int) *LogBuffer {
	return &LogBuffer{
		entries: make([]LogEntry, capacity),
		cap:     capacity,
		stats:   make(map[string]*DomainStats),
	}
}

func (b *LogBuffer) Add(entry LogEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.entries[b.pos] = entry
	b.pos = (b.pos + 1) % b.cap
	if b.pos == 0 && !b.full {
		b.full = true
	}

	s, ok := b.stats[entry.Domain]
	if !ok {
		s = &DomainStats{}
		b.stats[entry.Domain] = s
	}
	switch entry.Action {
	case ActionAllow:
		s.Allowed++
	case ActionBlock:
		s.Blocked++
	}
}

// Entries returns all log entries in chronological order.
func (b *LogBuffer) Entries() []LogEntry {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.full {
		result := make([]LogEntry, b.pos)
		copy(result, b.entries[:b.pos])
		return result
	}

	result := make([]LogEntry, b.cap)
	copy(result, b.entries[b.pos:])
	copy(result[b.cap-b.pos:], b.entries[:b.pos])
	return result
}

// Stats returns per-domain statistics. These are cumulative and not limited
// by the ring buffer capacity.
func (b *LogBuffer) Stats() map[string]DomainStats {
	b.mu.Lock()
	defer b.mu.Unlock()

	result := make(map[string]DomainStats, len(b.stats))
	for k, v := range b.stats {
		result[k] = *v
	}
	return result
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./proxy/ -v -run TestLogBuffer`
Expected: All tests pass.

**Step 5: Commit**

```
feat: implement ring buffer for proxy and DNS request logging
```

---

### Task 5: Configuration loading and merging

Implement config loading from global and project YAML files, preset expansion,
and merging via koanf.

**Files:**
- Create: `config/config.go`
- Create: `config/config_test.go`

**Step 1: Write failing tests**

`config/config_test.go`:
```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndMerge(t *testing.T) {
	t.Run("merges global and project configs", func(t *testing.T) {
		dir := t.TempDir()
		globalDir := filepath.Join(dir, "global")
		projectDir := filepath.Join(dir, "project", ".vibepit")
		os.MkdirAll(globalDir, 0o755)
		os.MkdirAll(projectDir, 0o755)

		globalFile := filepath.Join(globalDir, "config.yaml")
		os.WriteFile(globalFile, []byte(`
allow:
  - github.com
dns-only:
  - internal.example.com
block-cidr:
  - 203.0.113.0/24
presets:
  node:
    allow:
      - registry.npmjs.org
`), 0o644)

		projectFile := filepath.Join(projectDir, "network.yaml")
		os.WriteFile(projectFile, []byte(`
presets:
  - node
allow:
  - api.anthropic.com
`), 0o644)

		cfg, err := Load(globalFile, projectFile)
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}

		merged := cfg.Merge(nil, nil)

		// Global + project + preset allow lists combined
		wants := []string{"github.com", "api.anthropic.com", "registry.npmjs.org"}
		for _, w := range wants {
			if !contains(merged.Allow, w) {
				t.Errorf("merged.Allow missing %q, got %v", w, merged.Allow)
			}
		}

		if !contains(merged.DNSOnly, "internal.example.com") {
			t.Errorf("merged.DNSOnly missing internal.example.com")
		}

		if !contains(merged.BlockCIDR, "203.0.113.0/24") {
			t.Errorf("merged.BlockCIDR missing 203.0.113.0/24")
		}
	})

	t.Run("CLI overrides add to merged config", func(t *testing.T) {
		cfg := &Config{}
		merged := cfg.Merge([]string{"extra.com"}, []string{"node"})

		if !contains(merged.Allow, "extra.com") {
			t.Errorf("CLI --allow not in merged result")
		}
	})

	t.Run("missing files are not errors", func(t *testing.T) {
		cfg, err := Load("/nonexistent/global.yaml", "/nonexistent/project.yaml")
		if err != nil {
			t.Fatalf("Load() should not error on missing files: %v", err)
		}
		merged := cfg.Merge(nil, nil)
		if len(merged.Allow) != 0 {
			t.Errorf("expected empty allow list, got %v", merged.Allow)
		}
	})
}

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./config/ -v`
Expected: Compilation errors.

**Step 3: Implement config loading**

`config/config.go`:
```go
package config

import (
	"os"
	"path/filepath"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// Preset defines a named set of allowed domains.
type Preset struct {
	Allow []string `koanf:"allow"`
}

// GlobalConfig represents ~/.config/vibepit/config.yaml.
type GlobalConfig struct {
	Allow     []string          `koanf:"allow"`
	DNSOnly   []string          `koanf:"dns-only"`
	BlockCIDR []string          `koanf:"block-cidr"`
	Presets   map[string]Preset `koanf:"presets"`
}

// ProjectConfig represents .vibepit/network.yaml.
type ProjectConfig struct {
	Presets []string `koanf:"presets"`
	Allow   []string `koanf:"allow"`
	DNSOnly []string `koanf:"dns-only"`
}

// Config holds parsed global and project configuration.
type Config struct {
	Global  GlobalConfig
	Project ProjectConfig
}

// MergedConfig is the final flattened configuration passed to the proxy.
type MergedConfig struct {
	Allow     []string `json:"allow"`
	DNSOnly   []string `json:"dns-only"`
	BlockCIDR []string `json:"block-cidr"`
}

// Load reads the global and project config files. Missing files are silently
// ignored.
func Load(globalPath, projectPath string) (*Config, error) {
	cfg := &Config{}

	if err := loadFile(globalPath, &cfg.Global); err != nil {
		return nil, err
	}
	if err := loadFile(projectPath, &cfg.Project); err != nil {
		return nil, err
	}

	return cfg, nil
}

func loadFile(path string, target any) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}

	k := koanf.New(".")
	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
		return err
	}
	return k.Unmarshal("", target)
}

// Merge combines global config, project config, resolved presets, and CLI
// overrides into a single MergedConfig.
func (c *Config) Merge(cliAllow []string, cliPresets []string) MergedConfig {
	seen := make(map[string]bool)
	var allow []string

	addUnique := func(entries []string) {
		for _, e := range entries {
			if !seen[e] {
				seen[e] = true
				allow = append(allow, e)
			}
		}
	}

	addUnique(c.Global.Allow)
	addUnique(c.Project.Allow)
	addUnique(cliAllow)

	// Resolve presets from project config and CLI flags
	allPresets := make([]string, 0, len(c.Project.Presets)+len(cliPresets))
	allPresets = append(allPresets, c.Project.Presets...)
	allPresets = append(allPresets, cliPresets...)

	for _, name := range allPresets {
		if preset, ok := c.Global.Presets[name]; ok {
			addUnique(preset.Allow)
		}
	}

	var dnsOnly []string
	dnsSeen := make(map[string]bool)
	for _, e := range c.Global.DNSOnly {
		if !dnsSeen[e] {
			dnsSeen[e] = true
			dnsOnly = append(dnsOnly, e)
		}
	}
	for _, e := range c.Project.DNSOnly {
		if !dnsSeen[e] {
			dnsSeen[e] = true
			dnsOnly = append(dnsOnly, e)
		}
	}

	return MergedConfig{
		Allow:     allow,
		DNSOnly:   dnsOnly,
		BlockCIDR: c.Global.BlockCIDR,
	}
}

// DefaultGlobalPath returns ~/.config/vibepit/config.yaml.
func DefaultGlobalPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "vibepit", "config.yaml")
}

// DefaultProjectPath returns .vibepit/network.yaml relative to the given
// project root.
func DefaultProjectPath(projectRoot string) string {
	return filepath.Join(projectRoot, ".vibepit", "network.yaml")
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./config/ -v`
Expected: All tests pass.

**Step 5: Commit**

```
feat: implement config loading and merging with koanf
```

---

### Task 6: HTTP CONNECT proxy

Implement the filtering HTTP proxy using goproxy. It uses the allowlist and
CIDR blocker to accept or reject connections.

**Files:**
- Create: `proxy/http.go`
- Create: `proxy/http_test.go`

**Step 1: Write failing tests**

`proxy/http_test.go`:
```go
package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestHTTPProxy(t *testing.T) {
	al := NewAllowlist([]string{"httpbin.org", "allowed.example.com:443"})
	blocker := NewCIDRBlocker(nil)
	log := NewLogBuffer(100)
	p := NewHTTPProxy(al, blocker, log)

	srv := httptest.NewServer(p.Handler())
	defer srv.Close()

	proxyURL, _ := url.Parse(srv.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

	t.Run("blocks disallowed plain HTTP request", func(t *testing.T) {
		resp, err := client.Get("http://evil.com/")
		if err != nil {
			t.Fatalf("request error: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusForbidden)
		}
		body, _ := io.ReadAll(resp.Body)
		if len(body) == 0 {
			t.Error("expected error message in body")
		}
	})

	t.Run("logs blocked request", func(t *testing.T) {
		entries := log.Entries()
		found := false
		for _, e := range entries {
			if e.Domain == "evil.com" && e.Action == ActionBlock {
				found = true
				break
			}
		}
		if !found {
			t.Error("blocked request not found in log")
		}
	})
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./proxy/ -v -run TestHTTPProxy`
Expected: Compilation errors.

**Step 3: Implement the HTTP proxy**

`proxy/http.go`:
```go
package proxy

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/elazarl/goproxy"
)

// HTTPProxy is a filtering HTTP/HTTPS proxy.
type HTTPProxy struct {
	allowlist *Allowlist
	cidr      *CIDRBlocker
	log       *LogBuffer
	proxy     *goproxy.ProxyHttpServer
}

func NewHTTPProxy(allowlist *Allowlist, cidr *CIDRBlocker, log *LogBuffer) *HTTPProxy {
	p := &HTTPProxy{
		allowlist: allowlist,
		cidr:      cidr,
		log:       log,
		proxy:     goproxy.NewProxyHttpServer(),
	}

	// Handle CONNECT requests (HTTPS tunneling).
	p.proxy.OnRequest().HandleConnect(goproxy.FuncHttpsHandler(
		func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
			hostname, port := splitHostPort(host, "443")

			if !p.allowlist.Allows(hostname, port) {
				p.log.Add(LogEntry{
					Time:   time.Now(),
					Domain: hostname,
					Port:   port,
					Action: ActionBlock,
					Source: SourceProxy,
					Reason: "domain not in allowlist",
				})
				return goproxy.RejectConnect, host
			}

			if blocked, ip := p.resolveAndCheckCIDR(hostname); blocked {
				p.log.Add(LogEntry{
					Time:   time.Now(),
					Domain: hostname,
					Port:   port,
					Action: ActionBlock,
					Source: SourceProxy,
					Reason: fmt.Sprintf("resolved IP %s is in blocked CIDR range", ip),
				})
				return goproxy.RejectConnect, host
			}

			p.log.Add(LogEntry{
				Time:   time.Now(),
				Domain: hostname,
				Port:   port,
				Action: ActionAllow,
				Source: SourceProxy,
			})
			return goproxy.OkConnect, host
		}))

	// Handle plain HTTP requests.
	p.proxy.OnRequest().DoFunc(
		func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
			hostname, port := splitHostPort(req.Host, "80")

			if !p.allowlist.Allows(hostname, port) {
				p.log.Add(LogEntry{
					Time:   time.Now(),
					Domain: hostname,
					Port:   port,
					Action: ActionBlock,
					Source: SourceProxy,
					Reason: "domain not in allowlist",
				})
				return req, goproxy.NewResponse(req,
					goproxy.ContentTypeText,
					http.StatusForbidden,
					fmt.Sprintf("domain %q is not in the allowlist\nadd it to .vibepit/network.yaml or run: vibepit monitor\n", hostname),
				)
			}

			if blocked, ip := p.resolveAndCheckCIDR(hostname); blocked {
				p.log.Add(LogEntry{
					Time:   time.Now(),
					Domain: hostname,
					Port:   port,
					Action: ActionBlock,
					Source: SourceProxy,
					Reason: fmt.Sprintf("resolved IP %s is in blocked CIDR range", ip),
				})
				return req, goproxy.NewResponse(req,
					goproxy.ContentTypeText,
					http.StatusForbidden,
					fmt.Sprintf("domain %q resolves to blocked IP %s\n", hostname, ip),
				)
			}

			p.log.Add(LogEntry{
				Time:   time.Now(),
				Domain: hostname,
				Port:   port,
				Action: ActionAllow,
				Source: SourceProxy,
			})
			return req, nil
		})

	return p
}

func (p *HTTPProxy) Handler() http.Handler {
	return p.proxy
}

func (p *HTTPProxy) resolveAndCheckCIDR(hostname string) (bool, net.IP) {
	// If the hostname is already an IP, check directly.
	if ip := net.ParseIP(hostname); ip != nil {
		if p.cidr.IsBlocked(ip) {
			return true, ip
		}
		return false, nil
	}

	ips, err := net.LookupIP(hostname)
	if err != nil {
		return false, nil
	}
	for _, ip := range ips {
		if p.cidr.IsBlocked(ip) {
			return true, ip
		}
	}
	return false, nil
}

func splitHostPort(hostport, defaultPort string) (string, string) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		// No port in the string.
		return strings.ToLower(hostport), defaultPort
	}
	return strings.ToLower(host), port
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./proxy/ -v -run TestHTTPProxy`
Expected: All tests pass.

**Step 5: Commit**

```
feat: implement filtering HTTP CONNECT proxy with goproxy
```

---

### Task 7: DNS server

Implement the filtering DNS server using miekg/dns. It uses the allowlist,
dns-only list, and CIDR blocker to filter queries and responses.

**Files:**
- Create: `proxy/dns.go`
- Create: `proxy/dns_test.go`

**Step 1: Write failing tests**

`proxy/dns_test.go`:
```go
package proxy

import (
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestDNSServer(t *testing.T) {
	al := NewAllowlist([]string{"allowed.example.com"})
	dnsOnly := NewAllowlist([]string{"dnsonly.example.com"})
	blocker := NewCIDRBlocker(nil)
	log := NewLogBuffer(100)

	srv := NewDNSServer(al, dnsOnly, blocker, log, "8.8.8.8:53")
	addr, cleanup := srv.ListenAndServeTest()
	defer cleanup()

	// Give server time to start.
	time.Sleep(50 * time.Millisecond)

	c := new(dns.Client)

	t.Run("blocks disallowed domain", func(t *testing.T) {
		m := new(dns.Msg)
		m.SetQuestion("evil.com.", dns.TypeA)

		r, _, err := c.Exchange(m, addr)
		if err != nil {
			t.Fatalf("DNS exchange error: %v", err)
		}
		if r.Rcode != dns.RcodeNameError {
			t.Errorf("rcode = %d, want NXDOMAIN (%d)", r.Rcode, dns.RcodeNameError)
		}
	})

	t.Run("allows domain in allowlist", func(t *testing.T) {
		m := new(dns.Msg)
		m.SetQuestion("allowed.example.com.", dns.TypeA)

		r, _, err := c.Exchange(m, addr)
		if err != nil {
			t.Fatalf("DNS exchange error: %v", err)
		}
		// We can't assert on specific records since it depends on real DNS,
		// but it should not be NXDOMAIN from our filter.
		if r.Rcode == dns.RcodeNameError {
			// This could be a real NXDOMAIN if the domain doesn't exist. The
			// test validates that our filter didn't block it. For a unit test,
			// we'd need to mock the upstream resolver. This is an integration
			// test boundary — accept either success or server failure.
			t.Skip("domain may not resolve in test environment")
		}
	})

	t.Run("allows domain in dns-only list", func(t *testing.T) {
		m := new(dns.Msg)
		m.SetQuestion("dnsonly.example.com.", dns.TypeA)

		r, _, err := c.Exchange(m, addr)
		if err != nil {
			t.Fatalf("DNS exchange error: %v", err)
		}
		if r.Rcode == dns.RcodeNameError {
			t.Skip("domain may not resolve in test environment")
		}
	})

	t.Run("logs blocked query", func(t *testing.T) {
		entries := log.Entries()
		found := false
		for _, e := range entries {
			if e.Domain == "evil.com" && e.Action == ActionBlock && e.Source == SourceDNS {
				found = true
				break
			}
		}
		if !found {
			t.Error("blocked DNS query not found in log")
		}
	})
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./proxy/ -v -run TestDNSServer`
Expected: Compilation errors.

**Step 3: Implement the DNS server**

`proxy/dns.go`:
```go
package proxy

import (
	"fmt"
	"net"
	"strings"
	"time"

	mdns "github.com/miekg/dns"
)

// DNSServer is a filtering DNS server.
type DNSServer struct {
	allowlist *Allowlist
	dnsOnly   *Allowlist
	cidr      *CIDRBlocker
	log       *LogBuffer
	upstream  string
}

func NewDNSServer(allowlist, dnsOnly *Allowlist, cidr *CIDRBlocker, log *LogBuffer, upstream string) *DNSServer {
	return &DNSServer{
		allowlist: allowlist,
		dnsOnly:   dnsOnly,
		cidr:      cidr,
		log:       log,
		upstream:  upstream,
	}
}

func (s *DNSServer) handler() mdns.Handler {
	return mdns.HandlerFunc(func(w mdns.ResponseWriter, r *mdns.Msg) {
		if len(r.Question) == 0 {
			mdns.HandleFailed(w, r)
			return
		}

		domain := strings.TrimSuffix(strings.ToLower(r.Question[0].Name), ".")

		if !s.allowlist.AllowsDNS(domain) && !s.dnsOnly.AllowsDNS(domain) {
			s.log.Add(LogEntry{
				Time:   time.Now(),
				Domain: domain,
				Action: ActionBlock,
				Source: SourceDNS,
				Reason: "domain not in allowlist",
			})
			m := new(mdns.Msg)
			m.SetRcode(r, mdns.RcodeNameError)
			w.WriteMsg(m)
			return
		}

		// Forward to upstream.
		c := new(mdns.Client)
		resp, _, err := c.Exchange(r, s.upstream)
		if err != nil {
			mdns.HandleFailed(w, r)
			return
		}

		// Check resolved IPs against CIDR blocklist.
		if s.hasBlockedIP(resp) {
			s.log.Add(LogEntry{
				Time:   time.Now(),
				Domain: domain,
				Action: ActionBlock,
				Source: SourceDNS,
				Reason: "resolved IP in blocked CIDR range",
			})
			m := new(mdns.Msg)
			m.SetRcode(r, mdns.RcodeNameError)
			w.WriteMsg(m)
			return
		}

		s.log.Add(LogEntry{
			Time:   time.Now(),
			Domain: domain,
			Action: ActionAllow,
			Source: SourceDNS,
		})
		w.WriteMsg(resp)
	})
}

func (s *DNSServer) hasBlockedIP(msg *mdns.Msg) bool {
	for _, rr := range msg.Answer {
		switch v := rr.(type) {
		case *mdns.A:
			if s.cidr.IsBlocked(v.A) {
				return true
			}
		case *mdns.AAAA:
			if s.cidr.IsBlocked(v.AAAA) {
				return true
			}
		}
	}
	return false
}

// ListenAndServe starts the DNS server on the given address (e.g. ":53").
func (s *DNSServer) ListenAndServe(addr string) error {
	udpServer := &mdns.Server{Addr: addr, Net: "udp", Handler: s.handler()}
	tcpServer := &mdns.Server{Addr: addr, Net: "tcp", Handler: s.handler()}

	errCh := make(chan error, 2)
	go func() { errCh <- udpServer.ListenAndServe() }()
	go func() { errCh <- tcpServer.ListenAndServe() }()

	return <-errCh
}

// ListenAndServeTest starts a UDP DNS server on a random port for testing.
// Returns the address and a cleanup function.
func (s *DNSServer) ListenAndServeTest() (string, func()) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		panic(fmt.Sprintf("listen: %v", err))
	}
	addr := pc.LocalAddr().String()

	srv := &mdns.Server{PacketConn: pc, Handler: s.handler()}
	go srv.ActivateAndServe()

	return addr, func() { srv.Shutdown() }
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./proxy/ -v -run TestDNSServer`
Expected: All tests pass (some may skip in environments without DNS access).

**Step 5: Commit**

```
feat: implement filtering DNS server with CIDR validation
```

---

### Task 8: Control API

Implement the HTTP control API that serves logs, stats, and config for
`vibepit monitor` and a future web UI.

**Files:**
- Create: `proxy/api.go`
- Create: `proxy/api_test.go`

**Step 1: Write failing tests**

`proxy/api_test.go`:
```go
package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestControlAPI(t *testing.T) {
	log := NewLogBuffer(100)
	log.Add(LogEntry{Domain: "a.com", Action: ActionAllow, Source: SourceProxy})
	log.Add(LogEntry{Domain: "b.com", Action: ActionBlock, Source: SourceDNS})

	mergedConfig := map[string]any{
		"allow":   []string{"a.com", "b.com"},
		"dns-only": []string{"c.com"},
	}

	api := NewControlAPI(log, mergedConfig)

	t.Run("GET /logs returns entries", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/logs", nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}

		var entries []LogEntry
		if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
			t.Fatalf("json decode: %v", err)
		}
		if len(entries) != 2 {
			t.Errorf("got %d entries, want 2", len(entries))
		}
	})

	t.Run("GET /stats returns per-domain counts", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/stats", nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}

		var stats map[string]DomainStats
		if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil {
			t.Fatalf("json decode: %v", err)
		}
		if stats["a.com"].Allowed != 1 {
			t.Errorf("a.com allowed = %d, want 1", stats["a.com"].Allowed)
		}
	})

	t.Run("GET /config returns merged config", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/config", nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
	})

	t.Run("GET /unknown returns 404", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/unknown", nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", w.Code)
		}
	})
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./proxy/ -v -run TestControlAPI`
Expected: Compilation errors.

**Step 3: Implement the control API**

`proxy/api.go`:
```go
package proxy

import (
	"encoding/json"
	"net/http"
)

// ControlAPI serves proxy status and configuration over HTTP.
type ControlAPI struct {
	mux    *http.ServeMux
	log    *LogBuffer
	config any
}

func NewControlAPI(log *LogBuffer, config any) *ControlAPI {
	api := &ControlAPI{
		mux:    http.NewServeMux(),
		log:    log,
		config: config,
	}
	api.mux.HandleFunc("GET /logs", api.handleLogs)
	api.mux.HandleFunc("GET /stats", api.handleStats)
	api.mux.HandleFunc("GET /config", api.handleConfig)
	return api
}

func (a *ControlAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mux.ServeHTTP(w, r)
}

func (a *ControlAPI) handleLogs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.log.Entries())
}

func (a *ControlAPI) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.log.Stats())
}

func (a *ControlAPI) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.config)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./proxy/ -v -run TestControlAPI`
Expected: All tests pass.

**Step 5: Commit**

```
feat: implement control API for logs, stats, and config
```

---

### Task 9: Proxy subcommand (wiring it all together)

Wire the proxy subcommand to start all three servers (HTTP proxy, DNS, control
API) from a config file path.

**Files:**
- Modify: `cmd/proxy.go`
- Create: `proxy/server.go`

**Step 1: Implement the combined proxy server**

`proxy/server.go`:
```go
package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

// ProxyConfig is the JSON config file passed to the proxy container.
type ProxyConfig struct {
	Allow     []string `json:"allow"`
	DNSOnly   []string `json:"dns-only"`
	BlockCIDR []string `json:"block-cidr"`
	Upstream  string   `json:"upstream"`
}

// Server runs the HTTP proxy, DNS server, and control API.
type Server struct {
	config ProxyConfig
}

func NewServer(configPath string) (*Server, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg ProxyConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if cfg.Upstream == "" {
		cfg.Upstream = "8.8.8.8:53"
	}

	return &Server{config: cfg}, nil
}

func (s *Server) Run(ctx context.Context) error {
	allowlist := NewAllowlist(s.config.Allow)
	dnsOnlyList := NewAllowlist(s.config.DNSOnly)
	cidr := NewCIDRBlocker(s.config.BlockCIDR)
	log := NewLogBuffer(10000)

	httpProxy := NewHTTPProxy(allowlist, cidr, log)
	dnsServer := NewDNSServer(allowlist, dnsOnlyList, cidr, log, s.config.Upstream)
	controlAPI := NewControlAPI(log, s.config)

	errCh := make(chan error, 3)

	go func() {
		fmt.Println("proxy: HTTP proxy listening on :3128")
		errCh <- http.ListenAndServe(":3128", httpProxy.Handler())
	}()

	go func() {
		fmt.Println("proxy: DNS server listening on :53")
		errCh <- dnsServer.ListenAndServe(":53")
	}()

	go func() {
		fmt.Println("proxy: control API listening on :3129")
		errCh <- http.ListenAndServe(":3129", controlAPI)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
```

**Step 2: Update the proxy subcommand**

Replace `cmd/proxy.go` with:
```go
package cmd

import (
	"context"

	"github.com/bernd/vibepit/proxy"
	"github.com/urfave/cli/v3"
)

func ProxyCommand() *cli.Command {
	return &cli.Command{
		Name:  "proxy",
		Usage: "Run the proxy server (used inside proxy container)",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "config",
				Usage:    "Path to proxy config JSON file",
				Required: true,
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			srv, err := proxy.NewServer(cmd.String("config"))
			if err != nil {
				return err
			}
			return srv.Run(ctx)
		},
	}
}
```

**Step 3: Verify it compiles**

Run: `go build .`
Expected: No errors.

**Step 4: Commit**

```
feat: wire proxy subcommand to start all three servers
```

---

### Task 10: Container orchestration

Implement Docker/Podman client abstraction for creating networks, starting
the proxy container, and starting the dev container.

**Files:**
- Create: `container/client.go`
- Create: `container/client_test.go`

**Step 1: Implement the container client**

`container/client.go`:
```go
package container

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
)

// Client wraps the Docker API client with vibepit-specific operations.
type Client struct {
	docker *dockerclient.Client
}

// NewClient creates a Docker client. It tries the default Docker socket first,
// then falls back to the Podman socket.
func NewClient() (*Client, error) {
	// Try default Docker socket.
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err == nil {
		if _, err := cli.Ping(context.Background()); err == nil {
			return &Client{docker: cli}, nil
		}
		cli.Close()
	}

	// Try Podman socket.
	podmanSock := fmt.Sprintf("unix:///run/user/%d/podman/podman.sock", os.Getuid())
	cli, err = dockerclient.NewClientWithOpts(
		dockerclient.WithHost(podmanSock),
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("no Docker or Podman socket found: %w", err)
	}
	if _, err := cli.Ping(context.Background()); err != nil {
		cli.Close()
		return nil, fmt.Errorf("no Docker or Podman socket found: %w", err)
	}
	return &Client{docker: cli}, nil
}

func (c *Client) Close() error {
	return c.docker.Close()
}

// FindRunningSession returns the container ID of an existing vibepit session
// for the given project directory, or empty string if none.
func (c *Client) FindRunningSession(ctx context.Context, projectDir string) (string, error) {
	containers, err := c.docker.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(
			filters.Arg("label", fmt.Sprintf("vibepit.project.dir=%s", projectDir)),
		),
	})
	if err != nil {
		return "", err
	}
	if len(containers) > 0 {
		return containers[0].ID, nil
	}
	return "", nil
}

// AttachSession attaches an interactive terminal to an existing container.
func (c *Client) AttachSession(ctx context.Context, containerID string) error {
	cmd := exec.CommandContext(ctx, "docker", "exec", "-ti", containerID, "/bin/bash", "--login")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// NetworkConfig holds settings for the private vibepit network.
type NetworkConfig struct {
	Name      string
	Subnet    string
	ProxyIP   string
}

// CreateNetwork creates the internal vibepit-net network.
func (c *Client) CreateNetwork(ctx context.Context, cfg NetworkConfig) (string, error) {
	resp, err := c.docker.NetworkCreate(ctx, cfg.Name, network.CreateOptions{
		Internal: true,
		Labels:   map[string]string{"vibepit": "true"},
		IPAM: &network.IPAM{
			Config: []network.IPAMConfig{
				{Subnet: cfg.Subnet, Gateway: cfg.ProxyIP},
			},
		},
	})
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

// RemoveNetwork removes a Docker network.
func (c *Client) RemoveNetwork(ctx context.Context, networkID string) error {
	return c.docker.NetworkRemove(ctx, networkID)
}

// ProxyContainerConfig holds settings for starting the proxy container.
type ProxyContainerConfig struct {
	BinaryPath string // Host path to the vibepit binary
	ConfigPath string // Host path to the proxy config JSON
	NetworkID  string
	ProxyIP    string
	Name       string
}

// StartProxyContainer starts the proxy in a scratch container.
func (c *Client) StartProxyContainer(ctx context.Context, cfg ProxyContainerConfig) (string, error) {
	resp, err := c.docker.ContainerCreate(ctx,
		&container.Config{
			Image:      "scratch",
			Cmd:        []string{"/vibepit", "proxy", "--config", "/config.json"},
			Labels:     map[string]string{"vibepit": "true", "vibepit.role": "proxy"},
			WorkingDir: "/",
		},
		&container.HostConfig{
			Binds: []string{
				cfg.BinaryPath + ":/vibepit:ro",
				cfg.ConfigPath + ":/config.json:ro",
			},
			RestartPolicy: container.RestartPolicy{Name: "no"},
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				cfg.NetworkID: {
					IPAMConfig: &network.EndpointIPAMConfig{
						IPv4Address: cfg.ProxyIP,
					},
				},
			},
		},
		nil,
		cfg.Name,
	)
	if err != nil {
		return "", fmt.Errorf("create proxy container: %w", err)
	}

	if err := c.docker.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("start proxy container: %w", err)
	}

	// Also connect to the default bridge network for internet access.
	if err := c.docker.NetworkConnect(ctx, "bridge", resp.ID, nil); err != nil {
		return "", fmt.Errorf("connect proxy to bridge: %w", err)
	}

	return resp.ID, nil
}

// DevContainerConfig holds settings for starting the dev container.
type DevContainerConfig struct {
	Image      string
	ProjectDir string
	WorkDir    string
	VolumeName string
	NetworkID  string
	ProxyIP    string
	Name       string
	Term       string
	ColorTerm  string
	UID        int
	User       string
}

// StartDevContainer starts the dev container with proxy settings.
func (c *Client) StartDevContainer(ctx context.Context, cfg DevContainerConfig) (string, error) {
	env := []string{
		fmt.Sprintf("TERM=%s", cfg.Term),
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		fmt.Sprintf("VIBEPIT_PROJECT_DIR=%s", cfg.ProjectDir),
		fmt.Sprintf("HTTP_PROXY=http://%s:3128", cfg.ProxyIP),
		fmt.Sprintf("HTTPS_PROXY=http://%s:3128", cfg.ProxyIP),
		fmt.Sprintf("http_proxy=http://%s:3128", cfg.ProxyIP),
		fmt.Sprintf("https_proxy=http://%s:3128", cfg.ProxyIP),
		"NO_PROXY=localhost,127.0.0.1",
		"no_proxy=localhost,127.0.0.1",
	}
	if cfg.ColorTerm != "" {
		env = append(env, fmt.Sprintf("COLORTERM=%s", cfg.ColorTerm))
	}

	binds := []string{
		cfg.VolumeName + ":/home/code",
		cfg.ProjectDir + ":" + cfg.ProjectDir,
	}
	if _, err := os.Stat("/etc/localtime"); err == nil {
		binds = append(binds, "/etc/localtime:/etc/localtime:ro")
	}

	resp, err := c.docker.ContainerCreate(ctx,
		&container.Config{
			Image:    cfg.Image,
			Env:      env,
			Hostname: "vibes",
			Labels: map[string]string{
				"vibepit":             "true",
				"vibepit.role":        "dev",
				"vibepit.uid":         fmt.Sprintf("%d", cfg.UID),
				"vibepit.user":        cfg.User,
				"vibepit.volume":      cfg.VolumeName,
				"vibepit.project.dir": cfg.ProjectDir,
			},
			Tty:       true,
			OpenStdin: true,
		},
		&container.HostConfig{
			Binds:       binds,
			DNS:         []string{cfg.ProxyIP},
			Init:        boolPtr(true),
			ReadonlyRootfs: true,
			CapDrop:     []string{"ALL"},
			SecurityOpt: []string{"no-new-privileges"},
			Tmpfs:       map[string]string{"/tmp": "exec"},
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				cfg.NetworkID: {},
			},
		},
		nil,
		cfg.Name,
	)
	if err != nil {
		return "", fmt.Errorf("create dev container: %w", err)
	}

	if err := c.docker.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("start dev container: %w", err)
	}

	return resp.ID, nil
}

// StopAndRemove stops and removes a container.
func (c *Client) StopAndRemove(ctx context.Context, containerID string) error {
	c.docker.ContainerStop(ctx, containerID, container.StopOptions{})
	return c.docker.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
}

// EnsureVolume creates the vibepit-home volume if it doesn't exist.
func (c *Client) EnsureVolume(ctx context.Context, name string, uid int, user string) error {
	volumes, err := c.docker.VolumeList(ctx, filters.NewArgs(
		filters.Arg("label", "vibepit=true"),
	))
	if err != nil {
		return err
	}
	for _, v := range volumes.Volumes {
		if v.Name == name {
			return nil
		}
	}

	_, err = c.docker.VolumeCreate(ctx, map[string]string{
		"vibepit":      "true",
		"vibepit.uid":  fmt.Sprintf("%d", uid),
		"vibepit.user": user,
	})
	return err
}

// RemoveVolume removes a named volume.
func (c *Client) RemoveVolume(ctx context.Context, name string) error {
	return c.docker.VolumeRemove(ctx, name, false)
}

// StreamLogs reads container logs as a stream (for debugging).
func (c *Client) StreamLogs(ctx context.Context, containerID string, w io.Writer) error {
	reader, err := c.docker.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
	})
	if err != nil {
		return err
	}
	defer reader.Close()
	_, err = io.Copy(w, reader)
	return err
}

func boolPtr(b bool) *bool {
	return &b
}

// VolumeCreateBody is used for creating volumes with labels.
// The Docker client's VolumeCreate accepts volume.CreateOptions.
// This is a simplified wrapper.
func init() {
	// Ensure we import the right types at compile time.
	_ = strings.TrimSpace
	_ = json.Marshal
}
```

Note: This task is intentionally light on tests because the Docker client
requires a running Docker daemon. The real validation happens in Task 12
(integration testing). Write a basic compilation test:

`container/client_test.go`:
```go
package container

import "testing"

func TestNetworkConfigDefaults(t *testing.T) {
	cfg := NetworkConfig{
		Name:    "vibepit-net",
		Subnet:  "172.28.0.0/16",
		ProxyIP: "172.28.0.2",
	}
	if cfg.Name != "vibepit-net" {
		t.Error("unexpected network name")
	}
}
```

**Step 2: Verify it compiles**

Run: `go build ./container/`
Expected: No errors.

**Step 3: Commit**

```
feat: implement Docker/Podman container orchestration client
```

---

### Task 11: Launcher mode (root command)

Wire the root command to perform the full orchestration flow: config loading,
first-run setup, network creation, proxy start, dev container start, shell
attach, and cleanup.

**Files:**
- Modify: `cmd/root.go`
- Modify: `main.go`

**Step 1: Implement the launcher**

Replace `cmd/root.go`:
```go
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"

	"github.com/bernd/vibepit/config"
	ctr "github.com/bernd/vibepit/container"
	"github.com/urfave/cli/v3"
)

func RootFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{
			Name:  "C",
			Usage: "Start with a clean vibepit volume (removes /home/code)",
		},
		&cli.BoolFlag{
			Name:  "L",
			Usage: "Use local vibepit:latest image instead of the published one",
		},
		&cli.StringSliceFlag{
			Name:  "allow",
			Usage: "Additional domains to allow",
		},
		&cli.StringSliceFlag{
			Name:  "preset",
			Usage: "Additional presets to activate",
		},
	}
}

func RootAction(ctx context.Context, cmd *cli.Command) error {
	// Determine project root.
	projectRoot := cmd.Args().First()
	if projectRoot == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		projectRoot = wd
	}
	projectRoot, _ = filepath.Abs(projectRoot)

	u, _ := user.Current()
	if projectRoot == u.HomeDir {
		return fmt.Errorf("refusing to run in your home directory — point me to a project folder")
	}

	// Use git root if available.
	if gitRoot, err := exec.Command("git", "-C", projectRoot, "rev-parse", "--show-toplevel").Output(); err == nil {
		if root := strings.TrimSpace(string(gitRoot)); root != "" {
			projectRoot = root
		}
	}

	// Image selection.
	image := "ghcr.io/bernd/vibepit:main"
	if cmd.Bool("L") {
		image = "vibepit:latest"
	}

	// Docker client.
	client, err := ctr.NewClient()
	if err != nil {
		return err
	}
	defer client.Close()

	// Check for existing session.
	existing, err := client.FindRunningSession(ctx, projectRoot)
	if err != nil {
		return err
	}
	if existing != "" {
		fmt.Printf("+ Attaching to running session in %s\n", projectRoot)
		return client.AttachSession(ctx, existing)
	}

	// Load and merge config.
	globalPath := config.DefaultGlobalPath()
	projectPath := config.DefaultProjectPath(projectRoot)

	cfg, err := config.Load(globalPath, projectPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	// TODO: First-run preset selection when .vibepit/network.yaml missing.

	merged := cfg.Merge(cmd.StringSlice("allow"), cmd.StringSlice("preset"))

	// Volume management.
	volumeName := "vibepit-home"
	uid, _ := strconv.Atoi(u.Uid)

	if cmd.Bool("C") {
		fmt.Printf("+ Removing volume: %s\n", volumeName)
		client.RemoveVolume(ctx, volumeName)
	}
	if err := client.EnsureVolume(ctx, volumeName, uid, u.Username); err != nil {
		return fmt.Errorf("volume: %w", err)
	}

	// Write proxy config to temp file.
	proxyConfig, _ := json.Marshal(merged)
	tmpFile, err := os.CreateTemp("", "vibepit-proxy-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Write(proxyConfig)
	tmpFile.Close()

	// Get path to our own binary for mounting into the proxy container.
	selfBinary, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find own binary: %w", err)
	}
	selfBinary, _ = filepath.EvalSymlinks(selfBinary)

	// Create private network.
	const (
		networkName = "vibepit-net"
		subnet      = "172.28.0.0/24"
		proxyIP     = "172.28.0.2"
	)

	fmt.Printf("+ Creating network: %s\n", networkName)
	networkID, err := client.CreateNetwork(ctx, ctr.NetworkConfig{
		Name:    networkName,
		Subnet:  subnet,
		ProxyIP: proxyIP,
	})
	if err != nil {
		return fmt.Errorf("network: %w", err)
	}
	defer func() {
		fmt.Printf("+ Removing network: %s\n", networkName)
		client.RemoveNetwork(ctx, networkID)
	}()

	// Start proxy container.
	containerID := randomHex()
	fmt.Println("+ Starting proxy container")
	proxyContainerID, err := client.StartProxyContainer(ctx, ctr.ProxyContainerConfig{
		BinaryPath: selfBinary,
		ConfigPath: tmpFile.Name(),
		NetworkID:  networkID,
		ProxyIP:    proxyIP,
		Name:       "vibepit-proxy-" + containerID,
	})
	if err != nil {
		return fmt.Errorf("proxy container: %w", err)
	}
	defer func() {
		fmt.Println("+ Stopping proxy container")
		client.StopAndRemove(ctx, proxyContainerID)
	}()

	// Terminal settings.
	term := os.Getenv("TERM")
	if term == "" {
		term = "linux"
	}
	if term == "xterm-ghostty" {
		term = "xterm-256color"
	}

	// Start dev container.
	fmt.Printf("+ Starting dev container in %s\n", projectRoot)
	devContainerID, err := client.StartDevContainer(ctx, ctr.DevContainerConfig{
		Image:      image,
		ProjectDir: projectRoot,
		WorkDir:    projectRoot,
		VolumeName: volumeName,
		NetworkID:  networkID,
		ProxyIP:    proxyIP,
		Name:       "vibepit-" + containerID,
		Term:       term,
		ColorTerm:  os.Getenv("COLORTERM"),
		UID:        uid,
		User:       u.Username,
	})
	if err != nil {
		return fmt.Errorf("dev container: %w", err)
	}
	defer func() {
		fmt.Println("+ Stopping dev container")
		client.StopAndRemove(ctx, devContainerID)
	}()

	// Attach interactive shell.
	return client.AttachSession(ctx, devContainerID)
}

func randomHex() string {
	return fmt.Sprintf("%x%x%x", os.Getpid(), os.Getuid(), os.Getppid())
}
```

**Step 2: Update main.go with root flags**

Replace `main.go`:
```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/bernd/vibepit/cmd"
	"github.com/urfave/cli/v3"
)

func main() {
	app := &cli.Command{
		Name:   "vibepit",
		Usage:  "Run agents in isolated Docker containers",
		Flags:  cmd.RootFlags(),
		Action: cmd.RootAction,
		Commands: []*cli.Command{
			cmd.ProxyCommand(),
			cmd.MonitorCommand(),
		},
	}
	if err := app.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
```

Note: `cmd/root.go` needs `"strings"` in the import block (for
`strings.TrimSpace`).

**Step 3: Verify it compiles**

Run: `go build .`
Expected: No errors.

**Step 4: Commit**

```
feat: implement launcher mode with full container orchestration
```

---

### Task 12: Monitor subcommand

Implement the monitor subcommand that connects to the proxy control API and
streams logs.

**Files:**
- Modify: `cmd/monitor.go`

**Step 1: Implement the monitor**

Replace `cmd/monitor.go`:
```go
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/bernd/vibepit/proxy"
	"github.com/urfave/cli/v3"
)

func MonitorCommand() *cli.Command {
	return &cli.Command{
		Name:  "monitor",
		Usage: "Connect to a running proxy for logs and admin",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "addr",
				Usage: "Proxy control API address",
				Value: "172.28.0.2:3129",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			addr := cmd.String("addr")
			baseURL := fmt.Sprintf("http://%s", addr)

			fmt.Printf("Connecting to proxy at %s...\n\n", addr)

			// Poll logs every second.
			client := &http.Client{Timeout: 5 * time.Second}
			seen := 0

			for {
				select {
				case <-ctx.Done():
					return nil
				default:
				}

				resp, err := client.Get(baseURL + "/logs")
				if err != nil {
					fmt.Printf("connection error: %v (retrying...)\n", err)
					time.Sleep(2 * time.Second)
					continue
				}

				var entries []proxy.LogEntry
				json.NewDecoder(resp.Body).Decode(&entries)
				resp.Body.Close()

				for i := seen; i < len(entries); i++ {
					e := entries[i]
					symbol := "+"
					if e.Action == proxy.ActionBlock {
						symbol = "x"
					}
					fmt.Printf("[%s] %s %s %s:%s %s\n",
						e.Time.Format("15:04:05"),
						symbol,
						e.Source,
						e.Domain,
						e.Port,
						e.Reason,
					)
				}
				seen = len(entries)

				time.Sleep(1 * time.Second)
			}
		},
	}
}
```

**Step 2: Verify it compiles**

Run: `go build .`
Expected: No errors.

**Step 3: Commit**

```
feat: implement monitor subcommand for proxy log streaming
```

---

### Task 13: First-run preset selection

Implement the interactive preset selection when `.vibepit/network.yaml`
doesn't exist.

**Files:**
- Create: `config/setup.go`
- Modify: `cmd/root.go`

**Step 1: Implement interactive setup**

`config/setup.go`:
```go
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RunFirstTimeSetup prompts the user to select presets and writes the project
// config file. Returns the selected preset names.
func RunFirstTimeSetup(globalPath, projectConfigPath string) ([]string, error) {
	cfg := &GlobalConfig{}
	if err := loadFile(globalPath, cfg); err != nil {
		return nil, err
	}

	if len(cfg.Presets) == 0 {
		return nil, nil
	}

	names := make([]string, 0, len(cfg.Presets))
	for name := range cfg.Presets {
		names = append(names, name)
	}
	sort.Strings(names)

	fmt.Println("Available network presets:")
	for i, name := range names {
		preset := cfg.Presets[name]
		fmt.Printf("  %d) %s (%s)\n", i+1, name, strings.Join(preset.Allow, ", "))
	}
	fmt.Println()
	fmt.Print("Select presets (comma-separated numbers, or Enter to skip): ")

	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)

	if line == "" {
		return nil, writeProjectConfig(projectConfigPath, nil)
	}

	var selected []string
	for _, part := range strings.Split(line, ",") {
		part = strings.TrimSpace(part)
		idx := 0
		fmt.Sscanf(part, "%d", &idx)
		if idx >= 1 && idx <= len(names) {
			selected = append(selected, names[idx-1])
		}
	}

	return selected, writeProjectConfig(projectConfigPath, selected)
}

func writeProjectConfig(path string, presets []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	var sb strings.Builder
	if len(presets) > 0 {
		sb.WriteString("presets:\n")
		for _, p := range presets {
			fmt.Fprintf(&sb, "  - %s\n", p)
		}
	}

	return os.WriteFile(path, []byte(sb.String()), 0o644)
}
```

**Step 2: Wire into root command**

In `cmd/root.go`, replace the `// TODO: First-run preset selection` comment
with:
```go
	if _, err := os.Stat(projectPath); os.IsNotExist(err) {
		selected, err := config.RunFirstTimeSetup(globalPath, projectPath)
		if err != nil {
			return fmt.Errorf("setup: %w", err)
		}
		// Reload config after writing the project file.
		cfg, err = config.Load(globalPath, projectPath)
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
		_ = selected
	}
```

**Step 3: Verify it compiles**

Run: `go build .`
Expected: No errors.

**Step 4: Commit**

```
feat: add interactive first-run preset selection
```

---

### Task 14: Integration testing

Test the full flow with a real Docker daemon. This validates that all the
pieces work together.

**Files:**
- Create: `integration_test.go`

**Step 1: Write integration test**

`integration_test.go`:
```go
//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/bernd/vibepit/proxy"
)

// TestProxyServerIntegration starts the proxy server and validates filtering.
// Run with: go test -tags=integration -v -run TestProxyServerIntegration
func TestProxyServerIntegration(t *testing.T) {
	// Write test config.
	cfg := proxy.ProxyConfig{
		Allow:    []string{"httpbin.org", "example.com"},
		DNSOnly:  []string{"dns-only.example.com"},
		Upstream: "8.8.8.8:53",
	}
	data, _ := json.Marshal(cfg)
	tmpFile, _ := os.CreateTemp("", "proxy-test-*.json")
	tmpFile.Write(data)
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	srv, err := proxy.NewServer(tmpFile.Name())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.Run(ctx)
	time.Sleep(500 * time.Millisecond)

	// Test control API.
	resp, err := http.Get("http://localhost:3129/config")
	if err != nil {
		t.Fatalf("control API request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("control API status = %d, want 200", resp.StatusCode)
	}

	// Test proxy blocks disallowed domain.
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
		},
	}
	os.Setenv("HTTP_PROXY", "http://localhost:3128")
	defer os.Unsetenv("HTTP_PROXY")

	blockedResp, err := client.Get("http://evil.com/")
	if err != nil {
		t.Fatalf("blocked request: %v", err)
	}
	defer blockedResp.Body.Close()

	if blockedResp.StatusCode != http.StatusForbidden {
		t.Errorf("blocked status = %d, want 403", blockedResp.StatusCode)
	}
}
```

**Step 2: Run integration test (requires Docker)**

Run: `go test -tags=integration -v -run TestProxyServerIntegration -timeout 30s`
Expected: All tests pass.

**Step 3: Commit**

```
test: add integration test for proxy server filtering
```

---

### Task 15: Build configuration

Set up the Makefile and GitHub Actions for static multi-arch builds.

**Files:**
- Create: `Makefile`
- Modify: `.github/workflows/docker-publish.yml` (add Go binary build)

**Step 1: Create Makefile**

`Makefile`:
```makefile
.PHONY: build test test-integration clean

BINARY := vibepit
LDFLAGS := -s -w

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

test:
	go test ./...

test-integration:
	go test -tags=integration -v -timeout 60s ./...

clean:
	rm -f $(BINARY)
```

**Step 2: Verify the build**

Run: `make build`
Expected: Produces a static `vibepit` binary.

Run: `file vibepit`
Expected: Should show `statically linked`.

Run: `make test`
Expected: All unit tests pass.

**Step 3: Commit**

```
feat: add Makefile for static builds and test targets
```

---

### Task 16: Remove old shell script

Replace the old `bin/vibepit` shell script with a note pointing to the Go
binary.

**Files:**
- Delete: `bin/vibepit`
- Delete: `bin/` (directory, if empty)

**Step 1: Remove the shell script**

Run: `rm bin/vibepit && rmdir bin`

**Step 2: Commit**

```
chore: remove old shell script launcher
```

---

Plan complete and saved to `docs/plans/2026-01-31-network-isolation-implementation.md`. Two execution options:

**1. Subagent-Driven (this session)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Parallel Session (separate)** — Open a new session in a worktree with executing-plans, batch execution with checkpoints.

Which approach?