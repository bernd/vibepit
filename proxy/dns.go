package proxy

import (
	"fmt"
	"net"
	"strings"
	"time"

	mdns "github.com/miekg/dns"
)

// DNSServer is a filtering DNS server that checks queries against an allowlist
// and a dns-only list, and verifies resolved IPs against a CIDR blocklist.
type DNSServer struct {
	allowlist *Allowlist
	dnsOnly   *Allowlist
	cidr      *CIDRBlocker
	log       *LogBuffer
	upstream  string
	proxyIP   net.IP
}

// SetProxyIP sets the IP address that host.vibepit will resolve to.
func (s *DNSServer) SetProxyIP(ip net.IP) {
	s.proxyIP = ip
}

func NewDNSServer(allowlist, dnsOnly *Allowlist, cidr *CIDRBlocker, log *LogBuffer, upstream string) *DNSServer {
	return &DNSServer{
		allowlist: allowlist,
		dnsOnly:   dnsOnly,
		cidr:      cidr,
		log:       log,
		upstream:  upstream,
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

		domain := strings.TrimSuffix(strings.ToLower(r.Question[0].Name), ".")

		// Synthetic response for host.vibepit — resolves to the proxy IP
		// without upstream forwarding or CIDR validation.
		if domain == "host.vibepit" && s.proxyIP != nil && r.Question[0].Qtype == mdns.TypeA {
			s.log.Add(LogEntry{
				Time:   time.Now(),
				Domain: domain,
				Action: ActionAllow,
				Source: SourceDNS,
			})
			m := new(mdns.Msg)
			m.SetReply(r)
			m.Answer = append(m.Answer, &mdns.A{
				Hdr: mdns.RR_Header{
					Name:   r.Question[0].Name,
					Rrtype: mdns.TypeA,
					Class:  mdns.ClassINET,
					Ttl:    60,
				},
				A: s.proxyIP,
			})
			w.WriteMsg(m)
			return
		}

		if !s.allowlist.AllowsDNS(domain) && !s.dnsOnly.AllowsDNS(domain) {
			s.log.Add(LogEntry{
				Time:   time.Now(),
				Domain: domain,
				Action: ActionBlock,
				Source: SourceDNS,
				Reason: "domain not in allowlist",
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

		// Reject responses that resolve to blocked IP ranges (e.g. private networks).
		if s.hasBlockedIP(resp) {
			s.log.Add(LogEntry{
				Time:   time.Now(),
				Domain: domain,
				Action: ActionBlock,
				Source: SourceDNS,
				Reason: "resolved IP in blocked CIDR range",
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

// ListenAndServeTest starts a UDP DNS server on a random port for testing.
// Returns the address and a cleanup function.
func (s *DNSServer) ListenAndServeTest() (string, func()) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		panic(fmt.Sprintf("listen: %v", err))
	}
	addr := pc.LocalAddr().String()

	srv := &mdns.Server{PacketConn: pc, Handler: s.handler()}
	go srv.ActivateAndServe()

	return addr, func() { srv.Shutdown() }
}
