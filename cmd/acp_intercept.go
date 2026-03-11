package cmd

import (
	"context"
	"os"

	"github.com/bernd/vibepit/acp"
	"github.com/urfave/cli/v3"
)

func ACPInterceptCommand() *cli.Command {
	return &cli.Command{
		Name:   "acp-intercept",
		Usage:  "Run ACP interceptor (used inside sandbox container)",
		Hidden: true,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "agent",
				Usage:    "Agent command to run",
				Required: true,
			},
			&cli.StringSliceFlag{
				Name:  "agent-args",
				Usage: "Arguments to pass to the agent",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			interceptor := acp.NewInterceptor(
				cmd.String("agent"),
				cmd.StringSlice("agent-args"),
			)
			return interceptor.Run(ctx, os.Stdin, os.Stdout)
		},
	}
}
