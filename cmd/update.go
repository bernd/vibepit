package cmd

import (
	"context"
	"fmt"
	"os/user"

	ctr "github.com/bernd/vibepit/container"
	"github.com/urfave/cli/v3"
)

func UpdateCommand() *cli.Command {
	return &cli.Command{
		Name:     "update",
		Usage:    "Update binary and pull latest container image",
		Category: "Utilities",
		Action: func(ctx context.Context, command *cli.Command) error {
			u, err := user.Current()
			if err != nil {
				return fmt.Errorf("cannot determine current user: %w", err)
			}

			client, err := ctr.NewClient()
			if err != nil {
				return err
			}
			defer client.Close()

			if err := client.PullImage(ctx, imageName(u), false); err != nil {
				return fmt.Errorf("pull image: %w", err)
			}
			if err := client.PullImage(ctx, ctr.ProxyImage, false); err != nil {
				return fmt.Errorf("pull image: %w", err)
			}
			return nil
		},
	}
}
