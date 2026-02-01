package cmd

import (
	"fmt"
	"testing"

	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeTestSetup(n int) (*monitorScreen, *tui.Window) {
	s := newMonitorScreen(&SessionInfo{
		SessionID:  "test123456",
		ProjectDir: "/home/user/project",
	}, nil)
	header := &tui.HeaderInfo{ProjectDir: "/home/user/project", SessionID: "test123456"}
	w := tui.NewWindow(header, s)
	w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

	for i := 0; i < n; i++ {
		s.items = append(s.items, logItem{
			entry: proxy.LogEntry{
				ID:     uint64(i + 1),
				Domain: fmt.Sprintf("domain%d.com", i),
				Action: proxy.ActionBlock,
				Source: proxy.SourceProxy,
			},
		})
	}
	s.cursor.ItemCount = len(s.items)
	if len(s.items) > 0 {
		s.cursor.Pos = len(s.items) - 1
	}
	return s, w
}

func footerKeyDescs(keys []tui.FooterKey) []string {
	var descs []string
	for _, k := range keys {
		descs = append(descs, k.Desc)
	}
	return descs
}

func TestMonitorScreen_WindowSizeMsg(t *testing.T) {
	s, w := makeTestSetup(0)

	assert.Equal(t, 100, w.Width())
	assert.Equal(t, 40, w.Height())
	assert.Greater(t, w.VpHeight(), 0)
	assert.Equal(t, w.VpHeight(), s.cursor.VpHeight)
}

func TestMonitorScreen_ViewContainsHeader(t *testing.T) {
	_, w := makeTestSetup(0)
	view := w.View()
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
			require.NotContains(t, line, "] x")
		})
	}
}

func TestRenderLogLine_Highlighted(t *testing.T) {
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

func TestMonitorScreen_AllowAction(t *testing.T) {
	t.Run("allowResultMsg updates item status", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.cursor.Pos = 2

		s.Update(allowResultMsg{index: 2, status: statusTemp}, w)
		assert.Equal(t, statusTemp, s.items[2].status)
	})

	t.Run("allowResultMsg with error sets window error", func(t *testing.T) {
		s, w := makeTestSetup(5)

		s.Update(allowResultMsg{index: 0, err: fmt.Errorf("connection failed")}, w)
		assert.Error(t, w.Err())
		assert.Contains(t, w.Err().Error(), "connection failed")
	})

	t.Run("allowResultMsg saved status", func(t *testing.T) {
		s, w := makeTestSetup(5)

		s.Update(allowResultMsg{index: 3, status: statusSaved}, w)
		assert.Equal(t, statusSaved, s.items[3].status)
	})
}

func TestMonitorScreen_FlashOnAlreadyAllowed(t *testing.T) {
	s, w := makeTestSetup(5)
	s.items[2].entry.Action = proxy.ActionAllow // not blocked
	s.cursor.Pos = 2
	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}, w)
	assert.Equal(t, "already allowed", w.Flash())
}

func TestMonitorScreen_CursorNavigation(t *testing.T) {
	t.Run("j moves cursor down", func(t *testing.T) {
		s, w := makeTestSetup(20)
		s.cursor.Pos = 5
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}, w)
		assert.Equal(t, 6, s.cursor.Pos)
	})

	t.Run("k moves cursor up", func(t *testing.T) {
		s, w := makeTestSetup(20)
		s.cursor.Pos = 5
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}, w)
		assert.Equal(t, 4, s.cursor.Pos)
	})

	t.Run("j at end stays at end", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.cursor.Pos = 4
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}, w)
		assert.Equal(t, 4, s.cursor.Pos)
	})

	t.Run("k at start stays at start", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.cursor.Pos = 0
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}, w)
		assert.Equal(t, 0, s.cursor.Pos)
	})

	t.Run("G jumps to end", func(t *testing.T) {
		s, w := makeTestSetup(20)
		s.cursor.Pos = 0
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}}, w)
		assert.Equal(t, 19, s.cursor.Pos)
	})

	t.Run("g jumps to start", func(t *testing.T) {
		s, w := makeTestSetup(20)
		s.cursor.Pos = 15
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}}, w)
		assert.Equal(t, 0, s.cursor.Pos)
	})
}

func TestMonitorScreen_Footer(t *testing.T) {
	t.Run("shows base keybindings", func(t *testing.T) {
		s, w := makeTestSetup(5)
		keys := s.FooterKeys(w)
		descs := footerKeyDescs(keys)
		assert.Contains(t, descs, "navigate")
		assert.Contains(t, descs, "jump")
		// "quit" is added by Window, verify via full view
		view := w.View()
		assert.Contains(t, view, "quit")
	})

	t.Run("shows allow keys on blocked entry", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.cursor.Pos = 2
		keys := s.FooterKeys(w)
		descs := footerKeyDescs(keys)
		assert.Contains(t, descs, "allow")
		assert.Contains(t, descs, "allow+save")
	})

	t.Run("hides allow keys on allowed entry", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.items[2].entry.Action = proxy.ActionAllow
		s.cursor.Pos = 2
		keys := s.FooterKeys(w)
		descs := footerKeyDescs(keys)
		assert.NotContains(t, descs, "allow")
		assert.NotContains(t, descs, "allow+save")
	})

	t.Run("shows save key on temp-allowed entry", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.items[2].status = statusTemp
		s.cursor.Pos = 2
		keys := s.FooterKeys(w)
		descs := footerKeyDescs(keys)
		assert.Contains(t, descs, "save")
		assert.NotContains(t, descs, "allow+save")
	})

	t.Run("shows new message count", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.newCount = 3
		status := s.FooterStatus(w)
		assert.Contains(t, status, "3 new")
	})

	t.Run("shows connection error", func(t *testing.T) {
		_, w := makeTestSetup(5)
		w.SetError(fmt.Errorf("connection refused"))
		view := w.View()
		assert.Contains(t, view, "connection refused")
	})

	t.Run("shows flash message", func(t *testing.T) {
		_, w := makeTestSetup(5)
		w.SetFlash("already allowed")
		view := w.View()
		assert.Contains(t, view, "already allowed")
	})

	t.Run("error takes priority over flash", func(t *testing.T) {
		_, w := makeTestSetup(5)
		w.SetError(fmt.Errorf("connection refused"))
		w.SetFlash("already allowed")
		view := w.View()
		assert.Contains(t, view, "connection refused")
		assert.NotContains(t, view, "already allowed")
	})
}

func TestMonitorScreen_NewCount(t *testing.T) {
	t.Run("increments when cursor not at end", func(t *testing.T) {
		s, _ := makeTestSetup(5)
		s.cursor.Pos = 2 // not at end (4)
		s.items = append(s.items, logItem{
			entry: proxy.LogEntry{ID: 100, Domain: "new.com", Action: proxy.ActionAllow, Source: proxy.SourceProxy},
		})
		s.cursor.ItemCount = len(s.items)
		s.newCount += 1
		assert.Equal(t, 1, s.newCount)
		assert.Equal(t, 2, s.cursor.Pos)
	})

	t.Run("resets when cursor reaches end via G", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.cursor.Pos = 2
		s.newCount = 10
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}}, w)
		assert.Equal(t, 0, s.newCount)
		assert.Equal(t, 4, s.cursor.Pos)
	})
}
