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

func imageName(u *user.User) string {
	return fmt.Sprintf("%s-uid-%s-gid-%s", defaultImagePrefix, u.Uid, u.Gid)
}

// sessionInfra holds the shared resources created during session startup.
type sessionInfra struct {
	SessionID        string
	SessionDir       string
	SelfBinary       string
	UID              int
	NetworkInfo      ctr.NetworkInfo
	Merged           config.MergedConfig
	ProxyContainerID string
}

type infraOptions struct {
	Daemon bool // enables SSH forwarding and daemon-mode proxy settings
}

// resolveProjectRoot determines the project root from the first CLI argument
// or the current working directory, then finds the Git root if available.
func resolveProjectRoot(cmd *cli.Command) (string, error) {
	projectRoot := cmd.Args().First()
	if projectRoot == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		projectRoot = wd
	}
	projectRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", err
	}
	return config.FindProjectRoot(projectRoot)
}

// containerTerm returns a TERM value suitable for the sandbox container.
func containerTerm() string {
	t := os.Getenv("TERM")
	switch t {
	case "":
		return "linux"
	case "xterm-ghostty":
		return "xterm-256color"
	default:
		return t
	}
}

// sandboxFlags returns the shared flag definitions used by both run and up commands.
func sandboxFlags() []cli.Flag {
	return []cli.Flag{
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
	}
}

// resolveProjectAndUser resolves the project root from the CLI arguments,
// validates it, and returns the current user and container image name.
func resolveProjectAndUser(cmd *cli.Command) (string, *userInfo, error) {
	projectRoot, err := resolveProjectRoot(cmd)
	if err != nil {
		return "", nil, err
	}

	if _, err := os.Stat(projectRoot); os.IsNotExist(err) {
		return "", nil, fmt.Errorf("project folder %q does not exist", projectRoot)
	} else if os.IsPermission(err) {
		return "", nil, fmt.Errorf("can't access project folder %q: %w", projectRoot, err)
	} else if err != nil {
		return "", nil, fmt.Errorf("couldn't stat project folder %q: %w", projectRoot, err)
	}

	u, err := currentUserInfo()
	if err != nil {
		return "", nil, err
	}

	if projectRoot == u.HomeDir {
		return "", nil, fmt.Errorf("refusing to run in your home directory — point me to a project folder")
	}

	if cmd.Bool(localFlag) {
		u.Image = localImage
	}

	return projectRoot, u, nil
}

// userInfo holds resolved user details needed for session startup.
type userInfo struct {
	Username string
	HomeDir  string
	UID      int
	Image    string
}

func currentUserInfo() (*userInfo, error) {
	u, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("cannot determine current user: %w", err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	return &userInfo{
		Username: u.Username,
		HomeDir:  u.HomeDir,
		UID:      uid,
		Image:    imageName(u),
	}, nil
}

// startSessionInfra creates all session infrastructure: loads configuration,
// creates volumes, pulls images, creates the network, starts the proxy
// container, and writes session credentials.
//
// The returned cleanup functions must be called in reverse order when tearing
// down the session. The caller is responsible for deciding when to run them
// (always on exit for interactive mode, only on failure for daemon mode).
func startSessionInfra(ctx context.Context, cmd *cli.Command, client *ctr.Client, projectRoot string, u *userInfo, opts infraOptions) (*sessionInfra, []func(), error) {
	var cleanups []func()

	globalPath := config.DefaultGlobalPath()
	projectPath := config.DefaultProjectPath(projectRoot)

	cfg, err := config.Load(globalPath, projectPath)
	if err != nil {
		return nil, cleanups, fmt.Errorf("config: %w", err)
	}

	if cmd.Bool(reconfigureFlag) {
		if _, err := config.RunReconfigure(projectPath, projectRoot); err != nil {
			return nil, cleanups, fmt.Errorf("reconfigure: %w", err)
		}
		cfg, err = config.Load(globalPath, projectPath)
		if err != nil {
			return nil, cleanups, fmt.Errorf("config: %w", err)
		}
	} else if _, err := os.Stat(projectPath); os.IsNotExist(err) {
		if _, err := config.RunFirstTimeSetup(projectRoot, projectPath); err != nil {
			return nil, cleanups, fmt.Errorf("setup: %w", err)
		}
		cfg, err = config.Load(globalPath, projectPath)
		if err != nil {
			return nil, cleanups, fmt.Errorf("config: %w", err)
		}
	}

	merged, err := cfg.Merge(cmd.StringSlice(allowFlag), cmd.StringSlice(presetFlag))
	if err != nil {
		return nil, cleanups, fmt.Errorf("config: %w", err)
	}

	if err := client.EnsureVolume(ctx, homeVolumeName, u.UID, u.Username); err != nil {
		return nil, cleanups, fmt.Errorf("home volume: %w", err)
	}
	if err := client.EnsureVolume(ctx, linuxbrewVolumeName, u.UID, u.Username); err != nil {
		return nil, cleanups, fmt.Errorf("linuxbrew volume: %w", err)
	}

	selfBinary, err := os.Executable()
	if err != nil {
		return nil, cleanups, fmt.Errorf("cannot find own binary: %w", err)
	}
	selfBinary, _ = filepath.EvalSymlinks(selfBinary)

	if runtime.GOOS == "darwin" {
		proxyBinary, err := embeddedproxy.CachedProxyBinary()
		if err != nil {
			return nil, cleanups, fmt.Errorf("macOS support: %w", err)
		}
		selfBinary = proxyBinary
	}

	if err := client.EnsureImage(ctx, u.Image, false); err != nil {
		return nil, cleanups, fmt.Errorf("image: %w", err)
	}
	if err := client.EnsureImage(ctx, ctr.ProxyImage, false); err != nil {
		return nil, cleanups, fmt.Errorf("proxy image: %w", err)
	}

	sessionID := xid.New().String()
	networkName := networkNamePrefix + sessionID

	tui.Status("Creating", "network %s", networkName)
	netInfo, err := client.CreateNetwork(ctx, networkName)
	if err != nil {
		return nil, cleanups, fmt.Errorf("network: %w", err)
	}
	cleanups = append(cleanups, func() {
		tui.Status("Removing", "network %s", networkName)
		if err := client.RemoveNetwork(ctx, netInfo.ID); err != nil {
			tui.Error("%v", err)
		}
	})

	merged.ProxyIP = netInfo.ProxyIP
	merged.HostGateway = "host-gateway"

	proxyPort, err := config.RandomProxyPort(merged.AllowHostPorts)
	if err != nil {
		return nil, cleanups, fmt.Errorf("proxy port: %w", err)
	}
	controlAPIPort, err := config.RandomProxyPort(append(merged.AllowHostPorts, proxyPort))
	if err != nil {
		return nil, cleanups, fmt.Errorf("control API port: %w", err)
	}
	merged.ProxyPort = proxyPort
	merged.ControlAPIPort = controlAPIPort

	if opts.Daemon {
		merged.SSHForwardAddr = fmt.Sprintf("%s:2222", netInfo.SandboxIP)
	}

	proxyConfig, err := json.Marshal(merged)
	if err != nil {
		return nil, cleanups, fmt.Errorf("marshal proxy config: %w", err)
	}
	tmpFile, err := os.CreateTemp("", "vibepit-proxy-*.json")
	if err != nil {
		return nil, cleanups, err
	}
	if _, err := tmpFile.Write(proxyConfig); err != nil {
		tmpFile.Close()           //nolint:errcheck
		os.Remove(tmpFile.Name()) //nolint:errcheck
		return nil, cleanups, fmt.Errorf("write proxy config: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpFile.Name()) //nolint:errcheck
		return nil, cleanups, fmt.Errorf("close proxy config: %w", err)
	}
	cleanups = append(cleanups, func() {
		os.Remove(tmpFile.Name()) //nolint:errcheck
	})

	tui.Status("Generating", "mTLS credentials")
	creds, err := proxy.GenerateMTLSCredentials(30 * 24 * time.Hour)
	if err != nil {
		return nil, cleanups, fmt.Errorf("mtls: %w", err)
	}

	sessDir, err := WriteSessionCredentials(sessionID, creds)
	if err != nil {
		return nil, cleanups, fmt.Errorf("session credentials: %w", err)
	}
	cleanups = append(cleanups, func() {
		CleanupSessionCredentials(sessionID) //nolint:errcheck
	})
	tui.Status("Session", "%s (credentials in %s)", sessionID, sessDir)

	proxyCfg := ctr.ProxyContainerConfig{
		BinaryPath:     selfBinary,
		ConfigPath:     tmpFile.Name(),
		NetworkID:      netInfo.ID,
		ProxyIP:        netInfo.ProxyIP,
		ControlAPIPort: controlAPIPort,
		Name:           "vibepit-proxy-" + sessionID,
		SessionID:      sessionID,
		TLSKeyPEM:      string(creds.ServerKeyPEM()),
		TLSCertPEM:     string(creds.ServerCertPEM()),
		CACertPEM:      string(creds.CACertPEM()),
		ProjectDir:     projectRoot,
	}
	if opts.Daemon {
		proxyCfg.NoRestart = true
		proxyCfg.SSHPort = 2222
	}

	tui.Status("Starting", "proxy container")
	proxyContainerID, controlPort, err := client.StartProxyContainer(ctx, proxyCfg)
	if err != nil {
		return nil, cleanups, fmt.Errorf("proxy container: %w", err)
	}
	cleanups = append(cleanups, func() {
		tui.Status("Stopping", "proxy container")
		client.StopAndRemove(ctx, proxyContainerID) //nolint:errcheck
	})
	tui.Status("Listening", "control API on 127.0.0.1:%s", controlPort)

	return &sessionInfra{
		SessionID:        sessionID,
		SessionDir:       sessDir,
		SelfBinary:       selfBinary,
		UID:              u.UID,
		NetworkInfo:      netInfo,
		Merged:           merged,
		ProxyContainerID: proxyContainerID,
	}, cleanups, nil
}

// baseSandboxConfig returns a SandboxContainerConfig with the fields common
// to both interactive and daemon modes. Callers set daemon-specific fields
// on the returned value before passing it to CreateSandboxContainer.
func (infra *sessionInfra) baseSandboxConfig(projectRoot string, u *userInfo) ctr.SandboxContainerConfig {
	return ctr.SandboxContainerConfig{
		Image:               u.Image,
		ProjectDir:          projectRoot,
		WorkDir:             projectRoot,
		RuntimeDir:          infra.SessionDir,
		HomeVolumeName:      homeVolumeName,
		LinuxbrewVolumeName: linuxbrewVolumeName,
		NetworkID:           infra.NetworkInfo.ID,
		ProxyIP:             infra.NetworkInfo.ProxyIP,
		ProxyPort:           infra.Merged.ProxyPort,
		Name:                "vibepit-sandbox-" + infra.SessionID,
		Term:                containerTerm(),
		ColorTerm:           os.Getenv("COLORTERM"),
		UID:                 infra.UID,
		User:                u.Username,
		SessionID:           infra.SessionID,
	}
}

// runCleanups executes cleanup functions in reverse order.
func runCleanups(cleanups []func()) {
	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}
}
