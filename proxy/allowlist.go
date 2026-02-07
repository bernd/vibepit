package proxy

import (
	"strings"
	"sync/atomic"
)

// Rule represents a single parsed allowlist entry. Port-specific rules
// restrict matching to a single port; portless rules match any port.
// Wildcard rules (*.domain) match subdomains only, not the apex domain.
// Non-wildcard rules match both the exact domain and all its subdomains.
type Rule struct {
	Pattern  string
	Port     string
	Wildcard bool
	Domain   string
}

// Allowlist holds parsed rules and provides matching against host:port pairs.
// It is safe for concurrent use; rules can be added at runtime via Add.
type Allowlist struct {
	rules atomic.Pointer[[]Rule]
}

// NewAllowlist parses a list of allowlist entries into an Allowlist.
// Each entry may be a bare domain ("github.com"), a wildcard ("*.example.com"),
// or include a port suffix ("api.stripe.com:443", "*.cdn.example.com:443").
func NewAllowlist(entries []string) *Allowlist {
	rules := make([]Rule, 0, len(entries))
	for _, entry := range entries {
		r := parseRule(entry)
		rules = append(rules, r)
	}
	al := &Allowlist{}
	al.rules.Store(&rules)
	return al
}

// Add parses new entries and appends them to the allowlist atomically.
func (al *Allowlist) Add(entries []string) {
	newRules := make([]Rule, 0, len(entries))
	for _, entry := range entries {
		newRules = append(newRules, parseRule(entry))
	}

	for {
		current := al.rules.Load()
		merged := make([]Rule, len(*current), len(*current)+len(newRules))
		copy(merged, *current)
		merged = append(merged, newRules...)
		if al.rules.CompareAndSwap(current, &merged) {
			return
		}
	}
}

func parseRule(entry string) Rule {
	var r Rule
	// Split off a trailing numeric port if present.
	if idx := strings.LastIndex(entry, ":"); idx > 0 {
		possiblePort := entry[idx+1:]
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
// Rules are evaluated in specificity order so that port-specific exact matches
// take precedence over broader wildcard or portless rules. When a host matches
// a rule's domain pattern, that rule "claims" the host -- if the port doesn't
// match, broader rules won't be consulted.
func (al *Allowlist) Allows(host, port string) bool {
	if host == "" {
		return false
	}
	host = strings.ToLower(host)

	// Find the best (most specific) matching rule for this host.
	// Specificity tiers (highest first):
	//   1. Exact hostname with port
	//   2. Exact hostname, any port (also matches subdomains)
	//   3. Wildcard with port (subdomains only)
	//   4. Wildcard, any port (subdomains only)
	//
	// Within each tier we pick the rule whose domain is longest (most
	// specific). Once a domain pattern matches the host, we check the port
	// constraint. A port mismatch is a rejection -- we don't fall through
	// to less specific tiers.

	type candidate struct {
		specificity int
		portOK      bool
	}
	var best *candidate

	rules := *al.rules.Load()
	for _, r := range rules {
		var tier int
		var matches bool
		var portOK bool

		switch {
		case !r.Wildcard && r.Port != "":
			tier = 1
			matches = host == r.Domain
			portOK = port == r.Port
		case !r.Wildcard && r.Port == "":
			tier = 2
			matches = host == r.Domain || strings.HasSuffix(host, "."+r.Domain)
			portOK = true
		case r.Wildcard && r.Port != "":
			tier = 3
			// A wildcard rule claims its subdomains AND its apex (to
			// shadow broader rules), but only allows actual subdomains.
			if isSubdomainOf(host, r.Domain) {
				matches = true
				portOK = port == r.Port
			} else if host == r.Domain {
				// Apex is claimed but always rejected (wildcard = subdomains only).
				matches = true
				portOK = false
			}
		default: // wildcard, no port
			tier = 4
			if isSubdomainOf(host, r.Domain) {
				matches = true
				portOK = true
			} else if host == r.Domain {
				// Apex claimed but rejected.
				matches = true
				portOK = false
			}
		}

		if !matches {
			continue
		}

		// Higher specificity = lower tier number. Among same-tier rules,
		// longer domain = more specific.
		specificity := (5-tier)*10000 + len(r.Domain)

		if best == nil || specificity > best.specificity {
			best = &candidate{specificity: specificity, portOK: portOK}
		}
	}

	return best != nil && best.portOK
}

// AllowsPort is like Allows but rejects portless rules. A portless entry
// like "host.vibepit" will NOT grant access — only a port-specific entry
// like "host.vibepit:8002" will match. This prevents an accidentally
// broad allowlist entry from bypassing port restrictions.
func (al *Allowlist) AllowsPort(host, port string) bool {
	if host == "" || port == "" {
		return false
	}
	host = strings.ToLower(host)

	rules := *al.rules.Load()
	for _, r := range rules {
		if r.Port == "" {
			continue // skip portless rules
		}
		if r.Port != port {
			continue
		}
		if r.Wildcard {
			if isSubdomainOf(host, r.Domain) {
				return true
			}
		} else {
			if host == r.Domain {
				return true
			}
		}
	}
	return false
}

// AllowsDNS checks whether a domain is permitted for DNS resolution.
// Port constraints are ignored because DNS operates before a port is known.
func (al *Allowlist) AllowsDNS(host string) bool {
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
