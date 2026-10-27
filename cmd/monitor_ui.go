package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	"github.com/charmbracelet/x/ansi"
)

const pollInterval = time.Second

// hiddenHostsPollInterval keeps the HOSTS tab count fresh without fetching
// the whole host table every second while the tab is not shown.
const hiddenHostsPollInterval = 5 * time.Second

const disconnectGracePeriod = 3 * time.Second

var disconnectGraceTicks = int(disconnectGracePeriod / tui.TickInterval)

// selectionTimeout ends a HOSTS selection after this long without a key
// press. Rows move as traffic arrives, so a selection only exists while the
// user is actively picking; the allow keys never act on a row that moved
// under an idle cursor. Log lines never move, so LOGS keeps its selection
// and scroll position while the user reads.
const selectionTimeout = 6 * time.Second

var selectionTimeoutTicks = int(selectionTimeout / tui.TickInterval)

// monitorTabLines is the tab bar plus the rule below it.
const monitorTabLines = 2

type monitorTab int

const (
	tabHosts monitorTab = iota
	tabLogs
)

// monitorScreen implements tui.Screen for the monitor. It owns the feed,
// polling and allow state, and delegates rendering to the hosts and log tabs.
type monitorScreen struct {
	session *SessionInfo
	client  *ControlClient
	onBack  func() tui.Screen
	tab     monitorTab
	allows  allowState
	hosts   *hostsView
	logs    *logView
	// held is this screen's own: hold IDs start over when a proxy restarts,
	// so another session's holds must never leak into it.
	held              heldTargets
	feed              <-chan feedBatch
	stopFeed          context.CancelFunc
	hostsPollInFlight bool
	firstTickSeen     bool
	disconnectTick    int    // -1 = connected, 0+ = ticks since disconnect
	allowGen          uint64 // counts successful allows to spot stale host polls
	idleTicks         int    // ticks since the last key press
}

func newMonitorScreen(session *SessionInfo, client *ControlClient, onBack func() tui.Screen) *monitorScreen {
	return &monitorScreen{
		session:        session,
		client:         client,
		onBack:         onBack,
		allows:         allowState{},
		hosts:          &hostsView{},
		logs:           &logView{},
		disconnectTick: -1,
	}
}

// Results carry the screen that started them. A command can outlive its
// screen (disconnect, back to the session list, open another session), and
// its result must not reach the next monitor.

// feedMsg delivers one feed batch. closed reports the feed has ended.
type feedMsg struct {
	from   *monitorScreen
	batch  feedBatch
	closed bool
}

// hostsPollResultMsg is returned by async host stats polling.
type hostsPollResultMsg struct {
	from  *monitorScreen
	hosts []proxy.HostStats
	err   error
	gen   uint64 // allowGen when the poll started
}

func (s *monitorScreen) transitionBack(w *tui.Window) tui.Screen {
	s.closeFeed()
	if s.client != nil {
		s.client.Close()
	}
	w.SetHeader(selectorHeader())
	return s.onBack()
}

// openFeed starts the feed with the recent tail, so the tail and the live
// entries after it arrive without a gap, and returns the wait for its
// first batch.
func (s *monitorScreen) openFeed() tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	s.stopFeed = cancel
	s.feed = startFeed(ctx, s.client, proxy.TailSize, pollInterval)
	return s.waitFeed()
}

// closeFeed ends the feed. An open stream makes the proxy hold requests,
// so it must not outlive the monitor.
func (s *monitorScreen) closeFeed() {
	if s.stopFeed != nil {
		s.stopFeed()
	}
}

func (s *monitorScreen) waitFeed() tea.Cmd {
	feed := s.feed
	return func() tea.Msg {
		b, ok := <-feed
		return feedMsg{from: s, batch: b, closed: !ok}
	}
}

// applyFeed folds events into the hold table and appends their log lines.
// A hold-open event has no line of its own; it only marks its host as held.
func (s *monitorScreen) applyFeed(events []proxy.Event, now time.Time) {
	var entries []proxy.LogEntry
	for _, e := range events {
		s.held.Apply(e)
		if e.Type == proxy.EventLog {
			entries = append(entries, e.Log)
		}
	}
	s.logs.appendEntries(entries)
	s.hosts.refresh(&s.held, now)
}

// resetFeedState drops what the screen keeps by log or hold ID once the
// feed reached a new proxy, whose IDs start over and would collide.
func (s *monitorScreen) resetFeedState(now time.Time) {
	s.held = heldTargets{}
	s.logs.clear()
	s.hosts.merge(&s.held, now)
}

// pollHosts starts a hosts poll unless one is in flight or cannot succeed.
func (s *monitorScreen) pollHosts() tea.Cmd {
	if s.hostsPollInFlight || s.hosts.unsupported || s.client == nil {
		return nil
	}
	s.hostsPollInFlight = true
	gen := s.allowGen
	return func() tea.Msg {
		hosts, err := s.client.Hosts()
		return hostsPollResultMsg{from: s, hosts: hosts, err: err, gen: gen}
	}
}

func listHeight(w *tui.Window) int {
	return max(w.VpHeight()-monitorTabLines, 1)
}

// connectionLost is only driven by the feed. The hosts poll is heavier
// and may time out on a large table while the session is fine, so its
// failures stay on the HOSTS tab.
func (s *monitorScreen) connectionLost(w *tui.Window, err error) {
	if s.onBack != nil {
		if s.disconnectTick < 0 {
			s.disconnectTick = 0
		}
		w.SetError(fmt.Errorf("session %s disconnected", s.session.SessionID))
		return
	}
	w.SetError(err)
}

func (s *monitorScreen) connected(w *tui.Window) {
	s.disconnectTick = -1
	w.ClearError()
}

// hasSelection reports whether the visible tab has a selection.
func (s *monitorScreen) hasSelection() bool {
	if s.tab == tabLogs {
		return s.logs.selecting
	}
	return s.hosts.selecting
}

// clearSelection ends the visible tab's selection and reports whether there
// was one, so esc first clears it and only then leaves the monitor.
func (s *monitorScreen) clearSelection() bool {
	if !s.hasSelection() {
		return false
	}
	if s.tab == tabLogs {
		s.logs.deselect()
	} else {
		s.hosts.deselect()
	}
	return true
}

// entryTarget lets a log line's allow keys act on its host as HOSTS shows
// it, so both tabs agree when the host was allowed outside this monitor. A
// host missing from the table (evicted, or an older proxy) falls back to
// the line itself. The line of an open hold acts on the hold.
func (s *monitorScreen) entryTarget(e proxy.LogEntry, now time.Time) allowTarget {
	if s.held.OpenEntry(e, now) {
		return s.allows.forHeld(e.HostKey())
	}
	if i := s.hosts.indexOf(e.HostKey()); i >= 0 {
		return s.allows.forHost(s.hosts.hosts[i])
	}
	return s.allows.forEntry(e)
}

// heldSelection returns the host of the selected log line or host row when
// the proxy is holding requests for it.
func (s *monitorScreen) heldSelection(now time.Time) (proxy.HostKey, bool) {
	if s.tab == tabLogs {
		e, ok := s.logs.selected()
		return e.HostKey(), ok && s.held.OpenEntry(e, now)
	}
	h, ok := s.hosts.selected()
	if !ok {
		return proxy.HostKey{}, false
	}
	return h.HostKey, s.held.Held(h.HostKey, now)
}

func (s *monitorScreen) allowSelected(w *tui.Window, save bool) tea.Cmd {
	if s.tab == tabLogs {
		e, ok := s.logs.selected()
		if !ok {
			w.SetFlash("select an entry first")
			return nil
		}
		return s.requestAllow(w, s.entryTarget(e, w.Now()), save)
	}
	if s.hosts.unsupported {
		return nil
	}
	h, ok := s.hosts.selected()
	if !ok {
		w.SetFlash("select a host first")
		return nil
	}
	if s.held.Held(h.HostKey, w.Now()) {
		return s.requestAllow(w, s.allows.forHeld(h.HostKey), save)
	}
	return s.requestAllow(w, s.allows.forHost(h), save)
}

func (s *monitorScreen) Update(msg tea.Msg, w *tui.Window) (tui.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		s.idleTicks = 0
		switch msg.String() {
		case "tab":
			if s.tab == tabHosts {
				s.tab = tabLogs
			} else {
				s.tab = tabHosts
				// The host list may be up to hiddenHostsPollInterval old.
				return s, s.pollHosts()
			}
		case "a", "A":
			return s, s.allowSelected(w, msg.String() == "A")
		case "n":
			return s, s.denySelected(w)
		case "esc":
			if s.clearSelection() {
				break
			}
			if s.onBack != nil {
				return s.transitionBack(w), nil
			}
		case "q", "ctrl+c":
			s.closeFeed()
			return s, tea.Quit
		default:
			if s.tab == tabLogs {
				s.logs.handleKey(msg)
			} else {
				s.hosts.handleKey(msg)
			}
		}

	case tea.WindowSizeMsg:
		s.logs.setHeight(listHeight(w))
		s.hosts.setHeight(listHeight(w))

	case allowResultMsg:
		if msg.from != s {
			break
		}
		s.handleAllowResult(msg, w)

	case denyResultMsg:
		if msg.from != s {
			break
		}
		s.handleDenyResult(msg, w)

	case feedMsg:
		if msg.from != s || msg.closed {
			break
		}
		switch {
		case errors.Is(msg.batch.Err, ErrUnsupportedEndpoint):
			// A proxy started by an older vibepit has no event stream. It's
			// reachable, so the hosts tab still works.
			s.logs.unsupported = true
			s.connected(w)
		case msg.batch.Err != nil:
			s.connectionLost(w, msg.batch.Err)
		case msg.batch.Reset:
			s.resetFeedState(w.Now())
		case len(msg.batch.Events) == 0:
			s.connected(w)
		default:
			s.applyFeed(msg.batch.Events, w.Now())
		}
		return s, s.waitFeed()

	case hostsPollResultMsg:
		if msg.from != s {
			break
		}
		s.hostsPollInFlight = false
		switch {
		case errors.Is(msg.err, ErrUnsupportedEndpoint):
			// A proxy started by an older vibepit has no hosts endpoint.
			// The log tab still works.
			s.hosts.unsupported = true
			if s.tab == tabHosts {
				s.tab = tabLogs
			}
		case msg.err != nil:
			s.hosts.err = msg.err
		default:
			s.hosts.err = nil
			// A snapshot taken before an allow finished would show the
			// host as blocked again until the next poll.
			if msg.gen == s.allowGen {
				s.hosts.setHosts(msg.hosts, &s.held, w.Now())
			}
		}

	case tui.TickMsg:
		s.idleTicks++
		s.hosts.refresh(&s.held, w.Now()) // deadlines pass without an event
		if s.idleTicks >= selectionTimeoutTicks {
			s.hosts.selectionExpired()
		}

		if s.onBack != nil && s.disconnectTick >= 0 {
			s.disconnectTick++
			if s.disconnectTick >= disconnectGraceTicks {
				return s.transitionBack(w), nil
			}
			s.firstTickSeen = true
			return s, nil // Don't poll while disconnected.
		}

		due := w.IntervalElapsed(pollInterval) || !s.firstTickSeen
		s.firstTickSeen = true
		if s.client == nil {
			break
		}
		var cmds []tea.Cmd
		if s.feed == nil {
			cmds = append(cmds, s.openFeed())
		}
		if due && (s.tab == tabHosts || w.IntervalElapsed(hiddenHostsPollInterval)) {
			cmds = append(cmds, s.pollHosts())
		}
		return s, tea.Batch(cmds...)
	}

	return s, nil
}

// startSelection reveals the cursor at pos for a navigation key. Arrow keys
// only select that row; jumps and pages also move from it.
func startSelection(c *tui.Cursor, pos int, msg tea.KeyPressMsg) bool {
	prev := c.Pos
	c.Pos = pos
	switch msg.String() {
	case "j", "down", "k", "up":
		c.EnsureVisible()
		return true
	}
	if !c.HandleKey(msg) {
		c.Pos = prev
		return false
	}
	return true
}

func (s *monitorScreen) renderTabs() string {
	tab := func(t monitorTab, label string) string {
		if s.tab == t {
			return lipgloss.NewStyle().Bold(true).Render(label)
		}
		return lipgloss.NewStyle().Foreground(tui.ColorField).Render(label)
	}
	const gap = "   "
	hostsLabel := fmt.Sprintf("HOSTS (%d)", len(s.hosts.hosts))
	logsLabel := fmt.Sprintf("LOGS (%d)", len(s.logs.items))
	bar := tab(tabHosts, hostsLabel) + gap + tab(tabLogs, logsLabel)

	indent, label := "", hostsLabel
	if s.tab == tabLogs {
		indent, label = strings.Repeat(" ", ansi.StringWidth(hostsLabel+gap)), logsLabel
	}
	underline := lipgloss.NewStyle().Foreground(tui.ColorField).Render(strings.Repeat("─", ansi.StringWidth(label)))
	return bar + "\n" + indent + underline
}

func (s *monitorScreen) View(w *tui.Window) string {
	var content string
	if s.tab == tabLogs {
		content = s.logs.render(s.allows, &s.held, w.Now())
	} else {
		content = s.hosts.render(s.allows, &s.held, w.Width(), listHeight(w), w.Now())
	}
	return s.renderTabs() + "\n" + content
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (s *monitorScreen) FooterStatus(w *tui.Window) string {
	var indicator string
	if s.tab == tabHosts || !s.logs.selecting {
		glyph := spinnerFrames[w.TickFrame()%len(spinnerFrames)]
		indicator = lipgloss.NewStyle().Foreground(tui.ColorCyan).Render(glyph)
	} else {
		indicator = lipgloss.NewStyle().Foreground(tui.ColorField).Render("⠿")
	}

	if s.tab == tabLogs && s.logs.newCount > 0 {
		newMsg := lipgloss.NewStyle().Foreground(tui.ColorOrange).
			Render(fmt.Sprintf("↓ %d new", s.logs.newCount))
		return indicator + " " + newMsg
	}
	return indicator
}

func (s *monitorScreen) FooterKeys(w *tui.Window) []tui.FooterKey {
	keys, other, cursor := s.hosts.footerKeys(s.allows, &s.held, w.Now()), "logs", &s.hosts.cursor
	if s.tab == tabLogs {
		now := w.Now()
		keys = s.logs.footerKeys(func(e proxy.LogEntry) allowTarget { return s.entryTarget(e, now) })
		if _, held := s.heldSelection(now); held {
			keys = append(keys, tui.FooterKey{Key: "n", Desc: "deny"})
		}
		other, cursor = "hosts", &s.logs.cursor
	}
	keys = append(keys, tui.FooterKey{Key: "tab", Desc: other})

	switch {
	case s.hasSelection():
		keys = append(keys, tui.FooterKey{Key: "esc", Desc: "clear"})
	case s.onBack != nil:
		keys = append(keys, tui.FooterKey{Key: "esc", Desc: "sessions"})
	}

	return append(keys, cursor.FooterKeys()...)
}
