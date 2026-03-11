package cmd

import (
	"context"
	"fmt"
	"io"
	"os"

	ctr "github.com/bernd/vibepit/container"
	"github.com/urfave/cli/v3"
)

func ACPCommand() *cli.Command {
	return &cli.Command{
		Name:  "acp",
		Usage: "ACP bridge: relay stdio between IDE and sandboxed agent",
		Description: `Starts the ACP interceptor inside a running sandbox container.

The IDE spawns this command as a subprocess and communicates via stdin/stdout
using ACP's JSON-RPC protocol. This command does a single docker exec into
the sandbox to run "vibepit acp-intercept", then relays bytes between the
IDE and the interceptor.

Example IDE config:
  {"command": "vibepit", "args": ["acp", "--agent", "claude"]}`,
		Flags: []cli.Flag{
			sessionFlag,
			&cli.StringFlag{
				Name:  "agent",
				Usage: "Agent command to run inside the sandbox (e.g. claude)",
				Value: "claude",
			},
			&cli.StringSliceFlag{
				Name:  "agent-args",
				Usage: "Arguments to pass to the agent command",
			},
		},
		Action: acpAction,
	}
}

func acpAction(ctx context.Context, cmd *cli.Command) error {
	session, err := discoverSession(ctx, cmd.String("session"))
	if err != nil {
		return fmt.Errorf("session discovery: %w", err)
	}

	client, err := ctr.NewClient(ctr.WithDebug(cmd.Bool(debugFlag)))
	if err != nil {
		return err
	}
	defer client.Close()

	sandboxID, err := client.FindSandboxBySessionID(ctx, session.SessionID)
	if err != nil {
		return err
	}

	// Build the exec command: vibepit acp-intercept --agent <agent> [--agent-args ...]
	execCmd := []string{ctr.SandboxBinaryPath, "acp-intercept", "--agent", cmd.String("agent")}
	for _, arg := range cmd.StringSlice("agent-args") {
		execCmd = append(execCmd, "--agent-args", arg)
	}

	hijack, err := client.ExecNonInteractive(ctx, sandboxID, execCmd)
	if err != nil {
		return fmt.Errorf("exec into sandbox: %w", err)
	}
	defer hijack.Close()

	// Relay bytes: IDE stdin → exec stdin, exec stdout → IDE stdout.
	errCh := make(chan error, 2)

	go func() {
		_, err := io.Copy(hijack.Conn, os.Stdin)
		// Close exec stdin when IDE closes its stdin.
		hijack.Conn.Close()
		errCh <- err
	}()

	go func() {
		_, err := io.Copy(os.Stdout, hijack.Reader)
		errCh <- err
	}()

	// Wait for one direction to finish.
	<-errCh
	return nil
}
