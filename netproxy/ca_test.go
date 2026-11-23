package netproxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestInspectedOriginsPresentSingleHostCertificates(t *testing.T) {
	names := []string{"one.test", "two.test"}
	upCert, upstreamRoots := testCertificate(t, names...)
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Received-Host", r.Host)
		w.WriteHeader(http.StatusNoContent)
	}))
	upstream.TLS = &tls.Config{Certificates: []tls.Certificate{upCert}}
	upstream.StartTLS()
	t.Cleanup(upstream.Close)
	addr := netip.MustParseAddrPort(upstream.Listener.Addr().String()).Addr()
	_, port, err := net.SplitHostPort(upstream.Listener.Addr().String())
	require.NoError(t, err)
	cfg := upstreamConfig{staticHosts: make(map[string]netip.Addr), allowCIDRs: []netip.Prefix{netip.PrefixFrom(addr, 32)}}
	var origins []inspectionOrigin
	for _, name := range names {
		cfg.staticHosts[name] = addr
		origins = append(origins, inspectionOrigin{authority: net.JoinHostPort(name, port), rules: []requestRule{{paths: []string{"/public"}}}})
	}
	ca, roots := testInspectionCA(t)
	p, err := newInspectionProxy(origins, ca, upstreamRoots, cfg)
	require.NoError(t, err)
	t.Cleanup(p.closeIdleConnections)
	server := httptest.NewServer(p)
	t.Cleanup(server.Close)
	proxyURL, err := url.Parse(server.URL)
	require.NoError(t, err)
	tr := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots}}
	t.Cleanup(tr.CloseIdleConnections)
	client := &http.Client{Transport: tr, Timeout: 3 * time.Second}
	for i, name := range names {
		authority := net.JoinHostPort(name, port)
		res, err := client.Get("https://" + authority + "/public")
		require.NoError(t, err)
		res.Body.Close()
		require.Equal(t, http.StatusNoContent, res.StatusCode)
		require.Equal(t, authority, res.Header.Get("Received-Host"))
		leaf := res.TLS.PeerCertificates[0]
		require.Equal(t, []string{name}, leaf.DNSNames)
		require.Empty(t, leaf.IPAddresses)
		require.Error(t, leaf.VerifyHostname(names[1-i]))
	}
	// Even another admitted host's SNI cannot select its certificate on this CONNECT.
	c := tls.Client(openTunnel(t, server, net.JoinHostPort(names[0], port)), &tls.Config{ServerName: names[1], RootCAs: roots})
	defer c.Close()
	var mismatch x509.HostnameError
	require.ErrorAs(t, c.Handshake(), &mismatch)
	require.Equal(t, []string{names[0]}, mismatch.Certificate.DNSNames)
}

func testInspectionCA(t *testing.T) (*inspectionCA, *x509.CertPool) {
	t.Helper()
	ca, err := newInspectionCA()
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(ca.root)
	return ca, roots
}

func TestInspectionLeafCacheEvictsLeastRecentlyUsedHost(t *testing.T) {
	ca, _ := testInspectionCA(t)
	get := func(host string) *x509.Certificate {
		cert, err := ca.certificate(host)
		require.NoError(t, err)
		return cert.Leaf
	}
	first := get("first.test")
	second := get("second.test")
	for i := 2; i < 128; i++ {
		get(fmt.Sprintf("host-%d.test", i))
	}
	require.True(t, first.Equal(get("first.test")))
	get("overflow.test")
	require.True(t, first.Equal(get("first.test")))
	require.False(t, second.Equal(get("second.test")))
}

func TestInspectionLeafValidity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ca, roots := testInspectionCA(t)
		const host = "192.0.2.1"
		get := func() *x509.Certificate {
			cert, err := ca.certificate(host)
			require.NoError(t, err)
			leaf := cert.Leaf
			_, err = leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: host})
			require.NoError(t, err)
			require.Empty(t, leaf.DNSNames)
			require.Len(t, leaf.IPAddresses, 1)
			require.False(t, leaf.IsCA)
			require.Equal(t, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, leaf.ExtKeyUsage)
			require.Equal(t, elliptic.P256(), leaf.PublicKey.(*ecdsa.PublicKey).Curve)
			return leaf
		}
		first := get()
		require.Equal(t, 30*24*time.Hour, ca.root.NotAfter.Sub(ca.root.NotBefore))
		require.Equal(t, 24*time.Hour, first.NotAfter.Sub(first.NotBefore))
		_, otherSessionRoots := testInspectionCA(t)
		_, err := first.Verify(x509.VerifyOptions{Roots: otherSessionRoots, DNSName: host})
		var untrusted x509.UnknownAuthorityError
		require.ErrorAs(t, err, &untrusted)
		time.Sleep(12 * time.Hour)
		require.True(t, first.Equal(get()))
		time.Sleep(time.Until(first.NotAfter.Add(-time.Minute)))
		renewed := get()
		require.NotEqual(t, first.SerialNumber, renewed.SerialNumber)
		require.True(t, renewed.NotAfter.After(first.NotAfter))
		time.Sleep(time.Until(ca.root.NotAfter.Add(-time.Minute)))
		final := get()
		require.Equal(t, ca.root.NotAfter, final.NotAfter)
		require.True(t, final.Equal(get()))
		time.Sleep(time.Until(ca.root.NotAfter))
		_, err = ca.certificate(host)
		require.Error(t, err)
	})
}

func TestConcurrentCertificateRequestsShareOneLeaf(t *testing.T) {
	ca, _ := testInspectionCA(t)
	start := make(chan struct{})
	leaves := make(chan *tls.Certificate, 16)
	for range cap(leaves) {
		go func() {
			<-start
			cert, err := ca.certificate("shared.test")
			if err != nil {
				t.Error(err)
			}
			leaves <- cert
		}()
	}
	close(start)
	first := <-leaves
	require.NotNil(t, first)
	for i := 1; i < cap(leaves); i++ {
		next := <-leaves
		require.NotNil(t, next)
		require.True(t, first.Leaf.Equal(next.Leaf))
	}
}
