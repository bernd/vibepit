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
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "session",
				Usage: "Session ID or project path (skips interactive selection)",
			},
		},
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

			seen := 0

			for {
				select {
				case <-ctx.Done():
					return nil
				default:
				}

				entries, err := client.Logs()
				if err != nil {
					fmt.Printf("connection error: %v (retrying...)\n", err)
					time.Sleep(2 * time.Second)
					continue
				}

				for i := seen; i < len(entries); i++ {
					e := entries[i]
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
				}
				seen = len(entries)

				time.Sleep(1 * time.Second)
			}
		},
	}
}
