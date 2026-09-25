package proxy

import (
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDNSServer(t *testing.T) {
	al, err := NewDNSAllowlist([]string{"allowed.example.com", "dnsonly.example.com"})
	require.NoError(t, err)
	blocker := NewCIDRBlocker(nil, nil)
	log := NewLogBuffer(100)

	srv := NewDNSServer(al, blocker, log, "8.8.8.8:53")
	addr, cleanup := srv.ListenAndServeTest()
	defer cleanup()

	// Give server time to start.
	time.Sleep(50 * time.Millisecond)

	c := new(dns.Client)

	t.Run("blocks disallowed domain", func(t *testing.T) {
		m := new(dns.Msg)
		m.SetQuestion("evil.com.", dns.TypeA)

		r, _, err := c.Exchange(m, addr)
		require.NoError(t, err)
		assert.Equal(t, dns.RcodeNameError, r.Rcode)
	})

	t.Run("allows domain in allowlist", func(t *testing.T) {
		m := new(dns.Msg)
		m.SetQuestion("allowed.example.com.", dns.TypeA)

		r, _, err := c.Exchange(m, addr)
		require.NoError(t, err)
		if r.Rcode == dns.RcodeNameError {
			t.Skip("domain may not resolve in test environment")
		}
	})

	t.Run("allows domain in dns-only list", func(t *testing.T) {
		m := new(dns.Msg)
		m.SetQuestion("dnsonly.example.com.", dns.TypeA)

		r, _, err := c.Exchange(m, addr)
		require.NoError(t, err)
		if r.Rcode == dns.RcodeNameError {
			t.Skip("domain may not resolve in test environment")
		}
	})

	t.Run("logs blocked query", func(t *testing.T) {
		entries := log.Entries()
		found := false
		for _, e := range entries {
			if e.Domain == "evil.com" && e.Action == ActionBlock && e.Source == SourceDNS {
				assert.Equal(t, CauseAllowlist, e.Cause)
				found = true
				break
			}
		}
		assert.True(t, found, "blocked DNS query not found in log")
	})
}

func TestDNSServerBlockedIPCause(t *testing.T) {
	// The upstream answers with a private address, which the CIDR check
	// must block.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	up := &dns.Server{PacketConn: pc, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		m.Answer = append(m.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: r.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
			A:   net.ParseIP("10.0.0.1"),
		})
		w.WriteMsg(m)
	})}
	go up.ActivateAndServe()
	defer up.Shutdown()

	al, err := NewDNSAllowlist([]string{"rebind.example.com"})
	require.NoError(t, err)
	log := NewLogBuffer(10)
	srv := NewDNSServer(al, NewCIDRBlocker(nil, nil), log, pc.LocalAddr().String())
	addr, cleanup := srv.ListenAndServeTest()
	defer cleanup()
	time.Sleep(50 * time.Millisecond)

	m := new(dns.Msg)
	m.SetQuestion("rebind.example.com.", dns.TypeA)
	r, _, err := new(dns.Client).Exchange(m, addr)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeNameError, r.Rcode)

	entries := log.Entries()
	require.Len(t, entries, 1)
	assert.Equal(t, ActionBlock, entries[0].Action)
	assert.Equal(t, CauseBlockedIP, entries[0].Cause)
}

func TestDNSHostVibepit(t *testing.T) {
	al, err := NewDNSAllowlist(nil)
	require.NoError(t, err)
	blocker := NewCIDRBlocker(nil, nil)
	log := NewLogBuffer(100)

	proxyIP := net.ParseIP("10.42.0.2")

	srv := NewDNSServer(al, blocker, log, "8.8.8.8:53")
	srv.SetProxyIP(proxyIP)
	addr, cleanup := srv.ListenAndServeTest()
	defer cleanup()

	time.Sleep(50 * time.Millisecond)

	c := new(dns.Client)

	t.Run("resolves host.vibepit to proxy IP", func(t *testing.T) {
		m := new(dns.Msg)
		m.SetQuestion("host.vibepit.", dns.TypeA)

		r, _, err := c.Exchange(m, addr)
		require.NoError(t, err)
		require.Equal(t, dns.RcodeSuccess, r.Rcode)
		require.NotEmpty(t, r.Answer, "expected at least one answer record")

		a, ok := r.Answer[0].(*dns.A)
		require.True(t, ok, "expected A record, got %T", r.Answer[0])
		assert.True(t, a.A.Equal(proxyIP), "A record = %v, want %v", a.A, proxyIP)
	})

	t.Run("host.vibepit bypasses CIDR blocking", func(t *testing.T) {
		if !blocker.IsBlocked(proxyIP) {
			t.Skip("proxy IP is not in blocked CIDR range, test not meaningful")
		}

		m := new(dns.Msg)
		m.SetQuestion("host.vibepit.", dns.TypeA)

		r, _, err := c.Exchange(m, addr)
		require.NoError(t, err)
		assert.Equal(t, dns.RcodeSuccess, r.Rcode, "CIDR blocking should be bypassed")
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
		assert.True(t, found, "host.vibepit DNS query not found in log")
	})
}
