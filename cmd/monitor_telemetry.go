package cmd

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const telemetryPollInterval = time.Second

type telemetryScreen struct {
	client        *ControlClient
	cursor        tui.Cursor
	pollCursor    uint64
	events        []proxy.TelemetryEvent
	filter        agentFilter
	firstTickSeen bool
	heightOffset  int // lines reserved by parent (e.g. tab bar)
}

func newTelemetryScreen(client *ControlClient) *telemetryScreen {
	return &telemetryScreen{
		client: client,
	}
}

func (s *telemetryScreen) filteredEvents() []proxy.TelemetryEvent {
	if s.filter.active == "" {
		return s.events
	}
	var filtered []proxy.TelemetryEvent
	for _, e := range s.events {
		if e.Agent == s.filter.active {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

func (s *telemetryScreen) Update(msg tea.Msg, w *tui.Window) (tui.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "f":
			s.filter.cycle()
			filtered := s.filteredEvents()
			s.cursor.ItemCount = len(filtered)
			s.cursor.EnsureVisible()
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
				filtered := s.filteredEvents()
				s.cursor.ItemCount = len(filtered)
				if wasAtEnd && len(filtered) > 0 {
					s.cursor.Pos = len(filtered) - 1
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
		msg := lipgloss.NewStyle().Foreground(tui.ColorField).
			Render("Agent telemetry is disabled. Set agent-telemetry: true in .vibepit/network.yaml to enable.")
		pad := height / 2
		var lines []string
		for range pad {
			lines = append(lines, "")
		}
		lines = append(lines, "  "+msg)
		for len(lines) < height {
			lines = append(lines, "")
		}
		return strings.Join(lines, "\n")
	}

	filtered := s.filteredEvents()
	var lines []string
	end := min(s.cursor.Offset+height, len(filtered))
	for i := s.cursor.Offset; i < end; i++ {
		lines = append(lines, renderTelemetryLine(filtered[i], i == s.cursor.Pos))
	}
	for len(lines) < height {
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}

// stripControl removes ANSI escape sequences and control characters (except
// tab) from s. Applied at render time as defensive terminal hygiene.
func stripControl(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inEsc := false
	for _, r := range s {
		if inEsc {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '~' {
				inEsc = false
			}
			continue
		}
		if r == '\x1b' {
			inEsc = true
			continue
		}
		if r < 0x20 && r != '\t' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func renderTelemetryLine(e proxy.TelemetryEvent, highlighted bool) string {
	base, marker := tui.LineStyle(highlighted)

	ts := base.Foreground(tui.ColorField).Render(e.Time.Format("15:04:05"))
	agent := base.Foreground(tui.ColorOrange).Render(fmt.Sprintf("%-12s", stripControl(e.Agent)))
	event := base.Foreground(tui.ColorCyan).Render(fmt.Sprintf("%-14s", stripControl(e.EventName)))

	var details []string
	if v, ok := e.Attrs["tool_name"]; ok {
		details = append(details, base.Render(stripControl(v)))
	}
	if v, ok := e.Attrs["model"]; ok {
		details = append(details, base.Render(stripControl(v)))
	}
	if v, ok := e.Attrs["success"]; ok {
		if v == "true" {
			details = append(details, base.Foreground(tui.ColorCyan).Render("\u2713"))
		} else {
			details = append(details, base.Foreground(tui.ColorError).Render("\u2717"))
		}
	}
	if v, ok := e.Attrs["duration_ms"]; ok {
		details = append(details, base.Foreground(tui.ColorField).Render(v+"ms"))
	}
	if v, ok := e.Attrs["cost_usd"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			v = fmt.Sprintf("%.4g", f)
		}
		details = append(details, base.Foreground(tui.ColorField).Render("$"+v))
	}
	if v, ok := e.Attrs["input_tokens"]; ok {
		tok := v + "\u2191"
		if out, ok2 := e.Attrs["output_tokens"]; ok2 {
			tok += " " + out + "\u2193"
		}
		details = append(details, base.Foreground(tui.ColorField).Render(tok))
	}

	sp := base.Render(" ")
	detail := base.Render(strings.Join(details, sp))

	return marker + base.Render("[") + ts + base.Render("]") + sp + agent + sp + event + sp + detail
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
		{Key: "f", Desc: "filter agent"},
	}
	keys = append(keys, s.cursor.FooterKeys()...)
	return keys
}
