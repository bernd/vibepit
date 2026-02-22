package cmd

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
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

// noiseKeys are attribute keys filtered out of detail views because they
// duplicate information already shown in the compact line or are uninteresting.
var noiseKeys = map[string]bool{
	"user.id":            true,
	"user.email":         true,
	"organization.id":    true,
	"user.account_uuid":  true,
	"session.id":         true,
	"terminal.type":      true,
	"event.sequence":     true,
	"event.timestamp":    true,
	"event.name":         true,
	"app.version":        true,
	"prompt.id":          true,
	"auth_mode":          true,
	"conversation.id":    true,
	"originator":         true,
	"slug":               true,
	"user.account_id":    true,
}

func (s *telemetryScreen) filteredEvents() []proxy.TelemetryEvent {
	var filtered []proxy.TelemetryEvent
	for _, e := range s.events {
		if e.EventName == "" {
			continue
		}
		// Hide accepted tool_decisions — they're noise.
		if e.EventName == "tool_decision" && e.Attrs["decision"] == "accept" {
			continue
		}
		if e.EventName == "codex.tool_decision" && e.Attrs["decision"] == "approved" {
			continue
		}
		// Hide codex.sse_event streaming deltas — only keep response.completed
		// that actually carry token counts.
		if e.EventName == "codex.sse_event" {
			if e.Attrs["event.kind"] != "response.completed" {
				continue
			}
			if e.Attrs["input_token_count"] == "" && e.Attrs["output_token_count"] == "" {
				continue
			}
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
			text:    renderEventLine(e),
		})
		if s.expanded[e.ID] {
			for _, detail := range renderEventDetails(e) {
				s.lines = append(s.lines, telemetryLine{
					eventID: e.ID,
					isEvent: false,
					text:    detail,
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

func renderDisabledView(height int) string {
	msg := lipgloss.NewStyle().Foreground(tui.ColorField).
		Render("Agent telemetry is disabled. Set agent-telemetry: true in .vibepit/network.yaml to enable.")
	lines := make([]string, height)
	lines[height/2] = "  " + msg
	return strings.Join(lines, "\n")
}

// Column widths for aligned rendering.
const (
	colAgent = 12 // "claude-code" = 11
	colName  = 7  // "sonnet" = 6, tool names ≤ 6
	colDur   = 7  // "4757ms" = 6
	colCost  = 9  // "$0.00055" = 8
)

// renderEventLine returns a styled compact one-liner for a telemetry event.
func renderEventLine(e proxy.TelemetryEvent) string {
	field := lipgloss.NewStyle().Foreground(tui.ColorField)
	cyan := lipgloss.NewStyle().Foreground(tui.ColorCyan)
	orange := lipgloss.NewStyle().Foreground(tui.ColorOrange)
	errColor := lipgloss.NewStyle().Foreground(tui.ColorError)

	ts := field.Render(e.Time.Format("15:04:05"))
	agent := orange.Render(fmt.Sprintf("%-*s", colAgent, stripControl(e.Agent)))
	prefix := "[" + ts + "] " + agent + " "

	switch e.EventName {
	case "tool_result", "codex.tool_result":
		name := cyan.Render(fmt.Sprintf("%-*s", colName, stripControl(e.Attrs["tool_name"])))
		var status string
		if v := e.Attrs["success"]; v == "true" {
			status = cyan.Render("\u2713")
		} else if v == "false" {
			status = errColor.Render("\u2717")
		} else {
			status = " "
		}
		dur := field.Render(fmt.Sprintf("%*s", colDur, e.Attrs["duration_ms"]+"ms"))
		line := prefix + name + " " + status + " " + dur
		if desc := toolDescription(e.Attrs["tool_parameters"]); desc != "" {
			line += "  " + stripControl(desc)
		} else if desc := toolDescription(e.Attrs["arguments"]); desc != "" {
			line += "  " + stripControl(desc)
		} else if size := e.Attrs["tool_result_size_bytes"]; size != "" {
			line += "  " + field.Render(formatBytes(size))
		}
		return line

	case "api_request", "codex.api_request":
		model := telemetry.ShortModelName(e.Attrs["model"])
		name := cyan.Render(fmt.Sprintf("%-*s", colName, model))
		dur := field.Render(fmt.Sprintf("%*s", colDur, e.Attrs["duration_ms"]+"ms"))
		line := prefix + name + "   " + dur
		if v, ok := e.Attrs["cost_usd"]; ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				v = fmt.Sprintf("$%.4g", f)
			} else {
				v = "$" + v
			}
			line += " " + field.Render(fmt.Sprintf("%*s", colCost, v))
		}
		if v, ok := e.Attrs["input_tokens"]; ok {
			tok := fmt.Sprintf("%5s\u2191", v)
			if out, ok2 := e.Attrs["output_tokens"]; ok2 {
				tok += fmt.Sprintf(" %4s\u2193", out)
			}
			line += "  " + field.Render(tok)
		}
		return line

	case "tool_decision", "codex.tool_decision":
		toolName := stripControl(e.Attrs["tool_name"])
		source := stripControl(e.Attrs["source"])
		line := prefix + errColor.Render("\u2717") + " " + cyan.Render(toolName) + " rejected"
		if source != "" {
			line += " (" + source + ")"
		}
		return line

	case "codex.sse_event":
		// Only response.completed reaches here (others filtered out).
		model := telemetry.ShortModelName(e.Attrs["model"])
		name := cyan.Render(fmt.Sprintf("%-*s", colName, model))
		var tokParts []string
		if v := e.Attrs["input_token_count"]; v != "" && v != "0" {
			tokParts = append(tokParts, v+"\u2191")
		}
		if v := e.Attrs["output_token_count"]; v != "" && v != "0" {
			tokParts = append(tokParts, v+"\u2193")
		}
		if v := e.Attrs["cached_token_count"]; v != "" && v != "0" {
			tokParts = append(tokParts, v+" cached")
		}
		if v := e.Attrs["reasoning_token_count"]; v != "" && v != "0" {
			tokParts = append(tokParts, v+" reasoning")
		}
		line := prefix + name + "   "
		if len(tokParts) > 0 {
			line += field.Render(strings.Join(tokParts, "  "))
		}
		return line

	case "codex.user_prompt":
		prompt := stripControl(e.Attrs["prompt"])
		line := prefix + cyan.Render("prompt") + "  " + prompt
		return line

	default:
		event := cyan.Render(fmt.Sprintf("%-*s", colName, stripControl(e.EventName)))
		var kvParts []string
		for _, k := range sortedAttrKeys(e.Attrs) {
			if noiseKeys[k] {
				continue
			}
			kvParts = append(kvParts, k+"="+stripControl(e.Attrs[k]))
		}
		line := prefix + event
		if len(kvParts) > 0 {
			line += "   " + strings.Join(kvParts, " ")
		}
		return line
	}
}

// renderEventDetails returns detail lines for an expanded event.
func renderEventDetails(e proxy.TelemetryEvent) []string {
	// Keys already shown in the compact line, per event type.
	shown := map[string]bool{}
	switch e.EventName {
	case "tool_result", "codex.tool_result":
		shown["tool_name"] = true
		shown["success"] = true
		shown["duration_ms"] = true
		shown["tool_parameters"] = true
		shown["arguments"] = true
		shown["output"] = true
	case "api_request", "codex.api_request":
		shown["model"] = true
		shown["duration_ms"] = true
		shown["cost_usd"] = true
		shown["input_tokens"] = true
		shown["output_tokens"] = true
	case "tool_decision", "codex.tool_decision":
		shown["tool_name"] = true
		shown["decision"] = true
		shown["source"] = true
	case "codex.sse_event":
		shown["model"] = true
		shown["event.kind"] = true
		shown["input_token_count"] = true
		shown["output_token_count"] = true
		shown["cached_token_count"] = true
		shown["reasoning_token_count"] = true
	case "codex.user_prompt":
		shown["prompt"] = true
		shown["prompt_length"] = true
		shown["model"] = true
	}

	dim := lipgloss.NewStyle().Foreground(tui.ColorField)
	var lines []string

	// For tool_result, expand tool_parameters/arguments JSON into individual fields.
	if e.EventName == "tool_result" || e.EventName == "codex.tool_result" {
		paramsJSON := e.Attrs["tool_parameters"]
		if paramsJSON == "" {
			paramsJSON = e.Attrs["arguments"]
		}
		if paramsJSON != "" {
			var parsed map[string]any
			if err := json.Unmarshal([]byte(paramsJSON), &parsed); err == nil {
				paramKeys := make([]string, 0, len(parsed))
				for k := range parsed {
					paramKeys = append(paramKeys, k)
				}
				slices.Sort(paramKeys)
				for _, k := range paramKeys {
					v := fmt.Sprintf("%v", parsed[k])
					lines = append(lines, dim.Render("  \u2514 ")+k+": "+stripControl(v))
				}
			}
		}
	}

	keys := sortedAttrKeys(e.Attrs)
	for _, k := range keys {
		if noiseKeys[k] || shown[k] {
			continue
		}
		lines = append(lines, dim.Render("  \u2514 ")+k+": "+stripControl(e.Attrs[k]))
	}

	return lines
}

// toolDescription extracts a human-readable description from a tool_parameters
// JSON string. Tries fields in priority order: description, command, then
// tool-specific fields like file_path, pattern, url, query, skill.
func toolDescription(params string) string {
	if params == "" {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(params), &parsed); err != nil {
		return ""
	}
	// Explicit description or command first.
	for _, key := range []string{"description", "command"} {
		if v, ok := parsed[key].(string); ok && v != "" {
			return v
		}
	}
	// Tool-specific fallbacks.
	for _, key := range []string{"file_path", "pattern", "url", "query", "skill", "prompt"} {
		if v, ok := parsed[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// formatBytes converts a byte count string to a human-readable size.
func formatBytes(s string) string {
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return s + "B"
	}
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%.1fMB", n/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%.1fKB", n/1024)
	default:
		return fmt.Sprintf("%.0fB", n)
	}
}

func sortedAttrKeys(attrs map[string]string) []string {
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
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
