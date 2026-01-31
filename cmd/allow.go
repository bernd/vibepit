package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bernd/vibepit/config"
	"github.com/urfave/cli/v3"
)

func AllowCommand() *cli.Command {
	return &cli.Command{
		Name:      "allow",
		Usage:     "Add domains to the running proxy's allowlist",
		ArgsUsage: "<domain[:port]>...",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "no-save",
				Usage: "Skip persisting to project config",
			},
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
			entries := cmd.Args().Slice()
			if len(entries) == 0 {
				return fmt.Errorf("at least one domain is required")
			}

			httpClient, baseURL, err := controlAPIClient(ctx, cmd.String("addr"), cmd.String("session"))
			if err != nil {
				return err
			}

			body, _ := json.Marshal(map[string]any{"entries": entries})
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
			json.NewDecoder(resp.Body).Decode(&result)

			for _, d := range result.Added {
				fmt.Printf("+ allowed %s\n", d)
			}

			if cmd.Bool("no-save") {
				return nil
			}

			projectRoot, err := findProjectRoot()
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not find project root, skipping config save: %v\n", err)
				return nil
			}

			projectPath := config.DefaultProjectPath(projectRoot)
			if err := config.AppendAllow(projectPath, entries); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			fmt.Printf("+ saved to %s\n", projectPath)

			return nil
		},
	}
}

func findProjectRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	wd, _ = filepath.Abs(wd)

	if gitRoot, err := exec.Command("git", "-C", wd, "rev-parse", "--show-toplevel").Output(); err == nil {
		if root := strings.TrimSpace(string(gitRoot)); root != "" {
			return root, nil
		}
	}

	return wd, nil
}
