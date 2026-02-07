package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/bernd/vibepit/config"
	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// allowStatus tracks whether a log entry has been temporarily or permanently
// added to the allow list.
type allowStatus int

const (
	statusNone  allowStatus = iota
	statusTemp              // temporarily allowed this session
	statusSaved             // saved to persistent allow list
)

const pollInterval = time.Second

// logItem wraps a proxy log entry with its allow-list status.
type logItem struct {
	entry  proxy.LogEntry
	status allowStatus
}

// monitorScreen implements tui.Screen for the log monitor.
type monitorScreen struct {
	session       *SessionInfo
	client        *ControlClient
	cursor        tui.Cursor
	pollCursor    uint64
	items         []logItem
	newCount      int
	firstTickSeen bool
}

func newMonitorScreen(session *SessionInfo, client *ControlClient) *monitorScreen {
	return &monitorScreen{
		session: session,
		client:  client,
	}
}

// allowResultMsg is returned by the async allow command.
type allowResultMsg struct {
	index  int
	status allowStatus
	err    error
}

func (s *monitorScreen) allowCmd(index int, domain string, save bool) tea.Cmd {
	return func() tea.Msg {
		_, err := s.client.Allow([]string{domain})
		if err != nil {
			return allowResultMsg{index: index, err: err}
		}

		status := statusTemp
		if save {
			status = statusSaved
			projectPath := config.DefaultProjectPath(s.session.ProjectDir)
			if err := config.AppendAllow(projectPath, []string{domain}); err != nil {
				return allowResultMsg{index: index, err: err}
			}
		}
		return allowResultMsg{index: index, status: status}
	}
}

func (s *monitorScreen) Update(msg tea.Msg, w *tui.Window) (tui.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "a", "A":
			if s.cursor.Pos >= 0 && s.cursor.Pos < len(s.items) {
				item := s.items[s.cursor.Pos]
				if item.entry.Action == proxy.ActionBlock && item.status == statusNone {
					return s, s.allowCmd(s.cursor.Pos, item.entry.Domain, msg.String() == "A")
				}
				w.SetFlash("already allowed")
			}
		case "q", "ctrl+c":
			return s, tea.Quit
		default:
			oldAtEnd := s.cursor.AtEnd()
			if s.cursor.HandleKey(msg) {
				if !oldAtEnd && s.cursor.AtEnd() {
					s.newCount = 0
				}
				return s, nil
			}
		}

	case tea.WindowSizeMsg:
		s.cursor.VpHeight = w.VpHeight()
		if len(s.items) <= s.cursor.Offset+s.cursor.VpHeight {
			s.newCount = 0
		}
		s.cursor.EnsureVisible()

	case allowResultMsg:
		if msg.err != nil {
			w.SetError(msg.err)
		} else if msg.index >= 0 && msg.index < len(s.items) {
			s.items[msg.index].status = msg.status
			domain := s.items[msg.index].entry.Domain
			switch msg.status {
			case statusTemp:
				w.SetFlash(fmt.Sprintf("allowed %s", domain))
			case statusSaved:
				w.SetFlash(fmt.Sprintf("allowed and saved %s", domain))
			case statusNone:
				// ignored
			}
		}

	case tui.TickMsg:
		if w.IntervalElapsed(pollInterval) || !s.firstTickSeen {
			entries, err := s.client.LogsAfter(s.pollCursor)
			if err != nil {
				w.SetError(err)
			} else {
				w.ClearError()
				wasAtEnd := len(s.items) == 0 || s.cursor.AtEnd()
				for _, e := range entries {
					s.items = append(s.items, logItem{entry: e})
					s.pollCursor = e.ID
				}
				s.cursor.ItemCount = len(s.items)
				if !wasAtEnd && len(entries) > 0 && len(s.items) > s.cursor.Offset+s.cursor.VpHeight {
					s.newCount += len(entries)
				}
				if wasAtEnd && len(s.items) > 0 {
					s.cursor.Pos = len(s.items) - 1
					s.newCount = 0
					s.cursor.EnsureVisible()
				}
			}
		}
		s.firstTickSeen = true
	}

	return s, nil
}

func (s *monitorScreen) View(w *tui.Window) string {
	var logLines []string
	end := s.cursor.Offset + s.cursor.VpHeight
	if end > len(s.items) {
		end = len(s.items)
	}
	for i := s.cursor.Offset; i < end; i++ {
		logLines = append(logLines, renderLogLine(s.items[i], i == s.cursor.Pos))
	}
	for len(logLines) < s.cursor.VpHeight {
		logLines = append(logLines, "")
	}
	return strings.Join(logLines, "\n")
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (s *monitorScreen) FooterStatus(w *tui.Window) string {
	isTailing := len(s.items) == 0 || s.cursor.AtEnd()
	var indicator string
	if isTailing {
		glyph := spinnerFrames[w.TickFrame()%len(spinnerFrames)]
		indicator = lipgloss.NewStyle().Foreground(tui.ColorCyan).Render(glyph)
	} else {
		indicator = lipgloss.NewStyle().Foreground(tui.ColorField).Render("⠿")
	}

	if s.newCount > 0 {
		newMsg := lipgloss.NewStyle().Foreground(tui.ColorOrange).
			Render(fmt.Sprintf("↓ %d new", s.newCount))
		return indicator + " " + newMsg
	}
	return indicator
}

func (s *monitorScreen) FooterKeys(w *tui.Window) []tui.FooterKey {
	var keys []tui.FooterKey

	if s.cursor.Pos >= 0 && s.cursor.Pos < len(s.items) {
		item := s.items[s.cursor.Pos]
		switch {
		case item.entry.Action == proxy.ActionBlock && item.status == statusNone:
			keys = append(keys,
				tui.FooterKey{Key: "a", Desc: "allow"},
				tui.FooterKey{Key: "A", Desc: "allow+save"},
			)
		case item.status == statusTemp:
			keys = append(keys,
				tui.FooterKey{Key: "A", Desc: "save"},
			)
		}
	}

	keys = append(keys, s.cursor.FooterKeys()...)
	return keys
}

func renderLogLine(item logItem, highlighted bool) string {
	e := item.entry
	base, marker := tui.LineStyle(highlighted)

	var symbol string
	var sourceColor lipgloss.Color
	switch {
	case item.status == statusTemp:
		symbol = base.Foreground(tui.ColorOrange).Render("a")
		sourceColor = tui.ColorOrange
	case item.status == statusSaved:
		symbol = base.Foreground(tui.ColorOrange).Bold(true).Render("A")
		sourceColor = tui.ColorOrange
	case e.Action == proxy.ActionBlock:
		symbol = base.Foreground(tui.ColorError).Render("x")
		sourceColor = tui.ColorError
	default:
		symbol = base.Foreground(tui.ColorCyan).Render("+")
		sourceColor = tui.ColorCyan
	}
	host := e.Domain
	if e.Port != "" {
		host = e.Domain + ":" + e.Port
	}
	ts := base.Foreground(tui.ColorField).Render(e.Time.Format("15:04:05"))
	src := base.Foreground(sourceColor).Render(fmt.Sprintf("%-5s", string(e.Source)))
	hostStr := base.Render(host)
	reasonStr := base.Render(e.Reason)
	sp := base.Render(" ")
	return marker + base.Render("[") + ts + base.Render("]") + sp + symbol + sp + src + sp + hostStr + sp + reasonStr
}
