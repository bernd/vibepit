package cmd

import (
	"context"
	"fmt"

	"github.com/bernd/vibepit/config"
	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	"github.com/urfave/cli/v3"
)

func AllowHTTPCommand() *cli.Command {
	return &cli.Command{
		Name:      "allow-http",
		Usage:     "Add entries to the proxy HTTP allowlist",
		ArgsUsage: "<domain:port-pattern>...",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "no-save",
				Usage: "Skip persisting to project config",
			},
			sessionFlag,
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			entries := cmd.Args().Slice()
			if len(entries) == 0 {
				return cli.ShowSubcommandHelp(cmd)
			}
			if err := proxy.ValidateHTTPEntries(entries); err != nil {
				return err
			}

			session, err := discoverSession(ctx, cmd.String("session"))
			if err != nil {
				return fmt.Errorf("cannot find running proxy: %w", err)
			}

			client, err := NewControlClient(session)
			if err != nil {
				return err
			}

			added, err := client.AllowHTTP(entries)
			if err != nil {
				return err
			}

			for _, d := range added {
				tui.Status("Allowed", "%s", d)
			}

			if cmd.Bool("no-save") {
				return nil
			}

			projectPath := config.DefaultProjectPath(session.ProjectDir)
			if err := config.AppendAllowHTTP(projectPath, entries); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			tui.Status("Saved", "to %s", projectPath)

			return nil
		},
	}
}

func AllowDNSCommand() *cli.Command {
	return &cli.Command{
		Name:      "allow-dns",
		Usage:     "Add entries to the proxy DNS allowlist",
		ArgsUsage: "<domain-pattern>...",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "no-save",
				Usage: "Skip persisting to project config",
			},
			sessionFlag,
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			entries := cmd.Args().Slice()
			if len(entries) == 0 {
				return cli.ShowSubcommandHelp(cmd)
			}
			if err := proxy.ValidateDNSEntries(entries); err != nil {
				return err
			}

			session, err := discoverSession(ctx, cmd.String("session"))
			if err != nil {
				return fmt.Errorf("cannot find running proxy: %w", err)
			}

			client, err := NewControlClient(session)
			if err != nil {
				return err
			}

			added, err := client.AllowDNS(entries)
			if err != nil {
				return err
			}

			for _, d := range added {
				tui.Status("Allowed", "%s", d)
			}

			if cmd.Bool("no-save") {
				return nil
			}

			projectPath := config.DefaultProjectPath(session.ProjectDir)
			if err := config.AppendAllowDNS(projectPath, entries); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			tui.Status("Saved", "to %s", projectPath)

			return nil
		},
	}
}

// allowEntry adds the entry's target to the running proxy's allowlist and,
// when save is set, persists it to the project config. Shared by the monitor
// and approve screens.
func allowEntry(client *ControlClient, session *SessionInfo, entry proxy.LogEntry, save bool) (allowStatus, error) {
	value := entry.Target().String()
	live, persist := client.AllowHTTP, config.AppendAllowHTTP
	if entry.Source == proxy.SourceDNS {
		live, persist = client.AllowDNS, config.AppendAllowDNS
	}

	if _, err := live([]string{value}); err != nil {
		return statusNone, err
	}
	if !save {
		return statusTemp, nil
	}
	if err := persist(config.DefaultProjectPath(session.ProjectDir), []string{value}); err != nil {
		return statusNone, err
	}
	return statusSaved, nil
}
