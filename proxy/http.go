package proxy

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/elazarl/goproxy"
)

// HTTPProxy is a filtering HTTP/HTTPS proxy that uses an allowlist and
// CIDR blocker to decide whether to forward or reject each request.
type HTTPProxy struct {
	allowlist *Allowlist
	cidr      *CIDRBlocker
	log       *LogBuffer
	proxy     *goproxy.ProxyHttpServer
}

func NewHTTPProxy(allowlist *Allowlist, cidr *CIDRBlocker, log *LogBuffer, allowHTTP bool) *HTTPProxy {
	p := &HTTPProxy{
		allowlist: allowlist,
		cidr:      cidr,
		log:       log,
		proxy:     goproxy.NewProxyHttpServer(),
	}

	// Handle CONNECT requests (HTTPS tunneling).
	p.proxy.OnRequest().HandleConnect(goproxy.FuncHttpsHandler(
		func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
			hostname, port := splitHostPort(host, "443")

			if !p.allowlist.Allows(hostname, port) {
				p.logEntry(hostname, port, ActionBlock, "domain not in allowlist")
				return goproxy.RejectConnect, host
			}

			if blocked, ip := p.resolveAndCheckCIDR(hostname); blocked {
				p.logEntry(hostname, port, ActionBlock, fmt.Sprintf("resolved IP %s is in blocked CIDR range", ip))
				return goproxy.RejectConnect, host
			}

			p.logEntry(hostname, port, ActionAllow, "")
			return goproxy.OkConnect, host
		}))

	// Handle plain HTTP requests.
	p.proxy.OnRequest().DoFunc(
		func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
			hostname, port := splitHostPort(req.Host, "80")

			if !allowHTTP {
				p.logEntry(hostname, port, ActionBlock, "plain HTTP blocked, use HTTPS")
				return req, goproxy.NewResponse(req,
					goproxy.ContentTypeText,
					http.StatusForbidden,
					"plain HTTP blocked, use HTTPS\n",
				)
			}

			if !p.allowlist.Allows(hostname, port) {
				p.logEntry(hostname, port, ActionBlock, "domain not in allowlist")
				return req, goproxy.NewResponse(req,
					goproxy.ContentTypeText,
					http.StatusForbidden,
					fmt.Sprintf("domain %q is not in the allowlist\nadd it to .vibepit/network.yaml or run: vibepit monitor\n", hostname),
				)
			}

			if blocked, ip := p.resolveAndCheckCIDR(hostname); blocked {
				p.logEntry(hostname, port, ActionBlock, fmt.Sprintf("resolved IP %s is in blocked CIDR range", ip))
				return req, goproxy.NewResponse(req,
					goproxy.ContentTypeText,
					http.StatusForbidden,
					fmt.Sprintf("domain %q resolves to blocked IP %s\n", hostname, ip),
				)
			}

			p.logEntry(hostname, port, ActionAllow, "")
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

func splitHostPort(hostport, defaultPort string) (string, string) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		// No port in the string.
		return strings.ToLower(hostport), defaultPort
	}
	return strings.ToLower(host), port
}
