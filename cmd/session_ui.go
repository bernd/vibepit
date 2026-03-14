package cmd

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/tui"
)

// sortSessions sorts proxy sessions by SessionID for stable display order.
func sortSessions(sessions []ctr.ProxySession) {
	slices.SortFunc(sessions, func(a, b ctr.ProxySession) int {
		return cmp.Compare(a.SessionID, b.SessionID)
	})
}

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

// sessionPollResultMsg is returned by async session list polling.
type sessionPollResultMsg struct {
	sessions []ctr.ProxySession
	err      error
}

// sessionScreen implements tui.Screen for selecting a proxy session.
type sessionScreen struct {
	tui.Cursor
	sessions      []ctr.ProxySession
	selected      *SessionInfo
	onSelect      func(*SessionInfo) (tui.Screen, tea.Cmd)
	pollSessions  func() ([]ctr.ProxySession, error)
	pollInFlight  bool
	firstTickSeen bool
	firstPollDone bool
	lastPollErr   string
}

// sessionErrorMsg is sent when the onSelect callback fails.
type sessionErrorMsg struct{ err error }

// sessionSelectResultMsg is returned when async session selection work completes.
type sessionSelectResultMsg struct {
	selected *SessionInfo
	screen   tui.Screen
	cmd      tea.Cmd
}

func newSessionScreen(sessions []ctr.ProxySession, onSelect func(*SessionInfo) (tui.Screen, tea.Cmd), pollSessions func() ([]ctr.ProxySession, error)) *sessionScreen {
	sortSessions(sessions)
	return &sessionScreen{
		Cursor:        tui.Cursor{ItemCount: len(sessions)},
		sessions:      sessions,
		onSelect:      onSelect,
		pollSessions:  pollSessions,
		firstPollDone: len(sessions) > 0,
	}
}

func (s *sessionScreen) pollSessionsCmd() tea.Cmd {
	return func() tea.Msg {
		sessions, err := s.pollSessions()
		return sessionPollResultMsg{sessions: sessions, err: err}
	}
}

func (s *sessionScreen) Update(msg tea.Msg, w *tui.Window) (tui.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case sessionPollResultMsg:
		s.pollInFlight = false
		if msg.err != nil {
			errStr := msg.err.Error()
			if errStr != s.lastPollErr {
				s.lastPollErr = errStr
				w.SetError(msg.err)
			}
			break
		}
		s.firstPollDone = true
		s.lastPollErr = ""
		w.ClearError()
		sortSessions(msg.sessions)
		s.sessions = msg.sessions
		s.ItemCount = len(s.sessions)
		if s.Pos >= s.ItemCount {
			s.Pos = max(s.ItemCount-1, 0)
		}
		s.EnsureVisible()

	case tui.TickMsg:
		if s.pollSessions != nil && (w.IntervalElapsed(time.Second) || !s.firstTickSeen) && !s.pollInFlight {
			s.firstTickSeen = true
			s.pollInFlight = true
			return s, s.pollSessionsCmd()
		}
		s.firstTickSeen = true

	case tea.KeyPressMsg:
		switch msg.String() {
		case "enter":
			if s.Pos >= 0 && s.Pos < len(s.sessions) {
				selected := sessionInfoFromProxy(s.sessions[s.Pos])
				s.selected = selected
				if s.onSelect != nil {
					return s, func() tea.Msg {
						screen, cmd := s.onSelect(selected)
						return sessionSelectResultMsg{
							selected: selected,
							screen:   screen,
							cmd:      cmd,
						}
					}
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

	case sessionSelectResultMsg:
		if msg.selected != nil {
			s.selected = msg.selected
		}
		if msg.screen != nil {
			w.SetHeader(&tui.HeaderInfo{
				ProjectDir: s.selected.ProjectDir,
				SessionID:  s.selected.SessionID,
			})
			return msg.screen, msg.cmd
		}
		return s, msg.cmd

	case tea.WindowSizeMsg:
		s.VpHeight = w.VpHeight()
		s.EnsureVisible()
	}

	return s, nil
}

func (s *sessionScreen) View(w *tui.Window) string {
	var lines []string

	if len(s.sessions) == 0 && s.firstPollDone {
		note := lipgloss.NewStyle().Foreground(tui.ColorField).
			Render("No sessions running. Waiting...")
		lines = append(lines, note)
	} else if len(s.sessions) > 0 {
		note := lipgloss.NewStyle().Foreground(tui.ColorField).
			Render("Select a session:")
		lines = append(lines, note, "")

		headerLines := 2 // note + blank line
		end := s.Offset + s.VpHeight - headerLines
		end = min(end, len(s.sessions))
		now := time.Now()
		for i := s.Offset; i < end; i++ {
			lines = append(lines, renderSessionLine(s.sessions[i], i == s.Pos, now))
		}
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
	var keys []tui.FooterKey
	if len(s.sessions) > 0 {
		keys = append(keys, tui.FooterKey{Key: "enter", Desc: "select"})
	}
	keys = append(keys, s.Cursor.FooterKeys()...)
	return keys
}

func (s *sessionScreen) FooterStatus(w *tui.Window) string {
	if len(s.sessions) == 0 {
		return "waiting"
	}
	return fmt.Sprintf("%d sessions", len(s.sessions))
}

// selectSession runs a temporary TUI for the user to pick a session.
func selectSession(sessions []ctr.ProxySession) (*SessionInfo, error) {
	s := newSessionScreen(sessions, nil, nil)
	header := selectorHeader()
	w := tui.NewWindow(header, s)
	p := tea.NewProgram(w)
	if _, err := p.Run(); err != nil {
		return nil, fmt.Errorf("session selector: %w", err)
	}
	if s.selected == nil {
		return nil, fmt.Errorf("no session selected")
	}
	return s.selected, nil
}
