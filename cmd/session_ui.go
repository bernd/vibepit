package cmd

import (
	"fmt"
	"strings"
	"time"

	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// formatUptime returns a human-readable duration between started and now.
func formatUptime(started, now time.Time) string {
	d := now.Sub(started)
	if d < time.Minute {
		return "< 1m"
	}

	totalMinutes := int(d.Minutes())
	days := totalMinutes / (60 * 24)
	hours := (totalMinutes % (60 * 24)) / 60
	minutes := totalMinutes % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}

// sessionScreen implements tui.Screen for selecting a proxy session.
type sessionScreen struct {
	tui.Cursor
	sessions []ctr.ProxySession
	selected *SessionInfo
	onSelect func(*SessionInfo) (tui.Screen, tea.Cmd)
}

// sessionErrorMsg is sent when the onSelect callback fails.
type sessionErrorMsg struct{ err error }

func newSessionScreen(sessions []ctr.ProxySession, onSelect func(*SessionInfo) (tui.Screen, tea.Cmd)) *sessionScreen {
	return &sessionScreen{
		Cursor:   tui.Cursor{ItemCount: len(sessions)},
		sessions: sessions,
		onSelect: onSelect,
	}
}

func (s *sessionScreen) Update(msg tea.Msg, w *tui.Window) (tui.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			if s.Pos >= 0 && s.Pos < len(s.sessions) {
				s.selected = sessionInfoFromProxy(s.sessions[s.Pos])
				if s.onSelect != nil {
					screen, cmd := s.onSelect(s.selected)
					if screen != nil {
						w.SetHeader(&tui.HeaderInfo{
							ProjectDir: s.selected.ProjectDir,
							SessionID:  s.selected.SessionID,
						})
						return screen, cmd
					}
					return s, cmd
				}
				return s, tea.Quit
			}
		case "q", "ctrl+c":
			return s, tea.Quit
		default:
			s.HandleKey(msg)
		}

	case sessionErrorMsg:
		w.SetError(msg.err)

	case tea.WindowSizeMsg:
		s.VpHeight = w.VpHeight()
		s.EnsureVisible()
	}

	return s, nil
}

func (s *sessionScreen) View(w *tui.Window) string {
	var lines []string
	note := lipgloss.NewStyle().Foreground(tui.ColorField).
		Render("Multiple sessions running. Select one:")
	lines = append(lines, note, "")

	end := s.Offset + s.VpHeight
	end = min(end, len(s.sessions))
	now := time.Now()
	for i := s.Offset; i < end; i++ {
		lines = append(lines, renderSessionLine(s.sessions[i], i == s.Pos, now))
	}
	for len(lines) < s.VpHeight {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func renderSessionLine(ps ctr.ProxySession, highlighted bool, now time.Time) string {
	base, marker := tui.LineStyle(highlighted)

	id := base.Foreground(tui.ColorField).Render(fmt.Sprintf("%-16s", ps.SessionID))
	uptime := base.Foreground(tui.ColorOrange).Render(fmt.Sprintf("%-8s", formatUptime(ps.StartedAt, now)))
	dir := base.Foreground(tui.ColorCyan).Render(ps.ProjectDir)
	sp := base.Render(" ")

	return marker + id + sp + uptime + sp + dir
}

func (s *sessionScreen) FooterKeys(w *tui.Window) []tui.FooterKey {
	keys := []tui.FooterKey{
		{Key: "enter", Desc: "select"},
	}
	keys = append(keys, s.Cursor.FooterKeys()...)
	return keys
}

func (s *sessionScreen) FooterStatus(w *tui.Window) string {
	return fmt.Sprintf("%d sessions", len(s.sessions))
}

// selectSession runs a temporary TUI for the user to pick a session.
func selectSession(sessions []ctr.ProxySession) (*SessionInfo, error) {
	s := newSessionScreen(sessions, nil)
	header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "session selector"}
	w := tui.NewWindow(header, s)
	p := tea.NewProgram(w, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return nil, fmt.Errorf("session selector: %w", err)
	}
	if s.selected == nil {
		return nil, fmt.Errorf("no session selected")
	}
	return s.selected, nil
}
