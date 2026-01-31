package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/bernd/vibepit/config"
	"github.com/urfave/cli/v3"
)

func AllowCommand() *cli.Command {
	return &cli.Command{
		Name:      "allow",
		Usage:     "Add domains to the proxy allowlist",
		ArgsUsage: "<domain[:port]>...",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "no-save",
				Usage: "Skip persisting to project config",
			},
			&cli.StringFlag{
				Name:  "session",
				Usage: "Session ID or project path (skips interactive selection)",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			entries := cmd.Args().Slice()
			if len(entries) == 0 {
				return cli.ShowSubcommandHelp(cmd)
			}

			session, err := discoverSession(ctx, cmd.String("session"))
			if err != nil {
				return fmt.Errorf("cannot find running proxy: %w", err)
			}

			httpClient, baseURL, err := controlAPIClient(session)
			if err != nil {
				return err
			}

			body, err := json.Marshal(map[string]any{"entries": entries})
			if err != nil {
				return fmt.Errorf("marshal allow entries: %w", err)
			}
			resp, err := httpClient.Post(
				baseURL+"/allow",
				"application/json",
				bytes.NewReader(body),
			)
			if err != nil {
				return fmt.Errorf("POST /allow: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("proxy returned %s", resp.Status)
			}

			var result struct {
				Added []string `json:"added"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				return fmt.Errorf("decode allow response: %w", err)
			}

			for _, d := range result.Added {
				fmt.Printf("+ allowed %s\n", d)
			}

			if cmd.Bool("no-save") {
				return nil
			}

			projectPath := config.DefaultProjectPath(session.ProjectDir)
			if err := config.AppendAllow(projectPath, entries); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			fmt.Printf("+ saved to %s\n", projectPath)

			return nil
		},
	}
}

