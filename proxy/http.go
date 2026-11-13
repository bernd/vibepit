package proxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/elazarl/goproxy"
)

// HTTPProxy is a filtering HTTP/HTTPS proxy that uses an allowlist and
// CIDR blocker to decide whether to forward or reject each request.
type HTTPProxy struct {
	policy   *Policy
	cidr     *CIDRBlocker
	log      *LogBuffer
	proxy    *goproxy.ProxyHttpServer
	resolver *net.Resolver
	// aliases returns a host's CNAME chain; a field so tests can stub it.
	aliases        func(ctx context.Context, host string) ([]string, error)
	hostGateway    string
	allowHostPorts map[int]bool
	holds          *Holds
}

// filterResult captures the outcome of a proxy filter check.
type filterResult struct {
	action  Action
	reason  string
	code    BlockCode
	rewrite string // non-empty when host.vibepit should be rewritten to gateway
}

// checkRequest decides whether to allow or block a request and logs the
// decision. Both the CONNECT and plain HTTP handlers call this so the
// filtering logic stays in one place.
func (p *HTTPProxy) checkRequest(ctx context.Context, hostname, port string) filterResult {
	r := p.filter(ctx, hostname, port)
	var holdID uint64
	// Only a plain not-allowlisted block can be fixed by allowing the target.
	if r.code == BlockNotAllowlisted && p.holds != nil {
		var o HoldOutcome
		o, holdID = p.holds.Wait(ctx, HostKey{Source: SourceProxy, Domain: hostname, Port: port})
		t := Target{Source: SourceProxy, Host: hostname, Port: port}
		switch {
		case o == HoldAbandoned:
			// The client is gone; nobody sees this block, so don't log it.
			return r
		case o == HoldAllowed:
			// The allowlist changed, but the deny rules and the CIDR check
			// still apply.
			r = p.filter(ctx, hostname, port)
		case p.policy.Denied(t):
			// A deny rule added meanwhile ended or skipped the hold. Only
			// the reason changes: any other outcome stays a block, even if
			// an allow raced it, so a request the user denied never passes.
			r = deniedResult
		case o == HoldRejected:
			r.reason = reasonRejected
		}
	}
	if r.code == BlockResolutionFailed && r.reason == aliasLookupFailedReason && ctx.Err() != nil {
		// The client left during the alias lookup, which failed only for
		// that; logging it would look like a DNS outage nobody had.
		return r
	}
	p.logEntry(hostname, port, r, holdID)
	return r
}

var deniedResult = filterResult{action: ActionBlock, reason: "domain in denylist", code: BlockDenied}

func (p *HTTPProxy) filter(ctx context.Context, hostname, port string) filterResult {
	notAllowlisted := filterResult{action: ActionBlock, reason: reasonNotAllowlisted, code: BlockNotAllowlisted}

	decision := p.policy.Decide(Target{Source: SourceProxy, Host: hostname, Port: port})
	if decision == Denied {
		return deniedResult
	}

	if hostname == "host.vibepit" && p.hostGateway != "" {
		if !p.isHostPortAllowed(port) && decision != Allowed {
			return notAllowlisted
		}
		return filterResult{action: ActionAllow, reason: "host.vibepit", rewrite: net.JoinHostPort(p.hostGateway, port)}
	}

	if decision != Allowed {
		return notAllowlisted
	}

	if r, blocked := p.checkAliases(ctx, hostname, port); blocked {
		return r
	}

	if blocked, ip := p.resolveAndCheckCIDR(hostname); blocked {
		if ip == nil {
			return filterResult{action: ActionBlock, reason: "DNS resolution failed during CIDR check", code: BlockResolutionFailed}
		}
		return filterResult{action: ActionBlock, reason: fmt.Sprintf("resolved IP %s is in blocked CIDR range", ip), code: BlockCIDR}
	}

	return filterResult{action: ActionAllow}
}

// NewHTTPProxy creates the filtering proxy, which lets through what policy
// allows.
func NewHTTPProxy(policy *Policy, cidr *CIDRBlocker, log *LogBuffer, upstream string) *HTTPProxy {
	// Build a resolver that talks directly to the upstream DNS server
	// instead of using /etc/resolv.conf, which may point at the internal
	// network gateway that cannot resolve external names.
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{}
			return d.DialContext(ctx, "udp", upstream)
		},
	}
	dialer := &net.Dialer{Resolver: resolver}

	proxy := goproxy.NewProxyHttpServer()
	proxy.Tr = &http.Transport{
		DialContext: dialer.DialContext,
	}
	proxy.ConnectDial = func(network, addr string) (net.Conn, error) {
		return dialer.Dial(network, addr)
	}

	p := &HTTPProxy{
		policy:   policy,
		cidr:     cidr,
		log:      log,
		proxy:    proxy,
		resolver: resolver,
		aliases: func(ctx context.Context, host string) ([]string, error) {
			return lookupAliases(ctx, upstream, host)
		},
	}

	p.proxy.OnRequest().HandleConnect(goproxy.FuncHttpsHandler(
		func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
			hostname, port, ok := canonicalTarget(host, "443")
			if !ok {
				p.logInvalid(hostname, port)
				return goproxy.RejectConnect, host
			}
			result := p.checkRequest(ctx.Req.Context(), hostname, port)
			if result.action == ActionBlock {
				return goproxy.RejectConnect, host
			}
			if result.rewrite != "" {
				return goproxy.OkConnect, result.rewrite
			}
			// Dial exactly what was checked, not the client's spelling.
			return goproxy.OkConnect, net.JoinHostPort(hostname, port)
		}))

	p.proxy.OnRequest().DoFunc(
		func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
			// The transport dials req.URL, with the scheme's default port
			// when it names none, so that is what gets checked.
			defaultPort, known := schemePorts[req.URL.Scheme]
			hostname, port, ok := canonicalTarget(req.URL.Host, defaultPort)
			var result filterResult
			if !known || !ok {
				p.logInvalid(hostname, port)
				result = invalidResult
			} else {
				result = p.checkRequest(req.Context(), hostname, port)
			}
			if result.action == ActionBlock {
				msg := fmt.Sprintf("domain %q is not in the allowlist\nadd it to .vibepit/network.yaml or run: vibepit monitor\n", hostname)
				switch {
				case result.code == BlockInvalidTarget && !known:
					msg = fmt.Sprintf("scheme %q is not supported by the proxy\n", req.URL.Scheme)
				case result.code == BlockInvalidTarget:
					msg = fmt.Sprintf("request target %q names no valid host and port\n", req.URL.Host)
				case result.reason == reasonRejected:
					// The monitor won't ask about it again, so don't send
					// the agent there for a prompt.
					msg = fmt.Sprintf("domain %q was rejected for this session and will not be prompted again\nit can still be allowed from vibepit monitor\n", hostname)
				case result.code == BlockDenied && result != deniedResult:
					msg = fmt.Sprintf("domain %q is denied: %s\n", hostname, result.reason)
				case result.code == BlockDenied:
					msg = fmt.Sprintf("domain %q is in the denylist\n", hostname)
				case result.code == BlockCIDR:
					msg = fmt.Sprintf("domain %q resolves to a blocked IP\n", hostname)
				case result.code == BlockResolutionFailed:
					msg = fmt.Sprintf("domain %q could not be resolved safely\n", hostname)
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
			} else {
				// Dial exactly what was checked; the Host header keeps the
				// client's value.
				if req.Host == "" {
					req.Host = req.URL.Host
				}
				req.URL.Host = net.JoinHostPort(hostname, port)
			}
			return req, nil
		})

	return p
}

func (p *HTTPProxy) Handler() http.Handler {
	return p.proxy
}

func (p *HTTPProxy) logEntry(hostname, port string, r filterResult, holdID uint64) {
	p.log.Add(LogEntry{
		HoldID: holdID,
		Time:   time.Now(),
		Domain: hostname,
		Port:   port,
		Action: r.action,
		Source: SourceProxy,
		Reason: r.reason,
		Code:   r.code,
	})
}

// checkAliases blocks an allowed host that is an alias (CNAME) of a denied
// one. It fails closed: a chain that can't be looked up might hide a
// denied name. Without HTTP deny rules, or for an IP literal, there is
// nothing to look up.
func (p *HTTPProxy) checkAliases(ctx context.Context, hostname, port string) (filterResult, bool) {
	if !p.policy.HasHTTPDenies() || net.ParseIP(hostname) != nil {
		return filterResult{}, false
	}
	// Bound by the request too: a client that went away needs no answer.
	ctx, cancel := context.WithTimeout(ctx, aliasLookupTimeout)
	defer cancel()
	names, err := p.aliases(ctx, hostname)
	if err != nil {
		return filterResult{action: ActionBlock, reason: aliasLookupFailedReason, code: BlockResolutionFailed}, true
	}
	if alias, denied := p.policy.DeniedAlias(SourceProxy, names, port); denied {
		return aliasDeniedResult(alias), true
	}
	return filterResult{}, false
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

	addrs, err := p.resolver.LookupIPAddr(context.Background(), hostname)
	// Fail closed only when lookup returned no usable answers.
	if err != nil && len(addrs) == 0 {
		// Security: do not allow traffic when CIDR validation cannot be completed.
		// Failing open here would let requests bypass private-IP blocking during
		// DNS outages or resolver errors.
		return true, nil
	}
	for _, addr := range addrs {
		if p.cidr.IsBlocked(addr.IP) {
			return true, addr.IP
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

// SetHolds makes requests blocked only for not being allowlisted wait on h
// instead of failing at once. Nil keeps the immediate block.
func (p *HTTPProxy) SetHolds(h *Holds) {
	p.holds = h
}

func (p *HTTPProxy) isHostPortAllowed(port string) bool {
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return false
	}
	return p.allowHostPorts[portNum]
}

// schemePorts are the schemes the transport forwards, with their default ports.
var schemePorts = map[string]string{"http": "80", "https": "443"}

var invalidResult = filterResult{action: ActionBlock, reason: "invalid request target", code: BlockInvalidTarget}

// canonicalTarget turns a request's host[:port] into the one spelling every
// check, hold, log line and the dial use: a normalized host and a decimal
// port, the default when absent or empty. Rules compare strings, so each
// other spelling of the same port (zero padding, an empty port) would be a
// way around a deny. ok is false for a port no dial could use.
func canonicalTarget(hostport, defaultPort string) (host, port string, ok bool) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		// No port in the string. An IPv6 literal keeps its brackets then,
		// which SplitHostPort drops when there is a port.
		host, port = hostport, ""
		if len(host) > 1 && host[0] == '[' && host[len(host)-1] == ']' {
			host = host[1 : len(host)-1]
		}
	}
	host = normalizeHost(host)
	// An IP literal has other spellings of the same address, e.g. an
	// IPv4-mapped IPv6 one, which a wildcard allow could still match.
	addr, addrErr := netip.ParseAddr(host)
	if addrErr == nil {
		host = addr.Unmap().String()
	}
	if port == "" {
		port = defaultPort
	}
	// A colon outside an IP literal, e.g. "a.com:80:443", names nothing a
	// dial could reach. Neither does an empty host, e.g. "." once its dot is
	// trimmed: it would be held and prompted for, and dial the proxy itself.
	if host == "" || addrErr != nil && strings.ContainsRune(host, ':') {
		return host, port, false
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strings.TrimLeft(port, "0123456789") != "" {
		return host, port, false
	}
	return host, strconv.Itoa(n), true
}

// logInvalid records a request refused before any check, so it shows in the
// monitor; nothing an allow could fix.
func (p *HTTPProxy) logInvalid(hostname, port string) {
	p.logEntry(hostname, port, invalidResult, 0)
}

// normalizeHost lowercases host and drops the trailing dots of a fully
// qualified name, which resolves to the same host. Rules match labels
// exactly, so "example.com." must not slip past a rule for "example.com".
func normalizeHost(host string) string {
	return strings.TrimRight(strings.ToLower(host), ".")
}
