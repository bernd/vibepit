package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
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
	// imageRevision is bumped on backwards-incompatible image changes (r1, r2, ...).
	// Also update IMAGE_REVISION in .github/workflows/docker-publish.yml.
	imageRevision       = "r1"
	defaultImagePrefix  = "ghcr.io/bernd/vibepit:" + imageRevision
	localImage          = "vibepit:latest"
	homeVolumeName      = "vibepit-home"
	linuxbrewVolumeName = "vibepit-linuxbrew"
	networkNamePrefix   = "vibepit-net-"
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

	client, err := ctr.NewClient(ctr.WithDebug(cmd.Bool(debugFlag)))
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

	// Allocate ports for MCP proxy listeners.
	usedPorts := make([]int, 0, len(merged.AllowHostPorts)+2+len(merged.MCPServers))
	usedPorts = append(usedPorts, merged.AllowHostPorts...)
	usedPorts = append(usedPorts, proxyPort, controlAPIPort)
	for i := range merged.MCPServers {
		mcpPort, err := config.RandomProxyPort(usedPorts)
		if err != nil {
			return fmt.Errorf("MCP port for %s: %w", merged.MCPServers[i].Name, err)
		}
		usedPorts = append(usedPorts, mcpPort)
		merged.MCPServers[i].Port = mcpPort
	}

	// Start host-side TCP forwarders for MCP servers (after port allocation).
	var mcpForwarders []*TCPForwarder
	for i, mcpCfg := range merged.MCPServers {
		u, err := url.Parse(mcpCfg.URL)
		if err != nil {
			return fmt.Errorf("MCP %s URL: %w", mcpCfg.Name, err)
		}
		if !isLoopback(u.Hostname()) {
			return fmt.Errorf("MCP %s URL: host %q is not a loopback address (only 127.0.0.1/::1/localhost allowed)", mcpCfg.Name, u.Hostname())
		}
		target, err := mcpTargetAddr(u)
		if err != nil {
			return fmt.Errorf("MCP %s target: %w", mcpCfg.Name, err)
		}

		listenAddr := fmt.Sprintf("%s:%d", netInfo.GatewayIP, mcpCfg.Port)
		fwd, err := NewTCPForwarder(listenAddr, target)
		if err != nil {
			return fmt.Errorf("MCP forwarder %s: %w", mcpCfg.Name, err)
		}
		mcpForwarders = append(mcpForwarders, fwd)
		go fwd.Serve()

		// Update the MCP server URL in config to point to the forwarder
		// as seen from the proxy container (gateway IP). The TCP forwarder
		// is a raw tunnel — TLS passes through unchanged, so the original
		// scheme must be preserved. Path and query are also kept.
		fwdURL := *u
		fwdURL.Host = fmt.Sprintf("%s:%d", netInfo.GatewayIP, mcpCfg.Port)
		merged.MCPServers[i].URL = fwdURL.String()

		tui.Status("MCP", "%s proxy on :%d -> %s", mcpCfg.Name, mcpCfg.Port, target)
	}
	defer func() {
		for _, fwd := range mcpForwarders {
			fwd.Close()
		}
	}()

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
		if err := client.RemoveNetwork(ctx, netInfo.ID); err != nil {
			tui.Error("%v", err)
		}
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

	mcpEnvVars, err := BuildMCPEnvVars(merged.MCPServers, proxyIP)
	if err != nil {
		return fmt.Errorf("MCP env vars: %w", err)
	}

	tui.Status("Creating", "sandbox container in %s", projectRoot)
	sandboxContainer, err := client.CreateSandboxContainer(ctx, ctr.SandboxContainerConfig{
		Image:               image,
		ProjectDir:          projectRoot,
		WorkDir:             projectRoot,
		RuntimeDir:          sessionDir,
		HomeVolumeName:      homeVolumeName,
		LinuxbrewVolumeName: linuxbrewVolumeName,
		NetworkID:           netInfo.ID,
		ProxyIP:             proxyIP,
		ProxyPort:           proxyPort,
		Name:                "vibepit-sandbox-" + sessionID,
		Term:                term,
		ColorTerm:           os.Getenv("COLORTERM"),
		UID:                 uid,
		User:                u.Username,
		MCPEnvVars:          mcpEnvVars,
	})
	if err != nil {
		return fmt.Errorf("sandbox container: %w", err)
	}
	defer func() {
		tui.Status("Stopping", "sandbox container")
		client.StopAndRemove(ctx, sandboxContainer)
	}()

	tui.Status("Starting", "sandbox container")
	tui.Status("Attaching", "shell session")
	fmt.Println()
	return client.AttachAndStartSession(ctx, sandboxContainer)
}

// mcpTargetAddr returns a host:port dial target from a parsed URL,
// handling both IPv4 and IPv6 addresses correctly.
func mcpTargetAddr(u *url.URL) (string, error) {
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "https":
			port = "443"
		default:
			port = "80"
		}
	}
	if host == "" {
		return "", fmt.Errorf("empty host in URL %q", u.String())
	}
	return net.JoinHostPort(host, port), nil
}

// isLoopback returns true if host is a loopback address (127.0.0.0/8, ::1)
// or "localhost". MCP server URLs must point to the local host.
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
