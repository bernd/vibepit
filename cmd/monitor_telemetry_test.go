package cmd

import (
	"fmt"
	"testing"
	"time"

	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

func makeTelemetrySetup(n int) (*telemetryScreen, *tui.Window) {
	s := newTelemetryScreen(nil)
	s.disabled = false // allow event display without a live client
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
	s.cursor.ItemCount = len(s.events)
	if len(s.events) > 0 {
		s.cursor.Pos = len(s.events) - 1
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
		s.agents = []string{"claude-code", "codex"}

		assert.Equal(t, "", s.agentFilter) // all

		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}, w)
		assert.Equal(t, "claude-code", s.agentFilter)

		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}, w)
		assert.Equal(t, "codex", s.agentFilter)

		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}, w)
		assert.Equal(t, "", s.agentFilter) // back to all
	})
}

func TestTelemetryScreen_View(t *testing.T) {
	t.Run("renders event lines", func(t *testing.T) {
		s, w := makeTelemetrySetup(5)
		view := s.View(w)
		assert.Contains(t, view, "claude-code")
		assert.Contains(t, view, "Read")
	})

	t.Run("shows metrics header", func(t *testing.T) {
		s, w := makeTelemetrySetup(0)
		s.metricSummaries = []proxy.MetricSummary{
			{Name: "claude_code.token.usage", Agent: "claude-code", Value: 1234, Attributes: map[string]string{"type": "input"}},
		}
		view := s.View(w)
		assert.Contains(t, view, "1234")
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
	t.Run("shows filter key", func(t *testing.T) {
		s, w := makeTelemetrySetup(5)
		keys := s.FooterKeys(w)
		descs := footerKeyDescs(keys)
		assert.Contains(t, descs, "filter agent")
	})

	t.Run("shows active filter in footer", func(t *testing.T) {
		s, w := makeTelemetrySetup(0)
		s.agentFilter = "claude-code"
		status := s.FooterStatus(w)
		assert.Contains(t, status, "claude-code")
	})
}

func TestStripControl(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"plain text", "hello", "hello"},
		{"strips ANSI escape", "hello\x1b[31mworld\x1b[0m", "helloworld"},
		{"strips control chars", "hello\x00world", "helloworld"},
		{"preserves tabs", "hello\tworld", "hello\tworld"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, stripControl(tt.input))
		})
	}
}
