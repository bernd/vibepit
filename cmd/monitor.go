package cmd

import (
	"context"
	"fmt"

	ctr "github.com/bernd/vibepit/container"
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
		Aliases:  []string{"m", "tv"},
		Usage:    "Connect to a running proxy for logs and admin",
		Category: "Utilities",
		Flags:    []cli.Flag{sessionFlag},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			client, err := ctr.NewClient()
			if err != nil {
				return fmt.Errorf("cannot create container client: %w", err)
			}
			defer client.Close()

			pollSessions := func() ([]ctr.ProxySession, error) {
				return client.ListProxySessions(ctx)
			}

			var onSelect func(*SessionInfo) (tui.Screen, tea.Cmd)
			onBack := func() tui.Screen {
				return newSessionScreen(nil, onSelect, pollSessions)
			}
			onSelect = func(info *SessionInfo) (tui.Screen, tea.Cmd) {
				cc, err := NewControlClient(info)
				if err != nil {
					return nil, func() tea.Msg { return sessionErrorMsg{err} }
				}
				return newMonitorScreen(info, cc, onBack), nil
			}

			filter := cmd.String("session")
			sessions, err := client.ListProxySessions(ctx)
			if err != nil {
				return fmt.Errorf("cannot list sessions: %w", err)
			}

			var session *SessionInfo
			if filter != "" {
				for _, ps := range sessions {
					if matchSession(ps, filter) {
						session = sessionInfoFromProxy(ps)
						break
					}
				}
				if session == nil {
					return fmt.Errorf("no session matching %q found", filter)
				}
			} else if len(sessions) == 1 {
				session = sessionInfoFromProxy(sessions[0])
			}

			if session != nil {
				cc, err := NewControlClient(session)
				if err != nil {
					return err
				}
				defer cc.Close()
				screen := newMonitorScreen(session, cc, onBack)
				header := &tui.HeaderInfo{ProjectDir: session.ProjectDir, SessionID: session.SessionID}
				return runTUI(header, screen)
			}

			// Zero or multiple sessions — start with selector.
			return runTUI(selectorHeader(), newSessionScreen(sessions, onSelect, pollSessions))
		},
	}
}

func selectorHeader() *tui.HeaderInfo {
	return &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "session selector"}
}

func runTUI(header *tui.HeaderInfo, screen tui.Screen) error {
	w := tui.NewWindow(header, screen)
	p := tea.NewProgram(w, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("monitor UI: %w", err)
	}
	return nil
}
