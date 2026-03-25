package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	embeddedproxy "github.com/bernd/vibepit/embed/proxy"
	"github.com/rs/xid"

	"github.com/bernd/vibepit/config"
	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/keygen"
	"github.com/bernd/vibepit/proxy"
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

	projectRoot, err := resolveProjectRoot(cmd)
	if err != nil {
		return err
	}

	if _, err := os.Stat(projectRoot); os.IsNotExist(err) {
		return fmt.Errorf("project folder %q does not exist", projectRoot)
	} else if os.IsPermission(err) {
		return fmt.Errorf("can't access project folder %q: %w", projectRoot, err)
	} else if err != nil {
		return fmt.Errorf("couldn't stat project folder %q: %w", projectRoot, err)
	}

	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("cannot determine current user: %w", err)
	}

	if projectRoot == u.HomeDir {
		return fmt.Errorf("refusing to run in your home directory — point me to a project folder")
	}

	image := imageName(u)
	if cmd.Bool(localFlag) {
		image = localImage
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

	globalPath := config.DefaultGlobalPath()
	projectPath := config.DefaultProjectPath(projectRoot)

	cfg, err := config.Load(globalPath, projectPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	if cmd.Bool("reconfigure") {
		if _, err := config.RunReconfigure(projectPath, projectRoot); err != nil {
			return fmt.Errorf("reconfigure: %w", err)
		}
		cfg, err = config.Load(globalPath, projectPath)
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
	} else if _, err := os.Stat(projectPath); os.IsNotExist(err) {
		if _, err := config.RunFirstTimeSetup(projectRoot, projectPath); err != nil {
			return fmt.Errorf("setup: %w", err)
		}
		cfg, err = config.Load(globalPath, projectPath)
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
	}

	merged, err := cfg.Merge(cmd.StringSlice(allowFlag), cmd.StringSlice("preset"))
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	uid, _ := strconv.Atoi(u.Uid)

	if err := client.EnsureVolume(ctx, homeVolumeName, uid, u.Username); err != nil {
		return fmt.Errorf("home volume: %w", err)
	}
	if err := client.EnsureVolume(ctx, linuxbrewVolumeName, uid, u.Username); err != nil {
		return fmt.Errorf("linuxbrew volume: %w", err)
	}

	selfBinary, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find own binary: %w", err)
	}
	selfBinary, _ = filepath.EvalSymlinks(selfBinary)

	if runtime.GOOS == "darwin" {
		// We can't mount the self-binary into the Linux sandbox container on macOS, so we extract the embedded
		// Linux binary and use that.
		proxyBinary, err := embeddedproxy.CachedProxyBinary()
		if err != nil {
			return fmt.Errorf("macOS support: %w", err)
		}
		selfBinary = proxyBinary
	}

	// Pull images that are not available locally.
	if err := client.EnsureImage(ctx, image, false); err != nil {
		return fmt.Errorf("image: %w", err)
	}
	if err := client.EnsureImage(ctx, ctr.ProxyImage, false); err != nil {
		return fmt.Errorf("proxy image: %w", err)
	}

	// Generate a unique session ID.
	sessionID := xid.New().String()

	networkName := networkNamePrefix + sessionID

	tui.Status("Creating", "network %s", networkName)
	netInfo, err := client.CreateNetwork(ctx, networkName)
	if err != nil {
		return fmt.Errorf("network: %w", err)
	}
	proxyIP := netInfo.ProxyIP

	merged.ProxyIP = proxyIP
	merged.HostGateway = "host-gateway"

	// Generate random ports for proxy services, avoiding user's host ports.
	proxyPort, err := config.RandomProxyPort(merged.AllowHostPorts)
	if err != nil {
		return fmt.Errorf("proxy port: %w", err)
	}
	controlAPIPort, err := config.RandomProxyPort(append(merged.AllowHostPorts, proxyPort))
	if err != nil {
		return fmt.Errorf("control API port: %w", err)
	}
	merged.ProxyPort = proxyPort
	merged.ControlAPIPort = controlAPIPort

	proxyConfig, err := json.Marshal(merged)
	if err != nil {
		return fmt.Errorf("marshal proxy config: %w", err)
	}
	tmpFile, err := os.CreateTemp("", "vibepit-proxy-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name()) //nolint:errcheck
	if _, err := tmpFile.Write(proxyConfig); err != nil {
		tmpFile.Close() //nolint:errcheck
		return fmt.Errorf("write proxy config: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close proxy config: %w", err)
	}

	// Generate ephemeral mTLS credentials for the control API.
	tui.Status("Generating", "mTLS credentials")
	creds, err := proxy.GenerateMTLSCredentials(30 * 24 * time.Hour)
	if err != nil {
		return fmt.Errorf("mtls: %w", err)
	}

	// Write client credentials so subcommands can find them.
	sessDir, err := WriteSessionCredentials(sessionID, creds)
	if err != nil {
		return fmt.Errorf("session credentials: %w", err)
	}
	tui.Status("Session", "%s (credentials in %s)", sessionID, sessDir)

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
	if err := WriteSSHCredentials(sessionID, clientPriv, clientPub, hostPriv, hostPub); err != nil {
		return fmt.Errorf("write SSH credentials: %w", err)
	}

	hostKeyPath := filepath.Join(sessDir, "host-key")
	hostPubPath := filepath.Join(sessDir, "host-key.pub")

	tui.Status("Starting", "proxy container")
	_, controlPort, err := client.StartProxyContainer(ctx, ctr.ProxyContainerConfig{
		BinaryPath:     selfBinary,
		ConfigPath:     tmpFile.Name(),
		NetworkID:      netInfo.ID,
		ProxyIP:        proxyIP,
		ControlAPIPort: controlAPIPort,
		Name:           "vibepit-proxy-" + sessionID,
		SessionID:      sessionID,
		TLSKeyPEM:      string(creds.ServerKeyPEM()),
		TLSCertPEM:     string(creds.ServerCertPEM()),
		CACertPEM:      string(creds.CACertPEM()),
		ProjectDir:     projectRoot,
		NoRestart:      true,
	})
	if err != nil {
		return fmt.Errorf("proxy container: %w", err)
	}
	tui.Status("Listening", "control API on 127.0.0.1:%s", controlPort)

	tui.Status("Creating", "sandbox container in %s", projectRoot)
	sandboxContainerID, err := client.CreateSandboxContainer(ctx, ctr.SandboxContainerConfig{
		Image:               image,
		ProjectDir:          projectRoot,
		WorkDir:             projectRoot,
		RuntimeDir:          sessDir,
		HomeVolumeName:      homeVolumeName,
		LinuxbrewVolumeName: linuxbrewVolumeName,
		NetworkID:           netInfo.ID,
		ProxyIP:             proxyIP,
		ProxyPort:           proxyPort,
		Name:                "vibepit-sandbox-" + sessionID,
		Term:                containerTerm(),
		ColorTerm:           os.Getenv("COLORTERM"),
		UID:                 uid,
		User:                u.Username,
		SessionID:           sessionID,
		Daemon:              true,
		DaemonBinaryPath:    selfBinary,
		DaemonHostKeyPath:   hostKeyPath,
		DaemonHostPubPath:   hostPubPath,
		DaemonAuthorizedKey: string(clientPub),
		DaemonEntrypoint: []string{"/bin/bash", "-c",
			"source /etc/vibepit/lib.sh && source /etc/vibepit/entrypoint-lib.sh && migrate_linuxbrew_volume && init_home && exec /vibepit vibed"},
	})
	if err != nil {
		return fmt.Errorf("sandbox container: %w", err)
	}

	tui.Status("Starting", "sandbox container")
	if err := client.StartContainer(ctx, sandboxContainerID); err != nil {
		return fmt.Errorf("start sandbox container: %w", err)
	}

	// Find the published SSH port.
	sshPort, err := client.FindPublishedPort(ctx, sandboxContainerID, ctr.SSHContainerPort)
	if err != nil {
		return fmt.Errorf("find SSH port: %w", err)
	}

	fmt.Println()
	tui.Status("Ready", "session %s", sessionID)
	tui.Status("SSH", "ssh -p %d -i %s code@127.0.0.1", sshPort, filepath.Join(sessDir, "ssh-key"))
	tui.Status("Stop", "vibepit down")
	fmt.Println()

	return nil
}
