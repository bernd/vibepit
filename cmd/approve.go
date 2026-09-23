package cmd

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	"github.com/urfave/cli/v3"
)

// ApproveCommand renders a one-shot allow/deny prompt for a blocked target.
// It is launched by `run --prompt` inside a terminal overlay and is not meant
// to be invoked by hand.
func ApproveCommand() *cli.Command {
	return &cli.Command{
		Name:      "approve",
		Usage:     "Prompt to allow a blocked target (internal)",
		ArgsUsage: "domain[:port]",
		Hidden:    true,
		Flags: []cli.Flag{
			sessionFlag,
			&cli.StringFlag{Name: "source", Value: string(proxy.SourceProxy), Usage: "proxy or dns"},
			&cli.StringFlag{Name: "reason", Usage: "Block reason shown in the prompt"},
			&cli.StringFlag{Name: "control-port", Usage: "Control API port (skips session discovery)"},
			&cli.StringFlag{Name: "cred-dir", Usage: "Session credential directory"},
			&cli.StringFlag{Name: "project-dir", Usage: "Project directory of the session"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			err := runApprove(ctx, cmd)
			if err != nil {
				// Keep the overlay open long enough to read the error.
				fmt.Fprintf(os.Stderr, "approve: %v\npress enter to close", err)
				fmt.Scanln() //nolint:errcheck
			}
			return err
		},
	}
}

func runApprove(ctx context.Context, cmd *cli.Command) error {
	entry, err := parseApproveTarget(cmd.String("source"), cmd.Args().First())
	if err != nil {
		return err
	}
	entry.Reason = cmd.String("reason")
	entry.Time = time.Now()

	session, err := approveSession(ctx, cmd)
	if err != nil {
		return fmt.Errorf("discover session: %w", err)
	}
	client, err := NewControlClient(session)
	if err != nil {
		return fmt.Errorf("control client: %w", err)
	}
	defer client.Close()

	header := &tui.HeaderInfo{ProjectDir: session.ProjectDir, SessionID: session.SessionID}
	return runTUI(header, newApproveScreen(session, client, entry))
}

// approveSession prefers the session details passed by the parent. kitty
// launches the overlay with its own environment, so re-discovering the
// session could hit a different container runtime (DOCKER_HOST) or
// credential directory (XDG_*) than the parent used.
func approveSession(ctx context.Context, cmd *cli.Command) (*SessionInfo, error) {
	if port := cmd.String("control-port"); port != "" {
		return &SessionInfo{
			ControlPort: port,
			SessionID:   cmd.String("session"),
			ProjectDir:  cmd.String("project-dir"),
			CredDir:     cmd.String("cred-dir"),
		}, nil
	}
	return discoverSession(ctx, cmd.String("session"))
}

func parseApproveTarget(source, target string) (proxy.LogEntry, error) {
	if target == "" {
		return proxy.LogEntry{}, fmt.Errorf("missing target")
	}
	entry := proxy.LogEntry{Action: proxy.ActionBlock}
	switch proxy.Source(source) {
	case proxy.SourceDNS:
		entry.Source = proxy.SourceDNS
		entry.Domain = target
	case proxy.SourceProxy:
		host, port, err := net.SplitHostPort(target)
		if err != nil {
			return proxy.LogEntry{}, fmt.Errorf("proxy target must be domain:port: %w", err)
		}
		entry.Source = proxy.SourceProxy
		entry.Domain = host
		entry.Port = port
	default:
		return proxy.LogEntry{}, fmt.Errorf("unknown source %q", source)
	}
	return entry, nil
}

// approveCmdline builds the argv that runs the approve prompt for entry in
// the given session. parseApproveTarget must accept what this produces.
func approveCmdline(exe string, session *SessionInfo, entry proxy.LogEntry) []string {
	credDir := session.CredDir
	if credDir == "" {
		credDir = sessionDir(session.SessionID)
	}
	return []string{
		exe, "approve",
		"--session", session.SessionID,
		"--control-port", session.ControlPort,
		"--cred-dir", credDir,
		"--project-dir", session.ProjectDir,
		"--source", string(entry.Source),
		"--reason", entry.Reason,
		"--", allowValueForEntry(entry),
	}
}
