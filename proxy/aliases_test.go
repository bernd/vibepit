package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeUpstream answers every query for cdn.allowed.com with a CNAME chain
// through edge.cdn.net to telemetry.example.com and a public address, and
// fails queries for broken.example.com.
func fakeUpstream(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &dns.Server{PacketConn: pc, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		switch r.Question[0].Name {
		case "cdn.allowed.com.":
			hdr := func(name string, t uint16) dns.RR_Header {
				return dns.RR_Header{Name: name, Rrtype: t, Class: dns.ClassINET, Ttl: 60}
			}
			m.Answer = []dns.RR{
				&dns.CNAME{Hdr: hdr("cdn.allowed.com.", dns.TypeCNAME), Target: "edge.cdn.net."},
				&dns.CNAME{Hdr: hdr("edge.cdn.net.", dns.TypeCNAME), Target: "Telemetry.Example.com."},
				&dns.A{Hdr: hdr("Telemetry.Example.com.", dns.TypeA), A: net.ParseIP("93.184.216.34")},
			}
		case "broken.example.com.":
			m.Rcode = dns.RcodeServerFailure
		}
		_ = w.WriteMsg(m)
	})}
	go srv.ActivateAndServe()
	t.Cleanup(func() { _ = srv.Shutdown() })
	return pc.LocalAddr().String()
}

func TestLookupAliases(t *testing.T) {
	upstream := fakeUpstream(t)
	names, err := lookupAliases(context.Background(), upstream, "cdn.allowed.com")
	require.NoError(t, err)
	assert.Contains(t, names, "edge.cdn.net")
	assert.Contains(t, names, "telemetry.example.com")
	_, err = lookupAliases(context.Background(), upstream, "broken.example.com")
	assert.Error(t, err, "a failed lookup is an error, so the caller fails closed")
	names, err = lookupAliases(context.Background(), upstream, "plain.example.com")
	require.NoError(t, err)
	assert.Empty(t, names)
}

func TestDNSRefusesAliasOfDeniedName(t *testing.T) {
	upstream := fakeUpstream(t)
	for _, deny := range []string{"telemetry.example.com", "edge.cdn.net", "other.example.com"} {
		t.Run(deny, func(t *testing.T) {
			rules, err := NewDenyRules(nil, []string{deny})
			require.NoError(t, err)
			al, _ := NewDNSAllowlist([]string{"cdn.allowed.com"})
			log := NewLogBuffer(10)
			s := NewDNSServer(NewPolicy(nil, al, rules), NewCIDRBlocker(nil, nil), log, upstream)
			addr, stop := s.ListenAndServeTest()
			defer stop()
			m := new(dns.Msg)
			m.SetQuestion("cdn.allowed.com.", dns.TypeA)
			r, _, err := new(dns.Client).Exchange(m, addr)
			require.NoError(t, err)
			entries := log.Entries()
			require.Len(t, entries, 1)
			if deny == "other.example.com" {
				assert.Equal(t, dns.RcodeSuccess, r.Rcode)
				assert.Equal(t, ActionAllow, entries[0].Action)
				return
			}
			assert.Equal(t, dns.RcodeNameError, r.Rcode)
			assert.Equal(t, BlockDenied, entries[0].Code)
			assert.Contains(t, entries[0].Reason, "alias of denied domain "+deny)
		})
	}
}

func TestHTTPRefusesAliasOfDeniedName(t *testing.T) {
	for _, tc := range []struct {
		name, deny string
		aliasErr   error
		wantCode   BlockCode
	}{
		{"denied alias", "telemetry.example.com:443", nil, BlockDenied},
		{"wildcard denied alias", "*.example.com:*", nil, BlockDenied},
		{"other port", "telemetry.example.com:80", nil, BlockResolutionFailed},
		{"lookup fails closed", "unrelated.example.com:443", errors.New("upstream down"), BlockResolutionFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules, err := NewDenyRules([]string{tc.deny}, nil)
			require.NoError(t, err)
			al, _ := NewHTTPAllowlist([]string{"cdn.allowed.com:443"})
			p := NewHTTPProxy(NewPolicy(al, nil, rules), NewCIDRBlocker(nil, nil), NewLogBuffer(10), "127.0.0.1:1")
			p.aliases = func(_ context.Context, host string) ([]string, error) {
				require.Equal(t, "cdn.allowed.com", host)
				return []string{"cdn.allowed.com", "telemetry.example.com"}, tc.aliasErr
			}
			r := p.checkRequest(context.Background(), "cdn.allowed.com", "443")
			assert.Equal(t, tc.wantCode, r.code, r.reason)
			if tc.wantCode == BlockDenied {
				assert.Contains(t, r.reason, "alias of denied domain telemetry.example.com")
			}
		})
	}
}

// Without HTTP deny rules nothing could match an alias, so requests pay for
// no extra lookup; neither do IP literals.
func TestHTTPSkipsAliasLookupWhenUseless(t *testing.T) {
	dnsOnly, err := NewDenyRules(nil, []string{"telemetry.example.com"})
	require.NoError(t, err)
	withHTTP, err := NewDenyRules([]string{"telemetry.example.com:443"}, nil)
	require.NoError(t, err)
	for _, tc := range []struct {
		name  string
		rules *DenyRules
		host  string
	}{
		{"no rules", nil, "cdn.allowed.com"},
		{"dns rules only", dnsOnly, "cdn.allowed.com"},
		{"ip literal", withHTTP, "93.184.216.34"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			al, _ := NewHTTPAllowlist([]string{tc.host + ":443"})
			p := NewHTTPProxy(NewPolicy(al, nil, tc.rules), NewCIDRBlocker(nil, nil), NewLogBuffer(10), "127.0.0.1:1")
			p.aliases = func(context.Context, string) ([]string, error) {
				t.Error("alias lookup should be skipped")
				return nil, nil
			}
			p.checkRequest(context.Background(), tc.host, "443")
		})
	}
}

// A client that leaves during the alias lookup sees no block, so none is
// logged; a lookup that fails for a client still waiting is.
func TestAliasLookupAbandonedNotLogged(t *testing.T) {
	for _, abandoned := range []bool{true, false} {
		t.Run(fmt.Sprint("abandoned=", abandoned), func(t *testing.T) {
			rules, err := NewDenyRules([]string{"telemetry.example.com:443"}, nil)
			require.NoError(t, err)
			al, _ := NewHTTPAllowlist([]string{"cdn.allowed.com:443"})
			log := NewLogBuffer(10)
			p := NewHTTPProxy(NewPolicy(al, nil, rules), NewCIDRBlocker(nil, nil), log, "127.0.0.1:1")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p.aliases = func(lookupCtx context.Context, _ string) ([]string, error) {
				if abandoned {
					cancel()
					<-lookupCtx.Done()
					return nil, lookupCtx.Err()
				}
				return nil, errors.New("upstream down")
			}
			r := p.checkRequest(ctx, "cdn.allowed.com", "443")
			assert.Equal(t, BlockResolutionFailed, r.code)
			if abandoned {
				assert.Empty(t, log.Entries())
			} else {
				require.Len(t, log.Entries(), 1)
			}
		})
	}
}
