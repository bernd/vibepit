package cmd

import (
	"context"
	"fmt"

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
	defer client.Close() //nolint:errcheck

	projectRoot, err := resolveProjectRoot(cmd)
	if err != nil {
		return err
	}

	session, err := client.FindRunningSession(ctx, projectRoot)
	if err != nil {
		return err
	}
	if session == nil {
		return fmt.Errorf("no running session found for %s", projectRoot)
	}
	sessionID := session.SessionID

	containers, err := client.FindContainersBySessionID(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("find session containers: %w", err)
	}

	for _, id := range containers {
		tui.Status("Stopping", "container %s", id[:12])
		if err := client.StopAndRemove(ctx, id); err != nil {
			tui.Error("stop container %s: %v", id[:12], err)
		}
	}

	networkName := networkNamePrefix + sessionID
	if err := client.RemoveNetwork(ctx, networkName); err != nil {
		tui.Error("remove network %s: %v", networkName, err)
	}

	if err := CleanupSessionCredentials(sessionID); err != nil {
		tui.Error("cleanup credentials: %v", err)
	}

	tui.Status("Stopped", "session %s", sessionID)
	return nil
}
