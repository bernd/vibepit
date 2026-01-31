package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bernd/vibepit/config"
	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/proxy"
	"github.com/urfave/cli/v3"
)

const (
	DefaultImage      = "ghcr.io/bernd/vibepit:main"
	LocalImage        = "vibepit:latest"
	VolumeName        = "x-vibepit-home"
	NetworkNamePrefix = "vibepit-net-"
)

func RootFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{
			Name:  "C",
			Usage: "Start with a clean vibepit volume (removes /home/code)",
		},
		&cli.BoolFlag{
			Name:  "L",
			Usage: "Use local vibepit:latest image instead of the published one",
		},
		&cli.StringSliceFlag{
			Name:  "allow",
			Usage: "Additional domains to allow",
		},
		&cli.StringSliceFlag{
			Name:  "preset",
			Usage: "Additional presets to activate",
		},
		&cli.BoolFlag{
			Name:  "reconfigure",
			Usage: "Re-run the network preset selector",
		},
	}
}

func RootAction(ctx context.Context, cmd *cli.Command) error {
	projectRoot := cmd.Args().First()
	if projectRoot == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		projectRoot = wd
	}
	projectRoot, _ = filepath.Abs(projectRoot)

	u, _ := user.Current()
	if projectRoot == u.HomeDir {
		return fmt.Errorf("refusing to run in your home directory — point me to a project folder")
	}

	// Use git root if available.
	if gitRoot, err := exec.Command("git", "-C", projectRoot, "rev-parse", "--show-toplevel").Output(); err == nil {
		if root := strings.TrimSpace(string(gitRoot)); root != "" {
			projectRoot = root
		}
	}

	image := DefaultImage
	if cmd.Bool("L") {
		image = LocalImage
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

	merged := cfg.Merge(cmd.StringSlice("allow"), cmd.StringSlice("preset"))

	volumeName := VolumeName
	uid, _ := strconv.Atoi(u.Uid)

	if cmd.Bool("C") {
		fmt.Printf("+ Removing volume: %s\n", volumeName)
		client.RemoveVolume(ctx, volumeName)
	}
	if err := client.EnsureVolume(ctx, volumeName, uid, u.Username); err != nil {
		return fmt.Errorf("volume: %w", err)
	}

	proxyConfig, _ := json.Marshal(merged)
	tmpFile, err := os.CreateTemp("", "vibepit-proxy-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Write(proxyConfig)
	tmpFile.Close()

	selfBinary, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find own binary: %w", err)
	}
	selfBinary, _ = filepath.EvalSymlinks(selfBinary)

	// Pull images that are not available locally.
	if err := client.EnsureImage(ctx, image); err != nil {
		return fmt.Errorf("image: %w", err)
	}
	if err := client.EnsureImage(ctx, ctr.ProxyImage); err != nil {
		return fmt.Errorf("proxy image: %w", err)
	}

	containerID := randomHex()
	networkName := NetworkNamePrefix + containerID

	fmt.Printf("+ Creating network: %s\n", networkName)
	netInfo, err := client.CreateNetwork(ctx, networkName)
	if err != nil {
		return fmt.Errorf("network: %w", err)
	}
	proxyIP := netInfo.ProxyIP
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
	if term == "" {
		term = "linux"
	}
	if term == "xterm-ghostty" {
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

func randomHex() string {
	return fmt.Sprintf("%x%x%x", os.Getpid(), os.Getuid(), os.Getppid())
}

// randomSessionID returns a short random hex string for session identification.
func randomSessionID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
