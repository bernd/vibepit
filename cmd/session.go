package cmd

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bernd/vibepit/proxy"
)

func sessionBaseDir() (string, error) {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if runtime == "" {
		return "", fmt.Errorf("XDG_RUNTIME_DIR is not set")
	}
	return filepath.Join(runtime, "vibepit"), nil
}

func sessionDir(sessionID string) (string, error) {
	base, err := sessionBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, sessionID), nil
}

// WriteSessionCredentials persists the client TLS material for a session
// into $XDG_RUNTIME_DIR/vibepit/<sessionID>/ so that subcommands launched
// in separate processes can load them via LoadSessionTLSConfig.
func WriteSessionCredentials(sessionID string, creds *proxy.MTLSCredentials) (string, error) {
	dir, err := sessionDir(sessionID)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create session dir: %w", err)
	}
	// MkdirAll may inherit a broader umask; force the exact permission.
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

// LoadSessionTLSConfig reads the PEM files from the session directory and
// returns a *tls.Config suitable for dialing the filtering proxy as a client.
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

// CleanupSessionCredentials removes the entire session directory and its
// contents. Safe to call if the directory does not exist.
func CleanupSessionCredentials(sessionID string) error {
	dir, err := sessionDir(sessionID)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}
