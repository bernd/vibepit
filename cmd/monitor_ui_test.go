package cmd

import (
	"fmt"
	"testing"
	"time"

	"github.com/bernd/vibepit/proxy"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMonitorModel_WindowSizeMsg(t *testing.T) {
	m := newMonitorModel(&SessionInfo{
		SessionID:  "test123456",
		ProjectDir: "/home/user/project",
	}, nil)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	model := updated.(monitorModel)

	assert.Equal(t, 100, model.width)
	assert.Equal(t, 40, model.height)
	assert.Greater(t, model.vpHeight, 0)
}

func TestMonitorModel_ViewContainsHeader(t *testing.T) {
	m := newMonitorModel(&SessionInfo{
		SessionID:  "test123456",
		ProjectDir: "/home/user/project",
	}, nil)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	model := updated.(monitorModel)

	view := model.View()
	assert.Contains(t, view, "I PITY THE VIBES")
}

func TestRenderLogLine(t *testing.T) {
	item := logItem{
		entry: proxy.LogEntry{
			Domain: "example.com",
			Port:   "443",
			Action: proxy.ActionAllow,
			Source: proxy.SourceProxy,
		},
	}
	line := renderLogLine(item, false)
	require.Contains(t, line, "example.com:443")
	require.Contains(t, line, "+")
}

func TestRenderLogLine_Block(t *testing.T) {
	item := logItem{
		entry: proxy.LogEntry{
			Domain: "evil.com",
			Action: proxy.ActionBlock,
			Source: proxy.SourceDNS,
			Reason: "not allowed",
		},
	}
	line := renderLogLine(item, false)
	require.Contains(t, line, "evil.com")
	require.Contains(t, line, "x")
	require.Contains(t, line, "not allowed")
}

func TestRenderLogLine_AllowStatuses(t *testing.T) {
	tests := []struct {
		name           string
		status         allowStatus
		expectedSymbol string
	}{
		{
			name:           "temp shows lowercase a",
			status:         statusTemp,
			expectedSymbol: "a",
		},
		{
			name:           "saved shows uppercase A",
			status:         statusSaved,
			expectedSymbol: "A",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := logItem{
				entry: proxy.LogEntry{
					Domain: "example.com",
					Port:   "443",
					Action: proxy.ActionBlock,
					Source: proxy.SourceProxy,
				},
				status: tt.status,
			}
			line := renderLogLine(item, false)
			require.Contains(t, line, tt.expectedSymbol)
			// The allow status symbol should take precedence over block —
			// the line should not start with "x" as the symbol.
			// We check that the symbol position (after timestamp bracket)
			// does not contain "x" by verifying "] x" is absent.
			require.NotContains(t, line, "] x")
		})
	}
}

func TestRenderLogLine_Highlighted(t *testing.T) {
	// Force a color profile so lipgloss actually emits ANSI sequences.
	lipgloss.DefaultRenderer().SetColorProfile(termenv.ANSI)
	defer lipgloss.DefaultRenderer().SetColorProfile(termenv.Ascii)

	item := logItem{
		entry: proxy.LogEntry{
			Domain: "example.com",
			Port:   "443",
			Action: proxy.ActionAllow,
			Source: proxy.SourceProxy,
		},
	}
	normal := renderLogLine(item, false)
	highlighted := renderLogLine(item, true)
	require.NotEqual(t, normal, highlighted, "highlighted line should differ from normal")
}

func makeModelWithItems(n int) monitorModel {
	m := newMonitorModel(&SessionInfo{
		SessionID:  "test123456",
		ProjectDir: "/home/user/project",
	}, nil)
	m.vpHeight = 10
	m.width = 100
	m.height = 20
	for i := 0; i < n; i++ {
		m.items = append(m.items, logItem{
			entry: proxy.LogEntry{
				ID:     uint64(i + 1),
				Domain: fmt.Sprintf("domain%d.com", i),
				Action: proxy.ActionBlock,
				Source: proxy.SourceProxy,
			},
		})
	}
	m.cursor = len(m.items) - 1
	return m
}

func TestMonitorModel_AllowAction(t *testing.T) {
	t.Run("allowResultMsg updates item status", func(t *testing.T) {
		m := makeModelWithItems(5)
		m.cursor = 2

		updated, _ := m.Update(allowResultMsg{index: 2, status: statusTemp})
		model := updated.(monitorModel)

		assert.Equal(t, statusTemp, model.items[2].status)
	})

	t.Run("allowResultMsg with error sets model error", func(t *testing.T) {
		m := makeModelWithItems(5)

		updated, _ := m.Update(allowResultMsg{index: 0, err: fmt.Errorf("connection failed")})
		model := updated.(monitorModel)

		assert.Error(t, model.err)
		assert.Contains(t, model.err.Error(), "connection failed")
	})

	t.Run("allowResultMsg saved status", func(t *testing.T) {
		m := makeModelWithItems(5)

		updated, _ := m.Update(allowResultMsg{index: 3, status: statusSaved})
		model := updated.(monitorModel)

		assert.Equal(t, statusSaved, model.items[3].status)
	})
}

func TestMonitorModel_FlashOnAlreadyAllowed(t *testing.T) {
	m := makeModelWithItems(5)
	m.items[2].entry.Action = proxy.ActionAllow // not blocked
	m.cursor = 2
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	model := updated.(monitorModel)
	assert.Equal(t, "already allowed", model.flash)
}

func TestMonitorModel_CursorNavigation(t *testing.T) {
	t.Run("j moves cursor down", func(t *testing.T) {
		m := makeModelWithItems(20)
		m.cursor = 5
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		model := updated.(monitorModel)
		assert.Equal(t, 6, model.cursor)
	})

	t.Run("k moves cursor up", func(t *testing.T) {
		m := makeModelWithItems(20)
		m.cursor = 5
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
		model := updated.(monitorModel)
		assert.Equal(t, 4, model.cursor)
	})

	t.Run("j at end stays at end", func(t *testing.T) {
		m := makeModelWithItems(5)
		m.cursor = 4
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		model := updated.(monitorModel)
		assert.Equal(t, 4, model.cursor)
	})

	t.Run("k at start stays at start", func(t *testing.T) {
		m := makeModelWithItems(5)
		m.cursor = 0
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
		model := updated.(monitorModel)
		assert.Equal(t, 0, model.cursor)
	})

	t.Run("G jumps to end", func(t *testing.T) {
		m := makeModelWithItems(20)
		m.cursor = 0
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
		model := updated.(monitorModel)
		assert.Equal(t, 19, model.cursor)
	})

	t.Run("g jumps to start", func(t *testing.T) {
		m := makeModelWithItems(20)
		m.cursor = 15
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
		model := updated.(monitorModel)
		assert.Equal(t, 0, model.cursor)
	})
}

func TestRenderFooter(t *testing.T) {
	t.Run("shows base keybindings", func(t *testing.T) {
		m := makeModelWithItems(5)
		footer := m.renderFooter(100)
		assert.Contains(t, footer, "navigate")
		assert.Contains(t, footer, "Home")
		assert.Contains(t, footer, "End")
		assert.Contains(t, footer, "quit")
	})

	t.Run("shows allow keys on blocked entry", func(t *testing.T) {
		m := makeModelWithItems(5)
		m.cursor = 2
		footer := m.renderFooter(100)
		assert.Contains(t, footer, "allow")
		assert.Contains(t, footer, "allow+save")
	})

	t.Run("hides allow keys on allowed entry", func(t *testing.T) {
		m := makeModelWithItems(5)
		m.items[2].entry.Action = proxy.ActionAllow
		m.cursor = 2
		footer := m.renderFooter(100)
		assert.NotContains(t, footer, "allow")
	})

	t.Run("shows save key on temp-allowed entry", func(t *testing.T) {
		m := makeModelWithItems(5)
		m.items[2].status = statusTemp
		m.cursor = 2
		footer := m.renderFooter(100)
		assert.Contains(t, footer, "save")
		assert.NotContains(t, footer, "allow+save")
	})

	t.Run("shows new message count", func(t *testing.T) {
		m := makeModelWithItems(5)
		m.newCount = 3
		footer := m.renderFooter(100)
		assert.Contains(t, footer, "3 new")
	})

	t.Run("shows connection error", func(t *testing.T) {
		m := makeModelWithItems(5)
		m.err = fmt.Errorf("connection refused")
		footer := m.renderFooter(100)
		assert.Contains(t, footer, "connection refused")
	})

	t.Run("shows flash message", func(t *testing.T) {
		m := makeModelWithItems(5)
		m.flash = "already allowed"
		m.flashExp = time.Now().Add(2 * time.Second)
		footer := m.renderFooter(100)
		assert.Contains(t, footer, "already allowed")
	})

	t.Run("error takes priority over flash", func(t *testing.T) {
		m := makeModelWithItems(5)
		m.err = fmt.Errorf("connection refused")
		m.flash = "already allowed"
		m.flashExp = time.Now().Add(2 * time.Second)
		footer := m.renderFooter(100)
		assert.Contains(t, footer, "connection refused")
		assert.NotContains(t, footer, "already allowed")
	})
}

func TestMonitorModel_NewCount(t *testing.T) {
	t.Run("increments when cursor not at end", func(t *testing.T) {
		m := makeModelWithItems(5)
		m.cursor = 2 // not at end (4)
		m.items = append(m.items, logItem{
			entry: proxy.LogEntry{ID: 100, Domain: "new.com", Action: proxy.ActionAllow, Source: proxy.SourceProxy},
		})
		m.newCount += 1
		assert.Equal(t, 1, m.newCount)
		assert.Equal(t, 2, m.cursor)
	})

	t.Run("resets when cursor reaches end via G", func(t *testing.T) {
		m := makeModelWithItems(5)
		m.cursor = 2
		m.newCount = 10
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
		model := updated.(monitorModel)
		assert.Equal(t, 0, model.newCount)
		assert.Equal(t, 4, model.cursor)
	})
}
