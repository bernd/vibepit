package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

// stubScreen verifies the Screen interface can be implemented.
type stubScreen struct {
	updated bool
}

func (s *stubScreen) Update(msg tea.Msg, w *Window) (Screen, tea.Cmd) {
	s.updated = true
	return s, nil
}
func (s *stubScreen) View(w *Window) string           { return "stub" }
func (s *stubScreen) FooterKeys(w *Window) []FooterKey { return nil }
func (s *stubScreen) FooterStatus(w *Window) string    { return "" }

// switchScreen returns a different Screen from Update.
type switchScreen struct {
	target Screen
}

func (s *switchScreen) Update(msg tea.Msg, w *Window) (Screen, tea.Cmd) {
	return s.target, nil
}
func (s *switchScreen) View(w *Window) string           { return "" }
func (s *switchScreen) FooterKeys(w *Window) []FooterKey { return nil }
func (s *switchScreen) FooterStatus(w *Window) string    { return "" }

func TestScreenInterface(t *testing.T) {
	var s Screen = &stubScreen{}
	assert.Equal(t, "stub", s.View(nil))
}

func TestWindow_Init(t *testing.T) {
	w := NewWindow(&HeaderInfo{ProjectDir: "/test", SessionID: "abc123"}, &stubScreen{})
	cmd := w.Init()
	assert.NotNil(t, cmd)
}

func TestWindow_WindowSizeMsg(t *testing.T) {
	s := &stubScreen{}
	w := NewWindow(&HeaderInfo{ProjectDir: "/test", SessionID: "abc123"}, s)
	updated, _ := w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	win := updated.(*Window)
	assert.Equal(t, 100, win.Width())
	assert.Equal(t, 40, win.Height())
	assert.Greater(t, win.VpHeight(), 0)
}

func TestWindow_TickIncrementsFrame(t *testing.T) {
	s := &stubScreen{}
	w := NewWindow(&HeaderInfo{ProjectDir: "/test", SessionID: "abc123"}, s)
	w.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	before := w.TickFrame()
	w.Update(TickMsg{})
	assert.Equal(t, before+1, w.TickFrame())
}

func TestWindow_Flash(t *testing.T) {
	s := &stubScreen{}
	w := NewWindow(&HeaderInfo{ProjectDir: "/test", SessionID: "abc123"}, s)
	w.SetFlash("hello")
	assert.Equal(t, "hello", w.Flash())
}

func TestWindow_ScreenSwitch(t *testing.T) {
	s2 := &switchScreen{target: &stubScreen{}}

	w := NewWindow(&HeaderInfo{ProjectDir: "/test", SessionID: "abc123"}, s2)
	w.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	updated, _ := w.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	win := updated.(*Window)
	assert.IsType(t, &stubScreen{}, win.screen)
}
