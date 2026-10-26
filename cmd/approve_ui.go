package cmd

import (
	"fmt"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
)

// approveScreen is a prompt shown over the session while the proxy blocks or
// holds requests. It works through a queue of targets: the keys act on the
// first one (allow for the session, allow and save, or deny), the others are
// listed underneath.
type approveScreen struct {
	session       *SessionInfo
	client        *ControlClient
	queue         *promptQueue
	deciding      proxy.Target // the target the in-flight decision applies to
	busy          bool         // a decision is being applied
	checkInFlight bool
}

// checkResultMsg carries the targets that someone else has decided meanwhile,
// e.g. another client attached to the same session.
type checkResultMsg struct {
	decided []proxy.Target
	err     error
}

// decisionResultMsg reports whether the user's allow or deny was applied.
type decisionResultMsg struct {
	err error
}

func newApproveScreen(session *SessionInfo, client *ControlClient, q *promptQueue) *approveScreen {
	return &approveScreen{session: session, client: client, queue: q}
}

// The commands capture the entry when the key is pressed: the queue may move
// on before they run.
func (s *approveScreen) allowCmd(entry proxy.LogEntry, save bool) tea.Cmd {
	return func() tea.Msg {
		_, err := allowEntry(s.client, s.session, entry, save)
		return decisionResultMsg{err: err}
	}
}

func (s *approveScreen) denyCmd(entry proxy.LogEntry) tea.Cmd {
	return func() tea.Msg {
		return decisionResultMsg{err: s.client.Deny(entry)}
	}
}

// checkCmd asks the proxy about every queued target. Allows and denies of
// targets without an open hold produce no event, so polling is the only way
// to notice them.
func (s *approveScreen) checkCmd() tea.Cmd {
	s.checkInFlight = true
	items := s.queue.Items()
	return func() tea.Msg {
		var msg checkResultMsg
		for _, it := range items {
			res, err := s.client.Check(it.Entry)
			if err != nil {
				msg.err = err
				continue
			}
			if res.Decided() {
				msg.decided = append(msg.decided, it.Entry.Target())
			}
		}
		return msg
	}
}

func (s *approveScreen) decide(w *tui.Window, entry proxy.LogEntry, cmd tea.Cmd) (tui.Screen, tea.Cmd) {
	s.busy = true
	s.deciding = entry.Target()
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
		items := s.queue.Items()
		switch msg.String() {
		case "a", "A":
			if len(items) > 0 {
				return s.decide(w, items[0].Entry, s.allowCmd(items[0].Entry, msg.String() == "A"))
			}
		case "n":
			if len(items) > 0 {
				return s.decide(w, items[0].Entry, s.denyCmd(items[0].Entry))
			}
		case "esc", "q", "ctrl+c":
			// Dismiss only: no decision is recorded, other clients keep asking.
			return s, tea.Quit
		}

	case tui.TickMsg:
		// The queue may have been emptied elsewhere, e.g. by the event stream.
		if s.queue.Len() == 0 && !s.busy && w.Err() == nil {
			return s, tea.Quit
		}
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
		if !s.busy && w.Err() == nil {
			for _, t := range msg.decided {
				s.queue.Remove(t)
			}
			if s.queue.Len() == 0 {
				return s, tea.Quit
			}
		}

	case decisionResultMsg:
		s.busy = false
		if msg.err != nil {
			w.SetError(msg.err)
			return s, nil
		}
		s.queue.Remove(s.deciding)
		if s.queue.Len() == 0 {
			return s, tea.Quit
		}
	}
	return s, nil
}

func (s *approveScreen) View(w *tui.Window) string {
	items := s.queue.Items()
	if len(items) == 0 {
		return ""
	}
	now := w.Now()
	first := items[0]
	entry := first.Entry
	target := tui.SanitizeText(entry.Target().String())
	reason := tui.SanitizeText(entry.Reason)
	label := lipgloss.NewStyle().Foreground(tui.ColorField)
	value := lipgloss.NewStyle().Bold(true)

	title := lipgloss.NewStyle().Foreground(tui.ColorError).Bold(true).Render("blocked")
	lines := []string{""}
	if first.Held(now) {
		title = lipgloss.NewStyle().Foreground(tui.ColorYellow).Bold(true).Render("held")
		lines = append(lines,
			fmt.Sprintf("  %s %s", title, value.Render(target)),
			"  the agent is waiting · "+fmt.Sprintf("%ds left", secondsLeft(first.Deadline, now)),
		)
	} else {
		lines = append(lines, fmt.Sprintf("  %s %s", title, value.Render(target)))
	}
	lines = append(lines,
		"",
		fmt.Sprintf("  %s %s", label.Render("source:"), string(entry.Source)),
		fmt.Sprintf("  %s %s", label.Render("reason:"), reason),
		fmt.Sprintf("  %s %s", label.Render("time:  "), entry.Time.Format("15:04:05")),
		"",
		"  Allow this connection?",
	)
	if s.busy {
		lines = append(lines, "", "  applying...")
	}
	lines = append(lines, s.othersView(items[1:], now, w.VpHeight()-len(lines))...)
	return strings.Join(lines, "\n")
}

// secondsLeft rounds up so the countdown shows 1s until the deadline passes.
func secondsLeft(deadline, now time.Time) int {
	return int(math.Ceil(deadline.Sub(now).Seconds()))
}

// othersView lists the queued targets behind the first one in the room left
// on screen; what doesn't fit is summarized.
func (s *approveScreen) othersView(rest []promptItem, now time.Time, room int) []string {
	if len(rest) == 0 {
		return nil
	}
	lines := []string{"", lipgloss.NewStyle().Foreground(tui.ColorField).Render(fmt.Sprintf("  also waiting (%d)", len(rest)))}
	room -= len(lines)
	shown := len(rest)
	if shown > room {
		shown = max(room-1, 0)
	}
	held := lipgloss.NewStyle().Foreground(tui.ColorYellow)
	blocked := lipgloss.NewStyle().Foreground(tui.ColorError)
	for _, it := range rest[:shown] {
		target := tui.SanitizeText(it.Entry.Target().String())
		if it.Held(now) {
			lines = append(lines, held.Render(fmt.Sprintf("  ? %s  held %ds", target, secondsLeft(it.Deadline, now))))
		} else {
			lines = append(lines, blocked.Render(fmt.Sprintf("  x %s  blocked", target)))
		}
	}
	if shown < len(rest) {
		lines = append(lines, fmt.Sprintf("  … %d more", len(rest)-shown))
	}
	return lines
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
