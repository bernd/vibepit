package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/bernd/vibepit/proxy"
	"github.com/urfave/cli/v3"
)

// SessionInfo contains the information needed to connect to a proxy's control API.
type SessionInfo struct {
	ControlPort string
	SessionID   string
	ProjectDir  string
}

func MonitorCommand() *cli.Command {
	return &cli.Command{
		Name:     "monitor",
		Usage:    "Connect to a running proxy for logs and admin",
		Category: "Utilities",
		Flags:    []cli.Flag{sessionFlag},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			session, err := discoverSession(ctx, cmd.String("session"))
			if err != nil {
				return fmt.Errorf("cannot find running proxy: %w", err)
			}
			client, err := NewControlClient(session)
			if err != nil {
				return err
			}

			fmt.Println("Connecting to proxy...")

			var cursor uint64

			for {
				select {
				case <-ctx.Done():
					return nil
				default:
				}

				entries, err := client.LogsAfter(cursor)
				if err != nil {
					fmt.Printf("connection error: %v (retrying...)\n", err)
					time.Sleep(2 * time.Second)
					continue
				}

				for _, e := range entries {
					symbol := "+"
					if e.Action == proxy.ActionBlock {
						symbol = "x"
					}
					host := e.Domain
					if e.Port != "" {
						host = e.Domain + ":" + e.Port
					}
					fmt.Printf("[%s] %s %-5s %s %s\n",
						e.Time.Format("15:04:05"),
						symbol,
						e.Source,
						host,
						e.Reason,
					)
					cursor = e.ID
				}

				time.Sleep(1 * time.Second)
			}
		},
	}
}
