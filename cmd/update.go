package cmd

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"

	"strings"

	"charm.land/lipgloss/v2"
	"github.com/bernd/vibepit/config"
	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/cosign"
	"github.com/bernd/vibepit/selfupdate"
	"github.com/bernd/vibepit/tui"
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
		Action: runUpdate,
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
		return fmt.Errorf("binary update: %w; image update: %w", binErr, imgErr)
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

// updateChangelog returns the changelog text to display for an update, and
// whether it merges multiple releases. It falls back to the target's rendered
// changelog string (today's behavior) for a dev-build current version, a nil
// idx (the direct --use path, or a cross-channel update whose enumeration
// could not be completed), a single-release step, a fetch failure, or any
// in-range release whose JSON predates the structured changes field. A release
// that legitimately has no notes (an empty, non-nil changes map) is not a
// fallback trigger: it contributes nothing to the merge but does not suppress
// the other releases' notes.
func updateChangelog(client *selfupdate.Client, idx *selfupdate.ChannelIndex, current string, meta *selfupdate.VersionMetadata) (string, bool) {
	if idx == nil || selfupdate.IsDevBuild(current) {
		return meta.Changelog, false
	}

	rng := idx.ReleasesBetween(current, meta.Version)
	if len(rng) <= 1 {
		return meta.Changelog, false
	}

	metas := make([]*selfupdate.VersionMetadata, 0, len(rng))
	for _, r := range rng {
		var vm *selfupdate.VersionMetadata
		if r.Version == meta.Version {
			vm = meta // target metadata already in hand; no refetch
		} else {
			fetched, err := client.FetchVersionMetadata(r.Version)
			if err != nil {
				return meta.Changelog, false
			}
			vm = fetched
		}
		// A nil map means the JSON predates the structured changes field, so it
		// cannot be merged and we fall back. An empty (non-nil) map is a release
		// with no notes; it stays in the merge contributing nothing.
		if vm.Changes == nil {
			return meta.Changelog, false
		}
		metas = append(metas, vm)
	}

	return selfupdate.RenderMerged(selfupdate.MergeChanges(metas)), true
}

// enumerationIndex returns the channel index used to enumerate the releases a
// changelog should span, or nil when complete enumeration is not possible.
// Stable and prerelease releases live in separate indexes, so a cross-channel
// update must union both to avoid omitting the other channel's intervening
// releases; a same-channel update wants only its resolved index (so a stable
// update isn't polluted with prerelease notes). For a cross-channel update
// whose other index cannot be fetched, enumeration would be incomplete:
// returning the resolved index alone would render a partial merge under a
// heading claiming the full range, so we return nil to signal the caller to
// fall back to the target's changelog instead.
func enumerationIndex(client *selfupdate.Client, idx *selfupdate.ChannelIndex, channel string, crossChannel bool) *selfupdate.ChannelIndex {
	if !crossChannel {
		return idx
	}
	other, found, err := client.FetchChannelIndex(selfupdate.OtherChannel(channel))
	if err != nil || !found {
		return nil
	}
	return &selfupdate.ChannelIndex{
		Latest:   idx.Latest,
		Releases: selfupdate.CombineReleases(idx, other),
	}
}

func runBinaryUpdate(_ context.Context, client *selfupdate.Client, useVersion string, pre, yes bool) error {
	var meta *selfupdate.VersionMetadata
	var idx *selfupdate.ChannelIndex

	if useVersion != "" {
		// Direct version fetch, bypass channel logic.
		var err error
		meta, err = client.FetchVersionMetadata(useVersion)
		if err != nil {
			return err
		}
	} else {
		// Channel-based update check.
		resolved, channel, err := client.ResolveChannel(pre)
		if err != nil {
			return err
		}
		idx = resolved

		crossChannel := isCrossChannel(config.Version, channel)
		if !selfupdate.ShouldUpdate(config.Version, idx.Latest, crossChannel) {
			fmt.Println("Binary is up to date.")
			return nil
		}

		meta, err = client.FetchVersionMetadata(idx.Latest)
		if err != nil {
			return err
		}

		// For a cross-channel update, widen enumeration to both channels so the
		// merged changelog includes releases skipped on the other channel.
		idx = enumerationIndex(client, idx, channel, crossChannel)
	}

	// Find asset for current platform.
	asset, err := meta.FindAsset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	// Display update info.
	label := lipgloss.NewStyle().Foreground(tui.ColorCyan).Bold(true)
	fmt.Printf("%s %s\n", label.Render("Current version:"), config.Version)
	fmt.Printf("%s  %s (%s)\n", label.Render("Target version:"), meta.Version, meta.Timestamp)

	changelog, merged := updateChangelog(client, idx, config.Version, meta)
	if changelog != "" {
		heading := "Changelog:"
		if merged {
			heading = fmt.Sprintf("Changelog (v%s → v%s):", config.Version, meta.Version)
		}
		fmt.Printf("\n%s\n\n%s\n", label.Render(heading), changelog)
	}

	// Confirm.
	if !yes {
		prompt := lipgloss.NewStyle().Foreground(tui.ColorOrange).Bold(true)
		fmt.Printf(prompt.Render("\nInstall v%s?")+" [y/N] ", meta.Version)
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

	// Verify cosign bundle.
	if asset.CosignBundleURL == "" {
		return fmt.Errorf("release metadata missing cosign bundle URL for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if err := selfupdate.VerifyCosignBundle(client.HTTPClient, archivePath, asset.CosignBundleURL); err != nil {
		return err
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

	img := imageName(u)
	if err := client.PullImage(ctx, img, false); err != nil {
		return fmt.Errorf("pull image: %w", err)
	}
	digestRef, err := client.ImageRepoDigest(ctx, img)
	if err != nil {
		return fmt.Errorf("resolve image digest: %w", err)
	}
	tui.Status("Verifying", "image signature")
	if err := cosign.VerifyImage(ctx, digestRef); err != nil {
		return fmt.Errorf("image verification: %w", err)
	}
	if err := client.PullImage(ctx, ctr.ProxyImage, false); err != nil {
		return fmt.Errorf("pull image: %w", err)
	}
	proxyDigestRef, err := client.ImageRepoDigest(ctx, ctr.ProxyImage)
	if err != nil {
		return fmt.Errorf("resolve proxy image digest: %w", err)
	}
	tui.Status("Verifying", "image signature")
	if err := cosign.VerifyProxyImage(ctx, proxyDigestRef); err != nil {
		return fmt.Errorf("image verification: %w", err)
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
