package cmd

import (
	"context"

	"github.com/bernd/vibepit/proxy"
	"github.com/urfave/cli/v3"
)

func ProxyCommand() *cli.Command {
	return &cli.Command{
		Name:     "proxy",
		Usage:    "Run the proxy server (used inside proxy container)",
		Category: "Internal",
		Hidden:   true,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "config",
				Usage:    "Path to proxy config JSON file",
				Required: true,
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			srv, err := proxy.NewServer(cmd.String("config"))
			if err != nil {
				return err
			}
			return srv.Run(ctx)
		},
	}
}
