package proxy

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/elazarl/goproxy"
)

// HTTPProxy is a filtering HTTP/HTTPS proxy that uses an allowlist and
// CIDR blocker to decide whether to forward or reject each request.
type HTTPProxy struct {
	allowlist      *HTTPAllowlist
	cidr           *CIDRBlocker
	log            *LogBuffer
	proxy          *goproxy.ProxyHttpServer
	hostGateway    string
	allowHostPorts map[int]bool
}

// filterResult captures the outcome of a proxy filter check.
type filterResult struct {
	action  Action
	reason  string
	rewrite string // non-empty when host.vibepit should be rewritten to gateway
}

// checkRequest decides whether to allow or block a request. Both the CONNECT
// and plain HTTP handlers call this so the filtering logic stays in one place.
func (p *HTTPProxy) checkRequest(hostname, port string) filterResult {
	if hostname == "host.vibepit" && p.hostGateway != "" {
		if !p.isHostPortAllowed(port) && !p.allowlist.Allows(hostname, port) {
			p.logEntry(hostname, port, ActionBlock, "domain not in allowlist")
			return filterResult{action: ActionBlock, reason: "domain not in allowlist"}
		}
		rewritten := net.JoinHostPort(p.hostGateway, port)
		p.logEntry(hostname, port, ActionAllow, "host.vibepit")
		return filterResult{action: ActionAllow, rewrite: rewritten}
	}

	if !p.allowlist.Allows(hostname, port) {
		p.logEntry(hostname, port, ActionBlock, "domain not in allowlist")
		return filterResult{action: ActionBlock, reason: "domain not in allowlist"}
	}

	if blocked, ip := p.resolveAndCheckCIDR(hostname); blocked {
		reason := fmt.Sprintf("resolved IP %s is in blocked CIDR range", ip)
		p.logEntry(hostname, port, ActionBlock, reason)
		return filterResult{action: ActionBlock, reason: reason}
	}

	p.logEntry(hostname, port, ActionAllow, "")
	return filterResult{action: ActionAllow}
}

func NewHTTPProxy(allowlist *HTTPAllowlist, cidr *CIDRBlocker, log *LogBuffer) *HTTPProxy {
	p := &HTTPProxy{
		allowlist: allowlist,
		cidr:      cidr,
		log:       log,
		proxy:     goproxy.NewProxyHttpServer(),
	}

	p.proxy.OnRequest().HandleConnect(goproxy.FuncHttpsHandler(
		func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
			hostname, port := splitHostPort(host, "443")
			result := p.checkRequest(hostname, port)
			if result.action == ActionBlock {
				return goproxy.RejectConnect, host
			}
			if result.rewrite != "" {
				return goproxy.OkConnect, result.rewrite
			}
			return goproxy.OkConnect, host
		}))

	p.proxy.OnRequest().DoFunc(
		func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
			hostname, port := splitHostPort(req.Host, "80")
			result := p.checkRequest(hostname, port)
			if result.action == ActionBlock {
				msg := fmt.Sprintf("domain %q is not in the allowlist\nadd it to .vibepit/network.yaml or run: vibepit monitor\n", hostname)
				if strings.Contains(result.reason, "CIDR") {
					msg = fmt.Sprintf("domain %q resolves to a blocked IP\n", hostname)
				}
				return req, goproxy.NewResponse(req,
					goproxy.ContentTypeText,
					http.StatusForbidden,
					msg,
				)
			}
			if result.rewrite != "" {
				req.URL.Host = result.rewrite
				req.Host = result.rewrite
			}
			return req, nil
		})

	return p
}

func (p *HTTPProxy) Handler() http.Handler {
	return p.proxy
}

func (p *HTTPProxy) logEntry(hostname, port string, action Action, reason string) {
	p.log.Add(LogEntry{
		Time:   time.Now(),
		Domain: hostname,
		Port:   port,
		Action: action,
		Source: SourceProxy,
		Reason: reason,
	})
}

// resolveAndCheckCIDR resolves the hostname to IPs and checks whether any
// fall within a blocked CIDR range. This prevents DNS rebinding attacks
// where an allowed domain resolves to a private IP.
func (p *HTTPProxy) resolveAndCheckCIDR(hostname string) (bool, net.IP) {
	// If the hostname is already an IP, check it directly.
	if ip := net.ParseIP(hostname); ip != nil {
		if p.cidr.IsBlocked(ip) {
			return true, ip
		}
		return false, nil
	}

	ips, err := net.LookupIP(hostname)
	if err != nil {
		return false, nil
	}
	for _, ip := range ips {
		if p.cidr.IsBlocked(ip) {
			return true, ip
		}
	}
	return false, nil
}

// SetHostVibepit configures the proxy to rewrite host.vibepit requests to the
// given gateway address. If allowedPorts is non-nil, only those ports are
// auto-allowed without requiring an explicit allowlist entry.
func (p *HTTPProxy) SetHostVibepit(gateway string, allowedPorts []int) {
	// Strip port from gateway if present; the request port is used instead.
	if host, _, err := net.SplitHostPort(gateway); err == nil {
		p.hostGateway = host
	} else {
		p.hostGateway = gateway
	}
	p.allowHostPorts = make(map[int]bool)
	for _, port := range allowedPorts {
		p.allowHostPorts[port] = true
	}
}

func (p *HTTPProxy) isHostPortAllowed(port string) bool {
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return false
	}
	return p.allowHostPorts[portNum]
}

func splitHostPort(hostport, defaultPort string) (string, string) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		// No port in the string.
		return strings.ToLower(hostport), defaultPort
	}
	return strings.ToLower(host), port
}
