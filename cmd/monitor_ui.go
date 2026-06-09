package cmd

import (
	"context"
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

var disconnectGraceTicks = int(disconnectGracePeriod / tui.TickInterval)

// logItem wraps a proxy log entry with its allow-list status.
type logItem struct {
	entry  proxy.LogEntry
	status allowStatus
}

// monitorScreen implements tui.Screen for the log monitor. It renders log
// entries and connection status delivered by the client's WatchLogs stream; the
// client owns the subscription lifecycle (open, retry, reconnect-resubscribe),
// so the screen holds no consumer state beyond the event channel and its cancel.
type monitorScreen struct {
	session *SessionInfo
	client  *ControlClient
	onBack  func() tui.Screen
	cursor  tui.Cursor

	// events is the WatchLogs stream; cancelWatch tears it down on teardown.
	events      <-chan LogEvent
	cancelWatch context.CancelFunc

	items          []logItem
	newCount       int
	firstTickSeen  bool
	disconnectTick int  // -1 = connected, 0+ = ticks since disconnect
	connDown       bool // last observed connection state (set by LogConnEvent)
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

// logEventMsg carries the next item from the WatchLogs stream. ok is false when
// the stream channel has closed (the watch was torn down), so the reader stops
// re-arming instead of spinning on a closed channel.
type logEventMsg struct {
	ev LogEvent
	ok bool
}

// waitForEvent blocks on the WatchLogs stream and yields the next event,
// re-armed after each one. A closed channel yields ok=false so the read loop
// stops. The client owns reconnect/retry, so the screen never has to manage the
// subscription itself.
func waitForEvent(ch <-chan LogEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		return logEventMsg{ev: ev, ok: ok}
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

// startWatch opens the WatchLogs stream and arms the first read. The stream is
// canceled (and its channel closed) by transitionBack via cancelWatch.
func (s *monitorScreen) startWatch() tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancelWatch = cancel
	s.events = s.client.WatchLogs(ctx)
	return waitForEvent(s.events)
}

func (s *monitorScreen) transitionBack(w *tui.Window) tui.Screen {
	if s.cancelWatch != nil {
		s.cancelWatch()
		s.cancelWatch = nil
	}
	if s.client != nil {
		s.client.Close()
	}
	w.SetHeader(selectorHeader())
	return s.onBack()
}

// appendEntry adds a delivered log entry to the buffer, advancing the cursor and
// the "new" count according to whether the view is tailing the end.
func (s *monitorScreen) appendEntry(entry proxy.LogEntry) {
	// Connection state is driven solely by LogConnEvent, never by log delivery: an
	// entry may be buffered or replayed from history and arrive after the proxy
	// died, so it must not be treated as proof the session is healthy.
	wasAtEnd := len(s.items) == 0 || s.cursor.AtEnd()
	s.items = append(s.items, logItem{entry: entry})
	s.cursor.ItemCount = len(s.items)
	if !wasAtEnd && len(s.items) > s.cursor.Offset+s.cursor.VpHeight {
		s.newCount++
	}
	if wasAtEnd {
		s.cursor.Pos = len(s.items) - 1
		s.newCount = 0
		s.cursor.EnsureVisible()
	}
}

// handleConn applies a connection-state change. On disconnect it surfaces an
// error and, in session mode, arms the grace timer that returns to the selector;
// on reconnect it clears the error and disarms the timer. Resubscription is the
// client's job (WatchLogs), so there is nothing to restart here.
func (s *monitorScreen) handleConn(connected bool, w *tui.Window) {
	if connected {
		s.connDown = false
		s.disconnectTick = -1
		w.ClearError()
		return
	}
	s.connDown = true
	if s.onBack != nil && s.disconnectTick < 0 {
		s.disconnectTick = 0
	}
	w.SetError(fmt.Errorf("session %s disconnected", s.session.SessionID))
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

	case logEventMsg:
		if !msg.ok {
			return s, nil // watch stream closed (teardown); stop reading
		}
		switch msg.ev.Kind {
		case LogConnEvent:
			s.handleConn(msg.ev.Connected, w)
		case LogEntryEvent:
			s.appendEntry(msg.ev.Entry)
		}
		return s, waitForEvent(s.events)

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
				return s, s.startWatch()
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
