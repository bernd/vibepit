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
		Usage:  "Show session status",
		Action: StatusAction,
	}
}

func StatusAction(ctx context.Context, cmd *cli.Command) error {
	client, err := ctr.NewClient(ctr.WithDebug(cmd.Root().Bool(debugFlag)))
	if err != nil {
		return err
	}
	defer client.Close() //nolint:errcheck

	projectRoot, _ := resolveProjectRoot(cmd)

	// If we resolved a project root, try to show status for that project.
	if projectRoot != "" {
		session, err := client.FindRunningSession(ctx, projectRoot)
		if err != nil {
			return err
		}
		if session != nil {
			return printSessionStatus(ctx, client, session.SessionID, projectRoot)
		}
	}

	// No project-specific session found — list all running sessions.
	sessions, err := client.ListProxySessions(ctx)
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		if projectRoot != "" {
			fmt.Printf("No running session for %s\n", projectRoot)
		} else {
			fmt.Println("No active sessions.")
		}
		return nil
	}
	for i, s := range sessions {
		if i > 0 {
			fmt.Println()
		}
		if err := printSessionStatus(ctx, client, s.SessionID, s.ProjectDir); err != nil {
			return err
		}
	}
	return nil
}

func printSessionStatus(ctx context.Context, client *ctr.Client, sessionID, projectDir string) error {
	tui.Status("Session", "%s", sessionID)
	tui.Status("Project", "%s", projectDir)

	// Show per-container status.
	containers, err := client.SessionContainers(ctx, sessionID)
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
	proxyID, proxyErr := findProxyForSession(ctx, client, sessionID)
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
