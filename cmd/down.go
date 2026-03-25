package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bernd/vibepit/config"
	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/tui"
	"github.com/urfave/cli/v3"
)

func DownCommand() *cli.Command {
	return &cli.Command{
		Name:   "down",
		Usage:  "Stop and remove sandbox and proxy containers",
		Action: DownAction,
	}
}

func DownAction(ctx context.Context, cmd *cli.Command) error {
	client, err := ctr.NewClient(ctr.WithDebug(cmd.Root().Bool(debugFlag)))
	if err != nil {
		return err
	}
	defer client.Close()

	// Resolve project root — same logic as RunAction/UpAction.
	projectRoot := cmd.Args().First()
	if projectRoot == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		projectRoot = wd
	}
	projectRoot, err = filepath.Abs(projectRoot)
	if err != nil {
		return err
	}
	projectRoot, err = config.FindProjectRoot(projectRoot)
	if err != nil {
		return err
	}

	// Find sandbox for this project.
	sandboxID, err := client.FindRunningSession(ctx, projectRoot)
	if err != nil {
		return err
	}
	if sandboxID == "" {
		return fmt.Errorf("no running session found for %s", projectRoot)
	}

	// Get session ID from sandbox.
	sessionID, err := client.SessionIDFromContainer(ctx, sandboxID)
	if err != nil {
		return fmt.Errorf("get session ID: %w", err)
	}

	// Find all containers (sandbox + proxy) with this session ID.
	containers, err := client.FindContainersBySessionID(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("find session containers: %w", err)
	}

	// Best-effort cleanup: stop and remove all containers.
	for _, id := range containers {
		tui.Status("Stopping", "container %s", id[:12])
		client.StopAndRemove(ctx, id)
	}

	// Remove network by name.
	networkName := networkNamePrefix + sessionID
	client.RemoveNetwork(ctx, networkName)

	// Remove credentials.
	CleanupSessionCredentials(sessionID)

	tui.Status("Stopped", "session %s", sessionID)
	return nil
}
