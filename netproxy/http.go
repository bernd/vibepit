// Package netproxy implements request policy for Vibepit's filtering proxy.
package netproxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// inspectionOrigin binds an authority to its request policy.
type inspectionOrigin struct {
	authority string
	rules     []requestRule
	hostPort  bool // The embedder has authorized a host-port gateway rewrite.
}

// net/http detaches its dial context from the request so it can pool late
// connections. Keep request cancellation available during egress checks.
type requestContextKey struct{}

type inspectedRoute struct {
	authority  string
	policy     requestPolicy
	transport  *http.Transport
	dialTunnel func(context.Context) (net.Conn, error)
	inspect    bool
	tunnelName string // Empty for a literal address or authorized host port.
}

type inspectionProxy struct {
	ca              *inspectionCA
	routes          map[string]*inspectedRoute
	onTunnelRefusal func(tunnelRefusal)
}

func newInspectionProxy(origins []inspectionOrigin, ca *inspectionCA, roots *x509.CertPool, cfg upstreamConfig) (*inspectionProxy, error) {
	dialer, err := newUpstreamDialer(cfg)
	if err != nil {
		return nil, err
	}
	p := &inspectionProxy{ca: ca, routes: make(map[string]*inspectedRoute, len(origins))}
	for _, origin := range origins {
		key, err := canonicalAuthority(origin.authority, "")
		if err != nil {
			return nil, err
		}
		if _, exists := p.routes[key]; exists {
			return nil, fmt.Errorf("duplicate inspection origin %q", key)
		}
		policy, err := compileRules(origin.rules)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		if deny := policy.aliasDeny(); deny != noAliasDeny {
			dialer.aliasRules[key] = deny
		}
		inspect := policy.requiresInspection()
		if origin.hostPort && inspect {
			return nil, fmt.Errorf("%s: host ports cannot have request rules", key)
		}
		if !policy.admitsHost() {
			p.routes[key] = nil
			continue
		}
		if inspect && ca == nil {
			return nil, fmt.Errorf("%s requires a session inspection CA", key)
		}
		host, port, _ := net.SplitHostPort(key)
		tunnelName := host
		if ip, err := netip.ParseAddr(host); err == nil {
			if !ip.Is4() {
				return nil, fmt.Errorf("%s requires an IPv4 address", key)
			}
			tunnelName = ""
		} else if !origin.hostPort && cfg.resolver == nil && !dialer.config.staticHosts[host].IsValid() {
			return nil, fmt.Errorf("missing resolver for %s", host)
		}
		if origin.hostPort {
			if !dialer.config.gateway.IsValid() {
				return nil, fmt.Errorf("missing gateway for %s", key)
			}
			tunnelName = ""
		}
		tr := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				if requestCtx, ok := ctx.Value(requestContextKey{}).(context.Context); ok {
					if err := requestCtx.Err(); err != nil {
						return nil, err
					}
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					defer cancel()
					stop := context.AfterFunc(requestCtx, cancel)
					defer stop()
				}
				return dialer.dial(ctx, host, port, origin.hostPort, false)
			},
			TLSClientConfig:     &tls.Config{RootCAs: roots},
			ForceAttemptHTTP2:   true,
			TLSHandshakeTimeout: 10 * time.Second,
			IdleConnTimeout:     90 * time.Second,
		}
		p.routes[key] = &inspectedRoute{
			authority:  key,
			policy:     policy,
			transport:  tr,
			inspect:    inspect,
			tunnelName: tunnelName,
			dialTunnel: func(ctx context.Context) (net.Conn, error) {
				return dialer.dial(ctx, host, port, origin.hostPort, true)
			},
		}
	}
	return p, nil
}

func canonicalAuthority(authority, defaultPort string) (string, error) {
	u, err := url.Parse("//" + authority)
	if err != nil || u.Host == "" || u.User != nil || u.Host != authority || strings.HasSuffix(authority, ":") {
		return "", fmt.Errorf("invalid authority %q", authority)
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.Unmap().String()
	} else if _, err := canonicalDNSName(u.Hostname()); err != nil || strings.HasPrefix(authority, "[") {
		return "", fmt.Errorf("invalid host in authority %q", authority)
	}
	port := u.Port()
	if port == "" {
		port = defaultPort
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		return "", fmt.Errorf("invalid port in authority %q", authority)
	}
	return net.JoinHostPort(host, strconv.FormatUint(n, 10)), nil
}

func (p *inspectionProxy) closeIdleConnections() {
	for _, route := range p.routes {
		if route != nil {
			route.transport.CloseIdleConnections()
		}
	}
}

func (p *inspectionProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	isConnect := r.Method == http.MethodConnect
	host, port := r.URL.Host, "80"
	if isConnect {
		if r.RequestURI != r.Host {
			http.Error(w, "CONNECT requires authority form", http.StatusBadRequest)
			return
		}
		host, port = r.Host, "443"
	} else if r.URL.Scheme != "http" || r.URL.User != nil {
		http.Error(w, "expected an absolute http URL or CONNECT", http.StatusBadRequest)
		return
	}
	key, err := canonicalAuthority(host, port)
	if err != nil {
		http.Error(w, "invalid proxy target", http.StatusBadRequest)
		return
	}
	route := p.routes[key]
	if route == nil {
		http.Error(w, "origin is not allowed", http.StatusForbidden)
		return
	}
	if isConnect {
		if route.inspect {
			route.serveTLS(w, r, p.ca)
		} else {
			route.serveTunnel(w, r, p.onTunnelRefusal)
		}
		return
	}
	route.forward(w, r, "http")
}

func (route *inspectedRoute) serveInner(w http.ResponseWriter, r *http.Request) {
	host, err := canonicalAuthority(r.Host, "443")
	if err != nil || host != route.authority || r.URL.User != nil {
		http.Error(w, "request authority differs from CONNECT", http.StatusMisdirectedRequest)
		return
	}
	if r.URL.IsAbs() {
		target, err := canonicalAuthority(r.URL.Host, "443")
		if err != nil || target != route.authority || r.URL.Scheme != "https" {
			http.Error(w, "invalid absolute target inside CONNECT", http.StatusMisdirectedRequest)
			return
		}
		// net/http prefers the absolute URL and discards the wire Host.
		// In origin form the handler can verify the authority unambiguously.
		http.Error(w, "use origin form inside CONNECT", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodConnect || r.URL.Opaque != "" {
		http.Error(w, "unsupported request inside CONNECT", http.StatusBadRequest)
		return
	}
	route.forward(w, r, "https")
}

func (route *inspectedRoute) forward(w http.ResponseWriter, r *http.Request, scheme string) {
	if route.inspect && !route.policy.allows(r.URL) {
		http.Error(w, "request refused by policy", http.StatusForbidden)
		return
	}
	proxy := httputil.ReverseProxy{
		Transport:    route.transport,
		ErrorHandler: writeUpstreamError,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out = pr.Out.WithContext(context.WithValue(pr.Out.Context(), requestContextKey{}, pr.Out.Context()))
			pr.Out.URL.Scheme = scheme
			pr.Out.URL.Host = route.authority
			pr.Out.Host = route.authority
			// Path-only policy never parses the query. Preserve signed URLs,
			// including opaque components ReverseProxy cleans before Rewrite.
			pr.Out.URL.RawQuery = pr.In.URL.RawQuery
		},
	}
	proxy.ServeHTTP(w, r)
}

// Hijack leaves early client bytes in the HTTP server's reader. The TLS
// handshake consumes those bytes before reading directly from the socket.
type bufferedConn struct {
	net.Conn
	reader      io.Reader
	done        chan struct{}
	signalClose func()
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.reader.Read(b) }

func (c *bufferedConn) Close() error {
	defer c.signalClose()
	return c.Conn.Close()
}

// Serve accepts once, starts its connection goroutine, then calls Accept
// again. Wait for that connection to close before returning from the handler.
type singleConnListener struct {
	conn     net.Conn
	accepted bool
	done     chan struct{}
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	if !l.accepted {
		l.accepted = true
		return l.conn, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *singleConnListener) Addr() net.Addr { return l.conn.LocalAddr() }
func (l *singleConnListener) Close() error   { return l.conn.Close() }

func acceptConnect(w http.ResponseWriter, r *http.Request) *bufferedConn {
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		http.Error(w, "CONNECT requires HTTP/1", http.StatusBadRequest)
		return nil
	}

	if _, err := fmt.Fprintf(rw, "HTTP/%d.%d 200 Connection established\r\n\r\n", r.ProtoMajor, r.ProtoMinor); err != nil {
		conn.Close()
		return nil
	}
	if err := rw.Flush(); err != nil {
		conn.Close()
		return nil
	}
	done := make(chan struct{})
	reader := io.MultiReader(io.LimitReader(rw.Reader, int64(rw.Reader.Buffered())), conn)
	return &bufferedConn{Conn: conn, reader: reader, done: done, signalClose: sync.OnceFunc(func() { close(done) })}
}

func (c *bufferedConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return net.ErrClosed
}

func (route *inspectedRoute) serveTLS(w http.ResponseWriter, r *http.Request, ca *inspectionCA) {
	buffered := acceptConnect(w, r)
	if buffered == nil {
		return
	}
	defer buffered.Close()
	stopClose := context.AfterFunc(r.Context(), func() { buffered.Close() })
	defer stopClose()
	host, _, _ := net.SplitHostPort(route.authority)
	tlsConn := tls.Server(buffered, &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return ca.certificate(host)
		},
		NextProtos: []string{"h2", "http/1.1"},
	})
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	err := tlsConn.HandshakeContext(ctx)
	cancel()
	if err != nil {
		return
	}
	ln := &singleConnListener{conn: tlsConn, done: buffered.done}
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	srv := &http.Server{
		Handler:                      http.HandlerFunc(route.serveInner),
		BaseContext:                  func(net.Listener) context.Context { return r.Context() },
		DisableGeneralOptionsHandler: true,
		Protocols:                    &protocols,
		ReadHeaderTimeout:            10 * time.Second,
		IdleTimeout:                  90 * time.Second,
	}
	_ = srv.Serve(ln)
}
