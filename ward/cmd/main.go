package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/bernd/vibepit/ward"
	"github.com/urfave/cli/v3"
)

func main() {
	root := &cli.Command{
		Name:  "ward",
		Usage: "Terminal wrapper with notification overlay",
		Commands: []*cli.Command{
			runCmd(),
		},
	}

	if err := root.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runCmd() *cli.Command {
	return &cli.Command{
		Name:      "run",
		Usage:     "Run a command in the ward PTY wrapper",
		ArgsUsage: "-- [command] [args...]",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			args := cmd.Args().Slice()
			if len(args) == 0 {
				return fmt.Errorf("requires at least 1 arg")
			}

			ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
			defer cancel()

			w := ward.NewWrapper(ward.Options{
				Command: args,
			})

			exitCode, err := w.Run(ctx)
			if err != nil {
				return fmt.Errorf("ward: %w", err)
			}
			os.Exit(exitCode)
			return nil
		},
	}
}
