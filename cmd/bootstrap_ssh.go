package cmd

import (
	"context"
	"fmt"
	"github.com/bernd/vibepit/config"
	ctr "github.com/bernd/vibepit/container"
	"golang.org/x/crypto/ssh"
	"os"
	"path/filepath"
	"strconv"
)

// newSSHClient connects to the running sandbox for the current project.
// With withControl it also looks up the control API port for the returned
// SessionInfo; otherwise ControlPort is empty.
func newSSHClient(ctx context.Context, debug, withControl bool) (*ssh.Client, *SessionInfo, error) {
	client, err := ctr.NewClient(ctr.WithDebug(debug))
	if err != nil {
		return nil, nil, err
	}
	defer client.Close()

	// Always resolve project root from cwd — all positional args are the
	// remote command, not a project path.
	wd, err := os.Getwd()
	if err != nil {
		return nil, nil, err
	}
	projectRoot, err := config.FindProjectRoot(wd)
	if err != nil {
		return nil, nil, err
	}

	sandbox, err := client.FindRunningSession(ctx, projectRoot)
	if err != nil {
		return nil, nil, err
	}
	if sandbox == nil {
		return nil, nil, fmt.Errorf("no running sandbox found — run 'vibepit up' first")
	}

	// SSH port is published on the proxy container (forwarded to sandbox).
	proxyID, err := client.FindProxyContainerID(ctx, sandbox.SessionID)
	if err != nil {
		return nil, nil, err
	}
	port, err := client.FindPublishedPort(ctx, proxyID, ctr.SSHContainerPort)
	if err != nil {
		return nil, nil, fmt.Errorf("find SSH port: %w", err)
	}
	info := &SessionInfo{SessionID: sandbox.SessionID, ProjectDir: sandbox.ProjectDir}
	if withControl {
		controlPort, err := client.FindControlPort(ctx, proxyID)
		if err != nil {
			return nil, nil, fmt.Errorf("find control port: %w", err)
		}
		info.ControlPort = strconv.Itoa(controlPort)
	}

	sessDir := sessionDir(sandbox.SessionID)
	privateKey, err := os.ReadFile(filepath.Join(sessDir, SSHClientPrivFile))
	if err != nil {
		return nil, nil, fmt.Errorf("read ssh key: %w (credentials missing — run 'vibepit down && vibepit up')", err)
	}
	hostPubKey, err := os.ReadFile(filepath.Join(sessDir, SSHHostPubFile))
	if err != nil {
		return nil, nil, fmt.Errorf("read host key: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("parse private key: %w", err)
	}
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey(hostPubKey)
	if err != nil {
		return nil, nil, fmt.Errorf("parse host key: %w", err)
	}

	conn, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port), &ssh.ClientConfig{
		User:            "code",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.FixedHostKey(hostKey),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("ssh connect: %w", err)
	}

	return conn, info, nil
}
