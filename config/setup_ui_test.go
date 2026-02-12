package config

import (
	"maps"
	"testing"

	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildPresetItems(t *testing.T) {
	preChecked := map[string]bool{"default": true, "pkg-go": true}
	detected := []string{"pkg-go"}
	reg := proxy.NewPresetRegistry()

	items, checked := buildPresetItems(reg, preChecked, detected)

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
	reg := proxy.NewPresetRegistry()

	items, _ := buildPresetItems(reg, preChecked, detected)

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

	s := newPresetScreen(preChecked, detected)
	header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "setup"}
	w := tui.NewWindow(header, s)
	w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return s, w
}

func findLineByKind(s *presetScreen, kind lineKind) int {
	for i, l := range s.buildVisibleLines() {
		if l.kind == kind {
			return i
		}
	}
	return -1
}

func findPresetLine(s *presetScreen, name string) int {
	for i, l := range s.buildVisibleLines() {
		if l.kind == linePreset && s.items[l.itemIdx].presetName == name {
			return i
		}
	}
	return -1
}

func TestPresetScreen_InitialPosition(t *testing.T) {
	s, _ := makePresetTestSetup()
	assert.Equal(t, 0, s.Pos)
}

func TestPresetScreen_NavigationVisitsHeaders(t *testing.T) {
	s, w := makePresetTestSetup()
	s.Pos = 0

	lines := s.buildVisibleLines()
	var visitedHeader bool
	for range len(lines) + 5 {
		l := s.buildVisibleLines()
		if s.Pos < len(l) && l[s.Pos].kind == lineSection {
			visitedHeader = true
			break
		}
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}, w)
	}
	assert.True(t, visitedHeader, "should visit section headers")
}

func TestPresetScreen_NavigationVisitsAllVisibleLines(t *testing.T) {
	s, w := makePresetTestSetup()

	lines := s.buildVisibleLines()
	s.Pos = 0

	visited := make(map[int]bool)
	for range len(lines) + 5 {
		visited[s.Pos] = true
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}, w)
	}

	for i := range lines {
		assert.True(t, visited[i], "should visit line %d", i)
	}
}

func TestPresetScreen_GJumps(t *testing.T) {
	s, w := makePresetTestSetup()

	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}}, w)
	assert.Equal(t, s.ItemCount-1, s.Pos)

	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}}, w)
	assert.Equal(t, 0, s.Pos)
}

func TestPresetScreen_Toggle(t *testing.T) {
	s, w := makePresetTestSetup()

	idx := findLineByKind(s, linePreset)
	require.GreaterOrEqual(t, idx, 0)
	s.Pos = idx

	lines := s.buildVisibleLines()
	name := s.items[lines[s.Pos].itemIdx].presetName
	wasChecked := s.checked[name]

	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}, w)
	assert.NotEqual(t, wasChecked, s.checked[name])

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

	idx := findPresetLine(s, "anthropic")
	require.GreaterOrEqual(t, idx, 0)
	s.Pos = idx

	assert.False(t, s.checked["anthropic"])
	assert.NotEmpty(t, s.includedBy("anthropic"))
	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}, w)
	assert.False(t, s.checked["anthropic"], "should not toggle implicitly included preset")
	assert.Contains(t, w.Flash(), "included via default")
}

func TestPresetScreen_IncludedByTogglesWhenParentUnchecked(t *testing.T) {
	s, w := makePresetTestSetup()

	s.checked["default"] = false
	assert.Empty(t, s.includedBy("anthropic"))

	idx := findPresetLine(s, "anthropic")
	require.GreaterOrEqual(t, idx, 0)
	s.Pos = idx

	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}, w)
	assert.True(t, s.checked["anthropic"], "should toggle when parent unchecked")
}

func TestPresetScreen_ConfirmIncludesImplicit(t *testing.T) {
	s, w := makePresetTestSetup()

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

	footerDescs := func() []string {
		var descs []string
		for _, k := range s.FooterKeys(w) {
			descs = append(descs, k.Desc)
		}
		return descs
	}

	// On section header: no toggle hint.
	s.Pos = 0
	descs := footerDescs()
	assert.NotContains(t, descs, "toggle")
	assert.Contains(t, descs, "details")
	assert.Contains(t, descs, "confirm")
	assert.Contains(t, descs, "navigate")

	// On preset line: toggle hint visible.
	idx := findLineByKind(s, linePreset)
	require.GreaterOrEqual(t, idx, 0)
	s.Pos = idx
	descs = footerDescs()
	assert.Contains(t, descs, "toggle")

	status := s.FooterStatus(w)
	assert.Contains(t, status, "selected")
}

func TestPresetScreen_BuildVisibleLines(t *testing.T) {
	s, _ := makePresetTestSetup()

	lines := s.buildVisibleLines()

	assert.GreaterOrEqual(t, len(lines), len(s.items))
	assert.Equal(t, lineSection, lines[0].kind)

	var presetCount int
	for _, l := range lines {
		if l.kind == linePreset {
			presetCount++
		}
	}
	assert.Greater(t, presetCount, 0)

	for _, l := range lines {
		assert.NotEqual(t, lineDomain, l.kind, "no domains should be visible when nothing expanded")
		assert.NotEqual(t, lineSubGroup, l.kind, "no sub-groups should be visible when nothing expanded")
	}
}

func TestPresetScreen_ExpandPreset(t *testing.T) {
	s, w := makePresetTestSetup()

	lines := s.buildVisibleLines()
	for i, l := range lines {
		if l.kind == linePreset {
			s.Pos = i
			break
		}
	}

	s.Update(tea.KeyMsg{Type: tea.KeyRight}, w)
	name := s.items[lines[s.Pos].itemIdx].presetName
	assert.True(t, s.expanded[name], "preset should be expanded after right arrow")

	newLines := s.buildVisibleLines()
	var hasDomain bool
	for _, l := range newLines {
		if l.kind == lineDomain || l.kind == lineSubGroup {
			hasDomain = true
			break
		}
	}
	assert.True(t, hasDomain, "should have domain detail lines after expanding")

	s.Update(tea.KeyMsg{Type: tea.KeyLeft}, w)
	assert.False(t, s.expanded[name], "preset should be collapsed after left arrow")
}

func TestPresetScreen_LeftOnDomainCollapsesParent(t *testing.T) {
	s, w := makePresetTestSetup()

	// Find and expand pkg-go.
	idx := findPresetLine(s, "pkg-go")
	require.GreaterOrEqual(t, idx, 0)
	s.Pos = idx
	s.Update(tea.KeyMsg{Type: tea.KeyRight}, w)
	assert.True(t, s.expanded["pkg-go"])

	// Navigate to a domain line.
	lines := s.buildVisibleLines()
	var domainIdx int
	for i, l := range lines {
		if l.kind == lineDomain {
			domainIdx = i
			break
		}
	}
	require.Greater(t, domainIdx, 0)
	s.Pos = domainIdx

	// Press left on domain line.
	s.Update(tea.KeyMsg{Type: tea.KeyLeft}, w)

	// Preset should be collapsed and cursor should be on the preset row.
	assert.False(t, s.expanded["pkg-go"], "preset should collapse when left pressed on domain line")
	newLines := s.buildVisibleLines()
	require.Less(t, s.Pos, len(newLines))
	assert.Equal(t, linePreset, newLines[s.Pos].kind, "cursor should be on the preset row")
	assert.Equal(t, "pkg-go", s.items[newLines[s.Pos].itemIdx].presetName)
}

func TestPresetScreen_CollapseResetsOffset(t *testing.T) {
	// Use a small viewport so expanding forces scrolling.
	preChecked := map[string]bool{"default": true, "pkg-go": true}
	detected := []string{"pkg-go"}
	s := newPresetScreen(preChecked, detected)
	header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "setup"}
	w := tui.NewWindow(header, s)
	w.Update(tea.WindowSizeMsg{Width: 100, Height: 15}) // small viewport

	// Expand a preset with many domains.
	idx := findPresetLine(s, "pkg-go")
	require.GreaterOrEqual(t, idx, 0)
	s.Pos = idx
	s.Update(tea.KeyMsg{Type: tea.KeyRight}, w)

	// Scroll down past the start.
	for range 20 {
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}, w)
	}
	assert.Greater(t, s.Offset, 0, "should have scrolled down")

	// Collapse the preset.
	s.Pos = idx
	s.Update(tea.KeyMsg{Type: tea.KeyLeft}, w)

	// If everything fits, offset should be 0.
	lines := s.buildVisibleLines()
	if len(lines) <= s.VpHeight {
		assert.Equal(t, 0, s.Offset, "offset should reset when content fits in viewport")
	}
}

func TestPresetScreen_ExpandCollapseSection(t *testing.T) {
	s, w := makePresetTestSetup()

	lines := s.buildVisibleLines()
	assert.Equal(t, lineSection, lines[0].kind)

	sectionName := s.items[lines[0].itemIdx].section
	assert.True(t, s.expanded[sectionName])

	s.Pos = 0
	s.Update(tea.KeyMsg{Type: tea.KeyLeft}, w)
	assert.False(t, s.expanded[sectionName], "section should collapse on left arrow")

	collapsedLines := s.buildVisibleLines()
	assert.Less(t, len(collapsedLines), len(lines))

	s.Update(tea.KeyMsg{Type: tea.KeyRight}, w)
	assert.True(t, s.expanded[sectionName])
}

func TestPresetScreen_SpaceOnlyTogglesPresets(t *testing.T) {
	s, w := makePresetTestSetup()

	// Snapshot checked state before.
	checkedBefore := make(map[string]bool)
	maps.Copy(checkedBefore, s.checked)

	// Space on section header should not change checked state.
	s.Pos = 0
	lines := s.buildVisibleLines()
	assert.Equal(t, lineSection, lines[0].kind)
	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}, w)
	assert.Equal(t, checkedBefore, s.checked, "space on section header should not change checked state")

	// Expand a preset and navigate to a domain line.
	for i, l := range lines {
		if l.kind == linePreset {
			s.Pos = i
			break
		}
	}
	s.Update(tea.KeyMsg{Type: tea.KeyRight}, w)
	newLines := s.buildVisibleLines()
	for i, l := range newLines {
		if l.kind == lineDomain {
			s.Pos = i
			break
		}
	}

	// Snapshot again (expand may not have changed checked, but be safe).
	checkedBefore = make(map[string]bool)
	maps.Copy(checkedBefore, s.checked)

	// Space on domain line should not change checked state.
	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}, w)
	assert.Equal(t, checkedBefore, s.checked, "space on domain line should not change checked state")
}

func TestPresetScreen_ViewShowsExpandIndicators(t *testing.T) {
	s, w := makePresetTestSetup()

	view := s.View(w)
	assert.Contains(t, view, "▾")
	assert.Contains(t, view, "▸")
}

func TestPresetScreen_ViewShowsDomainsWhenExpanded(t *testing.T) {
	s, w := makePresetTestSetup()

	idx := findPresetLine(s, "pkg-go")
	require.GreaterOrEqual(t, idx, 0)
	s.Pos = idx
	s.expanded["pkg-go"] = true
	s.syncCursor()

	view := s.View(w)
	assert.Contains(t, view, "proxy.golang.org")
}

func TestPresetScreen_ViewShowsSubGroupsForMetaPreset(t *testing.T) {
	s, w := makePresetTestSetup()

	idx := findPresetLine(s, "default")
	require.GreaterOrEqual(t, idx, 0)
	s.Pos = idx
	s.expanded["default"] = true
	s.syncCursor()

	view := s.View(w)
	assert.Contains(t, view, "anthropic:")
	assert.Contains(t, view, "vcs-github:")
	assert.Contains(t, view, "api.anthropic.com")
}
