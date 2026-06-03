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

// Client certificate common names. NATS maps a TLS client to a user by the
// certificate's full RFC2253 subject DN (verified against nats-server v2.14.2),
// so a CN-only cert "vibepit-user" maps to NATS user "CN=vibepit-user".
const (
	cnUser     = "vibepit-user"
	cnInternal = "vibepit-internal"
	cnSandbox  = "vibepit-sandbox"

	// NATS user identities (full subject DN of the corresponding cert).
	NATSUserCN     = "CN=" + cnUser
	NATSInternalCN = "CN=" + cnInternal
	NATSSandboxCN  = "CN=" + cnSandbox
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

	// User client cert (CN=vibepit-user): host-side tools. Written to disk.
	ClientCert    *x509.Certificate
	clientCertDER []byte
	clientKey     ed25519.PrivateKey

	// Internal client cert (CN=vibepit-internal): the proxy's own connection.
	internalCertDER []byte
	internalKey     ed25519.PrivateKey

	// Sandbox client cert (CN=vibepit-sandbox): minted only, not distributed.
	sandboxCertDER []byte
	sandboxKey     ed25519.PrivateKey
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

	userCertDER, userKey, err := signClientCert(cnUser, caCert, caPriv, now, notAfter)
	if err != nil {
		return nil, err
	}
	userCert, err := x509.ParseCertificate(userCertDER)
	if err != nil {
		return nil, fmt.Errorf("parse user cert: %w", err)
	}
	internalCertDER, internalKey, err := signClientCert(cnInternal, caCert, caPriv, now, notAfter)
	if err != nil {
		return nil, err
	}
	sandboxCertDER, sandboxKey, err := signClientCert(cnSandbox, caCert, caPriv, now, notAfter)
	if err != nil {
		return nil, err
	}

	// CA private key is intentionally not stored — it leaves scope here.
	return &MTLSCredentials{
		CACert:          caCert,
		caCertDER:       caCertDER,
		ServerCert:      serverCert,
		serverCertDER:   serverCertDER,
		serverKey:       serverPriv,
		ClientCert:      userCert,
		clientCertDER:   userCertDER,
		clientKey:       userKey,
		internalCertDER: internalCertDER,
		internalKey:     internalKey,
		sandboxCertDER:  sandboxCertDER,
		sandboxKey:      sandboxKey,
	}, nil
}

// signClientCert mints an ed25519 client cert (EKU clientAuth) signed by the CA.
func signClientCert(cn string, ca *x509.Certificate, caPriv ed25519.PrivateKey, now, notAfter time.Time) (der []byte, key ed25519.PrivateKey, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate %s key: %w", cn, err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    now,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err = x509.CreateCertificate(rand.Reader, tmpl, ca, pub, caPriv)
	if err != nil {
		return nil, nil, fmt.Errorf("create %s cert: %w", cn, err)
	}
	return der, priv, nil
}

func randomSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}
	return serial, nil
}

// keyPEM encodes an ed25519 private key as PKCS#8 PEM.
func keyPEM(k ed25519.PrivateKey) []byte {
	raw, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		panic(fmt.Sprintf("marshal key: %v", err))
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw})
}

// certPEM encodes a DER-encoded certificate as PEM.
func certPEM(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// PEM encoding methods.

func (c *MTLSCredentials) CACertPEM() []byte     { return certPEM(c.caCertDER) }
func (c *MTLSCredentials) ServerCertPEM() []byte { return certPEM(c.serverCertDER) }
func (c *MTLSCredentials) ServerKeyPEM() []byte  { return keyPEM(c.serverKey) }
func (c *MTLSCredentials) ClientCertPEM() []byte { return certPEM(c.clientCertDER) }
func (c *MTLSCredentials) ClientKeyPEM() []byte  { return keyPEM(c.clientKey) }

func (c *MTLSCredentials) InternalClientCertPEM() []byte { return certPEM(c.internalCertDER) }
func (c *MTLSCredentials) InternalClientKeyPEM() []byte  { return keyPEM(c.internalKey) }
func (c *MTLSCredentials) SandboxClientCertPEM() []byte  { return certPEM(c.sandboxCertDER) }
func (c *MTLSCredentials) SandboxClientKeyPEM() []byte   { return keyPEM(c.sandboxKey) }

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

	EnvProxyInternalCert = "VIBEPIT_PROXY_INTERNAL_CERT"
	EnvProxyInternalKey  = "VIBEPIT_PROXY_INTERNAL_KEY"
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

// LoadInternalClientTLSConfigFromEnv builds the TLS config the proxy uses to
// dial its own embedded NATS server over the loopback listener, presenting the
// vibepit-internal client cert and pinning the session CA.
func LoadInternalClientTLSConfigFromEnv() (*tls.Config, error) {
	certPEM := os.Getenv(EnvProxyInternalCert)
	keyPEM := os.Getenv(EnvProxyInternalKey)
	caPEM := os.Getenv(EnvProxyCACert)
	if certPEM == "" || keyPEM == "" || caPEM == "" {
		return nil, fmt.Errorf("internal client TLS env vars must be set: %s, %s, %s",
			EnvProxyInternalCert, EnvProxyInternalKey, EnvProxyCACert)
	}
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("load internal client keypair: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM([]byte(caPEM)) {
		return nil, fmt.Errorf("failed to parse CA certificate from %s", EnvProxyCACert)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
		ServerName:   "127.0.0.1",
	}, nil
}
