package cmd

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeTelemetrySetup(n int) (*telemetryScreen, *tui.Window) {
	s := newTelemetryScreen(&ControlClient{}) // non-nil to avoid disabled state
	header := &tui.HeaderInfo{ProjectDir: "/test", SessionID: "test123"}
	w := tui.NewWindow(header, s)
	w.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	for i := range n {
		s.events = append(s.events, proxy.TelemetryEvent{
			ID:        uint64(i + 1),
			Time:      time.Now(),
			Agent:     "claude-code",
			EventName: fmt.Sprintf("event_%d", i),
			Attrs:     map[string]string{"tool_name": "Read"},
		})
	}
	s.rebuildLines()
	if len(s.lines) > 0 {
		s.cursor.Pos = len(s.lines) - 1
	}
	return s, w
}

func TestTelemetryScreen_CursorNavigation(t *testing.T) {
	t.Run("j moves cursor down", func(t *testing.T) {
		s, w := makeTelemetrySetup(20)
		s.cursor.Pos = 5
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}, w)
		assert.Equal(t, 6, s.cursor.Pos)
	})

	t.Run("G jumps to end", func(t *testing.T) {
		s, w := makeTelemetrySetup(20)
		s.cursor.Pos = 0
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}}, w)
		assert.Equal(t, 19, s.cursor.Pos)
	})
}

func TestTelemetryScreen_AgentFilter(t *testing.T) {
	t.Run("f key cycles agent filter", func(t *testing.T) {
		s, w := makeTelemetrySetup(0)
		s.filter.agents = []string{"claude-code", "codex"}

		assert.Equal(t, "", s.filter.active) // all

		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}, w)
		assert.Equal(t, "claude-code", s.filter.active)

		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}, w)
		assert.Equal(t, "codex", s.filter.active)

		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}, w)
		assert.Equal(t, "", s.filter.active) // back to all
	})
}

func TestTelemetryScreen_View(t *testing.T) {
	t.Run("renders event lines", func(t *testing.T) {
		s, w := makeTelemetrySetup(5)
		view := s.View(w)
		assert.Contains(t, view, "claude-code")
		assert.Contains(t, view, "Read")
	})

	t.Run("shows disabled message when client is nil", func(t *testing.T) {
		s := newTelemetryScreen(nil)
		header := &tui.HeaderInfo{ProjectDir: "/test", SessionID: "test123"}
		w := tui.NewWindow(header, s)
		w.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
		view := s.View(w)
		assert.Contains(t, view, "disabled")
	})
}

func TestTelemetryScreen_Footer(t *testing.T) {
	t.Run("shows expand and filter keys", func(t *testing.T) {
		s, w := makeTelemetrySetup(5)
		keys := s.FooterKeys(w)
		descs := footerKeyDescs(keys)
		assert.Contains(t, descs, "expand")
		assert.Contains(t, descs, "filter agent")
	})

	t.Run("shows active filter in footer", func(t *testing.T) {
		s, w := makeTelemetrySetup(0)
		s.filter.active = "claude-code"
		status := s.FooterStatus(w)
		assert.Contains(t, status, "claude-code")
	})
}

func TestTelemetryScreen_FilterAcceptedToolDecision(t *testing.T) {
	t.Run("accepted tool_decision hidden", func(t *testing.T) {
		s := newTelemetryScreen(&ControlClient{})
		header := &tui.HeaderInfo{ProjectDir: "/test", SessionID: "test123"}
		w := tui.NewWindow(header, s)
		w.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

		s.events = []proxy.TelemetryEvent{
			{ID: 1, Time: time.Now(), Agent: "claude-code", EventName: "tool_decision",
				Attrs: map[string]string{"decision": "accept", "tool_name": "Bash"}},
			{ID: 2, Time: time.Now(), Agent: "claude-code", EventName: "tool_result",
				Attrs: map[string]string{"tool_name": "Bash", "success": "true"}},
		}
		s.rebuildLines()

		// Only the tool_result should appear.
		require.Len(t, s.lines, 1)
		assert.Equal(t, uint64(2), s.lines[0].eventID)
	})

	t.Run("rejected tool_decision visible", func(t *testing.T) {
		s := newTelemetryScreen(&ControlClient{})
		header := &tui.HeaderInfo{ProjectDir: "/test", SessionID: "test123"}
		w := tui.NewWindow(header, s)
		w.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

		s.events = []proxy.TelemetryEvent{
			{ID: 1, Time: time.Now(), Agent: "claude-code", EventName: "tool_decision",
				Attrs: map[string]string{"decision": "reject", "tool_name": "Bash", "source": "user"}},
		}
		s.rebuildLines()

		require.Len(t, s.lines, 1)
		assert.Contains(t, s.lines[0].text, "\u2717")
		assert.Contains(t, s.lines[0].text, "rejected")
	})
}

func TestTelemetryScreen_ToolResultDescription(t *testing.T) {
	s := newTelemetryScreen(&ControlClient{})
	header := &tui.HeaderInfo{ProjectDir: "/test", SessionID: "test123"}
	w := tui.NewWindow(header, s)
	w.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	s.events = []proxy.TelemetryEvent{
		{ID: 1, Time: time.Now(), Agent: "claude-code", EventName: "tool_result",
			Attrs: map[string]string{
				"tool_name":       "Bash",
				"success":         "true",
				"duration_ms":     "70",
				"tool_parameters": `{"command":"go vet ./...","description":"Run Go vet"}`,
			}},
	}
	s.rebuildLines()

	require.Len(t, s.lines, 1)
	assert.Contains(t, s.lines[0].text, "Bash")
	assert.Contains(t, s.lines[0].text, "Run Go vet")
}

func TestTelemetryScreen_APIRequestShortModel(t *testing.T) {
	s := newTelemetryScreen(&ControlClient{})
	header := &tui.HeaderInfo{ProjectDir: "/test", SessionID: "test123"}
	w := tui.NewWindow(header, s)
	w.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	s.events = []proxy.TelemetryEvent{
		{ID: 1, Time: time.Now(), Agent: "claude-code", EventName: "api_request",
			Attrs: map[string]string{
				"model":         "claude-opus-4-6",
				"duration_ms":   "741",
				"cost_usd":      "0.0005",
				"input_tokens":  "311",
				"output_tokens": "32",
			}},
	}
	s.rebuildLines()

	require.Len(t, s.lines, 1)
	assert.Contains(t, s.lines[0].text, "opus")
	assert.Contains(t, s.lines[0].text, "741ms")
	assert.Contains(t, s.lines[0].text, "$0.0005")
	assert.Contains(t, s.lines[0].text, "311\u2191")
	assert.Contains(t, s.lines[0].text, "32\u2193")
}

func TestTelemetryScreen_ExpandCollapse(t *testing.T) {
	s := newTelemetryScreen(&ControlClient{})
	header := &tui.HeaderInfo{ProjectDir: "/test", SessionID: "test123"}
	w := tui.NewWindow(header, s)
	w.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	s.events = []proxy.TelemetryEvent{
		{ID: 1, Time: time.Now(), Agent: "claude-code", EventName: "tool_result",
			Attrs: map[string]string{
				"tool_name":       "Bash",
				"success":         "true",
				"duration_ms":     "70",
				"tool_parameters": `{"command":"go vet ./...","description":"Run Go vet"}`,
				"result_size":     "86",
			}},
		{ID: 2, Time: time.Now(), Agent: "claude-code", EventName: "tool_result",
			Attrs: map[string]string{
				"tool_name": "Read",
				"success":   "true",
			}},
	}
	s.rebuildLines()
	s.cursor.Pos = 0

	// Initially 2 lines (one per event).
	require.Len(t, s.lines, 2)

	// Press Enter to expand first event.
	enterKey := tea.KeyMsg{Type: tea.KeyEnter}
	s.Update(enterKey, w)

	assert.True(t, s.expanded[1])
	assert.Greater(t, len(s.lines), 2, "detail lines should appear")

	// Verify detail lines contain expanded info.
	var detailTexts []string
	for _, l := range s.lines {
		if !l.isEvent && l.eventID == 1 {
			detailTexts = append(detailTexts, l.text)
		}
	}
	joined := strings.Join(detailTexts, "\n")
	assert.Contains(t, joined, "result_size")

	// Press Enter again to collapse.
	s.Update(enterKey, w)
	assert.False(t, s.expanded[1])
	assert.Len(t, s.lines, 2, "back to collapsed")
}

func TestTelemetryScreen_CursorCountIncludesDetails(t *testing.T) {
	s := newTelemetryScreen(&ControlClient{})
	header := &tui.HeaderInfo{ProjectDir: "/test", SessionID: "test123"}
	w := tui.NewWindow(header, s)
	w.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	s.events = []proxy.TelemetryEvent{
		{ID: 1, Time: time.Now(), Agent: "claude-code", EventName: "tool_result",
			Attrs: map[string]string{
				"tool_name":   "Bash",
				"success":     "true",
				"result_size": "86",
			}},
	}
	s.rebuildLines()
	collapsedCount := s.cursor.ItemCount

	s.cursor.Pos = 0
	s.expanded[1] = true
	s.rebuildLines()

	assert.Greater(t, s.cursor.ItemCount, collapsedCount)
}
