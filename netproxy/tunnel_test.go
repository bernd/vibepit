package netproxy

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func wholeHostProxy(t *testing.T, host, addr string, report func(tunnelRefusal)) (*httptest.Server, string) {
	t.Helper()
	pinned := netip.MustParseAddrPort(addr)
	authority := net.JoinHostPort(host, fmt.Sprint(pinned.Port()))
	proxy, err := newInspectionProxy([]inspectionOrigin{{
		authority: authority, address: pinned.Addr(), rules: []requestRule{{}},
	}}, nil)
	require.NoError(t, err)
	proxy.onTunnelRefusal = report
	server := httptest.NewServer(proxy)
	t.Cleanup(server.Close)
	t.Cleanup(proxy.closeIdleConnections)
	return server, authority
}

func openTunnel(t *testing.T, server *httptest.Server, authority string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	_, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", authority, authority)
	require.NoError(t, err)
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return &readConn{Conn: conn, reader: reader}
}

type readConn struct {
	net.Conn
	reader io.Reader
}

func (c *readConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func TestWholeHostTLSIsEndToEnd(t *testing.T) {
	cert, roots := testCertificate(t)
	for _, tc := range []struct {
		name    string
		group   tls.CurveID
		version uint16
	}{
		{"TLS12", tls.X25519, tls.VersionTLS12},
		{"TLS13", tls.X25519, tls.VersionTLS13},
		{"TLS13 retry", tls.CurveP256, tls.VersionTLS13},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
			up.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, CurvePreferences: []tls.CurveID{tc.group}, MinVersion: tc.version, MaxVersion: tc.version}
			up.StartTLS()
			t.Cleanup(up.Close)
			proxy, authority := wholeHostProxy(t, "origin.test", up.Listener.Addr().String(), nil)
			proxyURL, err := url.Parse(proxy.URL)
			require.NoError(t, err)
			tr := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots, CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256}}}
			t.Cleanup(tr.CloseIdleConnections)
			client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
			resp, err := client.Get("https://" + authority + "/")
			require.NoError(t, err)
			resp.Body.Close()
			require.Equal(t, http.StatusNoContent, resp.StatusCode)
			require.Equal(t, cert.Certificate[0], resp.TLS.PeerCertificates[0].Raw)
			require.Equal(t, tc.name == "TLS13 retry", resp.TLS.HelloRetryRequest)
		})
	}
}

func TestTunnelRefusesUnboundServerNameBeforeForwarding(t *testing.T) {
	for _, serverName := range []string{"other.test", "127.0.0.1"} {
		t.Run(serverName, func(t *testing.T) {
			ln, err := net.Listen("tcp4", "127.0.0.1:0")
			require.NoError(t, err)
			t.Cleanup(func() { ln.Close() })
			proxy, authority := wholeHostProxy(t, "origin.test", ln.Addr().String(), nil)
			conn := openTunnel(t, proxy, authority)
			up, err := ln.Accept()
			require.NoError(t, err)
			defer up.Close()
			peer := tls.Client(conn, &tls.Config{ServerName: serverName})
			require.Error(t, peer.Handshake())
			require.NoError(t, up.SetReadDeadline(time.Now().Add(time.Second)))
			bytes, err := io.ReadAll(up)
			require.NoError(t, err)
			require.Empty(t, bytes)
		})
	}
}

func TestTunnelServerFirstAndHalfClose(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	proxy, authority := wholeHostProxy(t, "origin.test", ln.Addr().String(), nil)
	conn := openTunnel(t, proxy, authority)
	up, err := ln.Accept()
	require.NoError(t, err)
	defer up.Close()
	require.NoError(t, up.SetDeadline(time.Now().Add(5*time.Second)))
	_, err = io.WriteString(up, "hello\n")
	require.NoError(t, err)
	line, err := bufio.NewReader(conn).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "hello\n", line)
	_, err = io.WriteString(conn, "request")
	require.NoError(t, err)
	require.NoError(t, conn.(*readConn).Conn.(*net.TCPConn).CloseWrite())
	got, err := io.ReadAll(up)
	require.NoError(t, err)
	require.Equal(t, "request", string(got))
	_, err = io.WriteString(up, "reply")
	require.NoError(t, err)
	require.NoError(t, up.(*net.TCPConn).CloseWrite())
	got, err = io.ReadAll(conn)
	require.NoError(t, err)
	require.Equal(t, "reply", string(got))
}

func TestNonTLSBinaryTunnel(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	proxy, authority := wholeHostProxy(t, "origin.test", ln.Addr().String(), nil)
	client := openTunnel(t, proxy, authority)
	up, err := ln.Accept()
	require.NoError(t, err)
	defer up.Close()
	require.NoError(t, up.SetDeadline(time.Now().Add(3*time.Second)))
	want := []byte{0x90, 0x00, 0x42, 0x00}
	_, err = client.Write(want)
	require.NoError(t, err)
	got := make([]byte, len(want))
	_, err = io.ReadFull(up, got)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestTunnelRelaysUpstreamTLSRejection(t *testing.T) {
	cert, roots := testCertificate(t)
	up := httptest.NewUnstartedServer(http.NotFoundHandler())
	up.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MaxVersion: tls.VersionTLS12}
	up.StartTLS()
	defer up.Close()
	outcomes := make(chan tunnelRefusal, 1)
	proxy, authority := wholeHostProxy(t, "origin.test", up.Listener.Addr().String(), func(f tunnelRefusal) { outcomes <- f })
	conn := openTunnel(t, proxy, authority)
	client := tls.Client(conn, &tls.Config{ServerName: "origin.test", RootCAs: roots, MinVersion: tls.VersionTLS13})
	require.ErrorContains(t, client.Handshake(), "protocol version")
	select {
	case f := <-outcomes:
		t.Fatalf("upstream TLS alert reported as a policy refusal: %+v", f)
	default:
	}
}

func TestAddressAndHostPortTunnels(t *testing.T) {
	cert, roots := testCertificate(t)
	up := httptest.NewUnstartedServer(http.NotFoundHandler())
	up.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	up.StartTLS()
	defer up.Close()
	endpoint := netip.MustParseAddrPort(up.Listener.Addr().String())
	for _, tc := range []struct {
		host     string
		hostPort bool
	}{
		{"127.0.0.1", false}, {"gateway.test", true},
	} {
		t.Run(tc.host, func(t *testing.T) {
			authority := net.JoinHostPort(tc.host, fmt.Sprint(endpoint.Port()))
			p, err := newInspectionProxy([]inspectionOrigin{{authority: authority, address: endpoint.Addr(), rules: []requestRule{{}}, hostPort: tc.hostPort}}, nil)
			require.NoError(t, err)
			server := httptest.NewServer(p)
			defer server.Close()
			c := tls.Client(openTunnel(t, server, authority), &tls.Config{ServerName: "origin.test", RootCAs: roots})
			require.NoError(t, c.Handshake())
			require.Equal(t, cert.Certificate[0], c.ConnectionState().PeerCertificates[0].Raw)
			c.Close()
		})
	}
}

// Send the CONNECT line and the TLS client's first write together. A proxy
// that reads the raw hijacked socket loses any hello bytes net/http prefetched.
type earlyConnect struct {
	net.Conn
	authority string
	reader    *bufio.Reader
	started   bool
}

func (c *earlyConnect) Write(p []byte) (int, error) {
	if c.started {
		return c.Conn.Write(p)
	}
	c.started = true
	request := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", c.authority, c.authority)
	if _, err := c.Conn.Write(append([]byte(request), p...)); err != nil {
		return 0, err
	}
	resp, err := http.ReadResponse(c.reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("CONNECT returned %d", resp.StatusCode)
	}
	return len(p), nil
}
func (c *earlyConnect) Read(p []byte) (int, error) { return c.reader.Read(p) }

func TestPrefetchedTunnelHello(t *testing.T) {
	cert, roots := testCertificate(t)
	up := httptest.NewUnstartedServer(http.NotFoundHandler())
	up.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	up.StartTLS()
	defer up.Close()
	server, authority := wholeHostProxy(t, "origin.test", up.Listener.Addr().String(), nil)
	raw, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	require.NoError(t, err)
	defer raw.Close()
	require.NoError(t, raw.SetDeadline(time.Now().Add(3*time.Second)))
	c := tls.Client(&earlyConnect{Conn: raw, authority: authority, reader: bufio.NewReader(raw)}, &tls.Config{ServerName: "origin.test", RootCAs: roots})
	require.NoError(t, c.Handshake())
	require.Equal(t, cert.Certificate[0], c.ConnectionState().PeerCertificates[0].Raw)
	c.Close()
}
