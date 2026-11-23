package netproxy

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// Resolution carries the CNAME targets in traversal order and the terminal
// owner's A records, from the same lookup.
type Resolution struct {
	Aliases   []string
	Addresses []netip.Addr
}

// Resolver returns a complete IPv4 resolution or an error. Implementations
// may be called concurrently by independent upstream dials.
type Resolver interface {
	ResolveIPv4(context.Context, string) (Resolution, error)
}

type dnsResolver struct{ upstream netip.AddrPort }

// NewDNSResolver uses the supplied DNS server for A queries and retries
// truncated UDP replies over TCP. The server itself needs no name lookup.
func NewDNSResolver(upstream netip.AddrPort) (Resolver, error) {
	addr := upstream.Addr().Unmap()
	if !addr.IsValid() || upstream.Port() == 0 || addr.WithZone("").IsUnspecified() {
		return nil, fmt.Errorf("invalid upstream DNS address %v", upstream)
	}
	return dnsResolver{upstream: netip.AddrPortFrom(addr, upstream.Port())}, nil
}

func canonicalDNSName(s string) (string, error) {
	name := strings.TrimSuffix(s, ".")
	if len(name) == 0 || len(name) > 253 {
		return "", fmt.Errorf("invalid DNS name %q", s)
	}
	for label := range strings.SplitSeq(name, ".") {
		if len(label) == 0 || len(label) > 63 {
			return "", fmt.Errorf("invalid DNS name %q", s)
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return "", fmt.Errorf("invalid DNS name %q", s)
			}
		}
	}
	return strings.ToLower(name), nil
}

const maxAliases = 16
const resolutionTimeout = 5 * time.Second

func (r dnsResolver) ResolveIPv4(ctx context.Context, host string) (Resolution, error) {
	name, err := canonicalDNSName(host)
	if err != nil {
		return Resolution{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, resolutionTimeout)
	defer cancel()
	seen := map[string]bool{name: true}
	var result Resolution
	for {
		msg, err := r.query(ctx, name)
		if err != nil {
			return Resolution{}, err
		}
		queried := name
		for {
			next, addresses, err := ownerRecords(msg, name)
			if err != nil {
				return Resolution{}, err
			}
			if len(addresses) > 0 {
				result.Addresses = addresses
				return result, nil
			}
			if next == "" {
				if name == queried {
					return Resolution{}, fmt.Errorf("no A records for %s", name)
				}
				// The recursive server returned only part of the chain.
				break
			}
			if seen[next] || len(result.Aliases) >= maxAliases {
				return Resolution{}, fmt.Errorf("cyclic or excessive CNAME chain for %s", host)
			}
			seen[next] = true
			result.Aliases = append(result.Aliases, next)
			name = next
		}
	}
}

// Answers may be out of order and may contain data unrelated to the query.
// Only the current owner's IN records can extend or terminate its chain.
func ownerRecords(m *dns.Msg, name string) (string, []netip.Addr, error) {
	var next string
	var addrs []netip.Addr
	for _, record := range m.Answer {
		h := record.Header()
		if h.Class != dns.ClassINET || !strings.EqualFold(h.Name, name+".") {
			continue
		}
		switch rr := record.(type) {
		case *dns.CNAME:
			target, err := canonicalDNSName(rr.Target)
			if err != nil {
				return "", nil, err
			}
			if next != "" && next != target {
				return "", nil, fmt.Errorf("conflicting CNAME targets for %s", name)
			}
			next = target
		case *dns.A:
			addr, ok := netip.AddrFromSlice(rr.A)
			if !ok || !addr.Unmap().Is4() {
				return "", nil, fmt.Errorf("invalid A record for %s", name)
			}
			addrs = append(addrs, addr.Unmap())
		}
	}
	if next != "" && len(addrs) > 0 {
		return "", nil, fmt.Errorf("both A and CNAME records for %s", name)
	}
	return next, addrs, nil
}

func (r dnsResolver) query(ctx context.Context, name string) (*dns.Msg, error) {
	q := new(dns.Msg).SetQuestion(dns.Fqdn(name), dns.TypeA)
	for _, network := range []string{"udp", "tcp"} {
		c := dns.Client{Net: network}
		msg, err := exchangeDNS(ctx, &c, q, r.upstream.String())
		if err != nil {
			return nil, err
		}
		if !msg.Response || msg.Opcode != dns.OpcodeQuery || len(msg.Question) != 1 || msg.Question[0].Qtype != dns.TypeA ||
			msg.Question[0].Qclass != dns.ClassINET || !strings.EqualFold(msg.Question[0].Name, q.Question[0].Name) {
			return nil, fmt.Errorf("unexpected DNS response for %s", name)
		}
		if msg.Truncated {
			continue
		}
		if msg.Rcode != dns.RcodeSuccess {
			return nil, fmt.Errorf("DNS response for %s: %s", name, dns.RcodeToString[msg.Rcode])
		}
		return msg, nil
	}
	return nil, fmt.Errorf("truncated TCP DNS response for %s", name)
}

func exchangeDNS(ctx context.Context, client *dns.Client, q *dns.Msg, upstream string) (*dns.Msg, error) {
	conn, err := client.DialContext(ctx, upstream)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	// ExchangeWithConnContext applies the deadline, but an earlier cancel
	// must also unblock a socket already waiting for its response.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	msg, _, err := client.ExchangeWithConnContext(ctx, q, conn)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return msg, err
}
