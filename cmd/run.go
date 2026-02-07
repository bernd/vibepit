package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"github.com/bernd/vibepit/config"
	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/proxy"
	"github.com/urfave/cli/v3"
)

const (
	defaultImagePrefix = "ghcr.io/bernd/vibepit:next"
	localImage         = "vibepit:latest"
	volumeName         = "vibepit-home"
	networkNamePrefix  = "vibepit-net-"
)

const (
	allowFlag       = "allow"
	cleanFlag       = "clean"
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
				Name:    cleanFlag,
				Aliases: []string{"C"},
				Usage:   "Start with a clean vibepit volume (removes /home/code!)",
			},
			&cli.BoolFlag{
				Name:    localFlag,
				Aliases: []string{"L"},
				Usage:   fmt.Sprintf("Use local %q image instead of the published one", localImage),
			},
			&cli.StringSliceFlag{
				Name:    allowFlag,
				Aliases: []string{"a"},
				Usage:   "Additional domains to allow",
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
		fmt.Printf("+ Attaching to running session in %s\n", projectRoot)
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

	if err := merged.ValidateHostPorts(); err != nil {
		return err
	}

	uid, _ := strconv.Atoi(u.Uid)

	if cmd.Bool(cleanFlag) {
		fmt.Printf("+ Removing volume: %s\n", volumeName)
		client.RemoveVolume(ctx, volumeName)
	}
	if err := client.EnsureVolume(ctx, volumeName, uid, u.Username); err != nil {
		return fmt.Errorf("volume: %w", err)
	}

	selfBinary, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find own binary: %w", err)
	}
	selfBinary, _ = filepath.EvalSymlinks(selfBinary)

	// Pull images that are not available locally.
	if err := client.EnsureImage(ctx, image, false); err != nil {
		return fmt.Errorf("image: %w", err)
	}
	if err := client.EnsureImage(ctx, ctr.ProxyImage, false); err != nil {
		return fmt.Errorf("proxy image: %w", err)
	}

	containerID := containerIDSuffix()
	networkName := networkNamePrefix + containerID

	fmt.Printf("+ Creating network: %s\n", networkName)
	netInfo, err := client.CreateNetwork(ctx, networkName)
	if err != nil {
		return fmt.Errorf("network: %w", err)
	}
	proxyIP := netInfo.ProxyIP

	merged.ProxyIP = proxyIP
	merged.HostGateway = "host-gateway"

	proxyConfig, _ := json.Marshal(merged)
	tmpFile, err := os.CreateTemp("", "vibepit-proxy-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Write(proxyConfig)
	tmpFile.Close()

	defer func() {
		fmt.Printf("+ Removing network: %s\n", networkName)
		client.RemoveNetwork(ctx, netInfo.ID)
	}()

	// Generate ephemeral mTLS credentials for the control API.
	fmt.Println("+ Generating mTLS credentials")
	creds, err := proxy.GenerateMTLSCredentials(30 * 24 * time.Hour)
	if err != nil {
		return fmt.Errorf("mtls: %w", err)
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

	fmt.Println("+ Starting proxy container")
	proxyContainerID, controlPort, err := client.StartProxyContainer(ctx, ctr.ProxyContainerConfig{
		BinaryPath: selfBinary,
		ConfigPath: tmpFile.Name(),
		NetworkID:  netInfo.ID,
		ProxyIP:    proxyIP,
		Name:       "vibepit-proxy-" + containerID,
		SessionID:  sessionID,
		TLSKeyPEM:  string(creds.ServerKeyPEM()),
		TLSCertPEM: string(creds.ServerCertPEM()),
		CACertPEM:  string(creds.CACertPEM()),
		ProjectDir: projectRoot,
	})
	if err != nil {
		return fmt.Errorf("proxy container: %w", err)
	}
	fmt.Printf("+ Control API on 127.0.0.1:%s\n", controlPort)
	defer func() {
		fmt.Println("+ Stopping proxy container")
		client.StopAndRemove(ctx, proxyContainerID)
	}()

	term := os.Getenv("TERM")
	switch term {
	case "":
		term = "linux"
	case "xterm-ghostty": // Ghostty terminfo is not available in the container
		term = "xterm-256color"
	}

	fmt.Printf("+ Starting dev container in %s\n", projectRoot)
	devContainerID, err := client.StartDevContainer(ctx, ctr.DevContainerConfig{
		Image:      image,
		ProjectDir: projectRoot,
		WorkDir:    projectRoot,
		VolumeName: volumeName,
		NetworkID:  netInfo.ID,
		ProxyIP:    proxyIP,
		Name:       "vibepit-" + containerID,
		Term:       term,
		ColorTerm:  os.Getenv("COLORTERM"),
		UID:        uid,
		User:       u.Username,
	})
	if err != nil {
		return fmt.Errorf("dev container: %w", err)
	}
	defer func() {
		fmt.Println("+ Stopping dev container")
		client.StopAndRemove(ctx, devContainerID)
	}()

	return client.AttachSession(ctx, devContainerID)
}

// containerIDSuffix returns a hex string derived from the current process
// identifiers, used to create unique container and network names.
func containerIDSuffix() string {
	return fmt.Sprintf("%x%x%x", os.Getpid(), os.Getuid(), os.Getppid())
}

// randomSessionID returns a short random hex string for session identification.
func randomSessionID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
