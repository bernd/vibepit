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
	nets []*net.IPNet
}

func NewCIDRBlocker(extra []string) *CIDRBlocker {
	all := make([]string, 0, len(defaultBlockedCIDRs)+len(extra))
	all = append(all, defaultBlockedCIDRs...)
	all = append(all, extra...)

	nets := make([]*net.IPNet, 0, len(all))
	for _, cidr := range all {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		nets = append(nets, ipNet)
	}
	return &CIDRBlocker{nets: nets}
}

func (b *CIDRBlocker) IsBlocked(ip net.IP) bool {
	for _, n := range b.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
