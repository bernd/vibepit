// proxy/mtls.go
package proxy

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"time"
)

// MTLSCredentials holds the ephemeral CA, server, and client certificates
// generated for a single vibepit session. The CA private key is not stored
// after signing — only the public cert is retained for verification.
type MTLSCredentials struct {
	CACert    *x509.Certificate
	caCertDER []byte

	ServerCert    *x509.Certificate
	serverCertDER []byte
	serverKey     ed25519.PrivateKey

	ClientCert    *x509.Certificate
	clientCertDER []byte
	clientKey     ed25519.PrivateKey
}

// GenerateMTLSCredentials creates an ephemeral CA and signs a server cert
// (SAN: 127.0.0.1, EKU: serverAuth) and a client cert (EKU: clientAuth).
// The CA private key is discarded after signing.
func GenerateMTLSCredentials(lifetime time.Duration) (*MTLSCredentials, error) {
	now := time.Now()
	notAfter := now.Add(lifetime)

	// Generate ephemeral CA.
	caPub, caPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate CA key: %w", err)
	}

	caSerial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          caSerial,
		Subject:               pkix.Name{CommonName: "Vibepit Ephemeral CA"},
		NotBefore:             now,
		NotAfter:              notAfter,
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPub, caPriv)
	if err != nil {
		return nil, fmt.Errorf("create CA cert: %w", err)
	}
	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		return nil, fmt.Errorf("parse CA cert: %w", err)
	}

	// Generate server cert.
	serverPub, serverPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate server key: %w", err)
	}
	serverSerial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: serverSerial,
		Subject:      pkix.Name{CommonName: "vibepit proxy"},
		NotBefore:    now,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	serverCertDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, serverPub, caPriv)
	if err != nil {
		return nil, fmt.Errorf("create server cert: %w", err)
	}
	serverCert, err := x509.ParseCertificate(serverCertDER)
	if err != nil {
		return nil, fmt.Errorf("parse server cert: %w", err)
	}

	// Generate client cert.
	clientPub, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate client key: %w", err)
	}
	clientSerial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	clientTemplate := &x509.Certificate{
		SerialNumber: clientSerial,
		Subject:      pkix.Name{CommonName: "vibepit CLI"},
		NotBefore:    now,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientCertDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caCert, clientPub, caPriv)
	if err != nil {
		return nil, fmt.Errorf("create client cert: %w", err)
	}
	clientCert, err := x509.ParseCertificate(clientCertDER)
	if err != nil {
		return nil, fmt.Errorf("parse client cert: %w", err)
	}

	// CA private key is intentionally not stored — it leaves scope here.
	return &MTLSCredentials{
		CACert:        caCert,
		caCertDER:     caCertDER,
		ServerCert:    serverCert,
		serverCertDER: serverCertDER,
		serverKey:     serverPriv,
		ClientCert:    clientCert,
		clientCertDER: clientCertDER,
		clientKey:     clientPriv,
	}, nil
}

func randomSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}
	return serial, nil
}

// PEM encoding methods.

func (c *MTLSCredentials) CACertPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.caCertDER})
}

func (c *MTLSCredentials) ServerCertPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.serverCertDER})
}

func (c *MTLSCredentials) ServerKeyPEM() []byte {
	raw, err := x509.MarshalPKCS8PrivateKey(c.serverKey)
	if err != nil {
		panic(fmt.Sprintf("marshal server key: %v", err))
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw})
}

func (c *MTLSCredentials) ClientCertPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.clientCertDER})
}

func (c *MTLSCredentials) ClientKeyPEM() []byte {
	raw, err := x509.MarshalPKCS8PrivateKey(c.clientKey)
	if err != nil {
		panic(fmt.Sprintf("marshal client key: %v", err))
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw})
}

// ServerTLSConfig returns a tls.Config for the proxy control API server.
// It requires TLS 1.3 and verifies client certificates against the ephemeral CA.
func (c *MTLSCredentials) ServerTLSConfig() (*tls.Config, error) {
	serverCert, err := tls.X509KeyPair(c.ServerCertPEM(), c.ServerKeyPEM())
	if err != nil {
		return nil, fmt.Errorf("load server keypair: %w", err)
	}

	caPool := x509.NewCertPool()
	caPool.AddCert(c.CACert)

	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    caPool,
	}, nil
}

// ClientTLSConfig returns a tls.Config for CLI commands calling the control API.
// It pins the ephemeral CA as the only trusted root and presents the client cert.
func (c *MTLSCredentials) ClientTLSConfig() (*tls.Config, error) {
	clientCert, err := tls.X509KeyPair(c.ClientCertPEM(), c.ClientKeyPEM())
	if err != nil {
		return nil, fmt.Errorf("load client keypair: %w", err)
	}

	caPool := x509.NewCertPool()
	caPool.AddCert(c.CACert)

	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      caPool,
	}, nil
}

const (
	EnvProxyTLSKey  = "VIBEPIT_PROXY_TLS_KEY"
	EnvProxyTLSCert = "VIBEPIT_PROXY_TLS_CERT"
	EnvProxyCACert  = "VIBEPIT_PROXY_CA_CERT"
)

// LoadServerTLSConfigFromEnv reads PEM-encoded TLS material from environment
// variables and returns a tls.Config for the control API. Returns an error
// if any of the required env vars are missing — the control API must not
// start without mTLS.
func LoadServerTLSConfigFromEnv() (*tls.Config, error) {
	keyPEM := os.Getenv(EnvProxyTLSKey)
	certPEM := os.Getenv(EnvProxyTLSCert)
	caPEM := os.Getenv(EnvProxyCACert)

	if keyPEM == "" || certPEM == "" || caPEM == "" {
		return nil, fmt.Errorf("all three TLS env vars must be set: %s, %s, %s",
			EnvProxyTLSKey, EnvProxyTLSCert, EnvProxyCACert)
	}

	serverCert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("load server keypair from env: %w", err)
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM([]byte(caPEM)) {
		return nil, fmt.Errorf("failed to parse CA certificate from %s", EnvProxyCACert)
	}

	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    caPool,
	}, nil
}
