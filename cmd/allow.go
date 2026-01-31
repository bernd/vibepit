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
	"time"

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
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			entries := cmd.Args().Slice()
			if len(entries) == 0 {
				return fmt.Errorf("at least one domain is required")
			}

			addr := cmd.String("addr")
			if addr == "" {
				discovered, err := discoverProxyAddr(ctx)
				if err != nil {
					return fmt.Errorf("cannot find running proxy (use --addr to specify manually): %w", err)
				}
				addr = discovered
			}

			// POST entries to the proxy control API.
			body, _ := json.Marshal(map[string]any{"entries": entries})
			client := &http.Client{Timeout: 5 * time.Second}
			resp, err := client.Post(
				fmt.Sprintf("http://%s/allow", addr),
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

			// Find project root and persist to config.
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
