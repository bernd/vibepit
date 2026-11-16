package cmd

import (
	"cmp"
	"context"
	"fmt"
	"strings"

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

	return stopProjectSessions(ctx, client, projectRoot)
}

// sessionStopper is the part of the container client that down needs.
type sessionStopper interface {
	ProjectSessionIDs(ctx context.Context, projectDir string) ([]string, error)
	SessionContainers(ctx context.Context, sessionID string) ([]ctr.SessionContainer, error)
	StopAndRemove(ctx context.Context, containerID string) error
	RemoveNetwork(ctx context.Context, networkID string) error
}

// stopProjectSessions stops every session of the project, running or not.
// Failed cleanups can leave several sessions behind, and up refuses to start
// while any of them exists, so a single down must remove them all.
func stopProjectSessions(ctx context.Context, client sessionStopper, projectRoot string) error {
	sessionIDs, err := client.ProjectSessionIDs(ctx, projectRoot)
	if err != nil {
		return err
	}
	if len(sessionIDs) == 0 {
		return fmt.Errorf("no session found for %s", projectRoot)
	}

	var failed []string
	for _, sessionID := range sessionIDs {
		if !stopSession(ctx, client, sessionID) {
			failed = append(failed, sessionID)
		}
	}
	if len(failed) > 0 {
		noun := "session"
		if len(failed) > 1 {
			noun = "sessions"
		}
		return fmt.Errorf("could not fully remove %s %s", noun, strings.Join(failed, ", "))
	}
	return nil
}

// stopSession removes a session's containers, credentials and network. It
// reports errors as it goes and returns false if a container or the network
// is left. A container left behind keeps the credentials for a retry.
func stopSession(ctx context.Context, client sessionStopper, sessionID string) bool {
	containers, err := client.SessionContainers(ctx, sessionID)
	if err != nil {
		tui.Error("find containers of session %s: %v", sessionID, err)
		return false
	}

	ok := true
	for _, c := range containers {
		role := cmp.Or(c.Role, c.ID[:min(12, len(c.ID))])
		name := cmp.Or(c.Name, "unknown-name")
		tui.Status("Stopping", "%s container %s", role, name)
		if err := client.StopAndRemove(ctx, c.ID); err != nil {
			tui.Error("stop %s %s: %v", role, name, err)
			ok = false
		}
	}

	if !ok {
		// A container left behind keeps the network in use, so removing it
		// would only fail with an error that hides the real one.
		return false
	}

	// The credentials are useless once the containers are gone, and a later
	// down could not find this session to retry, so delete them even if the
	// network is left behind.
	if err := CleanupSessionCredentials(sessionID); err != nil {
		tui.Error("cleanup credentials: %v", err)
	}

	networkName := networkNamePrefix + sessionID
	if err := client.RemoveNetwork(ctx, networkName); err != nil {
		tui.Error("remove network %s: %v", networkName, err)
		return false
	}

	tui.Status("Stopped", "session %s", sessionID)
	return true
}
