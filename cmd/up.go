package cmd

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"time"

	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/keygen"
	"github.com/bernd/vibepit/tui"
	"github.com/urfave/cli/v3"
)

func UpCommand() *cli.Command {
	return &cli.Command{
		Name:   "up",
		Usage:  "Start a sandbox session in daemon mode (with SSH server)",
		Flags:  sandboxFlags(),
		Action: UpAction,
	}
}

func UpAction(ctx context.Context, cmd *cli.Command) error {
	tui.PrintHeader()

	projectRoot, u, err := resolveProjectAndUser(cmd)
	if err != nil {
		return err
	}

	client, err := ctr.NewClient(ctr.WithDebug(cmd.Bool(debugFlag)))
	if err != nil {
		return err
	}
	defer client.Close()

	// Idempotent: if a session is already running for this project, exit early.
	existing, err := client.FindRunningSession(ctx, projectRoot)
	if err != nil {
		return err
	}
	if existing != nil {
		tui.Status("Session", "already running for %s", projectRoot)
		return nil
	}

	// Check for orphaned containers from a previous failed attempt (e.g.
	// proxy still running after sandbox crashed).
	orphanedSessionID, err := client.FindAnySessionContainer(ctx, projectRoot)
	if err != nil {
		return err
	}
	if orphanedSessionID != "" {
		return fmt.Errorf("a previous session (%s) left orphaned containers — run 'vibepit down' first", orphanedSessionID)
	}

	infra, cleanups, err := startSessionInfra(ctx, cmd, client, projectRoot, u, infraOptions{Daemon: true})
	succeeded := false
	defer func() {
		if !succeeded {
			runCleanups(cleanups)
		}
	}()
	if err != nil {
		return err
	}

	// Generate SSH keypairs for daemon mode.
	tui.Status("Generating", "SSH keypairs")
	clientPriv, clientPub, err := keygen.GenerateEd25519Keypair()
	if err != nil {
		return fmt.Errorf("generate client SSH keypair: %w", err)
	}
	hostPriv, hostPub, err := keygen.GenerateEd25519Keypair()
	if err != nil {
		return fmt.Errorf("generate host SSH keypair: %w", err)
	}
	if err := WriteSSHCredentials(infra.SessionID, clientPriv, clientPub, hostPriv, hostPub); err != nil {
		return fmt.Errorf("write SSH credentials: %w", err)
	}

	hostKeyPath := filepath.Join(infra.SessionDir, SSHHostPrivFile)
	hostPubPath := filepath.Join(infra.SessionDir, SSHHostPubFile)

	tui.Status("Creating", "sandbox container in %s", projectRoot)
	sandboxCfg := infra.baseSandboxConfig(projectRoot, u)
	sandboxCfg.SandboxIP = infra.NetworkInfo.SandboxIP
	sandboxCfg.Daemon = true
	sandboxCfg.DaemonBinaryPath = infra.SelfBinary
	sandboxCfg.DaemonHostKeyPath = hostKeyPath
	sandboxCfg.DaemonHostPubPath = hostPubPath
	sandboxCfg.DaemonAuthorizedKey = string(clientPub)
	sandboxCfg.DaemonEntrypoint = []string{"/vibed"}
	sandboxContainerID, err := client.CreateSandboxContainer(ctx, sandboxCfg)
	if err != nil {
		return fmt.Errorf("sandbox container: %w", err)
	}
	cleanups = append(cleanups, func() {
		client.StopAndRemove(ctx, sandboxContainerID) //nolint:errcheck
	})

	tui.Status("Starting", "sandbox container")
	if err := client.StartContainer(ctx, sandboxContainerID); err != nil {
		if logs, logErr := client.ContainerLogs(ctx, sandboxContainerID, 20); logErr == nil && logs != "" {
			tui.Error("sandbox logs:\n%s", logs)
		}
		return fmt.Errorf("start sandbox container: %w", err)
	}

	// Wait briefly and verify the sandbox is still running. The entrypoint
	// may fail immediately (e.g. missing function), and StartContainer only
	// confirms the container was started, not that it stayed up.
	time.Sleep(500 * time.Millisecond)
	if status := client.ContainerStatus(ctx, sandboxContainerID); status != "running" {
		logContainerDiag(ctx, client, "sandbox", sandboxContainerID)
		return fmt.Errorf("sandbox container exited immediately (%s)", status)
	}

	// Find the published SSH port on the proxy container (SSH is forwarded
	// through the proxy to preserve sandbox network isolation).
	sshPort, err := client.FindPublishedPort(ctx, infra.ProxyContainerID, ctr.SSHContainerPort)
	if err != nil {
		// Dump proxy and sandbox diagnostics to help troubleshoot.
		logContainerDiag(ctx, client, "proxy", infra.ProxyContainerID)
		logContainerDiag(ctx, client, "sandbox", sandboxContainerID)
		return fmt.Errorf("find SSH port: %w", err)
	}

	// Wait for the SSH daemon to actually accept connections. The proxy
	// port is already listening (it forwards TCP to the sandbox), so a
	// bare TCP dial succeeds even before the sandbox daemon is up. We
	// verify readiness by reading the SSH version banner ("SSH-2.0-...")
	// which is only sent after the sandbox daemon accepts.
	tui.Status("Waiting", "for SSH daemon")
	sshAddr := fmt.Sprintf("127.0.0.1:%d", sshPort)
	sshReady := false
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", sshAddr, time.Second)
		if err == nil {
			conn.SetReadDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck
			buf := make([]byte, 4)
			n, _ := conn.Read(buf)
			conn.Close() //nolint:errcheck
			if n >= 4 && string(buf[:4]) == "SSH-" {
				sshReady = true
				break
			}
		}
		if status := client.ContainerStatus(ctx, sandboxContainerID); status != "running" {
			logContainerDiag(ctx, client, "sandbox", sandboxContainerID)
			return fmt.Errorf("sandbox container exited during startup (%s)", status)
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !sshReady {
		logContainerDiag(ctx, client, "sandbox", sandboxContainerID)
		return fmt.Errorf("SSH daemon did not become ready within 30s")
	}

	succeeded = true

	tui.Status("Listening", "SSH on 127.0.0.1:%d", sshPort)
	tui.Status("Ready", "session %s", infra.SessionID)
	fmt.Println()

	return nil
}

// logContainerDiag prints a container's status and recent logs for troubleshooting.
func logContainerDiag(ctx context.Context, client *ctr.Client, name, containerID string) {
	status := client.ContainerStatus(ctx, containerID)
	if logs, err := client.ContainerLogs(ctx, containerID, 20); err == nil && logs != "" {
		tui.Error("%s %s — logs:\n%s", name, status, logs)
	} else {
		tui.Error("%s %s", name, status)
	}
}
