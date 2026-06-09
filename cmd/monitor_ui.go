package cmd

import (
	"fmt"
	"image/color"
	"strings"
	"sync"
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

// subscribeRetryDelay is how long to wait before retrying a failed log
// subscription.
const subscribeRetryDelay = time.Second

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
	session *SessionInfo
	client  *ControlClient
	onBack  func() tui.Screen
	cursor  tui.Cursor

	// stopFn tears down the active log subscription. It is produced
	// asynchronously by the startSubscription cmd goroutine and consumed by
	// transitionBack and resubscribe on the Update goroutine, so all accesses are
	// guarded by mu. torndown lets a late subscription clean itself up if the
	// screen was already left. subGen increments on every (re)subscribe; messages
	// tagged with an older generation are ignored so a superseded subscription
	// can't deliver entries or mutate state after it has been replaced.
	mu             sync.Mutex
	stopFn         func()
	torndown       bool
	subGen         int
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

// logEntryMsg carries a single log entry delivered by the subscription, tagged
// with the subscription generation and the channel/done it was read from so the
// reader can be re-armed on the same subscription (and dropped if superseded).
type logEntryMsg struct {
	gen   int
	ch    chan proxy.LogEntry
	done  <-chan struct{}
	entry proxy.LogEntry
}

// subscribeErrMsg is returned when starting the log subscription fails. gen is
// the subscription generation that failed.
type subscribeErrMsg struct {
	gen int
	err error
}

// subscriptionStartedMsg confirms the log subscription is live and carries the
// generation plus the channel/done used to arm the reader. The stop func is
// handed off via s.stopFn under s.mu (see startSubscription) rather than this
// message, so it is not lost if the screen is torn down before the message is
// processed.
type subscriptionStartedMsg struct {
	gen  int
	ch   chan proxy.LogEntry
	done <-chan struct{}
}

// retrySubscribeMsg fires after a delay to retry a failed subscription. gen is
// the generation in effect when the retry was scheduled; if a newer subscription
// has since superseded it, the retry is ignored.
type retrySubscribeMsg struct{ gen int }

// waitForLog blocks on the subscription channel and yields the next entry. It
// also selects on done so the goroutine exits at teardown (returning a nil
// message, which Bubble Tea ignores) instead of leaking blocked on the channel.
func waitForLog(gen int, ch chan proxy.LogEntry, done <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		select {
		case e := <-ch:
			return logEntryMsg{gen: gen, ch: ch, done: done, entry: e}
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
	// Mark torn down and take ownership of the stop func under the lock. If the
	// subscription goroutine hasn't reported back yet, stopFn is nil here and the
	// goroutine will stop itself when it sees torndown — so the consumer is never
	// leaked regardless of ordering.
	s.mu.Lock()
	s.torndown = true
	stop := s.stopFn
	s.stopFn = nil
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	if s.client != nil {
		s.client.Close()
	}
	w.SetHeader(selectorHeader())
	return s.onBack()
}

// startSubscription opens a log subscription for generation gen on a cmd
// goroutine. The stop func is stored in s.stopFn under s.mu (not returned via the
// message) so it survives the screen being torn down before this goroutine
// reports back. If torndown is set, or a newer subscription has already
// superseded this one (gen != subGen), this goroutine stops the subscription
// itself instead of leaking it. Reading s.client is safe because it is set
// before the screen runs and is never mutated.
func (s *monitorScreen) startSubscription(gen int, ch chan proxy.LogEntry) tea.Cmd {
	return func() tea.Msg {
		stop, done, err := s.client.SubscribeLogs(ch)
		if err != nil {
			return subscribeErrMsg{gen: gen, err: err}
		}
		s.mu.Lock()
		if s.torndown || gen != s.subGen {
			s.mu.Unlock()
			stop() // screen left, or a newer subscription supersedes this one
			return nil
		}
		s.stopFn = stop
		s.mu.Unlock()
		return subscriptionStartedMsg{gen: gen, ch: ch, done: done}
	}
}

// resubscribe tears down any active subscription and starts a fresh one against
// the current stream. It is used on first connect and on every reconnect: the
// proxy may have restarted with a brand-new in-memory stream whose sequence
// numbers reset to 1, so the existing ordered consumer's cursor would be stale
// and silently deliver nothing. A fresh subscription recreates the consumer with
// a cursor valid for the current stream.
func (s *monitorScreen) resubscribe() tea.Cmd {
	s.mu.Lock()
	old := s.stopFn
	s.stopFn = nil
	s.subGen++
	gen := s.subGen
	s.mu.Unlock()
	if old != nil {
		old()
	}
	ch := make(chan proxy.LogEntry, logChannelBuffer)
	return s.startSubscription(gen, ch)
}

// retrySubscribeCmd schedules a delayed resubscribe after a failed attempt so a
// transient JetStream API error recovers on its own instead of leaving the
// monitor permanently log-less.
func retrySubscribeCmd(gen int) tea.Cmd {
	return tea.Tick(subscribeRetryDelay, func(time.Time) tea.Msg {
		return retrySubscribeMsg{gen: gen}
	})
}

// connStatusMsg carries a connection-state change from the control client:
// connected=false on disconnect/close, connected=true on reconnect.
type connStatusMsg struct{ connected bool }

// watchConn blocks on the client's connection-event channel and yields the next
// state change, re-armed after each one. This is how the monitor learns the
// proxy died mid-session even while the NATS client reconnects silently. It also
// selects on the client's closed channel so the goroutine exits at teardown
// (returning a nil message, which Bubble Tea ignores) instead of blocking until
// process exit when the final close event is dropped by the lossy buffer.
func (s *monitorScreen) watchConn() tea.Cmd {
	ch := s.client.ConnEvents()
	done := s.client.Closed()
	return func() tea.Msg {
		select {
		case c := <-ch:
			return connStatusMsg{connected: c}
		case <-done:
			return nil
		}
	}
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
		if msg.gen != s.subGen {
			return s, nil // superseded by a newer subscription
		}
		// The subscription is live, so the connection and log flow are healthy:
		// clear any disconnect state armed by an earlier failure or retry.
		s.disconnectTick = -1
		w.ClearError()
		return s, waitForLog(msg.gen, msg.ch, msg.done)

	case subscribeErrMsg:
		if msg.gen != s.subGen {
			return s, nil // stale failure from a superseded subscription
		}
		// Arm the disconnect grace timer (session mode) so a persistently broken
		// session still returns to the selector, but also retry so a transient
		// JetStream API failure recovers without user intervention.
		if s.onBack != nil {
			if s.disconnectTick < 0 {
				s.disconnectTick = 0
			}
			w.SetError(fmt.Errorf("session %s disconnected", s.session.SessionID))
		} else {
			w.SetError(msg.err)
		}
		return s, retrySubscribeCmd(msg.gen)

	case retrySubscribeMsg:
		if msg.gen != s.subGen || s.client == nil {
			return s, nil // superseded, or nothing to subscribe to
		}
		return s, s.resubscribe()

	case connStatusMsg:
		// Mid-session connection loss: surface the error and, when there is a
		// screen to return to, arm the disconnect grace timer that transitions
		// back. In standalone mode (no onBack) we still show the error but stay
		// put. On reconnect, clear the error and resubscribe: the stream may have
		// been recreated (e.g. a proxy restart resets sequence numbers), so the
		// old ordered consumer's cursor is stale and must be replaced rather than
		// assumed to resume on the same channel.
		if msg.connected {
			s.disconnectTick = -1
			w.ClearError()
			if s.client != nil {
				return s, tea.Batch(s.resubscribe(), s.watchConn())
			}
		} else {
			if s.onBack != nil && s.disconnectTick < 0 {
				s.disconnectTick = 0
			}
			w.SetError(fmt.Errorf("session %s disconnected", s.session.SessionID))
		}
		if s.client != nil {
			return s, s.watchConn()
		}

	case logEntryMsg:
		if msg.gen != s.subGen {
			return s, nil // entry from a superseded subscription; drop it
		}
		// Connection state is driven solely by connStatusMsg and the subscription
		// lifecycle, never by log delivery: an entry may be buffered or replayed
		// from history and arrive after the proxy died, so it must not be treated
		// as proof the session is healthy.
		wasAtEnd := len(s.items) == 0 || s.cursor.AtEnd()
		s.items = append(s.items, logItem{entry: msg.entry})
		s.cursor.ItemCount = len(s.items)
		if !wasAtEnd && len(s.items) > s.cursor.Offset+s.cursor.VpHeight {
			s.newCount++
		}
		if wasAtEnd {
			s.cursor.Pos = len(s.items) - 1
			s.newCount = 0
			s.cursor.EnsureVisible()
		}
		return s, waitForLog(msg.gen, msg.ch, msg.done)

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
				return s, tea.Batch(s.resubscribe(), s.watchConn())
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
