package cmd

import (
	"context"
	"fmt"

	"github.com/bernd/vibepit/tui"
	tea "github.com/charmbracelet/bubbletea"
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
			session, sessions, err := discoverSessionOrAll(ctx, cmd.String("session"))
			if err != nil {
				return fmt.Errorf("cannot find running proxy: %w", err)
			}

			if session != nil {
				return runMonitor(session)
			}

			// Multiple sessions — start with selector, transition to monitor on select.
			onSelect := func(info *SessionInfo) (tui.Screen, tea.Cmd) {
				client, err := NewControlClient(info)
				if err != nil {
					return nil, func() tea.Msg { return sessionErrorMsg{err} }
				}
				return newMonitorScreen(info, client), nil
			}
			s := newSessionScreen(sessions, onSelect)
			header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "session selector"}
			w := tui.NewWindow(header, s)
			p := tea.NewProgram(w, tea.WithAltScreen())
			if _, err := p.Run(); err != nil {
				return fmt.Errorf("monitor UI: %w", err)
			}
			return nil
		},
	}
}

func runMonitor(session *SessionInfo) error {
	client, err := NewControlClient(session)
	if err != nil {
		return err
	}
	screen := newMonitorScreen(session, client)
	header := &tui.HeaderInfo{ProjectDir: session.ProjectDir, SessionID: session.SessionID}
	w := tui.NewWindow(header, screen)
	p := tea.NewProgram(w, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("monitor UI: %w", err)
	}
	return nil
}
