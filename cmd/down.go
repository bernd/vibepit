package cmd

import (
	"cmp"
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

	// Try sandbox first, fall back to any session container (handles
	// partial failures where sandbox crashed but proxy is still running).
	var sessionID string
	session, err := client.FindRunningSession(ctx, projectRoot)
	if err != nil {
		return err
	}
	if session != nil {
		sessionID = session.SessionID
	} else {
		sessionID, err = client.FindAnySessionContainer(ctx, projectRoot)
		if err != nil {
			return err
		}
		if sessionID == "" {
			return fmt.Errorf("no running session found for %s", projectRoot)
		}
	}

	containers, err := client.SessionContainers(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("find session containers: %w", err)
	}

	var containersFailed bool
	for _, c := range containers {
		role := cmp.Or(c.Role, c.ID[:12])
		name := cmp.Or(c.Name, "unknown-name")
		tui.Status("Stopping", "%s container %s", role, name)
		if err := client.StopAndRemove(ctx, c.ID); err != nil {
			tui.Error("stop %s %s: %v", role, name, err)
			containersFailed = true
		}
	}

	networkName := networkNamePrefix + sessionID
	if err := client.RemoveNetwork(ctx, networkName); err != nil {
		tui.Error("remove network %s: %v", networkName, err)
	}

	if containersFailed {
		// Preserve credentials so the user can retry vibepit down or
		// manually clean up the remaining containers.
		return fmt.Errorf("some containers could not be removed — credentials preserved for retry")
	}

	if err := CleanupSessionCredentials(sessionID); err != nil {
		tui.Error("cleanup credentials: %v", err)
	}

	tui.Status("Stopped", "session %s", sessionID)
	return nil
}
