package sshd

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/bernd/vibepit/session"
	"github.com/bernd/vibepit/tui"
)

// selectorResult holds the outcome of the session selector.
type selectorResult struct {
	sessionID string // empty means "new session"
}

// selectorScreen implements tui.Screen for choosing an SSH session to attach to.
type selectorScreen struct {
	tui.Cursor
	sessions []session.SessionInfo
	result   *selectorResult
}

func newSelectorScreen(sessions []session.SessionInfo) *selectorScreen {
	return &selectorScreen{
		Cursor:   tui.Cursor{ItemCount: len(sessions) + 1}, // +1 for "new session"
		sessions: sessions,
	}
}

func (s *selectorScreen) Update(msg tea.Msg, w *tui.Window) (tui.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return s, tea.Quit
		case "n":
			s.result = &selectorResult{}
			return s, tea.Quit
		case "enter":
			if s.Pos < len(s.sessions) {
				s.result = &selectorResult{sessionID: s.sessions[s.Pos].ID}
			} else {
				s.result = &selectorResult{}
			}
			return s, tea.Quit
		default:
			s.HandleKey(msg)
		}
	case tea.WindowSizeMsg:
		s.VpHeight = w.VpHeight()
		s.EnsureVisible()
	}
	return s, nil
}

func (s *selectorScreen) View(w *tui.Window) string {
	var lines []string

	note := lipgloss.NewStyle().Foreground(tui.ColorField).
		Render("Select a session:")
	lines = append(lines, note, "")

	headerLines := 2 // note + blank line
	end := s.Offset + s.VpHeight - headerLines
	end = min(end, s.ItemCount)
	now := time.Now()

	for i := s.Offset; i < end; i++ {
		if i < len(s.sessions) {
			lines = append(lines, renderSelectorLine(s.sessions[i], i == s.Pos, now))
		} else {
			lines = append(lines, renderNewSessionLine(i == s.Pos))
		}
	}

	for len(lines) < s.VpHeight {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func renderSelectorLine(info session.SessionInfo, highlighted bool, now time.Time) string {
	base, marker := tui.LineStyle(highlighted)

	id := base.Foreground(tui.ColorField).Render(fmt.Sprintf("%-16s", info.ID))
	age := base.Foreground(tui.ColorOrange).Render(fmt.Sprintf("%-8s", formatDuration(now.Sub(info.CreatedAt))))
	detached := base.Foreground(tui.ColorCyan).Render(fmt.Sprintf("detached %s ago", formatDuration(now.Sub(info.DetachedAt))))
	sp := base.Render(" ")

	return marker + id + sp + age + sp + detached
}

func renderNewSessionLine(highlighted bool) string {
	base, marker := tui.LineStyle(highlighted)
	label := base.Foreground(tui.ColorField).Render("[new session]")
	return marker + label
}

// formatDuration formats a duration as a compact human-readable string.
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", max(int(d.Seconds()), 0))
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

func (s *selectorScreen) FooterKeys(w *tui.Window) []tui.FooterKey {
	keys := []tui.FooterKey{
		{Key: "enter", Desc: "select"},
		{Key: "n", Desc: "new session"},
	}
	keys = append(keys, s.Cursor.FooterKeys()...)
	return keys
}

func (s *selectorScreen) FooterStatus(w *tui.Window) string {
	return fmt.Sprintf("%d sessions", len(s.sessions))
}
