package cmd

import (
	"github.com/urfave/cli/v3"
)

func RootCommand() *cli.Command {
	return &cli.Command{
		Name:            "vibepit",
		Usage:           "Run agents in isolated container sandboxes",
		Description:     "I pity the vibes.",
		HideHelpCommand: true,
		DefaultCommand:  "run",
		Commands: []*cli.Command{
			// Order matters here!
			RunCommand(),
			AllowCommand(),
			ProxyCommand(),
			SessionsCommand(),
			MonitorCommand(),
		},
	}
}
