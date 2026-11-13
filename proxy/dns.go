package proxy

import (
	"net"
	"time"

	mdns "github.com/miekg/dns"
)

// DNSServer is a filtering DNS server that checks queries against an allowlist
// and verifies resolved IPs against a CIDR blocklist.
type DNSServer struct {
	policy   *Policy
	cidr     *CIDRBlocker
	log      *LogBuffer
	upstream string
	proxyIP  net.IP
}

// SetProxyIP sets the IP address that host.vibepit will resolve to.
func (s *DNSServer) SetProxyIP(ip net.IP) {
	s.proxyIP = ip
}

// NewDNSServer creates the filtering DNS server, which resolves what policy
// allows. It's a parameter, not a setter, so no construction path can forget
// the deny rules in it.
func NewDNSServer(policy *Policy, cidr *CIDRBlocker, log *LogBuffer, upstream string) *DNSServer {
	return &DNSServer{
		policy:   policy,
		cidr:     cidr,
		log:      log,
		upstream: upstream,
	}
}

func (s *DNSServer) handleFailed(w mdns.ResponseWriter, r *mdns.Msg) {
	_ = w.WriteMsg(new(mdns.Msg).SetRcode(r, mdns.RcodeServerFailure))
}

func (s *DNSServer) handler() mdns.Handler {
	return mdns.HandlerFunc(func(w mdns.ResponseWriter, r *mdns.Msg) {
		if len(r.Question) == 0 {
			s.handleFailed(w, r)
			return
		}
		// Only the first question is checked, and the whole message goes
		// upstream, so a second question would skip the allow and deny
		// checks. Resolvers send one question per query.
		if len(r.Question) > 1 {
			_ = w.WriteMsg(new(mdns.Msg).SetRcode(r, mdns.RcodeFormatError))
			return
		}

		domain := normalizeHost(r.Question[0].Name)

		decision := s.policy.Decide(Target{Source: SourceDNS, Host: domain})
		if decision == Denied {
			s.log.Add(LogEntry{Time: time.Now(), Domain: domain, Source: SourceDNS, Action: ActionBlock, Code: BlockDenied, Reason: deniedResult.reason})
			w.WriteMsg(new(mdns.Msg).SetRcode(r, mdns.RcodeNameError))
			return
		}

		// Synthetic response for host.vibepit — resolves to the proxy IP
		// without upstream forwarding or CIDR validation. Other query types
		// (resolvers send AAAA alongside A) get an empty answer instead of
		// falling through to the allowlist and being logged as blocked.
		if domain == "host.vibepit" && s.proxyIP != nil {
			s.log.Add(LogEntry{
				Time:   time.Now(),
				Domain: domain,
				Action: ActionAllow,
				Source: SourceDNS,
			})
			m := new(mdns.Msg)
			m.SetReply(r)
			if r.Question[0].Qtype == mdns.TypeA {
				m.Answer = append(m.Answer, &mdns.A{
					Hdr: mdns.RR_Header{
						Name:   r.Question[0].Name,
						Rrtype: mdns.TypeA,
						Class:  mdns.ClassINET,
						Ttl:    60,
					},
					A: s.proxyIP,
				})
			}
			w.WriteMsg(m)
			return
		}

		if decision != Allowed {
			s.log.Add(LogEntry{
				Time:   time.Now(),
				Domain: domain,
				Action: ActionBlock,
				Source: SourceDNS,
				Reason: reasonNotAllowlisted,
				Code:   BlockNotAllowlisted,
			})
			m := new(mdns.Msg)
			m.SetRcode(r, mdns.RcodeNameError)
			w.WriteMsg(m)
			return
		}

		// Forward to upstream resolver.
		c := new(mdns.Client)
		resp, _, err := c.Exchange(r, s.upstream)
		if err != nil {
			s.handleFailed(w, r)
			return
		}

		// An allowed name that aliases a denied one must not resolve.
		if alias, denied := s.policy.DeniedAlias(SourceDNS, cnameTargets(resp), ""); denied {
			res := aliasDeniedResult(alias)
			s.log.Add(LogEntry{Time: time.Now(), Domain: domain, Source: SourceDNS, Action: ActionBlock, Code: res.code, Reason: res.reason})
			w.WriteMsg(new(mdns.Msg).SetRcode(r, mdns.RcodeNameError))
			return
		}
		// Reject responses that resolve to blocked IP ranges (e.g. private networks).
		if s.hasBlockedIP(resp) {
			s.log.Add(LogEntry{
				Time:   time.Now(),
				Domain: domain,
				Action: ActionBlock,
				Source: SourceDNS,
				Reason: "resolved IP in blocked CIDR range",
				Code:   BlockCIDR,
			})
			m := new(mdns.Msg)
			m.SetRcode(r, mdns.RcodeNameError)
			w.WriteMsg(m)
			return
		}

		s.log.Add(LogEntry{
			Time:   time.Now(),
			Domain: domain,
			Action: ActionAllow,
			Source: SourceDNS,
		})
		w.WriteMsg(resp)
	})
}

func (s *DNSServer) hasBlockedIP(msg *mdns.Msg) bool {
	for _, rr := range msg.Answer {
		switch v := rr.(type) {
		case *mdns.A:
			if s.cidr.IsBlocked(v.A) {
				return true
			}
		case *mdns.AAAA:
			if s.cidr.IsBlocked(v.AAAA) {
				return true
			}
		}
	}
	return false
}

// ListenAndServe starts the DNS server on the given address (e.g. ":53").
func (s *DNSServer) ListenAndServe(addr string) error {
	udpServer := &mdns.Server{Addr: addr, Net: "udp", Handler: s.handler()}
	tcpServer := &mdns.Server{Addr: addr, Net: "tcp", Handler: s.handler()}

	errCh := make(chan error, 2)
	go func() { errCh <- udpServer.ListenAndServe() }()
	go func() { errCh <- tcpServer.ListenAndServe() }()

	return <-errCh
}
