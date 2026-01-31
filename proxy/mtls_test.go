// proxy/mtls_test.go
package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateMTLSCredentials(t *testing.T) {
	creds, err := GenerateMTLSCredentials(30 * 24 * time.Hour)
	require.NoError(t, err)

	t.Run("CA cert is self-signed and valid", func(t *testing.T) {
		require.NotNil(t, creds.CACert)
		assert.True(t, creds.CACert.IsCA)
		assert.Equal(t, creds.CACert.Issuer, creds.CACert.Subject)
	})

	t.Run("server cert has correct SAN and EKU", func(t *testing.T) {
		require.NotNil(t, creds.ServerCert)
		assert.Contains(t, creds.ServerCert.IPAddresses, net.IPv4(127, 0, 0, 1).To4())
		assert.Contains(t, creds.ServerCert.ExtKeyUsage, x509.ExtKeyUsageServerAuth)
		assert.NotContains(t, creds.ServerCert.ExtKeyUsage, x509.ExtKeyUsageClientAuth)
	})

	t.Run("client cert has correct EKU", func(t *testing.T) {
		require.NotNil(t, creds.ClientCert)
		assert.Contains(t, creds.ClientCert.ExtKeyUsage, x509.ExtKeyUsageClientAuth)
		assert.NotContains(t, creds.ClientCert.ExtKeyUsage, x509.ExtKeyUsageServerAuth)
	})

	t.Run("server cert is signed by CA", func(t *testing.T) {
		pool := x509.NewCertPool()
		pool.AddCert(creds.CACert)
		_, err := creds.ServerCert.Verify(x509.VerifyOptions{Roots: pool})
		require.NoError(t, err)
	})

	t.Run("client cert is signed by CA", func(t *testing.T) {
		pool := x509.NewCertPool()
		pool.AddCert(creds.CACert)
		_, err := creds.ClientCert.Verify(x509.VerifyOptions{
			Roots:     pool,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		})
		require.NoError(t, err)
	})

	t.Run("cert lifetime matches requested duration", func(t *testing.T) {
		diff := creds.CACert.NotAfter.Sub(creds.CACert.NotBefore)
		assert.InDelta(t, (30 * 24 * time.Hour).Seconds(), diff.Seconds(), 5)
	})
}

func TestMTLSCredentialsPEM(t *testing.T) {
	creds, err := GenerateMTLSCredentials(24 * time.Hour)
	require.NoError(t, err)

	t.Run("PEM round-trips for CA cert", func(t *testing.T) {
		pem := creds.CACertPEM()
		assert.Contains(t, string(pem), "BEGIN CERTIFICATE")
	})

	t.Run("PEM round-trips for server", func(t *testing.T) {
		certPEM, keyPEM := creds.ServerCertPEM(), creds.ServerKeyPEM()
		_, err := tls.X509KeyPair(certPEM, keyPEM)
		require.NoError(t, err)
	})

	t.Run("PEM round-trips for client", func(t *testing.T) {
		certPEM, keyPEM := creds.ClientCertPEM(), creds.ClientKeyPEM()
		_, err := tls.X509KeyPair(certPEM, keyPEM)
		require.NoError(t, err)
	})
}

func TestMTLSHandshake(t *testing.T) {
	creds, err := GenerateMTLSCredentials(24 * time.Hour)
	require.NoError(t, err)

	serverTLS, err := creds.ServerTLSConfig()
	require.NoError(t, err)

	// Start a TLS server with the proxy's config.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverTLS)
	require.NoError(t, err)
	defer ln.Close()

	srv := &http.Server{Handler: handler}
	go srv.Serve(ln)
	defer srv.Close()

	addr := ln.Addr().String()

	t.Run("client with valid cert succeeds", func(t *testing.T) {
		clientTLS, err := creds.ClientTLSConfig()
		require.NoError(t, err)

		client := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLS}}
		resp, err := client.Get("https://" + addr)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("client without cert is rejected", func(t *testing.T) {
		caPool := x509.NewCertPool()
		caPool.AddCert(creds.CACert)
		noCertTLS := &tls.Config{
			MinVersion: tls.VersionTLS13,
			RootCAs:    caPool,
		}

		client := &http.Client{Transport: &http.Transport{TLSClientConfig: noCertTLS}}
		_, err := client.Get("https://" + addr)
		require.Error(t, err)
	})

	t.Run("client with wrong CA is rejected", func(t *testing.T) {
		otherCreds, err := GenerateMTLSCredentials(24 * time.Hour)
		require.NoError(t, err)

		otherTLS, err := otherCreds.ClientTLSConfig()
		require.NoError(t, err)
		// Override RootCAs to trust the real server's CA so TLS dial succeeds
		// far enough for the server to check the client cert.
		caPool := x509.NewCertPool()
		caPool.AddCert(creds.CACert)
		otherTLS.RootCAs = caPool

		client := &http.Client{Transport: &http.Transport{TLSClientConfig: otherTLS}}
		_, err = client.Get("https://" + addr)
		require.Error(t, err)
	})
}

func TestServerTLSConfigFromEnv(t *testing.T) {
	creds, err := GenerateMTLSCredentials(24 * time.Hour)
	require.NoError(t, err)

	t.Setenv("VIBEPIT_PROXY_TLS_KEY", string(creds.ServerKeyPEM()))
	t.Setenv("VIBEPIT_PROXY_TLS_CERT", string(creds.ServerCertPEM()))
	t.Setenv("VIBEPIT_PROXY_CA_CERT", string(creds.CACertPEM()))

	tlsCfg, err := LoadServerTLSConfigFromEnv()
	require.NoError(t, err)
	require.NotNil(t, tlsCfg)
	assert.Equal(t, tls.RequireAndVerifyClientCert, tlsCfg.ClientAuth)
	assert.Equal(t, uint16(tls.VersionTLS13), tlsCfg.MinVersion)
}

func TestServerTLSConfigFromEnvMissing(t *testing.T) {
	// With no env vars set, returns an error — the control API refuses to
	// start without mTLS.
	tlsCfg, err := LoadServerTLSConfigFromEnv()
	require.Error(t, err)
	assert.Nil(t, tlsCfg)
}
