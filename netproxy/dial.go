package netproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"time"
)

type upstreamConfig struct {
	resolver               Resolver
	staticHosts            map[string]netip.Addr
	gateway                netip.Addr
	blockCIDRs, allowCIDRs []netip.Prefix
}

// The checked dialer is shared by ordinary requests, inspected requests and
// tunnels. Its policy and static host table are immutable after construction.
type upstreamDialer struct {
	config     upstreamConfig
	aliasRules map[string]aliasDeny
}

type aliasDeny uint8

const (
	noAliasDeny aliasDeny = iota
	denyTunnelAlias
	denyHostAlias
)

var defaultBlocked = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
}

func newUpstreamDialer(cfg upstreamConfig) (*upstreamDialer, error) {
	static := make(map[string]netip.Addr, len(cfg.staticHosts))
	for key, addr := range cfg.staticHosts {
		name, err := canonicalDNSName(key)
		if err != nil {
			return nil, err
		}
		if _, err := netip.ParseAddr(name); err == nil {
			return nil, fmt.Errorf("static host %q must be a name", key)
		}
		addr = addr.Unmap()
		if !addr.Is4() || addr.IsUnspecified() {
			return nil, fmt.Errorf("static host %q requires an IPv4 address", key)
		}
		if _, ok := static[name]; ok {
			return nil, fmt.Errorf("duplicate static host %q", name)
		}
		static[name] = addr
	}
	cfg.staticHosts = static
	cfg.gateway = cfg.gateway.Unmap()
	if cfg.gateway.IsValid() && (!cfg.gateway.Is4() || cfg.gateway.IsUnspecified()) {
		return nil, fmt.Errorf("gateway requires an IPv4 address")
	}
	var err error
	cfg.blockCIDRs, err = checkedPrefixes(cfg.blockCIDRs)
	if err != nil {
		return nil, err
	}
	cfg.allowCIDRs, err = checkedPrefixes(cfg.allowCIDRs)
	if err != nil {
		return nil, err
	}
	cfg.blockCIDRs = append(cfg.blockCIDRs, defaultBlocked...)
	return &upstreamDialer{config: cfg, aliasRules: make(map[string]aliasDeny)}, nil
}

func checkedPrefixes(prefixes []netip.Prefix) ([]netip.Prefix, error) {
	out := slices.Clone(prefixes)
	for i, p := range out {
		if !p.IsValid() {
			return nil, fmt.Errorf("invalid CIDR prefix")
		}
		if p.Addr().Is4In6() && p.Bits() >= 96 {
			out[i] = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
		}
	}
	return out, nil
}

type dialRefusal struct{ code, reason string }

func (e *dialRefusal) Error() string { return e.reason }

func writeUpstreamError(w http.ResponseWriter, _ *http.Request, err error) {
	var block *dialRefusal
	if errors.As(err, &block) {
		http.Error(w, block.reason, http.StatusForbidden)
		return
	}
	http.Error(w, "upstream unavailable", http.StatusBadGateway)
}

func (d *upstreamDialer) resolve(ctx context.Context, host string) (Resolution, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return Resolution{Addresses: []netip.Addr{ip}}, nil
	}
	if ip, ok := d.config.staticHosts[host]; ok {
		return Resolution{Addresses: []netip.Addr{ip}}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, resolutionTimeout)
	defer cancel()
	answer, err := d.config.resolver.ResolveIPv4(ctx, host)
	if ctx.Err() != nil {
		return Resolution{}, ctx.Err()
	}
	return answer, err
}

func (d *upstreamDialer) candidates(ctx context.Context, host, port string, hostPort, tunnel bool) ([]netip.Addr, error) {
	if hostPort {
		// Only an admitted host-port route selects the configured gateway.
		// A static or DNS name with the same address still gets CIDR checks.
		return []netip.Addr{d.config.gateway}, nil
	}
	answer, err := d.resolve(ctx, host)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || len(answer.Addresses) == 0 {
		return nil, &dialRefusal{"resolution_failed", "IPv4 resolution failed for " + host}
	}
	for _, alias := range answer.Aliases {
		name, err := canonicalDNSName(alias)
		if err != nil {
			return nil, &dialRefusal{"resolution_failed", "invalid alias in resolution for " + host}
		}
		deny := d.aliasRules[net.JoinHostPort(name, port)]
		if deny == denyHostAlias {
			return nil, &dialRefusal{"denied", "alias of denied domain " + name}
		}
		if tunnel && deny == denyTunnelAlias {
			return nil, &dialRefusal{"denied", "alias of domain with request deny " + name + "; configure request inspection on " + host}
		}
	}
	// Keep an owned copy of the values checked, even if a resolver caches its answers.
	checked := make([]netip.Addr, 0, len(answer.Addresses))
	for _, address := range answer.Addresses {
		ip := address.Unmap()
		if !ip.Is4() {
			return nil, &dialRefusal{"resolution_failed", "expected IPv4 address for " + host}
		}
		contains := func(p netip.Prefix) bool { return p.Contains(ip) }
		if ip.IsUnspecified() || (!slices.ContainsFunc(d.config.allowCIDRs, contains) && slices.ContainsFunc(d.config.blockCIDRs, contains)) {
			return nil, &dialRefusal{"blocked_cidr", "blocked IP address " + ip.String()}
		}
		checked = append(checked, ip)
	}
	return checked, nil
}

func (d *upstreamDialer) dial(ctx context.Context, host, port string, hostPort, tunnel bool) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	addresses, err := d.candidates(ctx, host, port, hostPort, tunnel)
	if err != nil {
		return nil, err
	}
	var dialer net.Dialer
	return dialAddresses(ctx, addresses, port, dialer.DialContext)
}

// Share the remaining deadline so one stalled address leaves time for the rest.
func dialAddresses(ctx context.Context, addresses []netip.Addr, port string, connect func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	deadline, _ := ctx.Deadline()
	var err error
	for i, ip := range addresses {
		attempt, cancel := context.WithTimeout(ctx, time.Until(deadline)/time.Duration(len(addresses)-i))
		var conn net.Conn
		conn, err = connect(attempt, "tcp4", net.JoinHostPort(ip.String(), port))
		cancel()
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, err
}
