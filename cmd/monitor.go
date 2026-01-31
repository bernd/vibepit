package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/proxy"
	"github.com/urfave/cli/v3"
)

func MonitorCommand() *cli.Command {
	return &cli.Command{
		Name:  "monitor",
		Usage: "Connect to a running proxy for logs and admin",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "addr",
				Usage: "Proxy control API address (auto-detected if omitted)",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			addr := cmd.String("addr")
			if addr == "" {
				discovered, err := discoverProxyAddr(ctx)
				if err != nil {
					return fmt.Errorf("cannot find running proxy (use --addr to specify manually): %w", err)
				}
				addr = discovered
			}
			baseURL := fmt.Sprintf("http://%s", addr)

			fmt.Printf("Connecting to proxy at %s...\n\n", addr)

			client := &http.Client{Timeout: 5 * time.Second}
			seen := 0

			for {
				select {
				case <-ctx.Done():
					return nil
				default:
				}

				resp, err := client.Get(baseURL + "/logs")
				if err != nil {
					fmt.Printf("connection error: %v (retrying...)\n", err)
					time.Sleep(2 * time.Second)
					continue
				}

				var entries []proxy.LogEntry
				json.NewDecoder(resp.Body).Decode(&entries)
				resp.Body.Close()

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

// discoverProxyAddr finds the running vibepit proxy container and returns its
// control API address by reading the container's network settings.
func discoverProxyAddr(ctx context.Context) (string, error) {
	client, err := ctr.NewClient()
	if err != nil {
		return "", err
	}
	defer client.Close()

	ip, err := client.FindProxyIP(ctx)
	if err != nil {
		return "", err
	}
	return ip + proxy.ControlAPIPort, nil
}
