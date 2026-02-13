package cmd

import (
	"github.com/urfave/cli/v3"
)

var sessionFlag = &cli.StringFlag{
	Name:  "session",
	Usage: "Session ID or project path (skips interactive selection)",
}

const debugFlag = "debug"

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
		},
		Commands: []*cli.Command{
			// Order matters here!
			RunCommand(),
			AllowHTTPCommand(),
			AllowDNSCommand(),
			ProxyCommand(),
			SessionsCommand(),
			MonitorCommand(),
			TelemetryCommand(),
			UpdateCommand(),
		},
	}
}
