package netproxy

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testCertificate(t *testing.T, names ...string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	if len(names) == 0 {
		names = []string{"origin.test"}
	}
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1), DNSNames: names,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, roots
}

func TestRequestAuthorization(t *testing.T) {
	for _, form := range []struct {
		name, scheme string
		proto        int
	}{
		{"plain", "http", 1}, {"CONNECT HTTP1", "https", 1}, {"CONNECT HTTP2", "https", 2},
	} {
		t.Run(form.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Received-URI", r.RequestURI)
				w.WriteHeader(http.StatusNoContent)
			}))
			upstreamCert, upstreamRoots := testCertificate(t)
			if form.scheme == "https" {
				upstream.TLS = &tls.Config{Certificates: []tls.Certificate{upstreamCert}}
				upstream.StartTLS()
			} else {
				upstream.Start()
			}
			t.Cleanup(upstream.Close)
			_, port, err := net.SplitHostPort(upstream.Listener.Addr().String())
			require.NoError(t, err)
			authority := net.JoinHostPort("origin.test", port)
			ca, roots := testInspectionCA(t)
			for _, policy := range []struct {
				name  string
				rules []requestRule
			}{
				{"explicit deny", []requestRule{{}, {deny: true, paths: []string{"/private"}}}},
				{"path allow", []requestRule{{paths: []string{"/public"}}}},
				{"condition allow", []requestRule{{when: `request.path == "/public"`}}},
				{"condition deny", []requestRule{{}, {deny: true, when: `request.path == "/private"`}}},
			} {
				t.Run(policy.name, func(t *testing.T) {
					proxy, err := newLoopbackProxy([]inspectionOrigin{{
						authority: authority,
						rules:     policy.rules,
					}}, ca, upstreamRoots)
					require.NoError(t, err)
					t.Cleanup(proxy.closeIdleConnections)
					server := httptest.NewServer(proxy)
					t.Cleanup(server.Close)
					proxyURL, err := url.Parse(server.URL)
					require.NoError(t, err)
					var protocols http.Protocols
					protocols.SetHTTP1(true)
					protocols.SetHTTP2(form.proto == 2)
					tr := &http.Transport{Proxy: http.ProxyURL(proxyURL), Protocols: &protocols, TLSClientConfig: &tls.Config{RootCAs: roots}}
					t.Cleanup(tr.CloseIdleConnections)
					client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
					for _, tc := range []struct {
						path   string
						status int
						count  int32
					}{
						{"/public", http.StatusNoContent, 1},
						{"/private", http.StatusForbidden, 0},
						{"/pub%6Cic?sort=recent", http.StatusNoContent, 1},
						{"/public?q=old;new", http.StatusNoContent, 1},
					} {
						before := calls.Load()
						resp, err := client.Get(form.scheme + "://" + authority + tc.path)
						require.NoError(t, err)
						resp.Body.Close()
						require.Equal(t, form.proto, resp.ProtoMajor)
						require.Equal(t, tc.status, resp.StatusCode, tc.path)
						require.Equal(t, tc.count, calls.Load()-before, tc.path)
						if tc.count == 1 {
							require.Equal(t, tc.path, resp.Header.Get("Received-URI"))
						}
					}
				})
			}
		})
	}
}

func TestInspectionBoundaries(t *testing.T) {
	ca, roots := testInspectionCA(t)
	// Even a matching certificate issued by the session CA is untrusted upstream.
	upstreamCert, err := ca.certificate("origin.test")
	require.NoError(t, err)
	var calls atomic.Int32
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	upstream.TLS = &tls.Config{Certificates: []tls.Certificate{*upstreamCert}}
	upstream.StartTLS()
	t.Cleanup(upstream.Close)
	_, port, err := net.SplitHostPort(upstream.Listener.Addr().String())
	require.NoError(t, err)
	authority := net.JoinHostPort("origin.test", port)
	proxy, err := newLoopbackProxy([]inspectionOrigin{{
		authority: authority,
		rules:     []requestRule{{paths: []string{"/**"}}},
	}}, ca, x509.NewCertPool())
	require.NoError(t, err)
	server := httptest.NewServer(proxy)
	t.Cleanup(server.Close)
	t.Cleanup(proxy.closeIdleConnections)
	proxyURL, err := url.Parse(server.URL)
	require.NoError(t, err)
	tr := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots}}
	t.Cleanup(tr.CloseIdleConnections)
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}

	for _, tc := range []struct {
		name, host string
		status     int
	}{
		{"inner authority", "other.test", http.StatusMisdirectedRequest},
		{"upstream certificate", authority, http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, "https://"+authority+"/public", nil)
			require.NoError(t, err)
			req.Host = tc.host
			resp, err := client.Do(req)
			require.NoError(t, err)
			resp.Body.Close()
			require.Equal(t, tc.status, resp.StatusCode)
			require.Zero(t, calls.Load())
		})
	}
	for _, tc := range []struct {
		target string
		status int
	}{
		{"https://" + authority + "/public", http.StatusBadRequest},
		{"http://other.test/public", http.StatusForbidden},
	} {
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.target, nil))
		require.Equal(t, tc.status, w.Code)
		require.Zero(t, calls.Load())
	}
}

func TestConnectAdmission(t *testing.T) {
	ca, _ := testInspectionCA(t)
	for _, tc := range []struct {
		name  string
		rules []requestRule
	}{
		{"whole host deny wins", []requestRule{{paths: []string{"/public"}}, {deny: true}}},
		{"deny alone does not admit", []requestRule{{deny: true, paths: []string{"/private"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy, err := newLoopbackProxy([]inspectionOrigin{{
				authority: "origin.test:443",
				rules:     tc.rules,
			}}, ca, nil)
			require.NoError(t, err)
			w := httptest.NewRecorder()
			proxy.ServeHTTP(w, httptest.NewRequest(http.MethodConnect, "origin.test:443", nil))
			require.Equal(t, http.StatusForbidden, w.Code)
		})
	}
}

func TestInspectedConnectionRequiresTLS(t *testing.T) {
	ca, _ := testInspectionCA(t)
	proxy, err := newLoopbackProxy([]inspectionOrigin{{
		authority: "origin.test:443",
		rules:     []requestRule{{paths: []string{"/public"}}},
	}}, ca, nil)
	require.NoError(t, err)
	server := httptest.NewServer(proxy)
	t.Cleanup(server.Close)
	conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(3*time.Second)))
	_, err = io.WriteString(conn, "CONNECT origin.test:443 HTTP/1.1\r\nHost: origin.test:443\r\n\r\n")
	require.NoError(t, err)
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_, err = io.WriteString(conn, "GET /public HTTP/1.1\r\nHost: origin.test\r\n\r\n")
	require.NoError(t, err)
	_, err = reader.ReadByte()
	require.ErrorIs(t, err, io.EOF)
}

func TestCancelServingContextEndsInspectedRequest(t *testing.T) {
	started, ended := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(ended)
	}))
	cert, upstreamRoots := testCertificate(t)
	upstream.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	upstream.StartTLS()
	t.Cleanup(upstream.Close)
	_, port, err := net.SplitHostPort(upstream.Listener.Addr().String())
	require.NoError(t, err)
	authority := net.JoinHostPort("origin.test", port)
	ca, roots := testInspectionCA(t)
	proxy, err := newLoopbackProxy([]inspectionOrigin{{
		authority: authority,
		rules:     []requestRule{{paths: []string{"/**"}}},
	}}, ca, upstreamRoots)
	require.NoError(t, err)
	server := httptest.NewUnstartedServer(proxy)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	server.Config.BaseContext = func(net.Listener) context.Context { return ctx }
	server.Start()
	t.Cleanup(server.Close)
	t.Cleanup(proxy.closeIdleConnections)
	proxyURL, err := url.Parse(server.URL)
	require.NoError(t, err)
	tr := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots}}
	t.Cleanup(tr.CloseIdleConnections)
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		if resp, err := client.Get("https://" + authority + "/public"); err == nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream never received the request")
	}
	cancel()
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("upstream request survived cancellation of the serving context")
	}
	select {
	case <-clientDone:
	case <-time.After(time.Second):
		t.Fatal("client connection survived cancellation of the serving context")
	}
}

func TestInnerHTTP1RequiresOriginForm(t *testing.T) {
	// net/http has already discarded the wire Host header in absolute form.
	route := inspectedRoute{authority: "origin.test:443"}
	req := httptest.NewRequest(http.MethodGet, "https://origin.test/public", nil)
	w := httptest.NewRecorder()
	route.serveInner(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

// Most wire tests use local upstreams. Opt into loopback explicitly, as an
// embedder must do for ordinary names and literals.
func newLoopbackProxy(origins []inspectionOrigin, ca *inspectionCA, roots *x509.CertPool) (*inspectionProxy, error) {
	return newInspectionProxy(origins, ca, roots, upstreamConfig{
		staticHosts: map[string]netip.Addr{"origin.test": netip.MustParseAddr("127.0.0.1")},
		gateway:     netip.MustParseAddr("127.0.0.1"),
		allowCIDRs:  []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
	})
}
