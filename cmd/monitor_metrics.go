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

type metricsScreen struct {
	client        *ControlClient
	cursor        tui.Cursor
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

	case tui.TickMsg:
		if s.client != nil && (w.IntervalElapsed(time.Second) || !s.firstTickSeen) {
			metrics, err := s.client.TelemetryMetrics(false)
			if err != nil {
				w.SetError(err)
			} else {
				w.ClearError()
				s.summaries = metrics
				for _, m := range metrics {
					s.filter.track(m.Agent)
				}
				s.rebuildLines()
			}
		}
		s.firstTickSeen = true
	}

	return s, nil
}

func (s *metricsScreen) rebuildLines() {
	byAgent := make(map[string][]proxy.MetricSummary)
	for _, m := range s.summaries {
		if s.filter.active != "" && m.Agent != s.filter.active {
			continue
		}
		byAgent[m.Agent] = append(byAgent[m.Agent], m)
	}

	s.lines = nil
	first := true
	for _, agent := range s.filter.agents {
		metrics, ok := byAgent[agent]
		if !ok {
			continue
		}
		if first {
			s.lines = append(s.lines, metricsLine{text: ""})
			first = false
		}
		s.lines = append(s.lines, metricsLine{isAgent: true, text: telemetry.DisplayName(agent, metrics)})
		for _, line := range telemetry.FormatAgent(agent, metrics) {
			s.lines = append(s.lines, metricsLine{text: line})
		}
	}
	s.cursor.ItemCount = len(s.lines)
	s.cursor.EnsureVisible()
}

func (s *metricsScreen) View(w *tui.Window) string {
	height := w.VpHeight() - s.heightOffset

	if s.client == nil {
		msg := lipgloss.NewStyle().Foreground(tui.ColorField).
			Render("Agent telemetry is disabled. Set agent-telemetry: true in .vibepit/network.yaml to enable.")
		pad := height / 2
		var out []string
		for range pad {
			out = append(out, "")
		}
		out = append(out, "  "+msg)
		for len(out) < height {
			out = append(out, "")
		}
		return strings.Join(out, "\n")
	}

	agentStyle := lipgloss.NewStyle().Foreground(tui.ColorOrange).Bold(true)
	labelStyle := lipgloss.NewStyle().Foreground(tui.ColorField)
	valueStyle := lipgloss.NewStyle().Foreground(tui.ColorCyan)

	var out []string
	end := min(s.cursor.Offset+height, len(s.lines))
	for i := s.cursor.Offset; i < end; i++ {
		line := s.lines[i]
		_, marker := tui.LineStyle(i == s.cursor.Pos)
		if line.isAgent {
			out = append(out, marker+agentStyle.Render(line.text))
		} else {
			// Parse "  name: value" for styled rendering.
			if idx := strings.Index(line.text, ": "); idx >= 0 {
				name := line.text[:idx]
				val := line.text[idx+2:]
				out = append(out, marker+labelStyle.Render(name+":")+valueStyle.Render(" "+val))
			} else {
				out = append(out, marker+labelStyle.Render(line.text))
			}
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
