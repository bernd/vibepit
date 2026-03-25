package cmd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"

	"github.com/adrg/xdg"
	"github.com/bernd/vibepit/config"
	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/proxy"
)

func sessionBaseDir() (string, error) {
	return filepath.Join(xdg.StateHome, config.RuntimeDirName, "sessions"), nil
}

func sessionDir(sessionID string) (string, error) {
	base, err := sessionBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, sessionID), nil
}

// matchSession reports whether a proxy session matches the given filter string,
// comparing against both SessionID and ProjectDir.
func matchSession(ps ctr.ProxySession, filter string) bool {
	return ps.SessionID == filter || ps.ProjectDir == filter
}

// discoverSession finds running vibepit proxy containers and returns connection
// info. If multiple sessions are running, prompts the user to select one.
// If filter is non-empty, it matches against SessionID or ProjectDir.
func discoverSession(ctx context.Context, filter string) (*SessionInfo, error) {
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

	if filter != "" {
		for _, s := range sessions {
			if matchSession(s, filter) {
				return sessionInfoFromProxy(s), nil
			}
		}
		return nil, fmt.Errorf("no session matching %q found", filter)
	}

	if len(sessions) == 1 {
		return sessionInfoFromProxy(sessions[0]), nil
	}

	// Multiple sessions — interactive selection.
	return selectSession(sessions)
}

// sessionInfoFromProxy converts a container.ProxySession to a SessionInfo.
func sessionInfoFromProxy(ps ctr.ProxySession) *SessionInfo {
	return &SessionInfo{
		ControlPort: ps.ControlPort,
		SessionID:   ps.SessionID,
		ProjectDir:  ps.ProjectDir,
	}
}

// WriteSessionCredentials persists the client TLS material for a session
// into $XDG_STATE_HOME/vibepit/sessions/<sessionID>/ so that subcommands
// launched in separate processes can load them via LoadSessionTLSConfig.
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

// WriteSSHCredentials persists SSH key material for a session into
// $XDG_STATE_HOME/vibepit/sessions/<sessionID>/ so that the SSH server
// and client can load them when establishing a session.
func WriteSSHCredentials(sessionID string, clientPriv, clientPub, hostPriv, hostPub []byte) error {
	dir, err := sessionDir(sessionID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return fmt.Errorf("chmod session dir: %w", err)
	}
	files := map[string]struct {
		data []byte
		perm os.FileMode
	}{
		"ssh-key":      {clientPriv, 0600},
		"ssh-key.pub":  {clientPub, 0644},
		"host-key":     {hostPriv, 0600},
		"host-key.pub": {hostPub, 0644},
	}
	for name, f := range files {
		if err := os.WriteFile(filepath.Join(dir, name), f.data, f.perm); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
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
