package proxy

import "net"

var defaultBlockedCIDRs = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"::1/128",
	"fc00::/7",
	"fe80::/10",
}

type CIDRBlocker struct {
	blockNets []*net.IPNet
	allowNets []*net.IPNet
}

func NewCIDRBlocker(block, allow []string) *CIDRBlocker {
	blocked := make([]string, 0, len(defaultBlockedCIDRs)+len(block))
	blocked = append(blocked, defaultBlockedCIDRs...)
	blocked = append(blocked, block...)

	blockNets := parseCIDRs(blocked)
	allowNets := parseCIDRs(allow)

	return &CIDRBlocker{
		blockNets: blockNets,
		allowNets: allowNets,
	}
}

func parseCIDRs(cidrs []string) []*net.IPNet {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		nets = append(nets, ipNet)
	}
	return nets
}

func (b *CIDRBlocker) IsAllowed(ip net.IP) bool {
	for _, n := range b.allowNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func (b *CIDRBlocker) IsBlocked(ip net.IP) bool {
	if b.IsAllowed(ip) {
		return false
	}
	for _, n := range b.blockNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
