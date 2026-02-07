package proxy

import (
	"net"
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

func TestDNSHostVibepit(t *testing.T) {
	al := NewAllowlist(nil)
	dnsOnly := NewAllowlist(nil)
	blocker := NewCIDRBlocker(nil)
	log := NewLogBuffer(100)

	proxyIP := net.ParseIP("10.42.0.2")

	srv := NewDNSServer(al, dnsOnly, blocker, log, "8.8.8.8:53")
	srv.SetProxyIP(proxyIP)
	addr, cleanup := srv.ListenAndServeTest()
	defer cleanup()

	time.Sleep(50 * time.Millisecond)

	c := new(dns.Client)

	t.Run("resolves host.vibepit to proxy IP", func(t *testing.T) {
		m := new(dns.Msg)
		m.SetQuestion("host.vibepit.", dns.TypeA)

		r, _, err := c.Exchange(m, addr)
		if err != nil {
			t.Fatalf("DNS exchange error: %v", err)
		}
		if r.Rcode != dns.RcodeSuccess {
			t.Fatalf("rcode = %d, want SUCCESS (%d)", r.Rcode, dns.RcodeSuccess)
		}
		if len(r.Answer) == 0 {
			t.Fatal("expected at least one answer record")
		}
		a, ok := r.Answer[0].(*dns.A)
		if !ok {
			t.Fatalf("expected A record, got %T", r.Answer[0])
		}
		if !a.A.Equal(proxyIP) {
			t.Errorf("A record = %v, want %v", a.A, proxyIP)
		}
	})

	t.Run("host.vibepit bypasses CIDR blocking", func(t *testing.T) {
		if !blocker.IsBlocked(proxyIP) {
			t.Skip("proxy IP is not in blocked CIDR range, test not meaningful")
		}

		m := new(dns.Msg)
		m.SetQuestion("host.vibepit.", dns.TypeA)

		r, _, err := c.Exchange(m, addr)
		if err != nil {
			t.Fatalf("DNS exchange error: %v", err)
		}
		if r.Rcode != dns.RcodeSuccess {
			t.Errorf("rcode = %d, want SUCCESS (%d) — CIDR blocking should be bypassed", r.Rcode, dns.RcodeSuccess)
		}
	})

	t.Run("host.vibepit is logged", func(t *testing.T) {
		entries := log.Entries()
		found := false
		for _, e := range entries {
			if e.Domain == "host.vibepit" && e.Action == ActionAllow && e.Source == SourceDNS {
				found = true
				break
			}
		}
		if !found {
			t.Error("host.vibepit DNS query not found in log")
		}
	})
}
