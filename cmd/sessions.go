package cmd

import (
	"context"
	"fmt"

	ctr "github.com/bernd/vibepit/container"
	"github.com/urfave/cli/v3"
)

func SessionsCommand() *cli.Command {
	return &cli.Command{
		Name:     "sessions",
		Usage:    "List active sessions",
		Category: "Utilities",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			client, err := ctr.NewClient()
			if err != nil {
				return err
			}
			defer client.Close()

			sessions, err := client.ListProxySessions(ctx)
			if err != nil {
				return err
			}

			if len(sessions) == 0 {
				fmt.Println("No active sessions.")
				return nil
			}

			for _, s := range sessions {
				fmt.Printf("%-20s %s (port %s)\n", s.SessionID, s.ProjectDir, s.ControlPort)
			}
			return nil
		},
	}
}
