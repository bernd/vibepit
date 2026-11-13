package proxy

import (
	"context"
	"fmt"
	"time"

	mdns "github.com/miekg/dns"
)

// aliasLookupTimeout bounds the extra CNAME query for one request.
const aliasLookupTimeout = 5 * time.Second

const aliasLookupFailedReason = "DNS alias lookup failed during deny check"

// cnameTargets returns the names a response's CNAME records lead through,
// normalized. A deny rule must also catch a name reached through an alias:
// otherwise any allowed name that aliases a denied one gets around it.
func cnameTargets(msg *mdns.Msg) []string {
	var names []string
	for _, rr := range msg.Answer {
		if c, ok := rr.(*mdns.CNAME); ok {
			names = append(names, normalizeHost(c.Hdr.Name), normalizeHost(c.Target))
		}
	}
	return names
}

// lookupAliases asks upstream for host's CNAME chain. net.Resolver, which
// the proxy dials through, never reports the chain, hence the extra query.
func lookupAliases(ctx context.Context, upstream, host string) ([]string, error) {
	q := new(mdns.Msg)
	q.SetQuestion(mdns.Fqdn(host), mdns.TypeA)
	c := &mdns.Client{Net: "udp", Timeout: aliasLookupTimeout}
	resp, _, err := c.ExchangeContext(ctx, q, upstream)
	if err == nil && resp.Truncated {
		c.Net = "tcp"
		resp, _, err = c.ExchangeContext(ctx, q, upstream)
	}
	if err != nil {
		return nil, err
	}
	switch resp.Rcode {
	case mdns.RcodeSuccess, mdns.RcodeNameError:
		return cnameTargets(resp), nil
	default:
		return nil, fmt.Errorf("alias lookup for %s: %s", host, mdns.RcodeToString[resp.Rcode])
	}
}

// DeniedAlias returns the first of names, the aliases a target was reached
// through, that a deny rule matches for source and port.
func (p *Policy) DeniedAlias(source Source, names []string, port string) (string, bool) {
	for _, name := range names {
		if p.Denied(Target{Source: source, Host: name, Port: port}) {
			return name, true
		}
	}
	return "", false
}

// HasHTTPDenies reports whether there are HTTP deny rules, so requests can
// skip the alias lookup when nothing could match.
func (p *Policy) HasHTTPDenies() bool {
	return p.rules != nil && len(*p.rules.http.rules.Load()) > 0
}

func aliasDeniedResult(alias string) filterResult {
	return filterResult{action: ActionBlock, reason: "alias of denied domain " + alias, code: BlockDenied}
}
