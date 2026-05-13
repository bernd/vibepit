package cmd

import (
	"charm.land/lipgloss/v2"
	"context"
	"fmt"
	"os"
	"time"

	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/tui"
	"github.com/urfave/cli/v3"
)

const statusVerboseFlag = "verbose"

func StatusCommand() *cli.Command {
	return &cli.Command{
		Name:  "status",
		Usage: "Show session status",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:    statusVerboseFlag,
				Aliases: []string{"v"},
				Usage:   "Enable verbose output",
			},
		},
		Action: StatusAction,
	}
}

func StatusAction(ctx context.Context, cmd *cli.Command) error {
	client, err := ctr.NewClient(ctr.WithDebug(cmd.Root().Bool(debugFlag)))
	if err != nil {
		return err
	}
	defer client.Close() //nolint:errcheck

	sessions, err := client.ListProxySessions(ctx)
	if err != nil {
		return err
	}

	isVerbose := cmd.Bool(statusVerboseFlag)
	projectRoot, _ := resolveProjectRoot(cmd)
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(tui.ColorField)
	warnStyle := lipgloss.NewStyle().Bold(true).Foreground(tui.ColorOrange)

	if len(sessions) == 0 {
		fmt.Println(warnStyle.Render("No active sessions"))
		return nil
	}

	var hasProjectSession bool
	var otherSessions []ctr.ProxySession

	for _, session := range sessions {
		if session.ProjectDir == projectRoot {
			fmt.Println(headerStyle.Render("This project:"))
			hasProjectSession = true
			if err := printSessionStatus(ctx, client, session.SessionID, projectRoot, isVerbose); err != nil {
				return err
			}
		} else {
			otherSessions = append(otherSessions, session)
		}
	}
	if len(otherSessions) > 0 {
		isHome := os.Getenv("HOME") == projectRoot
		if !hasProjectSession && !isHome {
			fmt.Println(warnStyle.Render("No active sessions for " + projectRoot))
		}
		if !isHome {
			fmt.Println()
			fmt.Println(headerStyle.Render("Other projects:"))
		}
	}
	for i, s := range otherSessions {
		if i > 0 {
			fmt.Println()
		}
		if err := printSessionStatus(ctx, client, s.SessionID, s.ProjectDir, isVerbose); err != nil {
			return err
		}
	}
	return nil
}

func printSessionStatus(ctx context.Context, client *ctr.Client, sessionID, projectDir string, verbose bool) error {
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
	proxyID, proxyErr := client.FindProxyContainerID(ctx, sessionID)
	if proxyErr == nil {
		if port, err := client.FindControlPort(ctx, proxyID); err == nil {
			apiAddr = fmt.Sprintf("127.0.0.1:%d", port)
		}
		if port, err := client.FindPublishedPort(ctx, proxyID, ctr.SSHContainerPort); err == nil {
			sshAddr = fmt.Sprintf("127.0.0.1:%d", port)
		}
	}

	if verbose {
		tui.Status("API", "%s", apiAddr)
		tui.Status("SSH", "%s", sshAddr)
	}

	return nil
}
