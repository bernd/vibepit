package cmd

import (
	"context"
	"fmt"

	"github.com/bernd/vibepit/config"
	"github.com/urfave/cli/v3"
)

func AllowCommand() *cli.Command {
	return &cli.Command{
		Name:      "allow",
		Usage:     "Add domains to the proxy allowlist",
		ArgsUsage: "<domain[:port]>...",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "no-save",
				Usage: "Skip persisting to project config",
			},
			&cli.StringFlag{
				Name:  "session",
				Usage: "Session ID or project path (skips interactive selection)",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			entries := cmd.Args().Slice()
			if len(entries) == 0 {
				return cli.ShowSubcommandHelp(cmd)
			}

			session, err := discoverSession(ctx, cmd.String("session"))
			if err != nil {
				return fmt.Errorf("cannot find running proxy: %w", err)
			}

			client, err := NewControlClient(session)
			if err != nil {
				return err
			}

			added, err := client.Allow(entries)
			if err != nil {
				return err
			}

			for _, d := range added {
				fmt.Printf("+ allowed %s\n", d)
			}

			if cmd.Bool("no-save") {
				return nil
			}

			projectPath := config.DefaultProjectPath(session.ProjectDir)
			if err := config.AppendAllow(projectPath, entries); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			fmt.Printf("+ saved to %s\n", projectPath)

			return nil
		},
	}
}

