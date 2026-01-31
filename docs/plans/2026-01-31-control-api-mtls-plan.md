# Control API mTLS Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Secure the proxy control API with ephemeral mTLS so only the host CLI can access it.

**Architecture:** At launch, generate an ephemeral Ed25519 CA, sign a server cert (SAN: 127.0.0.1) and a client cert, pass server material to the proxy via env vars, write client material to `$XDG_RUNTIME_DIR`, publish the control API to `127.0.0.1:<random>` on the host. The proxy requires TLS 1.3 + client cert verification. CLI subcommands discover the port via Docker labels and load certs from the runtime dir.

**Tech Stack:** Go stdlib `crypto/ed25519`, `crypto/x509`, `crypto/tls`, `encoding/pem`

**Design doc:** `docs/plans/2026-01-31-control-api-mtls-design.md`

---

### Task 1: Certificate Authority Generation (`proxy/mtls.go`)

**Files:**
- Create: `proxy/mtls.go`
- Test: `proxy/mtls_test.go`

This task creates the core crypto: generating an ephemeral CA, signing server and client certs. All in-memory, no disk I/O. The `proxy` package is the right home since both the proxy server and CLI need the cert types.

**Step 1: Write the failing test**

```go
// proxy/mtls_test.go
package proxy

import (
	"crypto/tls"
	"crypto/x509"
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
			Roots:    pool,
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
```

Note: add `"net"` to the imports in the first test function.

**Step 2: Run test to verify it fails**

Run: `cd /home/bernd/Code/vibepit/.worktrees/control-api-mtls && go test ./proxy/ -run TestGenerateMTLS -v`
Expected: Compilation error — `GenerateMTLSCredentials` undefined.

**Step 3: Write minimal implementation**

```go
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
	"time"
)

// MTLSCredentials holds the ephemeral CA, server, and client certificates
// generated for a single vibepit session. The CA private key is not stored
// after signing — only the public cert is retained for verification.
type MTLSCredentials struct {
	CACert *x509.Certificate
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
		Subject:               pkix.Name{CommonName: "vibepit ephemeral CA"},
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
	raw, _ := x509.MarshalPKCS8PrivateKey(c.serverKey)
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw})
}

func (c *MTLSCredentials) ClientCertPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.clientCertDER})
}

func (c *MTLSCredentials) ClientKeyPEM() []byte {
	raw, _ := x509.MarshalPKCS8PrivateKey(c.clientKey)
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
```

**Step 4: Run tests to verify they pass**

Run: `cd /home/bernd/Code/vibepit/.worktrees/control-api-mtls && go test ./proxy/ -run TestGenerateMTLS -v && go test ./proxy/ -run TestMTLSCredentialsPEM -v`
Expected: All pass.

**Step 5: Commit**

```bash
git add proxy/mtls.go proxy/mtls_test.go
git commit -m "feat: add ephemeral mTLS certificate generation"
```

---

### Task 2: mTLS Integration Test (`proxy/mtls_test.go`)

**Files:**
- Modify: `proxy/mtls_test.go`

Verify that an actual TLS handshake works end-to-end: server requires client cert, client presents one, and a client without certs is rejected.

**Step 1: Write the failing test**

Append to `proxy/mtls_test.go`:

```go
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
```

Add `"net/http"` to the imports.

**Step 2: Run test to verify it fails**

Run: `cd /home/bernd/Code/vibepit/.worktrees/control-api-mtls && go test ./proxy/ -run TestMTLSHandshake -v`
Expected: Should pass if Task 1 was done correctly. If it fails, debug and fix.

**Step 3: Commit**

```bash
git add proxy/mtls_test.go
git commit -m "test: add mTLS handshake integration tests"
```

---

### Task 3: Proxy Server Uses mTLS for Control API (`proxy/server.go`)

**Files:**
- Modify: `proxy/server.go:20-31` (ProxyConfig, Server struct)
- Modify: `proxy/server.go:51-84` (Run method)

The proxy reads TLS material from env vars and starts the control API with mTLS.

**Step 1: Write the failing test**

Append to `proxy/mtls_test.go`:

```go
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
	// With no env vars set, returns nil (no TLS).
	tlsCfg, err := LoadServerTLSConfigFromEnv()
	require.NoError(t, err)
	assert.Nil(t, tlsCfg)
}
```

**Step 2: Run test to verify it fails**

Run: `cd /home/bernd/Code/vibepit/.worktrees/control-api-mtls && go test ./proxy/ -run TestServerTLSConfigFromEnv -v`
Expected: Compilation error — `LoadServerTLSConfigFromEnv` undefined.

**Step 3: Implement `LoadServerTLSConfigFromEnv`**

Add to `proxy/mtls.go`:

```go
const (
	EnvProxyTLSKey  = "VIBEPIT_PROXY_TLS_KEY"
	EnvProxyTLSCert = "VIBEPIT_PROXY_TLS_CERT"
	EnvProxyCACert  = "VIBEPIT_PROXY_CA_CERT"
)

// LoadServerTLSConfigFromEnv reads PEM-encoded TLS material from environment
// variables and returns a tls.Config for the control API. Returns (nil, nil)
// if the env vars are not set, signaling that TLS should be disabled.
func LoadServerTLSConfigFromEnv() (*tls.Config, error) {
	keyPEM := os.Getenv(EnvProxyTLSKey)
	certPEM := os.Getenv(EnvProxyTLSCert)
	caPEM := os.Getenv(EnvProxyCACert)

	if keyPEM == "" && certPEM == "" && caPEM == "" {
		return nil, nil
	}
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
```

Add `"os"` to the imports in `proxy/mtls.go`.

**Step 4: Update `Server.Run()` to use mTLS**

In `proxy/server.go`, replace the control API goroutine (lines 73-76):

```go
// Before:
go func() {
    fmt.Printf("proxy: control API listening on %s\n", ControlAPIPort)
    errCh <- http.ListenAndServe(ControlAPIPort, controlAPI)
}()

// After:
go func() {
    tlsCfg, err := LoadServerTLSConfigFromEnv()
    if err != nil {
        errCh <- fmt.Errorf("control API TLS: %w", err)
        return
    }
    if tlsCfg != nil {
        fmt.Printf("proxy: control API listening on %s (mTLS)\n", ControlAPIPort)
        ln, err := tls.Listen("tcp", ControlAPIPort, tlsCfg)
        if err != nil {
            errCh <- err
            return
        }
        errCh <- http.Serve(ln, controlAPI)
    } else {
        fmt.Printf("proxy: control API listening on %s (no TLS)\n", ControlAPIPort)
        errCh <- http.ListenAndServe(ControlAPIPort, controlAPI)
    }
}()
```

Add `"crypto/tls"` to the imports in `proxy/server.go`.

**Step 5: Run all proxy tests**

Run: `cd /home/bernd/Code/vibepit/.worktrees/control-api-mtls && go test ./proxy/ -v`
Expected: All pass.

**Step 6: Commit**

```bash
git add proxy/mtls.go proxy/mtls_test.go proxy/server.go
git commit -m "feat: proxy control API supports mTLS via env vars"
```

---

### Task 4: Session Runtime Directory (`cmd/session.go`)

**Files:**
- Create: `cmd/session.go`
- Test: `cmd/session_test.go`

Create a session directory under `$XDG_RUNTIME_DIR/vibepit/<session-id>/` to store client TLS material that subcommands can read.

**Step 1: Write the failing test**

```go
// cmd/session_test.go
package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bernd/vibepit/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteSessionCredentials(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	sessionID := "test-session-abc"
	creds, err := proxy.GenerateMTLSCredentials(24 * time.Hour)
	require.NoError(t, err)

	dir, err := WriteSessionCredentials(sessionID, creds)
	require.NoError(t, err)

	expected := filepath.Join(tmpDir, "vibepit", sessionID)
	assert.Equal(t, expected, dir)

	// Verify files exist with correct permissions.
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), info.Mode().Perm())

	for _, name := range []string{"ca.pem", "client-key.pem", "client-cert.pem"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err, "reading %s", name)
		assert.NotEmpty(t, data)

		info, err := os.Stat(filepath.Join(dir, name))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	}
}

func TestReadSessionCredentials(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	sessionID := "test-session-read"
	creds, err := proxy.GenerateMTLSCredentials(24 * time.Hour)
	require.NoError(t, err)

	_, err = WriteSessionCredentials(sessionID, creds)
	require.NoError(t, err)

	tlsCfg, err := LoadSessionTLSConfig(sessionID)
	require.NoError(t, err)
	require.NotNil(t, tlsCfg)
	assert.NotEmpty(t, tlsCfg.Certificates)
	assert.NotNil(t, tlsCfg.RootCAs)
}

func TestCleanupSessionCredentials(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	sessionID := "test-session-cleanup"
	creds, err := proxy.GenerateMTLSCredentials(24 * time.Hour)
	require.NoError(t, err)

	dir, err := WriteSessionCredentials(sessionID, creds)
	require.NoError(t, err)

	err = CleanupSessionCredentials(sessionID)
	require.NoError(t, err)

	_, err = os.Stat(dir)
	assert.True(t, os.IsNotExist(err))
}
```

**Step 2: Run test to verify it fails**

Run: `cd /home/bernd/Code/vibepit/.worktrees/control-api-mtls && go test ./cmd/ -run TestWriteSession -v`
Expected: Compilation error.

**Step 3: Write implementation**

```go
// cmd/session.go
package cmd

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bernd/vibepit/proxy"
)

// sessionBaseDir returns $XDG_RUNTIME_DIR/vibepit.
func sessionBaseDir() (string, error) {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if runtime == "" {
		return "", fmt.Errorf("XDG_RUNTIME_DIR is not set")
	}
	return filepath.Join(runtime, "vibepit"), nil
}

// sessionDir returns the path for a specific session's credentials.
func sessionDir(sessionID string) (string, error) {
	base, err := sessionBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, sessionID), nil
}

// WriteSessionCredentials writes the client TLS material to the session
// runtime directory so subcommands (allow, monitor) can load it.
func WriteSessionCredentials(sessionID string, creds *proxy.MTLSCredentials) (string, error) {
	dir, err := sessionDir(sessionID)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create session dir: %w", err)
	}
	// Ensure the session-specific dir has correct permissions even if the
	// parent was created by a previous call.
	if err := os.Chmod(dir, 0700); err != nil {
		return "", fmt.Errorf("chmod session dir: %w", err)
	}

	files := map[string][]byte{
		"ca.pem":          creds.CACertPEM(),
		"client-key.pem":  creds.ClientKeyPEM(),
		"client-cert.pem": creds.ClientCertPEM(),
	}
	for name, data := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0600); err != nil {
			return "", fmt.Errorf("write %s: %w", name, err)
		}
	}

	return dir, nil
}

// LoadSessionTLSConfig reads the session credentials and returns a tls.Config
// suitable for calling the proxy control API.
func LoadSessionTLSConfig(sessionID string) (*tls.Config, error) {
	dir, err := sessionDir(sessionID)
	if err != nil {
		return nil, err
	}

	caCert, err := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if err != nil {
		return nil, fmt.Errorf("read CA cert: %w", err)
	}
	clientCert, err := os.ReadFile(filepath.Join(dir, "client-cert.pem"))
	if err != nil {
		return nil, fmt.Errorf("read client cert: %w", err)
	}
	clientKey, err := os.ReadFile(filepath.Join(dir, "client-key.pem"))
	if err != nil {
		return nil, fmt.Errorf("read client key: %w", err)
	}

	cert, err := tls.X509KeyPair(clientCert, clientKey)
	if err != nil {
		return nil, fmt.Errorf("load client keypair: %w", err)
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse CA certificate")
	}

	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
	}, nil
}

// CleanupSessionCredentials removes the session's credential directory.
func CleanupSessionCredentials(sessionID string) error {
	dir, err := sessionDir(sessionID)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}
```

**Step 4: Run tests**

Run: `cd /home/bernd/Code/vibepit/.worktrees/control-api-mtls && go test ./cmd/ -run TestWriteSession -v && go test ./cmd/ -run TestReadSession -v && go test ./cmd/ -run TestCleanupSession -v`
Expected: All pass.

**Step 5: Commit**

```bash
git add cmd/session.go cmd/session_test.go
git commit -m "feat: session credential storage for CLI subcommands"
```

---

### Task 5: Container Setup — Pass TLS Env Vars and Publish Port (`container/client.go`)

**Files:**
- Modify: `container/client.go:19-37` (add new label constants)
- Modify: `container/client.go:294-301` (ProxyContainerConfig)
- Modify: `container/client.go:306-343` (StartProxyContainer)

**Step 1: Write the failing test**

The existing container tests likely use mocks. Check `container/client_test.go` for the testing pattern, then add a test that verifies the proxy container config includes env vars and port bindings. For now, this is a structural change — we verify it compiles and the config struct has the new fields.

Add to `container/client_test.go`:

```go
func TestProxyContainerConfigHasTLSFields(t *testing.T) {
	cfg := ProxyContainerConfig{
		BinaryPath:  "/usr/bin/vibepit",
		ConfigPath:  "/tmp/config.json",
		NetworkID:   "net-123",
		ProxyIP:     "172.18.0.2",
		Name:        "vibepit-proxy-test",
		SessionID:   "session-abc",
		TLSKeyPEM:   "key-pem",
		TLSCertPEM:  "cert-pem",
		CACertPEM:   "ca-pem",
		ControlPort: "12345",
		ProjectDir:  "/home/user/project",
	}
	assert.Equal(t, "session-abc", cfg.SessionID)
	assert.Equal(t, "12345", cfg.ControlPort)
}
```

**Step 2: Run test to verify it fails**

Run: `cd /home/bernd/Code/vibepit/.worktrees/control-api-mtls && go test ./container/ -run TestProxyContainerConfigHasTLSFields -v`
Expected: Compilation error — unknown fields.

**Step 3: Implement the changes**

Add new label constants after line 25:

```go
LabelSessionID   = "x-vibepit.session-id"
LabelControlPort = "x-vibepit.control-port"
```

Update `ProxyContainerConfig` struct:

```go
type ProxyContainerConfig struct {
	BinaryPath  string
	ConfigPath  string
	NetworkID   string
	ProxyIP     string
	Name        string
	SessionID   string
	TLSKeyPEM   string
	TLSCertPEM  string
	CACertPEM   string
	ControlPort string
	ProjectDir  string
}
```

Update `StartProxyContainer` to pass env vars, labels, and port binding:

```go
func (c *Client) StartProxyContainer(ctx context.Context, cfg ProxyContainerConfig) (string, error) {
	env := []string{}
	if cfg.TLSKeyPEM != "" {
		env = append(env,
			"VIBEPIT_PROXY_TLS_KEY="+cfg.TLSKeyPEM,
			"VIBEPIT_PROXY_TLS_CERT="+cfg.TLSCertPEM,
			"VIBEPIT_PROXY_CA_CERT="+cfg.CACertPEM,
		)
	}

	labels := map[string]string{
		LabelVibepit:    "true",
		LabelRole:       RoleProxy,
		LabelProjectDir: cfg.ProjectDir,
	}
	if cfg.SessionID != "" {
		labels[LabelSessionID] = cfg.SessionID
	}
	if cfg.ControlPort != "" {
		labels[LabelControlPort] = cfg.ControlPort
	}

	portBindings := nat.PortMap{}
	exposedPorts := nat.PortSet{}
	if cfg.ControlPort != "" {
		containerPort, _ := nat.NewPort("tcp", "3129")
		exposedPorts[containerPort] = struct{}{}
		portBindings[containerPort] = []nat.PortBinding{
			{HostIP: "127.0.0.1", HostPort: cfg.ControlPort},
		}
	}

	resp, err := c.docker.ContainerCreate(ctx,
		&container.Config{
			Image:        ProxyImage,
			Cmd:          []string{ProxyBinaryPath, "proxy", "--config", ProxyConfigPath},
			Labels:       labels,
			Env:          env,
			WorkingDir:   "/",
			ExposedPorts: exposedPorts,
		},
		&container.HostConfig{
			Binds: []string{
				cfg.BinaryPath + ":" + ProxyBinaryPath + ":ro",
				cfg.ConfigPath + ":" + ProxyConfigPath + ":ro",
			},
			RestartPolicy: container.RestartPolicy{Name: "no"},
			PortBindings:  portBindings,
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				cfg.NetworkID: {
					IPAMConfig: &network.EndpointIPAMConfig{
						IPv4Address: cfg.ProxyIP,
					},
				},
			},
		},
		nil,
		cfg.Name,
	)
	if err != nil {
		return "", fmt.Errorf("create proxy container: %w", err)
	}
	if err := c.docker.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("start proxy container: %w", err)
	}
	if err := c.docker.NetworkConnect(ctx, "bridge", resp.ID, nil); err != nil {
		return "", fmt.Errorf("connect proxy to bridge: %w", err)
	}
	return resp.ID, nil
}
```

Add `"github.com/docker/go-connections/nat"` to the imports in `container/client.go`. Run `go get github.com/docker/go-connections` if needed (it should already be a transitive dependency of docker/docker).

**Step 4: Run tests**

Run: `cd /home/bernd/Code/vibepit/.worktrees/control-api-mtls && go test ./container/ -v`
Expected: All pass.

**Step 5: Commit**

```bash
git add container/client.go container/client_test.go
git commit -m "feat: proxy container supports TLS env vars and port binding"
```

---

### Task 6: Launcher Generates Certs and Wires Everything (`cmd/root.go`)

**Files:**
- Modify: `cmd/root.go:51-217` (RootAction)

This is the integration point. The launcher generates certs, picks a random port, writes session credentials, and passes everything to the container configs.

**Step 1: No new test for this task** — this is orchestration code that wires together already-tested components. It will be validated by the end-to-end test in Task 8.

**Step 2: Implement the changes**

In `cmd/root.go`, add imports:

```go
"crypto/rand"
"encoding/hex"
"net"
"time"

"github.com/bernd/vibepit/proxy"
```

After the network creation block (after line 168), add cert generation and session setup:

```go
// Generate ephemeral mTLS credentials for the control API.
fmt.Println("+ Generating mTLS credentials")
creds, err := proxy.GenerateMTLSCredentials(30 * 24 * time.Hour)
if err != nil {
    return fmt.Errorf("mtls: %w", err)
}

// Pick a random available port for the control API.
controlPort, err := randomPort()
if err != nil {
    return fmt.Errorf("control port: %w", err)
}

// Generate a unique session ID.
sessionID := randomSessionID()

// Write client credentials so subcommands can find them.
sessionDir, err := WriteSessionCredentials(sessionID, creds)
if err != nil {
    return fmt.Errorf("session credentials: %w", err)
}
defer CleanupSessionCredentials(sessionID)
fmt.Printf("+ Session: %s (credentials in %s)\n", sessionID, sessionDir)
```

Update the `StartProxyContainer` call to pass the new fields:

```go
proxyContainerID, err := client.StartProxyContainer(ctx, ctr.ProxyContainerConfig{
    BinaryPath:  selfBinary,
    ConfigPath:  tmpFile.Name(),
    NetworkID:   netInfo.ID,
    ProxyIP:     proxyIP,
    Name:        "vibepit-proxy-" + containerID,
    SessionID:   sessionID,
    TLSKeyPEM:   string(creds.ServerKeyPEM()),
    TLSCertPEM:  string(creds.ServerCertPEM()),
    CACertPEM:   string(creds.CACertPEM()),
    ControlPort: controlPort,
    ProjectDir:  projectRoot,
})
```

Add helper functions at the bottom of `cmd/root.go`:

```go
// randomPort finds an available TCP port on localhost.
func randomPort() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return fmt.Sprintf("%d", port), nil
}

// randomSessionID returns a short random hex string for session identification.
func randomSessionID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
```

Replace the existing `randomHex` function with `randomSessionID` or keep both — `randomHex` is used for container naming and is deterministic (pid-based), which is fine for container names.

**Step 3: Run build to verify compilation**

Run: `cd /home/bernd/Code/vibepit/.worktrees/control-api-mtls && go build ./...`
Expected: Compiles without errors.

**Step 4: Commit**

```bash
git add cmd/root.go
git commit -m "feat: launcher generates mTLS creds and wires to proxy container"
```

---

### Task 7: Update CLI Subcommands to Use mTLS (`cmd/allow.go`, `cmd/monitor.go`)

**Files:**
- Modify: `cmd/monitor.go:54-99` (discoverProxyAddr → discoverSession)
- Modify: `cmd/allow.go:40-56` (HTTP client setup)
- Modify: `cmd/monitor.go:25-48` (HTTP client setup)

Replace plain HTTP with mTLS HTTPS. The discovery function changes from finding a proxy IP to finding a session (port + session ID from labels).

**Step 1: Rewrite discovery function**

Replace `discoverProxyAddr` in `cmd/monitor.go` with a session-aware discovery:

```go
// SessionInfo contains the information needed to connect to a proxy's control API.
type SessionInfo struct {
	ControlPort string
	SessionID   string
	ProjectDir  string
}

// discoverSession finds running vibepit proxy containers and returns connection
// info. If multiple sessions are running, prompts the user to select one.
func discoverSession(ctx context.Context) (*SessionInfo, error) {
	client, err := ctr.NewClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	sessions, err := client.ListProxySessions(ctx)
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, fmt.Errorf("no running vibepit sessions found")
	}
	if len(sessions) == 1 {
		return &SessionInfo{
			ControlPort: sessions[0].ControlPort,
			SessionID:   sessions[0].SessionID,
			ProjectDir:  sessions[0].ProjectDir,
		}, nil
	}

	// Multiple sessions — interactive selection.
	options := make([]huh.Option[int], len(sessions))
	for i, s := range sessions {
		options[i] = huh.NewOption(s.ProjectDir, i)
	}
	var selected int
	err = huh.NewSelect[int]().
		Title("Select a session").
		Options(options...).
		Value(&selected).
		Run()
	if err != nil {
		return nil, fmt.Errorf("session selection: %w", err)
	}

	s := sessions[selected]
	return &SessionInfo{
		ControlPort: s.ControlPort,
		SessionID:   s.SessionID,
		ProjectDir:  s.ProjectDir,
	}, nil
}
```

Add `"github.com/charmbracelet/huh"` to the imports.

**Step 2: Add `ListProxySessions` to container client**

In `container/client.go`, add:

```go
// ProxySession describes a running proxy for session discovery.
type ProxySession struct {
	ContainerID string
	SessionID   string
	ControlPort string
	ProjectDir  string
}

// ListProxySessions returns all running vibepit proxy containers with their
// session metadata.
func (c *Client) ListProxySessions(ctx context.Context) ([]ProxySession, error) {
	containers, err := c.docker.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(
			filters.Arg("label", LabelVibepit+"=true"),
			filters.Arg("label", LabelRole+"="+RoleProxy),
		),
	})
	if err != nil {
		return nil, err
	}

	var sessions []ProxySession
	for _, c := range containers {
		sessions = append(sessions, ProxySession{
			ContainerID: c.ID,
			SessionID:   c.Labels[LabelSessionID],
			ControlPort: c.Labels[LabelControlPort],
			ProjectDir:  c.Labels[LabelProjectDir],
		})
	}
	return sessions, nil
}
```

**Step 3: Update `allow.go` to use mTLS**

Replace the HTTP client and URL construction in `cmd/allow.go`:

```go
Action: func(ctx context.Context, cmd *cli.Command) error {
    entries := cmd.Args().Slice()
    if len(entries) == 0 {
        return fmt.Errorf("at least one domain is required")
    }

    addr := cmd.String("addr")
    var httpClient *http.Client
    var baseURL string

    if addr != "" {
        // Manual address — assume plain HTTP (for debugging).
        httpClient = &http.Client{Timeout: 5 * time.Second}
        baseURL = fmt.Sprintf("http://%s", addr)
    } else {
        session, err := discoverSession(ctx)
        if err != nil {
            return fmt.Errorf("cannot find running proxy (use --addr to specify manually): %w", err)
        }
        tlsCfg, err := LoadSessionTLSConfig(session.SessionID)
        if err != nil {
            return fmt.Errorf("load TLS credentials: %w", err)
        }
        httpClient = &http.Client{
            Timeout:   5 * time.Second,
            Transport: &http.Transport{TLSClientConfig: tlsCfg},
        }
        baseURL = fmt.Sprintf("https://127.0.0.1:%s", session.ControlPort)
    }

    body, _ := json.Marshal(map[string]any{"entries": entries})
    resp, err := httpClient.Post(
        baseURL+"/allow",
        "application/json",
        bytes.NewReader(body),
    )
    // ... rest unchanged
```

**Step 4: Update `monitor.go` similarly**

Replace the HTTP client and URL construction:

```go
Action: func(ctx context.Context, cmd *cli.Command) error {
    addr := cmd.String("addr")
    var httpClient *http.Client
    var baseURL string

    if addr != "" {
        httpClient = &http.Client{Timeout: 5 * time.Second}
        baseURL = fmt.Sprintf("http://%s", addr)
    } else {
        session, err := discoverSession(ctx)
        if err != nil {
            return fmt.Errorf("cannot find running proxy (use --addr to specify manually): %w", err)
        }
        tlsCfg, err := LoadSessionTLSConfig(session.SessionID)
        if err != nil {
            return fmt.Errorf("load TLS credentials: %w", err)
        }
        httpClient = &http.Client{
            Timeout:   5 * time.Second,
            Transport: &http.Transport{TLSClientConfig: tlsCfg},
        }
        baseURL = fmt.Sprintf("https://127.0.0.1:%s", session.ControlPort)
    }

    fmt.Printf("Connecting to proxy at %s...\n\n", baseURL)
    // ... rest of polling loop uses httpClient and baseURL
```

**Step 5: Run build**

Run: `cd /home/bernd/Code/vibepit/.worktrees/control-api-mtls && go build ./...`
Expected: Compiles.

**Step 6: Run all tests**

Run: `cd /home/bernd/Code/vibepit/.worktrees/control-api-mtls && go test ./...`
Expected: All pass.

**Step 7: Commit**

```bash
git add cmd/allow.go cmd/monitor.go container/client.go
git commit -m "feat: CLI subcommands use mTLS for control API access"
```

---

### Task 8: Sessions Command (`cmd/sessions.go`)

**Files:**
- Create: `cmd/sessions.go`
- Modify: `main.go` (register command)

**Step 1: Implement the sessions command**

```go
// cmd/sessions.go
package cmd

import (
	"context"
	"fmt"

	ctr "github.com/bernd/vibepit/container"
	"github.com/urfave/cli/v3"
)

func SessionsCommand() *cli.Command {
	return &cli.Command{
		Name:  "sessions",
		Usage: "List active vibepit sessions",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			client, err := ctr.NewClient()
			if err != nil {
				return err
			}
			defer client.Close()

			sessions, err := client.ListProxySessions(ctx)
			if err != nil {
				return err
			}

			if len(sessions) == 0 {
				fmt.Println("No active sessions.")
				return nil
			}

			for _, s := range sessions {
				fmt.Printf("%-20s %s (port %s)\n", s.SessionID, s.ProjectDir, s.ControlPort)
			}
			return nil
		},
	}
}
```

**Step 2: Register in main.go**

Find where commands are registered (look for `AllowCommand`, `MonitorCommand`) and add `SessionsCommand()`.

**Step 3: Run build**

Run: `cd /home/bernd/Code/vibepit/.worktrees/control-api-mtls && go build ./...`
Expected: Compiles.

**Step 4: Commit**

```bash
git add cmd/sessions.go main.go
git commit -m "feat: add sessions command to list active vibepit sessions"
```

---

### Task 9: Add `--session` Flag to Allow and Monitor

**Files:**
- Modify: `cmd/allow.go` (add flag)
- Modify: `cmd/monitor.go` (add flag)

**Step 1: Add the flag to both commands**

In both `AllowCommand` and `MonitorCommand`, add to the Flags slice:

```go
&cli.StringFlag{
    Name:  "session",
    Usage: "Session ID or project path (skips interactive selection)",
},
```

**Step 2: Update `discoverSession` to accept an optional filter**

```go
func discoverSession(ctx context.Context, filter string) (*SessionInfo, error) {
```

When `filter` is non-empty, match it against either `SessionID` or `ProjectDir` and return that session directly without prompting.

**Step 3: Update callers**

In `allow.go` and `monitor.go`, pass `cmd.String("session")` to `discoverSession`.

**Step 4: Run build and tests**

Run: `cd /home/bernd/Code/vibepit/.worktrees/control-api-mtls && go build ./... && go test ./...`

**Step 5: Commit**

```bash
git add cmd/allow.go cmd/monitor.go
git commit -m "feat: --session flag for allow and monitor commands"
```

---

### Task 10: Final Verification

**Step 1: Run all tests**

Run: `cd /home/bernd/Code/vibepit/.worktrees/control-api-mtls && go test ./... -v`

**Step 2: Run vet and format**

Run: `cd /home/bernd/Code/vibepit/.worktrees/control-api-mtls && go vet ./... && goimports -l .`

**Step 3: Manual smoke test** (if Docker is available)

Run: `cd /home/bernd/Code/vibepit/.worktrees/control-api-mtls && go run . -L`

Verify:
- "Generating mTLS credentials" appears in output
- "Session:" line shows session ID and credential path
- In another terminal: `go run . sessions` shows the session
- `go run . allow example.com` works via mTLS
- `go run . monitor` works via mTLS
- `curl https://127.0.0.1:<port>/logs` (without client cert) is rejected
