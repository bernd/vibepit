package cmd

import (
	"context"
	"fmt"
	"time"

	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/tui"
	"github.com/urfave/cli/v3"
)

func StatusCommand() *cli.Command {
	return &cli.Command{
		Name:   "status",
		Usage:  "Show session status for the current project",
		Action: StatusAction,
	}
}

func StatusAction(ctx context.Context, cmd *cli.Command) error {
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
		fmt.Printf("No running session for %s\n", projectRoot)
		return nil
	}

	tui.Status("Session", "%s", session.SessionID)
	tui.Status("Project", "%s", projectRoot)

	// Show per-container status.
	containers, err := client.SessionContainers(ctx, session.SessionID)
	if err != nil {
		return fmt.Errorf("list session containers: %w", err)
	}
	for _, c := range containers {
		status := client.ContainerStatus(ctx, c.ID)
		startedAt := client.ContainerStartedAt(ctx, c.ID)
		if !startedAt.IsZero() {
			uptime := time.Since(startedAt).Truncate(time.Second)
			status = fmt.Sprintf("%s: %s (up %s)", status, c.Name, uptime)
		}
		label := "Sandbox"
		if c.Role == ctr.RoleProxy {
			label = "Proxy"
		}
		tui.Status(label, "%s", status)
	}

	// Show ports published on the proxy container.
	sshAddr := "N/A"
	apiAddr := "N/A"
	proxyID, proxyErr := findProxyForSession(ctx, client, session.SessionID)
	if proxyErr == nil {
		if port, err := client.FindControlPort(ctx, proxyID); err == nil {
			apiAddr = fmt.Sprintf("127.0.0.1:%d", port)
		}
		if port, err := client.FindPublishedPort(ctx, proxyID, ctr.SSHContainerPort); err == nil {
			sshAddr = fmt.Sprintf("127.0.0.1:%d", port)
		}
	}
	tui.Status("API", "%s", apiAddr)
	tui.Status("SSH", "%s", sshAddr)

	return nil
}
