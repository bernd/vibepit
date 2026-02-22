package proxy

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// domainPattern holds a parsed domain pattern for matching.
type domainPattern struct {
	labels        []string // split by ".", lowercased
	doubleStarIdx int      // index of "**" label, or -1 if none
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

func portMatches(pattern, port string) bool {
	if pattern == "*" {
		return true
	}
	return pattern == port
}

// HTTPRule represents a parsed allow-http entry with a domain pattern and port.
type HTTPRule struct {
	Domain domainPattern
	Port   string
}

// HTTPAllowlist holds parsed HTTP allow rules. Safe for concurrent use.
type HTTPAllowlist struct {
	rules atomic.Pointer[[]HTTPRule]
}

// NewHTTPAllowlist parses allow-http entries into an HTTPAllowlist.
// Each entry must be "domain:port" (e.g. "github.com:443", "*.example.com:*").
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

// Add parses new entries and appends them atomically.
func (al *HTTPAllowlist) Add(entries []string) error {
	if err := ValidateHTTPEntries(entries); err != nil {
		return err
	}
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
			return nil
		}
	}
}

func parseHTTPRule(entry string) HTTPRule {
	var r HTTPRule
	if idx := strings.LastIndex(entry, ":"); idx > 0 {
		r.Port = entry[idx+1:]
		entry = entry[:idx]
	}
	r.Domain = parseDomainPattern(entry)
	return r
}

// Allows checks whether a host:port pair is permitted.
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

// DNSRule represents a parsed allow-dns entry with a domain pattern.
type DNSRule struct {
	Domain domainPattern
}

// DNSAllowlist holds parsed DNS allow rules. Safe for concurrent use.
type DNSAllowlist struct {
	rules atomic.Pointer[[]DNSRule]
}

// NewDNSAllowlist parses allow-dns entries (bare domains, no ports).
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

// Add parses new DNS entries and appends them atomically.
func (al *DNSAllowlist) Add(entries []string) error {
	if err := ValidateDNSEntries(entries); err != nil {
		return err
	}
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
			return nil
		}
	}
}

func parseDNSRule(entry string) DNSRule {
	return DNSRule{Domain: parseDomainPattern(entry)}
}

// Allows checks whether a domain is permitted for DNS resolution.
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

// validateDomainPattern validates a domain pattern string.
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
// Entry format is "domain:port" where port is an exact number or '*'.
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
	if port != "*" {
		for _, ch := range port {
			if ch < '0' || ch > '9' {
				return fmt.Errorf("invalid allow entry %q: port must be a number or '*'", entry)
			}
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
// Entry format is a domain pattern without port.
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
