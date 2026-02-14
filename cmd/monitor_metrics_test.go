package cmd

import (
	"testing"

	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

func makeMetricsSetup(summaries []proxy.MetricSummary) (*metricsScreen, *tui.Window) {
	s := newMetricsScreen(&ControlClient{}) // non-nil to avoid disabled state
	header := &tui.HeaderInfo{ProjectDir: "/test", SessionID: "test123"}
	w := tui.NewWindow(header, s)
	w.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	s.summaries = summaries
	for _, m := range summaries {
		s.filter.track(m.Agent)
	}
	s.rebuildLines()
	return s, w
}

func sampleMetrics() []proxy.MetricSummary {
	return []proxy.MetricSummary{
		{Name: "token_usage", Agent: "claude-code", Value: 1234, Attributes: map[string]string{"type": "input"}},
		{Name: "token_usage", Agent: "claude-code", Value: 567, Attributes: map[string]string{"type": "output"}},
		{Name: "api_calls", Agent: "claude-code", Value: 42},
		{Name: "token_usage", Agent: "aider", Value: 800, Attributes: map[string]string{"type": "input"}},
	}
}

func TestMetricsScreen_CursorNavigation(t *testing.T) {
	t.Run("j moves cursor down", func(t *testing.T) {
		s, w := makeMetricsSetup(sampleMetrics())
		s.cursor.Pos = 0
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}, w)
		assert.Equal(t, 1, s.cursor.Pos)
	})

	t.Run("G jumps to end", func(t *testing.T) {
		s, w := makeMetricsSetup(sampleMetrics())
		s.cursor.Pos = 0
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}}, w)
		assert.Equal(t, len(s.lines)-1, s.cursor.Pos)
	})
}

func TestMetricsScreen_AgentFilter(t *testing.T) {
	t.Run("f key cycles agent filter", func(t *testing.T) {
		s, w := makeMetricsSetup(sampleMetrics())

		assert.Equal(t, "", s.filter.active)

		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}, w)
		assert.Equal(t, "claude-code", s.filter.active)

		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}, w)
		assert.Equal(t, "aider", s.filter.active)

		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}, w)
		assert.Equal(t, "", s.filter.active)
	})

	t.Run("filter reduces visible lines", func(t *testing.T) {
		s, w := makeMetricsSetup(sampleMetrics())
		allLines := len(s.lines)

		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}, w)
		assert.Less(t, len(s.lines), allLines)
	})
}

func TestMetricsScreen_View(t *testing.T) {
	t.Run("renders agent headers and metric values", func(t *testing.T) {
		s, w := makeMetricsSetup(sampleMetrics())
		view := s.View(w)
		assert.Contains(t, view, "claude-code")
		assert.Contains(t, view, "aider")
		assert.Contains(t, view, "token_usage(input)")
		assert.Contains(t, view, "1234")
	})

	t.Run("shows disabled message when client is nil", func(t *testing.T) {
		s := newMetricsScreen(nil)
		header := &tui.HeaderInfo{ProjectDir: "/test", SessionID: "test123"}
		w := tui.NewWindow(header, s)
		w.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
		view := s.View(w)
		assert.Contains(t, view, "disabled")
	})
}

func TestMetricsScreen_RebuildLines(t *testing.T) {
	t.Run("groups metrics by agent", func(t *testing.T) {
		s, _ := makeMetricsSetup(sampleMetrics())

		// First line is a spacer, second is an agent header.
		assert.False(t, s.lines[0].isAgent)
		assert.Equal(t, "", s.lines[0].text)
		assert.True(t, s.lines[1].isAgent)
		// Followed by metric lines (not agent headers).
		assert.False(t, s.lines[2].isAgent)
	})

	t.Run("cursor item count matches lines", func(t *testing.T) {
		s, _ := makeMetricsSetup(sampleMetrics())
		assert.Equal(t, len(s.lines), s.cursor.ItemCount)
	})
}

func TestMetricsScreen_Footer(t *testing.T) {
	t.Run("shows filter key", func(t *testing.T) {
		s, w := makeMetricsSetup(sampleMetrics())
		keys := s.FooterKeys(w)
		descs := footerKeyDescs(keys)
		assert.Contains(t, descs, "filter agent")
	})

	t.Run("shows active filter in footer", func(t *testing.T) {
		s, w := makeMetricsSetup(sampleMetrics())
		s.filter.active = "claude-code"
		status := s.FooterStatus(w)
		assert.Contains(t, status, "claude-code")
	})
}
