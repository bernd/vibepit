package proxy

import "errors"

// Decision is what the policy says about a target.
type Decision int

const (
	// NotAllowlisted: no allow rule admits the target. An allow would fix it.
	NotAllowlisted Decision = iota
	// Allowed: an allow rule admits the target and no deny rule matches it.
	Allowed
	// Denied: an enforced deny rule matches the target, whatever allows it.
	Denied
)

// Policy decides which targets the proxy lets through: the HTTP and DNS
// allowlists, overridden by the enforced deny rules. Every component asks it
// instead of combining the pieces itself, so "deny wins over allow" lives in
// one place and no new allow check can miss it. Any part may be nil, meaning
// empty. Safe for concurrent use.
type Policy struct {
	http  *HTTPAllowlist
	dns   *DNSAllowlist
	rules *DenyRules
}

func NewPolicy(http *HTTPAllowlist, dns *DNSAllowlist, rules *DenyRules) *Policy {
	return &Policy{http: http, dns: dns, rules: rules}
}

// Decide checks the deny rules first, then the allowlist for t's source.
func (p *Policy) Decide(t Target) Decision {
	if p.rules.Denied(t) {
		return Denied
	}
	if p.allowlisted(t) {
		return Allowed
	}
	return NotAllowlisted
}

// Denied reports whether an enforced deny rule matches t.
func (p *Policy) Denied(t Target) bool { return p.rules.Denied(t) }

// allowlisted reports whether the allowlist admits t, ignoring deny rules.
// Only Decide may use it.
func (p *Policy) allowlisted(t Target) bool {
	switch t.Source {
	case SourceProxy:
		return p.http != nil && p.http.Allows(t.Host, t.Port)
	case SourceDNS:
		return p.dns != nil && p.dns.Allows(t.Host)
	}
	return false
}

// AddAllowHTTP and AddAllowDNS extend the allowlists at runtime.
func (p *Policy) AddAllowHTTP(entries []string) error {
	if p.http == nil {
		return errNoAllowlist
	}
	return p.http.Add(entries)
}

func (p *Policy) AddAllowDNS(entries []string) error {
	if p.dns == nil {
		return errNoAllowlist
	}
	return p.dns.Add(entries)
}

var errNoAllowlist = errors.New("no allowlist configured")

// Rules returns the enforced deny rules, nil when there are none to add to.
func (p *Policy) Rules() *DenyRules { return p.rules }
