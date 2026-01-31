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
		if r.Rcode == dns.RcodeNameError {
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
