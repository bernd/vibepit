package cmd

import (
	"regexp"
	"strings"
	"time"

	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/telemetry"
	"github.com/bernd/vibepit/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// metricsPollResultMsg is returned by the async metrics polling command.
type metricsPollResultMsg struct {
	metrics []proxy.MetricSummary
	err     error
}

type metricsScreen struct {
	client        *ControlClient
	cursor        tui.Cursor
	pollInFlight  bool
	summaries     []proxy.MetricSummary
	filter        agentFilter
	firstTickSeen bool
	heightOffset  int
	lines         []metricsLine // rendered line model for cursor mapping
}

type metricsLine struct {
	isAgent bool
	text    string
}

func newMetricsScreen(client *ControlClient) *metricsScreen {
	return &metricsScreen{
		client: client,
	}
}

func (s *metricsScreen) pollMetricsCmd() tea.Cmd {
	return func() tea.Msg {
		metrics, err := s.client.TelemetryMetrics(false)
		return metricsPollResultMsg{metrics: metrics, err: err}
	}
}

func (s *metricsScreen) Update(msg tea.Msg, w *tui.Window) (tui.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "f":
			s.filter.cycle()
			s.rebuildLines()
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

	case metricsPollResultMsg:
		s.pollInFlight = false
		if msg.err != nil {
			w.SetError(msg.err)
			break
		}
		w.ClearError()
		s.summaries = msg.metrics
		for _, m := range msg.metrics {
			s.filter.track(m.Agent)
		}
		s.rebuildLines()

	case tui.TickMsg:
		if s.client != nil && (w.IntervalElapsed(time.Second) || !s.firstTickSeen) && !s.pollInFlight {
			s.firstTickSeen = true
			s.pollInFlight = true
			return s, s.pollMetricsCmd()
		}
		s.firstTickSeen = true
	}

	return s, nil
}

func (s *metricsScreen) rebuildLines() {
	// Group metrics by agent, preserving sorted order from summaries.
	type group struct {
		agent   string
		metrics []proxy.MetricSummary
	}
	var groups []group
	seen := map[string]int{} // agent -> index into groups

	for _, m := range s.summaries {
		if s.filter.active != "" && m.Agent != s.filter.active {
			continue
		}
		if idx, ok := seen[m.Agent]; ok {
			groups[idx].metrics = append(groups[idx].metrics, m)
		} else {
			seen[m.Agent] = len(groups)
			groups = append(groups, group{agent: m.Agent, metrics: []proxy.MetricSummary{m}})
		}
	}

	s.lines = nil
	for _, g := range groups {
		s.lines = append(s.lines, metricsLine{text: ""})
		header := telemetry.DisplayName(g.agent, g.metrics)
		s.lines = append(s.lines, metricsLine{isAgent: true, text: header})
		for _, line := range telemetry.FormatAgent(g.agent, g.metrics) {
			s.lines = append(s.lines, metricsLine{text: line})
		}
	}
	s.cursor.ItemCount = len(s.lines)
	s.cursor.EnsureVisible()
}

func (s *metricsScreen) View(w *tui.Window) string {
	height := w.VpHeight() - s.heightOffset

	if s.client == nil {
		return renderDisabledView(height)
	}

	agentStyle := lipgloss.NewStyle().Foreground(tui.ColorOrange).Bold(true)
	sectionStyle := lipgloss.NewStyle().Foreground(tui.ColorCyan)
	accentStyle := lipgloss.NewStyle().Foreground(tui.ColorCyan)

	scrollable := len(s.lines) > height
	if !scrollable {
		s.cursor.Offset = 0
	}

	var out []string
	end := min(s.cursor.Offset+height, len(s.lines))
	for i := s.cursor.Offset; i < end; i++ {
		line := s.lines[i]
		_, marker := tui.LineStyle(scrollable && i == s.cursor.Pos)
		if line.isAgent {
			out = append(out, marker+agentStyle.Render(line.text))
		} else {
			out = append(out, marker+renderMetricsLine(line.text, sectionStyle, accentStyle))
		}
	}
	for len(out) < height {
		out = append(out, "")
	}

	return strings.Join(out, "\n")
}

func (s *metricsScreen) FooterStatus(w *tui.Window) string {
	var parts []string
	glyph := spinnerFrames[w.TickFrame()%len(spinnerFrames)]
	parts = append(parts, lipgloss.NewStyle().Foreground(tui.ColorCyan).Render(glyph))

	if s.filter.active != "" {
		parts = append(parts, lipgloss.NewStyle().Foreground(tui.ColorOrange).Render("agent:"+s.filter.active))
	}
	return strings.Join(parts, " ")
}

func (s *metricsScreen) FooterKeys(w *tui.Window) []tui.FooterKey {
	keys := []tui.FooterKey{
		{Key: "f", Desc: "filter agent"},
	}
	keys = append(keys, s.cursor.FooterKeys()...)
	return keys
}

// Section headers: lines with only leading spaces + a single word (no digits, no colons).
var sectionHeaderRe = regexp.MustCompile(`^(\s+)([A-Z][a-z]+)$`)

// Dollar amounts like $0.3421 or $0.17.
var dollarRe = regexp.MustCompile(`\$\d+\.\d+`)

// renderMetricsLine applies selective color to a metrics display line.
// Section headers (Models, Efficiency, etc.) get sectionStyle.
// Dollar amounts get accentStyle. Everything else renders in default color.
func renderMetricsLine(text string, sectionStyle, accentStyle lipgloss.Style) string {
	if sectionHeaderRe.MatchString(text) {
		return sectionStyle.Render(text)
	}

	// Highlight dollar amounts within the line.
	result := dollarRe.ReplaceAllStringFunc(text, func(match string) string {
		return accentStyle.Render(match)
	})
	return result
}
