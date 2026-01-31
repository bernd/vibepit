package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/charmbracelet/huh"

	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/proxy"
	"github.com/urfave/cli/v3"
)

// SessionInfo contains the information needed to connect to a proxy's control API.
type SessionInfo struct {
	ControlPort string
	SessionID   string
	ProjectDir  string
}

// controlAPIClient returns an HTTP client and base URL for the proxy control API.
// If addr is non-empty, it uses plain HTTP to that address (for debugging).
// Otherwise it discovers a running session and sets up mTLS.
func controlAPIClient(ctx context.Context, addr, sessionFilter string) (*http.Client, string, error) {
	if addr != "" {
		return &http.Client{Timeout: 5 * time.Second}, fmt.Sprintf("http://%s", addr), nil
	}

	session, err := discoverSession(ctx, sessionFilter)
	if err != nil {
		return nil, "", fmt.Errorf("cannot find running proxy (use --addr to specify manually): %w", err)
	}
	tlsCfg, err := LoadSessionTLSConfig(session.SessionID)
	if err != nil {
		return nil, "", fmt.Errorf("load TLS credentials: %w", err)
	}
	httpClient := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}
	baseURL := fmt.Sprintf("https://127.0.0.1:%s", session.ControlPort)
	return httpClient, baseURL, nil
}

// discoverSession finds running vibepit proxy containers and returns connection
// info. If multiple sessions are running, prompts the user to select one.
// If filter is non-empty, it matches against SessionID or ProjectDir.
func discoverSession(ctx context.Context, filter string) (*SessionInfo, error) {
	client, err := ctr.NewClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	sessions, err := client.ListProxySessions(ctx)
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, fmt.Errorf("no running vibepit sessions found")
	}

	if filter != "" {
		for _, s := range sessions {
			if s.SessionID == filter || s.ProjectDir == filter {
				return &SessionInfo{
					ControlPort: s.ControlPort,
					SessionID:   s.SessionID,
					ProjectDir:  s.ProjectDir,
				}, nil
			}
		}
		return nil, fmt.Errorf("no session matching %q found", filter)
	}

	if len(sessions) == 1 {
		return &SessionInfo{
			ControlPort: sessions[0].ControlPort,
			SessionID:   sessions[0].SessionID,
			ProjectDir:  sessions[0].ProjectDir,
		}, nil
	}

	// Multiple sessions — interactive selection.
	options := make([]huh.Option[int], len(sessions))
	for i, s := range sessions {
		options[i] = huh.NewOption(s.ProjectDir, i)
	}
	var selected int
	err = huh.NewSelect[int]().
		Title("Select a session").
		Options(options...).
		Value(&selected).
		Run()
	if err != nil {
		return nil, fmt.Errorf("session selection: %w", err)
	}

	s := sessions[selected]
	return &SessionInfo{
		ControlPort: s.ControlPort,
		SessionID:   s.SessionID,
		ProjectDir:  s.ProjectDir,
	}, nil
}

func MonitorCommand() *cli.Command {
	return &cli.Command{
		Name:  "monitor",
		Usage: "Connect to a running proxy for logs and admin",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "addr",
				Usage: "Proxy control API address (auto-detected if omitted)",
			},
			&cli.StringFlag{
				Name:  "session",
				Usage: "Session ID or project path (skips interactive selection)",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			httpClient, baseURL, err := controlAPIClient(ctx, cmd.String("addr"), cmd.String("session"))
			if err != nil {
				return err
			}

			fmt.Printf("Connecting to proxy at %s...\n\n", baseURL)

			seen := 0

			for {
				select {
				case <-ctx.Done():
					return nil
				default:
				}

				resp, err := httpClient.Get(baseURL + "/logs")
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
