package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/bernd/vibepit/config"
	"github.com/bernd/vibepit/proxy"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// allowStatus tracks whether a log entry has been temporarily or permanently
// added to the allow list.
type allowStatus int

const (
	statusNone  allowStatus = iota
	statusTemp              // temporarily allowed this session
	statusSaved             // saved to persistent allow list
)

// logItem wraps a proxy log entry with its allow-list status.
type logItem struct {
	entry  proxy.LogEntry
	status allowStatus
}

// monitorModel is the bubbletea model for the interactive monitor TUI.
type monitorModel struct {
	session    *SessionInfo
	client     *ControlClient
	width      int
	height     int
	pollCursor uint64 // cursor for API polling (LogEntry.ID)
	cursor     int    // highlighted line index
	offset     int    // scroll offset (first visible line)
	vpHeight   int    // number of visible lines
	items      []logItem
	err        error
	flash      string
	flashExp   time.Time
	newCount   int // unseen log entries when cursor is not following tail
	tickFrame  int // animation frame for the tailing indicator
}

func newMonitorModel(session *SessionInfo, client *ControlClient) monitorModel {
	return monitorModel{
		session: session,
		client:  client,
	}
}

func (m monitorModel) headerHeight() int {
	h := RenderHeader(m.session, m.width)
	return strings.Count(h, "\n") + 1
}

// ensureCursorVisible adjusts the scroll offset so the cursor is within the
// visible window.
func (m *monitorModel) ensureCursorVisible() {
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+m.vpHeight {
		m.offset = m.cursor - m.vpHeight + 1
	}
}

// allowResultMsg is returned by the async allow command.
type allowResultMsg struct {
	index  int
	status allowStatus
	err    error
}

// tickMsg triggers a periodic poll of the proxy log buffer.
type tickMsg struct{}

const (
	tickInterval = 250 * time.Millisecond
	pollEveryNth = 4 // poll API every 4 ticks (~1s)
)

func doTick() tea.Cmd {
	return tea.Tick(tickInterval, func(time.Time) tea.Msg {
		return tickMsg{}
	})
}

func (m monitorModel) allowCmd(index int, domain string, save bool) tea.Cmd {
	return func() tea.Msg {
		_, err := m.client.Allow([]string{domain})
		if err != nil {
			return allowResultMsg{index: index, err: err}
		}

		status := statusTemp
		if save {
			status = statusSaved
			projectPath := config.DefaultProjectPath(m.session.ProjectDir)
			if err := config.AppendAllow(projectPath, []string{domain}); err != nil {
				return allowResultMsg{index: index, err: err}
			}
		}
		return allowResultMsg{index: index, status: status}
	}
}

func (m monitorModel) Init() tea.Cmd {
	return tea.Batch(doTick(), tea.WindowSize())
}

func (m monitorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "a":
			if m.cursor >= 0 && m.cursor < len(m.items) {
				item := m.items[m.cursor]
				if item.entry.Action == proxy.ActionBlock && item.status == statusNone {
					return m, m.allowCmd(m.cursor, item.entry.Domain, false)
				}
				m.flash = "already allowed"
				m.flashExp = time.Now().Add(2 * time.Second)
			}
		case "A":
			if m.cursor >= 0 && m.cursor < len(m.items) {
				item := m.items[m.cursor]
				if item.entry.Action == proxy.ActionBlock && item.status == statusNone {
					return m, m.allowCmd(m.cursor, item.entry.Domain, true)
				}
				m.flash = "already allowed"
				m.flashExp = time.Now().Add(2 * time.Second)
			}
		case "q", "ctrl+c":
			return m, tea.Quit
		case "j", "down":
			if m.cursor < len(m.items)-1 {
				m.cursor++
				m.ensureCursorVisible()
				if m.cursor == len(m.items)-1 {
					m.newCount = 0
				}
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
				m.ensureCursorVisible()
			}
		case "G", "end":
			if len(m.items) > 0 {
				m.cursor = len(m.items) - 1
				m.newCount = 0
				m.ensureCursorVisible()
			}
		case "g", "home":
			m.cursor = 0
			m.ensureCursorVisible()
		case "pgdown":
			m.cursor += m.vpHeight
			if m.cursor >= len(m.items) {
				m.cursor = len(m.items) - 1
			}
			if m.cursor < 0 {
				m.cursor = 0
			}
			m.ensureCursorVisible()
			if m.cursor == len(m.items)-1 {
				m.newCount = 0
			}
		case "pgup":
			m.cursor -= m.vpHeight
			if m.cursor < 0 {
				m.cursor = 0
			}
			m.ensureCursorVisible()
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		headerH := m.headerHeight()
		vpHeight := m.height - headerH - 2 // 1 separator after header + 1 footer line
		if vpHeight < 1 {
			vpHeight = 1
		}
		m.vpHeight = vpHeight
		if len(m.items) <= m.offset+m.vpHeight {
			// Viewport increased and we have more space.
			m.newCount = 0
		}
		m.ensureCursorVisible()

	case allowResultMsg:
		if msg.err != nil {
			m.err = msg.err
		} else if msg.index >= 0 && msg.index < len(m.items) {
			m.items[msg.index].status = msg.status
			domain := m.items[msg.index].entry.Domain
			switch msg.status {
			case statusTemp:
				m.flash = fmt.Sprintf("allowed %s", domain)
			case statusSaved:
				m.flash = fmt.Sprintf("allowed and saved %s", domain)
			}
			m.flashExp = time.Now().Add(2 * time.Second)
		}

	case tickMsg:
		m.tickFrame++
		if m.flash != "" && time.Now().After(m.flashExp) {
			m.flash = ""
		}
		// Poll the API every Nth tick to avoid excessive requests.
		if m.tickFrame%pollEveryNth == 0 {
			entries, err := m.client.LogsAfter(m.pollCursor)
			if err != nil {
				m.err = err
			} else {
				m.err = nil
				// Auto-follow: if cursor was at the last item (or no items yet),
				// advance it to the new last item after appending.
				wasAtEnd := len(m.items) == 0 || m.cursor == len(m.items)-1
				for _, e := range entries {
					m.items = append(m.items, logItem{entry: e})
					m.pollCursor = e.ID
				}
				// Only show the new count if the new messages grow outside of the viewport.
				if !wasAtEnd && len(entries) > 0 && len(m.items) > m.offset+m.vpHeight {
					m.newCount += len(entries)
				}
				if wasAtEnd && len(m.items) > 0 {
					m.cursor = len(m.items) - 1
					m.newCount = 0
					m.ensureCursorVisible()
				}
			}
		}
		cmds = append(cmds, doTick())
	}

	return m, tea.Batch(cmds...)
}

func renderLogLine(item logItem, highlighted bool) string {
	e := item.entry

	// Base style carries the background for highlighted lines.
	base := lipgloss.NewStyle()
	marker := "  "
	if highlighted {
		base = base.Background(colorHighlight)
		marker = lipgloss.NewStyle().Foreground(colorCyan).Background(colorHighlight).Render("▸") + base.Render(" ")
	}

	var symbol string
	var sourceColor lipgloss.Color
	switch {
	case item.status == statusTemp:
		symbol = base.Foreground(colorOrange).Render("a")
		sourceColor = colorOrange
	case item.status == statusSaved:
		symbol = base.Foreground(colorOrange).Bold(true).Render("A")
		sourceColor = colorOrange
	case e.Action == proxy.ActionBlock:
		symbol = base.Foreground(colorError).Render("x")
		sourceColor = colorError
	default:
		symbol = base.Foreground(colorCyan).Render("+")
		sourceColor = colorCyan
	}
	host := e.Domain
	if e.Port != "" {
		host = e.Domain + ":" + e.Port
	}
	ts := base.Foreground(colorField).Render(e.Time.Format("15:04:05"))
	src := base.Foreground(sourceColor).Render(fmt.Sprintf("%-5s", string(e.Source)))
	hostStr := base.Render(host)
	reasonStr := base.Render(e.Reason)
	sp := base.Render(" ")
	return marker + base.Render("[") + ts + base.Render("]") + sp + symbol + sp + src + sp + hostStr + sp + reasonStr
}

// tailingIndicator returns an animated trigram when live-tailing, or a pause
// icon when the cursor is not following the tail.
var trigrams = []string{"☱", "☲", "☴"}

func (m monitorModel) tailingIndicator() string {
	isTailing := len(m.items) == 0 || m.cursor == len(m.items)-1
	if isTailing {
		glyph := trigrams[m.tickFrame%len(trigrams)]
		return lipgloss.NewStyle().Foreground(colorCyan).Render(glyph)
	}
	return lipgloss.NewStyle().Foreground(colorField).Render("⏸")
}

func (m monitorModel) renderFooter(width int) string {
	keyStyle := lipgloss.NewStyle().Foreground(colorCyan)
	descStyle := lipgloss.NewStyle().Foreground(colorField)

	indicator := m.tailingIndicator() + " "

	// Left side: status indicators
	var left string
	if m.err != nil {
		left = lipgloss.NewStyle().Foreground(colorError).
			Render(fmt.Sprintf("connection error: %v", m.err))
	} else if m.flash != "" && time.Now().Before(m.flashExp) {
		left = lipgloss.NewStyle().Foreground(colorOrange).Render(m.flash)
	} else if m.newCount > 0 {
		left = lipgloss.NewStyle().Foreground(colorOrange).
			Render(fmt.Sprintf("↓ %d new", m.newCount))
	}
	left = indicator + left

	var keys []string

	if m.cursor >= 0 && m.cursor < len(m.items) {
		item := m.items[m.cursor]
		switch {
		case item.entry.Action == proxy.ActionBlock && item.status == statusNone:
			keys = append(keys,
				keyStyle.Render("a")+" "+descStyle.Render("allow"),
				keyStyle.Render("A")+" "+descStyle.Render("allow+save"),
			)
		case item.status == statusTemp:
			keys = append(keys,
				keyStyle.Render("A")+" "+descStyle.Render("save"),
			)
		}
	}

	// Right side: context-sensitive keybindings
	keys = append(keys, []string{
		keyStyle.Render("↑/↓ k/j") + " " + descStyle.Render("navigate"),
		keyStyle.Render("Home/End g/G") + " " + descStyle.Render("jump"),
		keyStyle.Render("q") + " " + descStyle.Render("quit"),
	}...)

	right := strings.Join(keys, "  ")

	// Pad between left and right
	leftWidth := ansi.StringWidth(left)
	rightWidth := ansi.StringWidth(right)
	gap := width - leftWidth - rightWidth
	if gap < 2 {
		gap = 2
	}

	return left + strings.Repeat(" ", gap) + right
}

func (m monitorModel) View() string {
	if m.width == 0 {
		return "Starting..."
	}

	header := RenderHeader(m.session, m.width)

	var logLines []string
	end := m.offset + m.vpHeight
	if end > len(m.items) {
		end = len(m.items)
	}
	for i := m.offset; i < end; i++ {
		logLines = append(logLines, renderLogLine(m.items[i], i == m.cursor))
	}
	for len(logLines) < m.vpHeight {
		logLines = append(logLines, "")
	}

	footer := m.renderFooter(m.width)

	return header + "\n" + strings.Join(logLines, "\n") + "\n" + footer
}
