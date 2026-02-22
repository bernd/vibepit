package cmd

import (
	"strings"
	"time"

	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/telemetry"
	"github.com/bernd/vibepit/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const telemetryPollInterval = time.Second

// telemetryLine represents a single rendered line in the events view.
// Event header lines have isEvent=true; detail sub-lines have isEvent=false.
type telemetryLine struct {
	eventID uint64
	isEvent bool
	text    string // pre-rendered styled content (cursor marker added in View)
}

type telemetryScreen struct {
	client        *ControlClient
	cursor        tui.Cursor
	pollCursor    uint64
	events        []proxy.TelemetryEvent
	expanded      map[uint64]bool
	lines         []telemetryLine
	filter        agentFilter
	firstTickSeen bool
	heightOffset  int // lines reserved by parent (e.g. tab bar)
}

func newTelemetryScreen(client *ControlClient) *telemetryScreen {
	return &telemetryScreen{
		client:   client,
		expanded: map[uint64]bool{},
	}
}

func (s *telemetryScreen) filteredEvents() []proxy.TelemetryEvent {
	var filtered []proxy.TelemetryEvent
	for _, e := range s.events {
		if telemetry.IsEventNoise(e) {
			continue
		}
		if s.filter.active != "" && e.Agent != s.filter.active {
			continue
		}
		filtered = append(filtered, e)
	}
	return filtered
}

func (s *telemetryScreen) rebuildLines() {
	filtered := s.filteredEvents()
	s.lines = nil
	for _, e := range filtered {
		s.lines = append(s.lines, telemetryLine{
			eventID: e.ID,
			isEvent: true,
			text:    renderSpans(telemetry.RenderEventLine(e)),
		})
		if s.expanded[e.ID] {
			for _, detailSpans := range telemetry.RenderEventDetails(e) {
				s.lines = append(s.lines, telemetryLine{
					eventID: e.ID,
					isEvent: false,
					text:    renderSpans(detailSpans),
				})
			}
		}
	}
	s.cursor.ItemCount = len(s.lines)
	s.cursor.EnsureVisible()
}

func (s *telemetryScreen) Update(msg tea.Msg, w *tui.Window) (tui.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "f":
			s.filter.cycle()
			s.rebuildLines()
			return s, nil
		case "enter":
			if s.cursor.Pos >= 0 && s.cursor.Pos < len(s.lines) {
				id := s.lines[s.cursor.Pos].eventID
				s.expanded[id] = !s.expanded[id]
				s.rebuildLines()
			}
			return s, nil
		case "q", "ctrl+c":
			return s, tea.Quit
		default:
			if s.cursor.HandleKey(msg) {
				return s, nil
			}
		}

	case tea.WindowSizeMsg:
		s.cursor.VpHeight = w.VpHeight() - s.heightOffset
		s.cursor.EnsureVisible()

	case tui.TickMsg:
		if s.client != nil && (w.IntervalElapsed(telemetryPollInterval) || !s.firstTickSeen) {
			events, err := s.client.TelemetryEventsAfter(s.pollCursor, "", false)
			if err != nil {
				w.SetError(err)
			} else {
				w.ClearError()
				wasAtEnd := len(s.events) == 0 || s.cursor.AtEnd()
				for _, e := range events {
					s.events = append(s.events, e)
					s.pollCursor = e.ID
					s.filter.track(e.Agent)
				}
				s.rebuildLines()
				if wasAtEnd && len(s.lines) > 0 {
					s.cursor.Pos = len(s.lines) - 1
					s.cursor.EnsureVisible()
				}
			}
		}
		s.firstTickSeen = true
	}

	return s, nil
}

func (s *telemetryScreen) View(w *tui.Window) string {
	height := w.VpHeight() - s.heightOffset

	if s.client == nil {
		return renderDisabledView(height)
	}

	var out []string
	end := min(s.cursor.Offset+height, len(s.lines))
	for i := s.cursor.Offset; i < end; i++ {
		line := s.lines[i]
		_, marker := tui.LineStyle(i == s.cursor.Pos)
		out = append(out, marker+line.text)
	}
	for len(out) < height {
		out = append(out, "")
	}

	return strings.Join(out, "\n")
}

func renderDisabledView(height int) string {
	msg := lipgloss.NewStyle().Foreground(tui.ColorField).
		Render("Agent telemetry is disabled. Set agent-telemetry: true in .vibepit/network.yaml to enable.")
	lines := make([]string, height)
	lines[height/2] = "  " + msg
	return strings.Join(lines, "\n")
}

// renderSpans converts structured EventSpan slices into a styled string by
// mapping semantic roles to lipgloss styles.
func renderSpans(spans []telemetry.EventSpan) string {
	var b strings.Builder
	for _, span := range spans {
		switch span.Role {
		case telemetry.RoleField:
			b.WriteString(lipgloss.NewStyle().Foreground(tui.ColorField).Render(span.Text))
		case telemetry.RoleAccent:
			b.WriteString(lipgloss.NewStyle().Foreground(tui.ColorCyan).Render(span.Text))
		case telemetry.RoleAgent:
			b.WriteString(lipgloss.NewStyle().Foreground(tui.ColorOrange).Render(span.Text))
		case telemetry.RoleError:
			b.WriteString(lipgloss.NewStyle().Foreground(tui.ColorError).Render(span.Text))
		default:
			b.WriteString(span.Text)
		}
	}
	return b.String()
}

func (s *telemetryScreen) FooterStatus(w *tui.Window) string {
	var parts []string

	isTailing := len(s.events) == 0 || s.cursor.AtEnd()
	if isTailing {
		glyph := spinnerFrames[w.TickFrame()%len(spinnerFrames)]
		parts = append(parts, lipgloss.NewStyle().Foreground(tui.ColorCyan).Render(glyph))
	} else {
		parts = append(parts, lipgloss.NewStyle().Foreground(tui.ColorField).Render("\u283f"))
	}

	if s.filter.active != "" {
		parts = append(parts, lipgloss.NewStyle().Foreground(tui.ColorOrange).Render("agent:"+s.filter.active))
	}

	return strings.Join(parts, " ")
}

func (s *telemetryScreen) FooterKeys(w *tui.Window) []tui.FooterKey {
	keys := []tui.FooterKey{
		{Key: "enter", Desc: "expand"},
		{Key: "f", Desc: "filter agent"},
	}
	keys = append(keys, s.cursor.FooterKeys()...)
	return keys
}
