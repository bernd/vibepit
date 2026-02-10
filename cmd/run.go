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
	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	"github.com/urfave/cli/v3"
)

const (
	defaultImagePrefix = "ghcr.io/bernd/vibepit:main"
	localImage         = "vibepit:latest"
	volumeName         = "vibepit-home"
	networkNamePrefix  = "vibepit-net-"
)

const (
	allowFlag       = "allow"
	localFlag       = "local"
	presetFlag      = "preset"
	reconfigureFlag = "reconfigure"
)

func RunCommand() *cli.Command {
	return &cli.Command{
		Name:  "run",
		Usage: "Start the sandbox",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:    localFlag,
				Aliases: []string{"L"},
				Usage:   fmt.Sprintf("Use local %q image instead of the published one", localImage),
			},
			&cli.StringSliceFlag{
				Name:    allowFlag,
				Aliases: []string{"a"},
				Usage:   "Additional domain:port to allow (e.g. api.example.com:443)",
			},
			&cli.StringSliceFlag{
				Name:    presetFlag,
				Aliases: []string{"p"},
				Usage:   "Additional presets to activate",
			},
			&cli.BoolFlag{
				Name:    reconfigureFlag,
				Aliases: []string{"r"},
				Usage:   "Re-run the network preset selector",
			},
		},
		Action: RunAction,
	}
}

func imageName(u *user.User) string {
	return fmt.Sprintf("%s-uid-%s-gid-%s", defaultImagePrefix, u.Uid, u.Gid)
}

func RunAction(ctx context.Context, cmd *cli.Command) error {
	tui.PrintHeader()

	projectRoot := cmd.Args().First()
	if projectRoot == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		projectRoot = wd
	}
	projectRoot, err := filepath.Abs(projectRoot)
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

	// Use Git root if available.
	projectRoot, err = config.FindProjectRoot(projectRoot)
	if err != nil {
		return err
	}

	image := imageName(u)
	if cmd.Bool(localFlag) {
		image = localImage
	}

	client, err := ctr.NewClient()
	if err != nil {
		return err
	}
	defer client.Close()

	existing, err := client.FindRunningSession(ctx, projectRoot)
	if err != nil {
		return err
	}
	if existing != "" {
		tui.Status("Attaching", "to running session in %s", projectRoot)
		return client.ExecSession(ctx, existing)
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

	merged := cfg.Merge(cmd.StringSlice(allowFlag), cmd.StringSlice("preset"))

	uid, _ := strconv.Atoi(u.Uid)

	if err := client.EnsureVolume(ctx, volumeName, uid, u.Username); err != nil {
		return fmt.Errorf("volume: %w", err)
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
	excluded := make(map[int]bool, len(merged.AllowHostPorts))
	for _, p := range merged.AllowHostPorts {
		excluded[p] = true
	}
	proxyPort, err := config.RandomProxyPort(excluded)
	if err != nil {
		return fmt.Errorf("proxy port: %w", err)
	}
	excluded[proxyPort] = true
	controlAPIPort, err := config.RandomProxyPort(excluded)
	if err != nil {
		return fmt.Errorf("control API port: %w", err)
	}
	merged.ProxyPort = proxyPort
	merged.ControlAPIPort = controlAPIPort

	proxyConfig, _ := json.Marshal(merged)
	tmpFile, err := os.CreateTemp("", "vibepit-proxy-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Write(proxyConfig)
	tmpFile.Close()

	defer func() {
		tui.Status("Removing", "network %s", networkName)
		client.RemoveNetwork(ctx, netInfo.ID)
	}()

	// Generate ephemeral mTLS credentials for the control API.
	tui.Status("Generating", "mTLS credentials")
	creds, err := proxy.GenerateMTLSCredentials(30 * 24 * time.Hour)
	if err != nil {
		return fmt.Errorf("mtls: %w", err)
	}

	// Write client credentials so subcommands can find them.
	sessionDir, err := WriteSessionCredentials(sessionID, creds)
	if err != nil {
		return fmt.Errorf("session credentials: %w", err)
	}
	defer CleanupSessionCredentials(sessionID)
	tui.Status("Session", "%s (credentials in %s)", sessionID, sessionDir)

	tui.Status("Starting", "proxy container")
	proxyContainerID, controlPort, err := client.StartProxyContainer(ctx, ctr.ProxyContainerConfig{
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
	})
	if err != nil {
		return fmt.Errorf("proxy container: %w", err)
	}
	tui.Status("Listening", "control API on 127.0.0.1:%s", controlPort)
	defer func() {
		tui.Status("Stopping", "proxy container")
		client.StopAndRemove(ctx, proxyContainerID)
	}()

	term := os.Getenv("TERM")
	switch term {
	case "":
		term = "linux"
	case "xterm-ghostty": // Ghostty terminfo is not available in the container
		term = "xterm-256color"
	}

	tui.Status("Starting", "dev container in %s", projectRoot)
	devContainerID, err := client.StartDevContainer(ctx, ctr.DevContainerConfig{
		Image:      image,
		ProjectDir: projectRoot,
		WorkDir:    projectRoot,
		RuntimeDir: sessionDir,
		VolumeName: volumeName,
		NetworkID:  netInfo.ID,
		ProxyIP:    proxyIP,
		ProxyPort:  proxyPort,
		Name:       "vibepit-sandbox-" + sessionID,
		Term:       term,
		ColorTerm:  os.Getenv("COLORTERM"),
		UID:        uid,
		User:       u.Username,
	})
	if err != nil {
		return fmt.Errorf("dev container: %w", err)
	}
	defer func() {
		tui.Status("Stopping", "dev container")
		client.StopAndRemove(ctx, devContainerID)
	}()

	fmt.Println()
	return client.AttachSession(ctx, devContainerID)
}
