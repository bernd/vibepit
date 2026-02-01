package config

import (
	"testing"

	"github.com/bernd/vibepit/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

func TestBuildPresetItems(t *testing.T) {
	preChecked := map[string]bool{"default": true, "pkg-go": true}
	detected := []string{"pkg-go"}

	items, checked := buildPresetItems(preChecked, detected)

	var headers []string
	var presets []string
	for _, item := range items {
		if item.isHeader {
			headers = append(headers, item.section)
		} else {
			presets = append(presets, item.presetName)
		}
	}

	assert.Contains(t, headers, "Detected")
	assert.Contains(t, headers, "Defaults")
	assert.Contains(t, headers, "Package Managers")
	assert.Contains(t, headers, "Infrastructure")

	// pkg-go should appear after Detected header.
	var detectedIdx, pkgGoIdx int
	for i, item := range items {
		if item.isHeader && item.section == "Detected" {
			detectedIdx = i
		}
		if item.presetName == "pkg-go" {
			pkgGoIdx = i
		}
	}
	assert.Greater(t, pkgGoIdx, detectedIdx)

	assert.True(t, checked["default"])
	assert.True(t, checked["pkg-go"])
	assert.False(t, checked["pkg-node"])
	assert.Greater(t, len(presets), 5)
}

func TestBuildPresetItems_NoDetected(t *testing.T) {
	preChecked := map[string]bool{"default": true}
	detected := []string{}

	items, _ := buildPresetItems(preChecked, detected)

	var headers []string
	for _, item := range items {
		if item.isHeader {
			headers = append(headers, item.section)
		}
	}

	assert.NotContains(t, headers, "Detected")
	assert.Contains(t, headers, "Defaults")
}

func makePresetTestSetup() (*presetScreen, *tui.Window) {
	preChecked := map[string]bool{"default": true, "pkg-go": true}
	detected := []string{"pkg-go"}
	items, checked := buildPresetItems(preChecked, detected)

	s := newPresetScreen(items, checked)
	header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "setup"}
	w := tui.NewWindow(header, s)
	w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return s, w
}

func TestPresetScreen_InitialPosition(t *testing.T) {
	s, _ := makePresetTestSetup()
	assert.False(t, s.items[s.Pos].isHeader)
}

func TestPresetScreen_NavigationSkipsHeaders(t *testing.T) {
	s, w := makePresetTestSetup()

	for range 20 {
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}, w)
		if s.Pos < len(s.items) {
			assert.False(t, s.items[s.Pos].isHeader, "cursor on header at pos %d", s.Pos)
		}
	}

	for range 20 {
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}, w)
		assert.False(t, s.items[s.Pos].isHeader, "cursor on header at pos %d", s.Pos)
	}
}

func TestPresetScreen_GJumpsSkipHeaders(t *testing.T) {
	s, w := makePresetTestSetup()

	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}}, w)
	assert.False(t, s.items[s.Pos].isHeader)

	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}}, w)
	assert.False(t, s.items[s.Pos].isHeader)
}

func TestPresetScreen_Toggle(t *testing.T) {
	s, w := makePresetTestSetup()

	name := s.items[s.Pos].presetName
	wasChecked := s.checked[name]

	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}, w)
	assert.NotEqual(t, wasChecked, s.checked[name])

	// Toggle again to restore.
	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}, w)
	assert.Equal(t, wasChecked, s.checked[name])
}

func TestPresetScreen_Confirm(t *testing.T) {
	s, w := makePresetTestSetup()

	_, cmd := s.Update(tea.KeyMsg{Type: tea.KeyEnter}, w)
	assert.NotNil(t, cmd, "enter should return quit cmd")
	assert.NotNil(t, s.selected)
	assert.Contains(t, s.selected, "default")
	assert.Contains(t, s.selected, "pkg-go")
}

func TestPresetScreen_QuitWithoutSelection(t *testing.T) {
	s, w := makePresetTestSetup()

	_, cmd := s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}, w)
	assert.NotNil(t, cmd, "q should return quit cmd")
	assert.Nil(t, s.selected)
}

func TestPresetScreen_View(t *testing.T) {
	_, w := makePresetTestSetup()
	view := w.View()
	assert.Contains(t, view, "Select network presets")
	assert.Contains(t, view, "Detected")
	assert.Contains(t, view, "pkg-go")
	assert.Contains(t, view, "default")
}

func TestPresetScreen_IncludedByBlocksToggle(t *testing.T) {
	s, w := makePresetTestSetup()

	// default is checked and includes anthropic+vcs-github.
	// Find anthropic in the item list and navigate to it.
	var anthropicIdx int
	for i, item := range s.items {
		if item.presetName == "anthropic" {
			anthropicIdx = i
			break
		}
	}
	s.Pos = anthropicIdx

	// anthropic should not be directly checked.
	assert.False(t, s.checked["anthropic"])

	// Trying to toggle should be blocked (included via default).
	assert.NotEmpty(t, s.includedBy("anthropic"))
	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}, w)
	assert.False(t, s.checked["anthropic"], "should not toggle implicitly included preset")

	// Flash should show the "included via" message.
	assert.Contains(t, w.Flash(), "included via default")
}

func TestPresetScreen_IncludedByTogglesWhenParentUnchecked(t *testing.T) {
	s, w := makePresetTestSetup()

	// Uncheck default.
	s.checked["default"] = false

	// Now anthropic should be freely toggleable.
	assert.Empty(t, s.includedBy("anthropic"))

	var anthropicIdx int
	for i, item := range s.items {
		if item.presetName == "anthropic" {
			anthropicIdx = i
			break
		}
	}
	s.Pos = anthropicIdx

	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}, w)
	assert.True(t, s.checked["anthropic"], "should toggle when parent unchecked")
}

func TestPresetScreen_ConfirmIncludesImplicit(t *testing.T) {
	s, w := makePresetTestSetup()

	// default is checked and includes anthropic and vcs-github.
	s.Update(tea.KeyMsg{Type: tea.KeyEnter}, w)

	assert.Contains(t, s.selected, "default")
	assert.Contains(t, s.selected, "anthropic", "implicitly included presets should be in selected")
	assert.Contains(t, s.selected, "vcs-github", "implicitly included presets should be in selected")
}

func TestPresetScreen_ViewShowsViaIndicator(t *testing.T) {
	_, w := makePresetTestSetup()
	view := w.View()
	assert.Contains(t, view, "via default")
}

func TestPresetScreen_Footer(t *testing.T) {
	s, w := makePresetTestSetup()

	keys := s.FooterKeys(w)
	var descs []string
	for _, k := range keys {
		descs = append(descs, k.Desc)
	}
	assert.Contains(t, descs, "toggle")
	assert.Contains(t, descs, "confirm")
	assert.Contains(t, descs, "navigate")

	status := s.FooterStatus(w)
	assert.Contains(t, status, "selected")
}
