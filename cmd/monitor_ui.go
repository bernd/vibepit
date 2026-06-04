package cmd

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/bernd/vibepit/config"
	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
)

// allowStatus tracks whether a log entry has been temporarily or permanently
// added to the allow list.
type allowStatus int

const (
	statusNone  allowStatus = iota
	statusTemp              // temporarily allowed this session
	statusSaved             // saved to persistent allow list
)

const disconnectGracePeriod = 3 * time.Second

// logChannelBuffer is the receive-side window for log entries delivered from the
// bus subscription to the Bubble Tea loop.
const logChannelBuffer = 256

var disconnectGraceTicks = int(disconnectGracePeriod / tui.TickInterval)

// logItem wraps a proxy log entry with its allow-list status.
type logItem struct {
	entry  proxy.LogEntry
	status allowStatus
}

// monitorScreen implements tui.Screen for the log monitor.
type monitorScreen struct {
	session        *SessionInfo
	client         *ControlClient
	onBack         func() tui.Screen
	cursor         tui.Cursor
	logCh          chan proxy.LogEntry
	logsDone       <-chan struct{}
	stopLogs       func()
	items          []logItem
	newCount       int
	firstTickSeen  bool
	disconnectTick int // -1 = connected, 0+ = ticks since disconnect
}

func newMonitorScreen(session *SessionInfo, client *ControlClient, onBack func() tui.Screen) *monitorScreen {
	return &monitorScreen{
		session:        session,
		client:         client,
		onBack:         onBack,
		disconnectTick: -1,
	}
}

// allowResultMsg is returned by the async allow command.
type allowResultMsg struct {
	index  int
	status allowStatus
	err    error
}

// logEntryMsg carries a single log entry delivered by the subscription.
type logEntryMsg proxy.LogEntry

// subscribeErrMsg is returned when starting the log subscription fails.
type subscribeErrMsg struct{ err error }

// subscriptionStartedMsg confirms the log subscription is live and carries the
// stop function and done channel used to tear it down. It is delivered to the
// main Update goroutine so these are never written from a cmd goroutine.
type subscriptionStartedMsg struct {
	stop func()
	done <-chan struct{}
}

// waitForLog blocks on the subscription channel and yields the next entry. It
// also selects on done so the goroutine exits at teardown (returning a nil
// message, which Bubble Tea ignores) instead of leaking blocked on the channel.
func waitForLog(ch <-chan proxy.LogEntry, done <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		select {
		case e := <-ch:
			return logEntryMsg(e)
		case <-done:
			return nil
		}
	}
}

func allowValueForEntry(entry proxy.LogEntry) string {
	if entry.Source == proxy.SourceProxy && entry.Port != "" {
		return entry.Domain + ":" + entry.Port
	}
	return entry.Domain
}

func (s *monitorScreen) allowCmd(index int, entry proxy.LogEntry, save bool) tea.Cmd {
	return func() tea.Msg {
		value := allowValueForEntry(entry)

		var err error
		switch entry.Source {
		case proxy.SourceDNS:
			_, err = s.client.AllowDNS([]string{value})
		default:
			_, err = s.client.AllowHTTP([]string{value})
		}
		if err != nil {
			return allowResultMsg{index: index, err: err}
		}

		status := statusTemp
		if save {
			status = statusSaved
			projectPath := config.DefaultProjectPath(s.session.ProjectDir)
			switch entry.Source {
			case proxy.SourceDNS:
				err = config.AppendAllowDNS(projectPath, []string{value})
			default:
				err = config.AppendAllowHTTP(projectPath, []string{value})
			}
			if err != nil {
				return allowResultMsg{index: index, err: err}
			}
		}
		return allowResultMsg{index: index, status: status}
	}
}

func (s *monitorScreen) transitionBack(w *tui.Window) tui.Screen {
	if s.stopLogs != nil {
		s.stopLogs()
		s.stopLogs = nil
	}
	if s.client != nil {
		s.client.Close()
	}
	w.SetHeader(selectorHeader())
	return s.onBack()
}

// startSubscription opens the log subscription on a cmd goroutine and reports
// the result via a message. It must not mutate any screen field: the channel is
// created by Update on the main goroutine and passed in, and the stop function
// is returned through subscriptionStartedMsg so Update owns all field writes.
// Reading s.client here is safe because it is set before the screen runs and is
// never mutated.
func (s *monitorScreen) startSubscription(ch chan proxy.LogEntry) tea.Cmd {
	return func() tea.Msg {
		stop, done, err := s.client.SubscribeLogs(ch)
		if err != nil {
			return subscribeErrMsg{err: err}
		}
		return subscriptionStartedMsg{stop: stop, done: done}
	}
}

// connStatusMsg carries a connection-state change from the control client:
// connected=false on disconnect/close, connected=true on reconnect.
type connStatusMsg struct{ connected bool }

// watchConn blocks on the client's connection-event channel and yields the next
// state change, re-armed after each one. This is how the monitor learns the
// proxy died mid-session even while the NATS client reconnects silently.
func (s *monitorScreen) watchConn() tea.Cmd {
	ch := s.client.ConnEvents()
	return func() tea.Msg { return connStatusMsg{connected: <-ch} }
}

func (s *monitorScreen) Update(msg tea.Msg, w *tui.Window) (tui.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "a", "A":
			if s.cursor.Pos >= 0 && s.cursor.Pos < len(s.items) {
				item := s.items[s.cursor.Pos]
				if item.entry.Action == proxy.ActionBlock && item.status == statusNone {
					return s, s.allowCmd(s.cursor.Pos, item.entry, msg.String() == "A")
				}
				w.SetFlash("already allowed")
			}
		case "esc":
			if s.onBack != nil {
				return s.transitionBack(w), nil
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

	case subscriptionStartedMsg:
		s.stopLogs = msg.stop
		s.logsDone = msg.done
		return s, waitForLog(s.logCh, s.logsDone)

	case subscribeErrMsg:
		if s.onBack != nil {
			if s.disconnectTick < 0 {
				s.disconnectTick = 0
			}
			w.SetError(fmt.Errorf("session %s disconnected", s.session.SessionID))
		} else {
			w.SetError(msg.err)
		}

	case connStatusMsg:
		// Mid-session connection loss: arm the disconnect grace timer. On
		// reconnect, clear it (the ordered consumer resumes on the same channel).
		if msg.connected {
			s.disconnectTick = -1
			w.ClearError()
		} else if s.onBack != nil && s.disconnectTick < 0 {
			s.disconnectTick = 0
			w.SetError(fmt.Errorf("session %s disconnected", s.session.SessionID))
		}
		if s.client != nil {
			return s, s.watchConn()
		}

	case logEntryMsg:
		s.disconnectTick = -1
		w.ClearError()

		wasAtEnd := len(s.items) == 0 || s.cursor.AtEnd()
		s.items = append(s.items, logItem{entry: proxy.LogEntry(msg)})
		s.cursor.ItemCount = len(s.items)
		if !wasAtEnd && len(s.items) > s.cursor.Offset+s.cursor.VpHeight {
			s.newCount++
		}
		if wasAtEnd {
			s.cursor.Pos = len(s.items) - 1
			s.newCount = 0
			s.cursor.EnsureVisible()
		}
		return s, waitForLog(s.logCh, s.logsDone)

	case tui.TickMsg:
		if s.onBack != nil && s.disconnectTick >= 0 {
			s.disconnectTick++
			if s.disconnectTick >= disconnectGraceTicks {
				return s.transitionBack(w), nil
			}
			s.firstTickSeen = true
			return s, nil // Don't reconnect while disconnected.
		}

		if !s.firstTickSeen {
			s.firstTickSeen = true
			if s.client != nil {
				s.logCh = make(chan proxy.LogEntry, logChannelBuffer)
				return s, tea.Batch(s.startSubscription(s.logCh), s.watchConn())
			}
		}
	}

	return s, nil
}

func (s *monitorScreen) View(w *tui.Window) string {
	var logLines []string
	end := min(s.cursor.Offset+s.cursor.VpHeight, len(s.items))
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

	if s.onBack != nil {
		keys = append(keys, tui.FooterKey{Key: "esc", Desc: "sessions"})
	}

	keys = append(keys, s.cursor.FooterKeys()...)
	return keys
}

func renderLogLine(item logItem, highlighted bool) string {
	e := item.entry
	base, marker := tui.LineStyle(highlighted)

	var symbol string
	var sourceColor color.Color
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
