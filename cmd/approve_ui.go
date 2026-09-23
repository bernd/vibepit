package cmd

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
)

// approveScreen is a single-question prompt shown in a terminal overlay when
// the proxy blocks a request: allow for the session, allow and save, or deny.
type approveScreen struct {
	session       *SessionInfo
	client        *ControlClient
	entry         proxy.LogEntry
	busy          bool
	failed        bool // an allow attempt errored; keep the prompt until the user acts
	checkInFlight bool
	firstTickSeen bool
}

// checkResultMsg carries the result of polling whether the target has been
// decided by someone else, e.g. another client attached to the same session.
type checkResultMsg struct {
	res CheckResult
	err error
}

// denyResultMsg is returned once the deny was recorded in the proxy.
type denyResultMsg struct {
	err error
}

func newApproveScreen(session *SessionInfo, client *ControlClient, entry proxy.LogEntry) *approveScreen {
	return &approveScreen{session: session, client: client, entry: entry}
}

func (s *approveScreen) allowCmd(save bool) tea.Cmd {
	return func() tea.Msg {
		status, err := allowEntry(s.client, s.session, s.entry, save)
		return allowResultMsg{index: -1, status: status, err: err}
	}
}

func (s *approveScreen) checkCmd() tea.Cmd {
	s.checkInFlight = true
	return func() tea.Msg {
		res, err := s.client.Check(s.entry)
		return checkResultMsg{res: res, err: err}
	}
}

func (s *approveScreen) denyCmd() tea.Cmd {
	return func() tea.Msg {
		return denyResultMsg{err: s.client.Deny(s.entry)}
	}
}

func (s *approveScreen) Update(msg tea.Msg, w *tui.Window) (tui.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if s.busy {
			return s, nil
		}
		switch msg.String() {
		case "a", "A":
			s.busy = true
			s.failed = false
			w.ClearError()
			return s, s.allowCmd(msg.String() == "A")
		case "n":
			s.busy = true
			s.failed = false
			w.ClearError()
			return s, s.denyCmd()
		case "esc", "q", "ctrl+c":
			// Dismiss only: no decision is recorded, other clients keep asking.
			return s, tea.Quit
		}

	case tui.TickMsg:
		first := !s.firstTickSeen
		s.firstTickSeen = true
		if s.busy || s.failed || s.checkInFlight || s.client == nil {
			return s, nil
		}
		if first || w.IntervalElapsed(pollInterval) {
			return s, s.checkCmd()
		}

	case checkResultMsg:
		s.checkInFlight = false
		// Errors are ignored: an older proxy without /check, or a transient
		// failure, must not take the prompt away from the user. A failed
		// allow+save leaves the target in the live allowlist, so a pending
		// error must also block auto-close or it would vanish unseen.
		if msg.err == nil && msg.res.Decided() && !s.busy && !s.failed {
			return s, tea.Quit
		}

	case denyResultMsg:
		s.busy = false
		if msg.err != nil {
			s.failed = true
			w.SetError(msg.err)
			return s, nil
		}
		return s, tea.Quit

	case allowResultMsg:
		s.busy = false
		if msg.err != nil {
			s.failed = true
			w.SetError(msg.err)
			return s, nil
		}
		return s, tea.Quit
	}
	return s, nil
}

func (s *approveScreen) View(w *tui.Window) string {
	target := sanitizeText(allowValueForEntry(s.entry))
	reason := sanitizeText(s.entry.Reason)
	label := lipgloss.NewStyle().Foreground(tui.ColorField)
	value := lipgloss.NewStyle().Bold(true)
	blocked := lipgloss.NewStyle().Foreground(tui.ColorError).Bold(true).Render("blocked")

	lines := []string{
		"",
		fmt.Sprintf("  %s %s", blocked, value.Render(target)),
		"",
		fmt.Sprintf("  %s %s", label.Render("source:"), string(s.entry.Source)),
		fmt.Sprintf("  %s %s", label.Render("reason:"), reason),
		fmt.Sprintf("  %s %s", label.Render("time:  "), s.entry.Time.Format("15:04:05")),
		"",
		"  Allow this connection?",
	}
	if s.busy {
		lines = append(lines, "", "  applying...")
	}
	return strings.Join(lines, "\n")
}

func (s *approveScreen) FooterStatus(w *tui.Window) string { return "" }

func (s *approveScreen) FooterKeys(w *tui.Window) []tui.FooterKey {
	return []tui.FooterKey{
		{Key: "a", Desc: "allow"},
		{Key: "A", Desc: "allow+save"},
		{Key: "n", Desc: "deny"},
		{Key: "esc", Desc: "dismiss"},
	}
}
