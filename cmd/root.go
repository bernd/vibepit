package cmd

import (
	"context"
	"fmt"
	"github.com/bernd/vibepit/config"
	"github.com/urfave/cli/v3"
	"os"
)

var sessionFlag = &cli.StringFlag{
	Name:  "session",
	Usage: "Session ID or project path (skips interactive selection)",
}

const debugFlag = "debug"
const versionFlag = "version"

func RootCommand() *cli.Command {
	return &cli.Command{
		Name:            "vibepit",
		Usage:           "Run agents in isolated container sandboxes",
		Description:     "I pity the vibes.",
		HideHelpCommand: true,
		DefaultCommand:  "run",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  debugFlag,
				Usage: "Enable debug output",
			},
			&cli.BoolFlag{
				Name:  versionFlag,
				Usage: "Show version",
			},
		},
		Before: func(ctx context.Context, command *cli.Command) (context.Context, error) {
			if command.Bool(versionFlag) {
				fmt.Printf("%s (%s)\n", config.Version, config.CommitID)
				os.Exit(0)
			}
			return ctx, nil
		},
		Commands: []*cli.Command{
			// Order matters here!
			RunCommand(),
			UpCommand(),
			AllowHTTPCommand(),
			AllowDNSCommand(),
			ProxyCommand(),
			VibedCommand(),
			SessionsCommand(),
			MonitorCommand(),
			UpdateCommand(),
		},
	}
}
