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

// inspectionOrigin supplies the checked IPv4 address and the session's server
// certificate for one authority. This first slice serves inspected origins only.
type inspectionOrigin struct {
	authority   string
	address     netip.Addr
	certificate tls.Certificate
	rules       []requestRule
}

type inspectedRoute struct {
	authority   string
	certificate tls.Certificate
	policy      requestPolicy
	transport   *http.Transport
}

type inspectionProxy struct {
	routes map[string]*inspectedRoute
}

func newInspectionProxy(origins []inspectionOrigin, roots *x509.CertPool) (*inspectionProxy, error) {
	p := &inspectionProxy{routes: make(map[string]*inspectedRoute, len(origins))}
	for _, origin := range origins {
		key, err := canonicalAuthority(origin.authority, "")
		if err != nil {
			return nil, err
		}
		if _, exists := p.routes[key]; exists {
			return nil, fmt.Errorf("duplicate inspection origin %q", key)
		}
		address := origin.address.Unmap()
		if !address.Is4() || len(origin.certificate.Certificate) == 0 || origin.certificate.PrivateKey == nil {
			return nil, fmt.Errorf("%s requires a checked IPv4 address and a TLS certificate", key)
		}
		policy, err := compileRules(origin.rules)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		if !policy.admitsHost() {
			p.routes[key] = nil
			continue
		}
		if !policy.requiresInspection() {
			return nil, fmt.Errorf("%s has no request-level rule for inspection", key)
		}
		_, port, _ := net.SplitHostPort(key)
		pinned := net.JoinHostPort(address.String(), port)
		dialer := net.Dialer{Timeout: 10 * time.Second}
		tr := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "tcp4", pinned)
			},
			TLSClientConfig:     &tls.Config{RootCAs: roots},
			ForceAttemptHTTP2:   true,
			TLSHandshakeTimeout: 10 * time.Second,
			IdleConnTimeout:     90 * time.Second,
		}
		p.routes[key] = &inspectedRoute{authority: key, certificate: origin.certificate, policy: policy, transport: tr}
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
	} else if host == "" || strings.ContainsAny(host, ":[]%*") {
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
		route.serveTLS(w, r)
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
	if !route.policy.allows(r.URL) {
		http.Error(w, "request refused by policy", http.StatusForbidden)
		return
	}
	proxy := httputil.ReverseProxy{
		Transport: route.transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
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

func (route *inspectedRoute) serveTLS(w http.ResponseWriter, r *http.Request) {
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		http.Error(w, "CONNECT requires HTTP/1", http.StatusBadRequest)
		return
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(rw, "HTTP/%d.%d 200 Connection established\r\n\r\n", r.ProtoMajor, r.ProtoMinor); err != nil {
		return
	}
	if err := rw.Flush(); err != nil {
		return
	}
	done := make(chan struct{})
	reader := io.MultiReader(io.LimitReader(rw.Reader, int64(rw.Reader.Buffered())), conn)
	buffered := &bufferedConn{Conn: conn, reader: reader, signalClose: sync.OnceFunc(func() { close(done) })}
	stopClose := context.AfterFunc(r.Context(), func() { buffered.Close() })
	defer stopClose()
	tlsConn := tls.Server(buffered, &tls.Config{
		Certificates: []tls.Certificate{route.certificate},
		NextProtos:   []string{"h2", "http/1.1"},
	})
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	err = tlsConn.HandshakeContext(ctx)
	cancel()
	if err != nil {
		return
	}
	ln := &singleConnListener{conn: tlsConn, done: done}
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
