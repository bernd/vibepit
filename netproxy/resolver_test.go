package netproxy

import (
	"context"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

// Bind UDP and TCP at the same port so the production resolver can retry a
// truncated reply against the same upstream.
func localDNS(t *testing.T, handler dns.HandlerFunc) netip.AddrPort {
	t.Helper()
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	udp, err := net.ListenPacket("udp4", tcp.Addr().String())
	if err != nil {
		tcp.Close()
		t.Fatal(err)
	}
	for _, s := range []*dns.Server{{Listener: tcp}, {PacketConn: udp}} {
		ready := make(chan struct{})
		s.NotifyStartedFunc = func() { close(ready) }
		s.Handler = handler
		done := make(chan error, 1)
		go func() { done <- s.ActivateAndServe() }()
		select {
		case <-ready:
		case err := <-done:
			t.Fatalf("DNS server startup: %v", err)
		}
		t.Cleanup(func() { require.NoError(t, s.Shutdown()); require.NoError(t, <-done) })
	}
	return netip.MustParseAddrPort(tcp.Addr().String())
}

func TestDNSResolutionUsesConnectedAnswers(t *testing.T) {
	var udp, tcp atomic.Int32
	up := localDNS(t, func(w dns.ResponseWriter, q *dns.Msg) {
		m := new(dns.Msg).SetReply(q)
		if w.LocalAddr().Network() == "udp" {
			udp.Add(1)
			m.Truncated = true
		} else {
			tcp.Add(1)
			var answers []string
			switch q.Question[0].Name {
			case "origin.test.":
				answers = []string{"origin.test. 10 IN CNAME first.test."}
			case "first.test.":
				answers = []string{"unrelated.test. 10 IN A 192.0.2.99", "end.test. 10 IN A 192.0.2.2", "FIRST.test. 10 IN CNAME end.test.", "end.test. 10 IN A 192.0.2.3"}
			}
			for _, a := range answers {
				rr, err := dns.NewRR(a)
				if err != nil {
					t.Error(err)
					return
				}
				m.Answer = append(m.Answer, rr)
			}
		}
		if err := w.WriteMsg(m); err != nil {
			t.Error(err)
		}
	})
	resolver, err := NewDNSResolver(up)
	require.NoError(t, err)
	got, err := resolver.ResolveIPv4(t.Context(), "Origin.TEST.")
	require.NoError(t, err)
	require.Equal(t, []string{"first.test", "end.test"}, got.Aliases)
	require.Equal(t, []netip.Addr{netip.MustParseAddr("192.0.2.2"), netip.MustParseAddr("192.0.2.3")}, got.Addresses)
	require.EqualValues(t, 2, udp.Load())
	require.EqualValues(t, 2, tcp.Load())
}

func TestDNSResolutionFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rcode   int
		answers []string
	}{
		{"no address", dns.RcodeSuccess, []string{"unrelated.test. 10 IN A 192.0.2.2"}},
		{"cycle", dns.RcodeSuccess, []string{"origin.test. 10 IN CNAME end.test.", "end.test. 10 IN CNAME origin.test."}},
		{"ambiguous", dns.RcodeSuccess, []string{"origin.test. 10 IN CNAME end.test.", "origin.test. 10 IN A 192.0.2.2"}},
		{"two alias targets", dns.RcodeSuccess, []string{"origin.test. 10 IN CNAME first.test.", "origin.test. 10 IN CNAME second.test."}},
		{"not found", dns.RcodeNameError, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := localDNS(t, func(w dns.ResponseWriter, q *dns.Msg) {
				m := new(dns.Msg).SetRcode(q, tc.rcode)
				for _, s := range tc.answers {
					rr, err := dns.NewRR(s)
					if err != nil {
						t.Error(err)
						return
					}
					m.Answer = append(m.Answer, rr)
				}
				w.WriteMsg(m)
			})
			resolver, err := NewDNSResolver(up)
			require.NoError(t, err)
			got, err := resolver.ResolveIPv4(t.Context(), "origin.test")
			require.Error(t, err)
			require.Empty(t, got)
		})
	}
}

func TestDNSContextCancellation(t *testing.T) {
	started := make(chan struct{})
	up := localDNS(t, func(w dns.ResponseWriter, q *dns.Msg) { close(started) })
	resolver, err := NewDNSResolver(up)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := resolver.ResolveIPv4(ctx, "origin.test"); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("no DNS query")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("lookup survived cancellation")
	}
}

func TestDNSResolverRequiresConcreteServer(t *testing.T) {
	for _, endpoint := range []string{"0.0.0.0:53", "[::]:53", "[::ffff:0.0.0.0]:53", "[::%lo]:53"} {
		_, err := NewDNSResolver(netip.MustParseAddrPort(endpoint))
		require.Errorf(t, err, "%s", endpoint)
	}
}
