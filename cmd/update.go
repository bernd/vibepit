package cmd

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"

	"strings"

	"github.com/bernd/vibepit/config"
	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/selfupdate"
	"github.com/urfave/cli/v3"
	"golang.org/x/term"
)

func UpdateCommand() *cli.Command {
	return &cli.Command{
		Name:     "update",
		Usage:    "Update binary and pull latest container images",
		Category: "Utilities",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "Skip confirmation prompt"},
			&cli.BoolFlag{Name: "bin", Usage: "Update binary only"},
			&cli.BoolFlag{Name: "images", Usage: "Update images only"},
			&cli.StringFlag{Name: "use", Usage: "Install a specific version"},
			&cli.BoolFlag{Name: "list", Usage: "List available releases"},
			&cli.BoolFlag{Name: "check", Usage: "Check for updates"},
			&cli.BoolFlag{Name: "pre", Usage: "Use prerelease channel"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return runUpdate(ctx, cmd)
		},
	}
}

func runUpdate(ctx context.Context, cmd *cli.Command) error {
	// Validate flag combinations.
	if err := validateUpdateFlags(cmd); err != nil {
		return err
	}

	useVersion := cmd.String("use")
	list := cmd.Bool("list")
	check := cmd.Bool("check")
	pre := cmd.Bool("pre")
	binOnly := cmd.Bool("bin") || useVersion != ""
	imagesOnly := cmd.Bool("images")
	yes := cmd.Bool("yes")

	client := selfupdate.NewClient()

	// Handle --list.
	if list {
		return runList(client, pre)
	}

	// Handle --check.
	if check {
		return runCheck(client, pre)
	}

	// Determine what to update.
	doBin := !imagesOnly
	doImages := !binOnly

	// Binary and image updates are independent paths. Neither gates the other.
	var binErr, imgErr error

	if doBin {
		binErr = runBinaryUpdate(ctx, client, useVersion, pre, yes)
	}

	if doImages {
		imgErr = runImageUpdate(ctx)
	}

	// Report errors from both paths.
	if binErr != nil && imgErr != nil {
		return fmt.Errorf("binary update: %w; image update: %v", binErr, imgErr)
	}
	if binErr != nil {
		return binErr
	}
	return imgErr
}

func validateUpdateFlags(cmd *cli.Command) error {
	use := cmd.String("use")
	list := cmd.Bool("list")
	check := cmd.Bool("check")
	images := cmd.Bool("images")

	bin := cmd.Bool("bin")
	if bin && images {
		return fmt.Errorf("--bin and --images are mutually exclusive")
	}
	if use != "" && images {
		return fmt.Errorf("--use cannot be combined with --images")
	}
	if list && check {
		return fmt.Errorf("--list and --check are mutually exclusive")
	}
	if list && use != "" {
		return fmt.Errorf("--list and --use are mutually exclusive")
	}
	if check && use != "" {
		return fmt.Errorf("--check and --use are mutually exclusive")
	}
	return nil
}

func runList(client *selfupdate.Client, pre bool) error {
	idx, _, err := client.ResolveChannel(pre)
	if err != nil {
		return err
	}

	fmt.Printf("%-20s %s\n", "VERSION", "TIMESTAMP")
	for _, r := range idx.Releases {
		suffix := ""
		if r.Version == config.Version {
			suffix = "  (installed)"
		}
		fmt.Printf("%-20s %s%s\n", r.Version, r.Timestamp, suffix)
	}
	return nil
}

func runCheck(client *selfupdate.Client, pre bool) error {
	idx, channel, err := client.ResolveChannel(pre)
	if err != nil {
		return err
	}

	crossChannel := isCrossChannel(config.Version, channel)
	if selfupdate.ShouldUpdate(config.Version, idx.Latest, crossChannel) {
		fmt.Printf("Update available: %s -> %s (%s channel)\n", config.Version, idx.Latest, channel)
	} else {
		fmt.Println("Already up to date.")
	}
	return nil
}

func runBinaryUpdate(ctx context.Context, client *selfupdate.Client, useVersion string, pre, yes bool) error {
	var meta *selfupdate.VersionMetadata

	if useVersion != "" {
		// Direct version fetch, bypass channel logic.
		var err error
		meta, err = client.FetchVersionMetadata(useVersion)
		if err != nil {
			return err
		}
	} else {
		// Channel-based update check.
		idx, channel, err := client.ResolveChannel(pre)
		if err != nil {
			return err
		}

		crossChannel := isCrossChannel(config.Version, channel)
		if !selfupdate.ShouldUpdate(config.Version, idx.Latest, crossChannel) {
			fmt.Println("Binary is up to date.")
			return nil
		}

		meta, err = client.FetchVersionMetadata(idx.Latest)
		if err != nil {
			return err
		}
	}

	// Find asset for current platform.
	asset, err := meta.FindAsset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	// Display update info.
	fmt.Printf("Current version: %s\n", config.Version)
	fmt.Printf("Target version:  %s (%s)\n", meta.Version, meta.Timestamp)
	if meta.Changelog != "" {
		fmt.Printf("\nChangelog:\n%s\n", meta.Changelog)
	}

	// Confirm.
	if !yes {
		fmt.Printf("\nInstall vibepit v%s? [y/N] ", meta.Version)
		var answer string
		fmt.Scanln(&answer)
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "y" && answer != "yes" {
			fmt.Println("Update cancelled.")
			return nil
		}
	}

	// Resolve binary path. Check the original (unresolved) path for package
	// manager detection since symlinks like /snap/bin/foo -> /usr/bin/snap
	// would cause incorrect prefix matching against the resolved path.
	originalPath, binPath, err := selfupdate.ResolveBinaryPath()
	if err != nil {
		return err
	}

	// Check both original and resolved paths. The original catches prefix-based
	// managers (snap, nix), while the resolved path catches managers where the
	// symlink target reveals the manager (e.g. /usr/local/bin/vibepit ->
	// /usr/local/Cellar/vibepit/...).
	if manager, managed := selfupdate.DetectPackageManager(originalPath); managed {
		return fmt.Errorf("vibepit appears to be managed by %s; use your package manager to update instead", manager)
	}
	if manager, managed := selfupdate.DetectPackageManager(binPath); managed {
		return fmt.Errorf("vibepit appears to be managed by %s; use your package manager to update instead", manager)
	}

	binDir := filepath.Dir(binPath)
	if err := selfupdate.CheckWritePermission(binDir); err != nil {
		return err
	}

	// Download.
	isTTY := term.IsTerminal(int(os.Stdout.Fd()))
	archivePath, err := selfupdate.DownloadArchive(client.HTTPClient, asset.URL, binDir, isTTY)
	if err != nil {
		return err
	}
	defer os.Remove(archivePath)

	// Verify SHA256.
	if err := selfupdate.VerifySHA256(archivePath, asset.SHA256); err != nil {
		return err
	}

	// Verify cosign bundle (skip if bundle URL is empty -- cosign signing
	// may not yet be deployed in CI).
	if asset.CosignBundleURL != "" {
		if err := selfupdate.VerifyCosignBundle(client.HTTPClient, archivePath, asset.CosignBundleURL); err != nil {
			return err
		}
	}

	// Extract and replace.
	extractedPath, err := selfupdate.ExtractBinary(archivePath, binDir, "vibepit")
	if err != nil {
		return err
	}

	if err := selfupdate.ReplaceBinary(binPath, extractedPath); err != nil {
		os.Remove(extractedPath)
		return err
	}

	fmt.Printf("Updated vibepit %s -> %s\n", config.Version, meta.Version)
	return nil
}

func runImageUpdate(ctx context.Context) error {
	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("cannot determine current user: %w", err)
	}

	client, err := ctr.NewClient()
	if err != nil {
		return err
	}
	defer client.Close()

	if err := client.PullImage(ctx, imageName(u), false); err != nil {
		return fmt.Errorf("pull image: %w", err)
	}
	if err := client.PullImage(ctx, ctr.ProxyImage, false); err != nil {
		return fmt.Errorf("pull image: %w", err)
	}
	fmt.Println("Container images updated.")
	return nil
}

// isCrossChannel returns true if the current version is on a different channel
// than the one being checked.
func isCrossChannel(currentVersion, targetChannel string) bool {
	if selfupdate.IsDevBuild(currentVersion) {
		return false
	}
	currentIsPre := selfupdate.IsPrerelease(currentVersion)
	targetIsPre := targetChannel == selfupdate.ChannelPrerelease
	return currentIsPre != targetIsPre
}
