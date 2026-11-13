package proxy

import (
	"fmt"
	"sync"
)

// DenyRules holds enforced HTTP and DNS rules independently. Safe for concurrent use.
// Unlike DenySet, matching these rules must block even an allowed target.
type DenyRules struct {
	http *HTTPAllowlist
	dns  *DNSAllowlist

	// mu serializes adds so a repeated deny of the same entry, e.g. a
	// retried save, doesn't grow the lists every request scans.
	mu   sync.Mutex
	seen map[string]bool // "http:"/"dns:" + normalized entry
}

func NewDenyRules(httpEntries, dnsEntries []string) (*DenyRules, error) {
	http, _ := NewHTTPAllowlist(nil)
	dns, _ := NewDNSAllowlist(nil)
	d := &DenyRules{http: http, dns: dns, seen: make(map[string]bool)}
	if err := d.AddHTTP(httpEntries); err != nil {
		return nil, err
	}
	if err := d.AddDNS(dnsEntries); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *DenyRules) AddHTTP(entries []string) error {
	if err := ValidateHTTPEntries(entries); err != nil {
		return fmt.Errorf("deny-http: %w", err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	fresh := d.unseen("http:", entries)
	if len(fresh) == 0 {
		return nil
	}
	if err := d.http.Add(fresh); err != nil {
		return fmt.Errorf("deny-http: %w", err)
	}
	d.markSeen("http:", fresh)
	return nil
}

func (d *DenyRules) AddDNS(entries []string) error {
	if err := ValidateDNSEntries(entries); err != nil {
		return fmt.Errorf("deny-dns: %w", err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	fresh := d.unseen("dns:", entries)
	if len(fresh) == 0 {
		return nil
	}
	if err := d.dns.Add(fresh); err != nil {
		return fmt.Errorf("deny-dns: %w", err)
	}
	d.markSeen("dns:", fresh)
	return nil
}

// unseen returns the entries not added before, each once, compared by
// EntryKey as rule matching does. Caller must hold d.mu, or own d exclusively.
func (d *DenyRules) unseen(prefix string, entries []string) []string {
	var fresh []string
	batch := make(map[string]bool, len(entries))
	for _, entry := range entries {
		key := prefix + EntryKey(entry)
		if !d.seen[key] && !batch[key] {
			batch[key] = true
			fresh = append(fresh, entry)
		}
	}
	return fresh
}

func (d *DenyRules) markSeen(prefix string, entries []string) {
	for _, entry := range entries {
		d.seen[prefix+EntryKey(entry)] = true
	}
}

func (d *DenyRules) Denied(t Target) bool {
	if d == nil {
		return false
	}
	// Normalized here as well, so no caller can bypass a deny by spelling.
	host := normalizeHost(t.Host)
	switch t.Source {
	case SourceProxy:
		return d.http.Allows(host, t.Port)
	case SourceDNS:
		return d.dns.Allows(host)
	default:
		return false
	}
}
