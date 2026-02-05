package cmd

import (
	"context"
	"fmt"

	ctr "github.com/bernd/vibepit/container"
	"github.com/urfave/cli/v3"
)

func UpdateCommand() *cli.Command {
	return &cli.Command{
		Name:     "update",
		Usage:    "Update binary and pull latest container image",
		Category: "Utilities",
		Flags:    []cli.Flag{},
		Action: func(ctx context.Context, command *cli.Command) error {
			client, err := ctr.NewClient()
			if err != nil {
				return err
			}

			if err := client.PullImage(ctx, defaultImage, false); err != nil {
				return fmt.Errorf("pull image: %w", err)
			}
			return nil
		},
	}
}
