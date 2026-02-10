package proxy

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// HTTPRule represents a parsed allow-http entry with a domain pattern and port glob.
type HTTPRule struct {
	Domain   string
	Port     string
	Wildcard bool
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
		if portGlobMatch(r.Port, port) && domainMatches(host, r.Domain, r.Wildcard) {
			return true
		}
	}
	return false
}

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

// Add parses new DNS entries and appends them atomically.
func (al *DNSAllowlist) Add(entries []string) {
	newRules := make([]DNSRule, 0, len(entries))
	for _, entry := range entries {
		newRules = append(newRules, parseDNSRule(entry))
	}

	for {
		current := al.rules.Load()
		merged := make([]DNSRule, len(*current), len(*current)+len(newRules))
		copy(merged, *current)
		merged = append(merged, newRules...)
		if al.rules.CompareAndSwap(current, &merged) {
			return
		}
	}
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
		if domainMatches(host, r.Domain, r.Wildcard) {
			return true
		}
	}
	return false
}

// portGlobMatch reports whether port matches the glob pattern.
// The only special character is '*', which matches any sequence of characters.
func portGlobMatch(pattern, port string) bool {
	for len(pattern) > 0 {
		if pattern[0] == '*' {
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

func domainMatches(host, domain string, wildcard bool) bool {
	if wildcard {
		return isSubdomainOf(host, domain)
	}
	return host == domain
}

func isSubdomainOf(host, domain string) bool {
	return strings.HasSuffix(host, "."+domain)
}

// ValidateHTTPEntries validates all entries and returns the first error.
func ValidateHTTPEntries(entries []string) error {
	for _, entry := range entries {
		if err := ValidateHTTPEntry(entry); err != nil {
			return err
		}
	}
	return nil
}

// ValidateHTTPEntry validates a single allow-http entry.
// Entry format is "domain:port-pattern" where port pattern allows digits and '*'.
func ValidateHTTPEntry(entry string) error {
	if entry == "" {
		return fmt.Errorf("invalid allow entry: empty string")
	}

	idx := strings.LastIndex(entry, ":")
	if idx <= 0 || idx == len(entry)-1 {
		return fmt.Errorf("invalid allow entry %q: expected domain:port-pattern", entry)
	}

	domain := entry[:idx]
	port := entry[idx+1:]

	if strings.HasPrefix(domain, "*.") && len(domain) == 2 {
		return fmt.Errorf("invalid allow entry %q: wildcard domain must include a suffix", entry)
	}
	if strings.Contains(domain, ":") {
		return fmt.Errorf("invalid allow entry %q: domain must not contain ':'", entry)
	}
	if strings.Contains(domain, " ") || strings.Contains(port, " ") {
		return fmt.Errorf("invalid allow entry %q: spaces are not allowed", entry)
	}

	for _, ch := range port {
		if ch == '*' {
			continue
		}
		if ch < '0' || ch > '9' {
			return fmt.Errorf("invalid allow entry %q: port pattern must contain only digits or '*'", entry)
		}
	}

	return nil
}

// ValidateDNSEntries validates all allow-dns entries and returns the first error.
func ValidateDNSEntries(entries []string) error {
	for _, entry := range entries {
		if err := ValidateDNSEntry(entry); err != nil {
			return err
		}
	}
	return nil
}

// ValidateDNSEntry validates a single allow-dns entry.
// Entry format is "domain" or "*.domain" and must not include a port.
func ValidateDNSEntry(entry string) error {
	if entry == "" {
		return fmt.Errorf("invalid allow-dns entry: empty string")
	}
	if strings.Contains(entry, ":") {
		return fmt.Errorf("invalid allow-dns entry %q: ports are not allowed", entry)
	}
	if strings.HasPrefix(entry, "*.") && len(entry) == 2 {
		return fmt.Errorf("invalid allow-dns entry %q: wildcard domain must include a suffix", entry)
	}
	if strings.Contains(entry, " ") {
		return fmt.Errorf("invalid allow-dns entry %q: spaces are not allowed", entry)
	}
	return nil
}
