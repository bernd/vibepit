package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/bernd/vibepit/cmd"
	ctr "github.com/bernd/vibepit/container"
	"github.com/urfave/cli/v3"
)

func main() {
	app := &cli.Command{
		Name:   "vibepit",
		Usage:  "Run agents in isolated Docker containers",
		Flags:  cmd.RootFlags(),
		Action: cmd.RootAction,
		Commands: []*cli.Command{
			cmd.ProxyCommand(),
			cmd.MonitorCommand(),
			cmd.AllowCommand(),
		},
	}
	if err := app.Run(context.Background(), os.Args); err != nil {
		var exitErr *ctr.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.Code)
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
