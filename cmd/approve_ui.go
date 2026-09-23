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
	busy          bool // a decision is being applied
	checkInFlight bool
}

// checkResultMsg carries the result of polling whether the target has been
// decided by someone else, e.g. another client attached to the same session.
type checkResultMsg struct {
	res CheckResult
	err error
}

// decisionResultMsg reports whether the user's allow or deny was applied.
type decisionResultMsg struct {
	err error
}

func newApproveScreen(session *SessionInfo, client *ControlClient, entry proxy.LogEntry) *approveScreen {
	return &approveScreen{session: session, client: client, entry: entry}
}

func (s *approveScreen) allowCmd(save bool) tea.Cmd {
	return func() tea.Msg {
		_, err := allowEntry(s.client, s.session, s.entry, save)
		return decisionResultMsg{err: err}
	}
}

func (s *approveScreen) denyCmd() tea.Cmd {
	return func() tea.Msg {
		return decisionResultMsg{err: s.client.Deny(s.entry)}
	}
}

func (s *approveScreen) checkCmd() tea.Cmd {
	s.checkInFlight = true
	return func() tea.Msg {
		res, err := s.client.Check(s.entry)
		return checkResultMsg{res: res, err: err}
	}
}

func (s *approveScreen) decide(w *tui.Window, cmd tea.Cmd) (tui.Screen, tea.Cmd) {
	s.busy = true
	w.ClearError()
	return s, cmd
}

// Update keeps the prompt open while an error from the user's own decision
// is shown. A failed allow+save leaves the target in the live allowlist, so
// the decided-elsewhere poll would otherwise close the prompt and hide it.
func (s *approveScreen) Update(msg tea.Msg, w *tui.Window) (tui.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if s.busy {
			return s, nil
		}
		switch msg.String() {
		case "a", "A":
			return s.decide(w, s.allowCmd(msg.String() == "A"))
		case "n":
			return s.decide(w, s.denyCmd())
		case "esc", "q", "ctrl+c":
			// Dismiss only: no decision is recorded, other clients keep asking.
			return s, tea.Quit
		}

	case tui.TickMsg:
		if s.busy || w.Err() != nil || s.checkInFlight || s.client == nil {
			return s, nil
		}
		// Check right away on open, then once per poll interval.
		if w.TickFrame() <= 1 || w.IntervalElapsed(pollInterval) {
			return s, s.checkCmd()
		}

	case checkResultMsg:
		s.checkInFlight = false
		// Errors are ignored: an older proxy without /check, or a transient
		// failure, must not take the prompt away from the user.
		if msg.err == nil && msg.res.Decided() && !s.busy && w.Err() == nil {
			return s, tea.Quit
		}

	case decisionResultMsg:
		s.busy = false
		if msg.err != nil {
			w.SetError(msg.err)
			return s, nil
		}
		return s, tea.Quit
	}
	return s, nil
}

func (s *approveScreen) View(w *tui.Window) string {
	target := tui.SanitizeText(s.entry.Target().String())
	reason := tui.SanitizeText(s.entry.Reason)
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
