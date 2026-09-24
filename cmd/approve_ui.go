package cmd

import (
	"fmt"
	"strings"
	"time"

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
	confirmSave   bool // A was pressed, waiting for y to save for good
	checkInFlight bool
}

// approveQuiet and approveSettle define a deliberate key press, the only
// input the prompt takes: a single key with no other key approveQuiet before
// and approveSettle after it. The sandbox decides when a prompt appears, and
// the user may be typing into the agent at that moment: a stray a would
// allow, a stray A would even save the rule. approveSettle exceeds common
// key repeat delays (X11 defaults to 660ms), so a held key repeats before
// it settles.
const (
	approveQuiet  = 400 * time.Millisecond
	approveSettle = 700 * time.Millisecond
)

// newApproveModel builds the prompt program model for entry, behind the
// key gate. Both the inline and the kitty prompter show it.
func newApproveModel(session *SessionInfo, client *ControlClient, entry proxy.LogEntry) tea.Model {
	header := &tui.HeaderInfo{ProjectDir: session.ProjectDir, SessionID: session.SessionID}
	window := tui.NewWindow(header, newApproveScreen(session, client, entry))
	return tui.NewKeyGate(window, isApproveKey, approveQuiet, approveSettle)
}

// isApproveKey reports whether k answers the prompt: allow, allow and save,
// deny, or dismiss.
func isApproveKey(k tea.KeyPressMsg) bool {
	switch k.String() {
	case "a", "A", "n", "q", "esc", "ctrl+c":
		return true
	}
	return false
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
		if s.confirmSave {
			switch msg.String() {
			case "y":
				s.confirmSave = false
				return s.decide(w, s.allowCmd(true))
			case "q", "ctrl+c":
				return s, tea.Quit
			default:
				// Anything else backs out to the question.
				s.confirmSave = false
			}
			return s, nil
		}
		switch msg.String() {
		case "a":
			return s.decide(w, s.allowCmd(false))
		case "A":
			// Saving outlives the session: ask once more.
			s.confirmSave = true
			return s, nil
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
	if s.confirmSave {
		lines[len(lines)-1] = fmt.Sprintf("  Allow and save %s to the project config?", value.Render(target))
	}
	if s.busy {
		lines = append(lines, "", "  applying...")
	}
	return strings.Join(lines, "\n")
}

func (s *approveScreen) FooterStatus(w *tui.Window) string { return "" }

func (s *approveScreen) FooterKeys(w *tui.Window) []tui.FooterKey {
	if s.confirmSave {
		return []tui.FooterKey{
			{Key: "y", Desc: "save"},
			{Key: "any", Desc: "back"},
		}
	}
	return []tui.FooterKey{
		{Key: "a", Desc: "allow"},
		{Key: "A", Desc: "allow+save"},
		{Key: "n", Desc: "deny"},
		{Key: "esc", Desc: "dismiss"},
	}
}
