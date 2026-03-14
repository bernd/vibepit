# Expandable Preset Details Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Let users expand/collapse presets and section headers in the network setup UI to see included domains.

**Architecture:** Replace the flat `items` slice rendering with a two-layer model: the `items` slice stays unchanged, but `View()` builds a `visibleLines` slice on each render that includes dynamically inserted domain detail lines. The cursor operates on visible line indices. An `expanded` map tracks which items are expanded.

**Tech Stack:** Go, Bubble Tea, Lipgloss, testify

---

### Task 1: Refactor cursor to operate on visible lines

Currently the cursor position maps 1:1 to `items` indices and `skipToSelectable` jumps over headers. We need a new model where the cursor moves through all visible lines (headers, presets, domain details) and the screen maps visible line index back to the underlying item when needed.

**Files:**
- Modify: `config/setup_ui.go:14-21` (presetItem struct)
- Modify: `config/setup_ui.go:79-96` (presetScreen struct and constructor)
- Modify: `config/setup_ui.go:98-112` (remove skipToSelectable)
- Test: `config/setup_ui_test.go`

**Step 1: Write the failing test for visible line building**

Add to `config/setup_ui_test.go`:

```go
func TestPresetScreen_BuildVisibleLines(t *testing.T) {
	s, _ := makePresetTestSetup()

	lines := s.buildVisibleLines()

	// Should have at least headers + presets.
	assert.Greater(t, len(lines), len(s.items))

	// First line should be a section header.
	assert.Equal(t, lineSection, lines[0].kind)

	// Should contain preset lines.
	var presetCount int
	for _, l := range lines {
		if l.kind == linePreset {
			presetCount++
		}
	}
	assert.Greater(t, presetCount, 0)

	// No domain lines when nothing is expanded.
	for _, l := range lines {
		assert.NotEqual(t, lineDomain, l.kind, "no domains should be visible when nothing expanded")
		assert.NotEqual(t, lineSubGroup, l.kind, "no sub-groups should be visible when nothing expanded")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./config/ -run TestPresetScreen_BuildVisibleLines -v`
Expected: FAIL (buildVisibleLines, lineSection, linePreset, lineDomain, lineSubGroup undefined)

**Step 3: Add visible line types and the `expanded` map to presetScreen**

In `config/setup_ui.go`, add line kind constants and the `visibleLine` struct after the `presetItem` struct:

```go
// lineKind identifies what a visible line represents.
type lineKind int

const (
	lineSection  lineKind = iota // section header (expandable)
	linePreset                   // preset row (expandable, toggleable)
	lineDomain                   // plain domain string (inert)
	lineSubGroup                 // "presetname:" sub-header in meta-preset detail (inert)
)

// visibleLine is one rendered row in the viewport. Built dynamically from
// items + expanded state on each render cycle.
type visibleLine struct {
	kind       lineKind
	itemIdx    int    // index into items slice (-1 for domain/sub-group lines)
	text       string // only used for domain/sub-group display text
	presetName string // set for lineDomain/lineSubGroup: which preset's details
}
```

Update `presetScreen`:

```go
type presetScreen struct {
	tui.Cursor
	items    []presetItem
	checked  map[string]bool
	expanded map[string]bool // key: section name or preset name
	registry *proxy.PresetRegistry
	selected []string
}
```

Update `newPresetScreen`:

```go
func newPresetScreen(items []presetItem, checked map[string]bool) *presetScreen {
	reg := proxy.NewPresetRegistry()

	// Sections default to expanded.
	expanded := make(map[string]bool)
	for _, item := range items {
		if item.isHeader {
			expanded[item.section] = true
		}
	}

	s := &presetScreen{
		items:    items,
		checked:  checked,
		expanded: expanded,
		registry: reg,
	}
	lines := s.buildVisibleLines()
	s.Cursor = tui.Cursor{ItemCount: len(lines)}
	return s
}
```

**Step 4: Implement `buildVisibleLines`**

```go
func (s *presetScreen) buildVisibleLines() []visibleLine {
	var lines []visibleLine
	var currentSection string

	for i, item := range s.items {
		if item.isHeader {
			currentSection = item.section
			lines = append(lines, visibleLine{kind: lineSection, itemIdx: i})
			continue
		}

		// Skip presets in collapsed sections.
		if !s.expanded[currentSection] {
			continue
		}

		lines = append(lines, visibleLine{kind: linePreset, itemIdx: i})

		// If this preset is expanded, add its domain details.
		if !s.expanded[item.presetName] {
			continue
		}

		if len(item.includes) > 0 {
			// Meta-preset: show sub-groups.
			for _, inc := range item.includes {
				p, ok := s.registry.Get(inc)
				if !ok {
					continue
				}
				lines = append(lines, visibleLine{
					kind:       lineSubGroup,
					itemIdx:    -1,
					text:       inc,
					presetName: item.presetName,
				})
				for _, d := range p.Domains {
					lines = append(lines, visibleLine{
						kind:       lineDomain,
						itemIdx:    -1,
						text:       d,
						presetName: inc,
					})
				}
			}
		} else {
			// Regular preset: show domains directly.
			p, ok := s.registry.Get(item.presetName)
			if !ok {
				continue
			}
			for _, d := range p.Domains {
				lines = append(lines, visibleLine{
					kind:       lineDomain,
					itemIdx:    -1,
					text:       d,
					presetName: item.presetName,
				})
			}
		}
	}

	return lines
}
```

**Step 5: Run test to verify it passes**

Run: `go test ./config/ -run TestPresetScreen_BuildVisibleLines -v`
Expected: PASS

**Step 6: Remove `skipToSelectable`**

Delete the `skipToSelectable` method (lines 98-112). It is no longer needed since the cursor can now land on any visible line. Also remove calls to it from `newPresetScreen` (already handled in step 3) and from `Update`.

**Step 7: Run all existing tests to check what breaks**

Run: `go test ./config/ -v`
Expected: Several tests fail because `Update` and `View` still use the old model. We'll fix these in the next tasks.

**Step 8: Commit**

```
feat(setup-ui): add visible line model and buildVisibleLines

Introduces lineKind, visibleLine, expanded map, and registry on
presetScreen. Sections default to expanded, presets to collapsed.
Removes skipToSelectable in preparation for new cursor model.
```

---

### Task 2: Rewrite Update to use visible lines

Replace the cursor navigation and key handling to work with the visible line model.

**Files:**
- Modify: `config/setup_ui.go:130-186` (Update method)
- Test: `config/setup_ui_test.go`

**Step 1: Write failing tests for expand/collapse**

Add to `config/setup_ui_test.go`:

```go
func TestPresetScreen_ExpandPreset(t *testing.T) {
	s, w := makePresetTestSetup()

	// Navigate to first preset line.
	lines := s.buildVisibleLines()
	for i, l := range lines {
		if l.kind == linePreset {
			s.Pos = i
			break
		}
	}

	// Press right to expand.
	s.Update(tea.KeyMsg{Type: tea.KeyRight}, w)
	name := s.items[lines[s.Pos].itemIdx].presetName
	assert.True(t, s.expanded[name], "preset should be expanded after right arrow")

	// Should now have domain lines.
	newLines := s.buildVisibleLines()
	var hasDomain bool
	for _, l := range newLines {
		if l.kind == lineDomain || l.kind == lineSubGroup {
			hasDomain = true
			break
		}
	}
	assert.True(t, hasDomain, "should have domain detail lines after expanding")

	// Press left to collapse.
	s.Update(tea.KeyMsg{Type: tea.KeyLeft}, w)
	assert.False(t, s.expanded[name], "preset should be collapsed after left arrow")
}

func TestPresetScreen_ExpandCollapseSection(t *testing.T) {
	s, w := makePresetTestSetup()

	// First visible line should be a section header.
	lines := s.buildVisibleLines()
	assert.Equal(t, lineSection, lines[0].kind)

	sectionName := s.items[lines[0].itemIdx].section

	// Section starts expanded.
	assert.True(t, s.expanded[sectionName])

	// Navigate to header and collapse.
	s.Pos = 0
	s.Update(tea.KeyMsg{Type: tea.KeyLeft}, w)
	assert.False(t, s.expanded[sectionName], "section should collapse on left arrow")

	// Visible lines should have fewer entries.
	collapsedLines := s.buildVisibleLines()
	assert.Less(t, len(collapsedLines), len(lines))

	// Expand again.
	s.Update(tea.KeyMsg{Type: tea.KeyRight}, w)
	assert.True(t, s.expanded[sectionName])
}

func TestPresetScreen_NavigationVisitsAllVisibleLines(t *testing.T) {
	s, w := makePresetTestSetup()

	lines := s.buildVisibleLines()
	s.Pos = 0

	// Navigate down through all lines.
	visited := make(map[int]bool)
	for range len(lines) + 5 {
		visited[s.Pos] = true
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}, w)
	}

	// Should have visited every line index.
	for i := range lines {
		assert.True(t, visited[i], "should visit line %d", i)
	}
}

func TestPresetScreen_SpaceOnlyTogglesPresets(t *testing.T) {
	s, w := makePresetTestSetup()

	// Move to section header.
	s.Pos = 0
	lines := s.buildVisibleLines()
	assert.Equal(t, lineSection, lines[0].kind)

	// Space on header should do nothing.
	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}, w)
	// No crash, no change.

	// Expand a preset and move to a domain line.
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

	// Space on domain line should do nothing.
	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}, w)
	// No crash.
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./config/ -run "TestPresetScreen_Expand|TestPresetScreen_NavigationVisits|TestPresetScreen_SpaceOnly" -v`
Expected: FAIL

**Step 3: Rewrite the Update method**

Replace the entire `Update` method in `config/setup_ui.go`:

```go
func (s *presetScreen) Update(msg tea.Msg, w *tui.Window) (tui.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		lines := s.buildVisibleLines()

		switch msg.String() {
		case " ":
			if s.Pos >= 0 && s.Pos < len(lines) && lines[s.Pos].kind == linePreset {
				item := s.items[lines[s.Pos].itemIdx]
				if parent := s.includedBy(item.presetName); parent != "" {
					w.SetFlash(fmt.Sprintf("included via %s", parent))
				} else {
					s.checked[item.presetName] = !s.checked[item.presetName]
				}
			}

		case "right", "l":
			if s.Pos >= 0 && s.Pos < len(lines) {
				l := lines[s.Pos]
				switch l.kind {
				case lineSection:
					s.expanded[s.items[l.itemIdx].section] = true
				case linePreset:
					s.expanded[s.items[l.itemIdx].presetName] = true
				}
				s.syncCursor()
			}

		case "left", "h":
			if s.Pos >= 0 && s.Pos < len(lines) {
				l := lines[s.Pos]
				switch l.kind {
				case lineSection:
					s.expanded[s.items[l.itemIdx].section] = false
				case linePreset:
					s.expanded[s.items[l.itemIdx].presetName] = false
				}
				s.syncCursor()
			}

		case "enter":
			var selected []string
			seen := make(map[string]bool)
			for _, item := range s.items {
				if item.isHeader {
					continue
				}
				if s.checked[item.presetName] && !seen[item.presetName] {
					seen[item.presetName] = true
					selected = append(selected, item.presetName)
					for _, inc := range item.includes {
						if !seen[inc] {
							seen[inc] = true
							selected = append(selected, inc)
						}
					}
				}
			}
			s.selected = selected
			return s, tea.Quit

		case "q", "ctrl+c":
			return s, tea.Quit

		default:
			if s.HandleKey(msg) {
				s.EnsureVisible()
			}
		}

	case tea.WindowSizeMsg:
		s.VpHeight = w.VpHeight()
		s.syncCursor()
	}

	return s, nil
}

// syncCursor updates ItemCount from the current visible lines and clamps
// the cursor position.
func (s *presetScreen) syncCursor() {
	lines := s.buildVisibleLines()
	s.ItemCount = len(lines)
	if s.Pos >= s.ItemCount {
		s.Pos = s.ItemCount - 1
	}
	if s.Pos < 0 {
		s.Pos = 0
	}
	s.EnsureVisible()
}
```

**Step 4: Run all tests**

Run: `go test ./config/ -run "TestPresetScreen_Expand|TestPresetScreen_NavigationVisits|TestPresetScreen_SpaceOnly" -v`
Expected: PASS

**Step 5: Commit**

```
feat(setup-ui): rewrite Update for visible line cursor model

Cursor now moves through all visible lines. Left/right expands and
collapses sections and presets. Space only toggles on preset lines.
syncCursor keeps ItemCount in sync after expand/collapse changes.
```

---

### Task 3: Rewrite View to render expandable lines

Replace the `View` and `renderPresetLine` functions to render from visible lines with expand/collapse indicators.

**Files:**
- Modify: `config/setup_ui.go:188-242` (View and renderPresetLine)
- Test: `config/setup_ui_test.go`

**Step 1: Write failing tests for expand indicators in view**

Add to `config/setup_ui_test.go`:

```go
func TestPresetScreen_ViewShowsExpandIndicators(t *testing.T) {
	s, w := makePresetTestSetup()

	view := s.View(w)

	// Sections are expanded by default, should show ▾.
	assert.Contains(t, view, "▾")

	// Presets are collapsed by default, should show ▸.
	assert.Contains(t, view, "▸")
}

func TestPresetScreen_ViewShowsDomainsWhenExpanded(t *testing.T) {
	s, w := makePresetTestSetup()

	// Find and expand pkg-go.
	lines := s.buildVisibleLines()
	for i, l := range lines {
		if l.kind == linePreset {
			item := s.items[l.itemIdx]
			if item.presetName == "pkg-go" {
				s.Pos = i
				s.expanded["pkg-go"] = true
				break
			}
		}
	}
	s.syncCursor()

	view := s.View(w)
	assert.Contains(t, view, "proxy.golang.org")
}

func TestPresetScreen_ViewShowsSubGroupsForMetaPreset(t *testing.T) {
	s, w := makePresetTestSetup()

	// Find and expand default (meta-preset).
	lines := s.buildVisibleLines()
	for i, l := range lines {
		if l.kind == linePreset {
			item := s.items[l.itemIdx]
			if item.presetName == "default" {
				s.Pos = i
				s.expanded["default"] = true
				break
			}
		}
	}
	s.syncCursor()

	view := s.View(w)
	assert.Contains(t, view, "anthropic:")
	assert.Contains(t, view, "vcs-github:")
	assert.Contains(t, view, "api.anthropic.com")
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./config/ -run "TestPresetScreen_ViewShows" -v`
Expected: FAIL

**Step 3: Rewrite View and rendering**

Replace `View` and `renderPresetLine` in `config/setup_ui.go`:

```go
func (s *presetScreen) View(w *tui.Window) string {
	var out []string
	note := lipgloss.NewStyle().Foreground(tui.ColorField).
		Render("Select network presets. Space to toggle, Enter to confirm.")
	out = append(out, note, "")

	lines := s.buildVisibleLines()
	end := min(s.Offset+s.VpHeight, len(lines))
	for i := s.Offset; i < end; i++ {
		out = append(out, s.renderLine(lines[i], i == s.Pos))
	}
	for len(out) < s.VpHeight {
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}

func (s *presetScreen) renderLine(l visibleLine, highlighted bool) string {
	switch l.kind {
	case lineSection:
		return s.renderSectionLine(l, highlighted)
	case linePreset:
		return s.renderPresetLine(l, highlighted)
	case lineSubGroup:
		return s.renderSubGroupLine(l, highlighted)
	case lineDomain:
		return s.renderDomainLine(l, highlighted)
	}
	return ""
}

func (s *presetScreen) renderSectionLine(l visibleLine, highlighted bool) string {
	item := s.items[l.itemIdx]
	arrow := "▾"
	if !s.expanded[item.section] {
		arrow = "▸"
	}

	base := lipgloss.NewStyle()
	marker := "  "
	if highlighted {
		base = base.Background(tui.ColorHighlight)
		marker = lipgloss.NewStyle().Foreground(tui.ColorCyan).Background(tui.ColorHighlight).Render("➔") + base.Render(" ")
	}

	header := base.Foreground(tui.ColorField).Render(fmt.Sprintf("%s ── %s ──", arrow, item.section))
	return marker + header
}

func (s *presetScreen) renderPresetLine(l visibleLine, highlighted bool) string {
	item := s.items[l.itemIdx]
	via := s.includedBy(item.presetName)
	dimmed := via != ""

	arrow := "▸"
	if s.expanded[item.presetName] {
		arrow = "▾"
	}

	base := lipgloss.NewStyle()
	marker := "  "
	if highlighted {
		base = base.Background(tui.ColorHighlight)
		marker = lipgloss.NewStyle().Foreground(tui.ColorCyan).Background(tui.ColorHighlight).Render("➔") + base.Render(" ")
	}

	arrowStyle := base.Foreground(tui.ColorField)
	sp := base.Render(" ")

	if dimmed {
		checkbox := base.Faint(true).Render("[·]")
		name := base.Faint(true).Render(fmt.Sprintf("%-16s", item.presetName))
		desc := base.Faint(true).Render(fmt.Sprintf("%s (via %s)", item.description, via))
		return marker + arrowStyle.Render(arrow) + sp + checkbox + sp + name + sp + desc
	}

	checkbox := base.Foreground(tui.ColorField).Render("[ ]")
	if s.checked[item.presetName] {
		checkbox = base.Foreground(tui.ColorCyan).Render("[x]")
	}

	name := base.Foreground(tui.ColorCyan).Render(fmt.Sprintf("%-16s", item.presetName))
	desc := base.Foreground(tui.ColorField).Render(item.description)

	return marker + arrowStyle.Render(arrow) + sp + checkbox + sp + name + sp + desc
}

func (s *presetScreen) renderSubGroupLine(l visibleLine, highlighted bool) string {
	base := lipgloss.NewStyle()
	marker := "  "
	if highlighted {
		base = base.Background(tui.ColorHighlight)
		marker = lipgloss.NewStyle().Foreground(tui.ColorCyan).Background(tui.ColorHighlight).Render("➔") + base.Render(" ")
	}

	indent := base.Render("        ")
	text := base.Foreground(tui.ColorField).Render(l.text + ":")
	return marker + indent + text
}

func (s *presetScreen) renderDomainLine(l visibleLine, highlighted bool) string {
	base := lipgloss.NewStyle()
	marker := "  "
	if highlighted {
		base = base.Background(tui.ColorHighlight)
		marker = lipgloss.NewStyle().Foreground(tui.ColorCyan).Background(tui.ColorHighlight).Render("➔") + base.Render(" ")
	}

	indent := base.Render("          ")
	text := base.Faint(true).Render(l.text)
	return marker + indent + text
}
```

Also delete the old standalone `renderPresetLine` function (lines 209-242).

**Step 4: Run tests**

Run: `go test ./config/ -run "TestPresetScreen_ViewShows" -v`
Expected: PASS

**Step 5: Commit**

```
feat(setup-ui): render expandable lines with ▸/▾ indicators

Sections show ▾ when expanded, ▸ when collapsed. Presets show the
same indicators. Expanded presets show domain lists inline; meta-presets
show sub-group headers with their included preset domains.
```

---

### Task 4: Update footer and fix all existing tests

Update the footer to include the expand/collapse key hint, and fix any existing tests that broke due to the refactor.

**Files:**
- Modify: `config/setup_ui.go:244-268` (FooterKeys, FooterStatus)
- Modify: `config/setup_ui_test.go` (fix broken tests)

**Step 1: Update FooterKeys**

In `config/setup_ui.go`, update `FooterKeys`:

```go
func (s *presetScreen) FooterKeys(w *tui.Window) []tui.FooterKey {
	keys := []tui.FooterKey{
		{Key: "space", Desc: "toggle"},
		{Key: "←/→", Desc: "details"},
		{Key: "enter", Desc: "confirm"},
	}
	keys = append(keys, s.Cursor.FooterKeys()...)
	return keys
}
```

**Step 2: Update existing tests**

Several existing tests need updating:

- `TestPresetScreen_InitialPosition`: Cursor starts at 0 now (section header), not first preset. Update assertion.
- `TestPresetScreen_NavigationSkipsHeaders`: Navigation no longer skips headers. Replace with a test that verifies cursor visits headers.
- `TestPresetScreen_GJumpsSkipHeaders`: G/g no longer skip headers. Update to just verify cursor stays in bounds.
- `TestPresetScreen_Toggle`: Must navigate to a preset line first.
- `TestPresetScreen_IncludedByBlocksToggle`: Must find anthropic by iterating visible lines.
- `TestPresetScreen_IncludedByTogglesWhenParentUnchecked`: Same.
- `TestPresetScreen_View`: Check for `▾` instead of raw section names since rendering changed.
- `TestPresetScreen_Footer`: Check for "details" key hint.

Replace the full test file with updated tests:

```go
package config

import (
	"testing"

	"github.com/bernd/vibepit/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// findLineByKind returns the visible line index of the first line with the
// given kind, or -1 if not found.
func findLineByKind(s *presetScreen, kind lineKind) int {
	for i, l := range s.buildVisibleLines() {
		if l.kind == kind {
			return i
		}
	}
	return -1
}

// findPresetLine returns the visible line index of the named preset, or -1.
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
	// Cursor starts at 0 (first visible line, which is a section header).
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

	keys := s.FooterKeys(w)
	var descs []string
	for _, k := range keys {
		descs = append(descs, k.Desc)
	}
	assert.Contains(t, descs, "toggle")
	assert.Contains(t, descs, "details")
	assert.Contains(t, descs, "confirm")
	assert.Contains(t, descs, "navigate")

	status := s.FooterStatus(w)
	assert.Contains(t, status, "selected")
}

func TestPresetScreen_BuildVisibleLines(t *testing.T) {
	s, _ := makePresetTestSetup()

	lines := s.buildVisibleLines()

	assert.Greater(t, len(lines), len(s.items))
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

	s.Pos = 0
	lines := s.buildVisibleLines()
	assert.Equal(t, lineSection, lines[0].kind)

	// Space on header should do nothing (no panic).
	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}, w)

	// Expand a preset and move to a domain line.
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

	// Space on domain line should do nothing (no panic).
	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}, w)
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
```

**Step 3: Run all tests**

Run: `go test ./config/ -v`
Expected: ALL PASS

**Step 4: Commit**

```
feat(setup-ui): update footer with details hint and fix all tests

Adds ←/→ details to footer key hints. Rewrites all tests to work
with the new visible line cursor model where headers are visitable.
```

---

### Task 5: Update `buildPresetItems` to pass registry and clean up

The `buildPresetItems` function currently creates a new `PresetRegistry` internally. Since `presetScreen` now holds its own registry, we should avoid creating it twice. Also remove the old standalone `renderPresetLine` function if not already deleted.

**Files:**
- Modify: `config/setup_ui.go:23-77` (buildPresetItems)
- Modify: `config/setup_ui.go:272-285` (runPresetSelectorTUI)

**Step 1: Pass registry into `buildPresetItems`**

```go
func buildPresetItems(reg *proxy.PresetRegistry, preChecked map[string]bool, detected []string) ([]presetItem, map[string]bool) {
	allPresets := reg.All()
	// ... rest unchanged
}
```

Update `newPresetScreen` to create the registry first and pass it:

```go
func newPresetScreen(preChecked map[string]bool, detected []string) *presetScreen {
	reg := proxy.NewPresetRegistry()
	items, checked := buildPresetItems(reg, preChecked, detected)

	expanded := make(map[string]bool)
	for _, item := range items {
		if item.isHeader {
			expanded[item.section] = true
		}
	}

	s := &presetScreen{
		items:    items,
		checked:  checked,
		expanded: expanded,
		registry: reg,
	}
	lines := s.buildVisibleLines()
	s.Cursor = tui.Cursor{ItemCount: len(lines)}
	return s
}
```

Update `runPresetSelectorTUI`:

```go
func runPresetSelectorTUI(preChecked map[string]bool, detected []string) ([]string, error) {
	s := newPresetScreen(preChecked, detected)
	header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "setup"}
	w := tui.NewWindow(header, s)
	p := tea.NewProgram(w, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return nil, fmt.Errorf("preset selector: %w", err)
	}
	if s.selected == nil {
		return nil, fmt.Errorf("no presets selected")
	}
	return s.selected, nil
}
```

Update `makePresetTestSetup` in tests:

```go
func makePresetTestSetup() (*presetScreen, *tui.Window) {
	preChecked := map[string]bool{"default": true, "pkg-go": true}
	detected := []string{"pkg-go"}

	s := newPresetScreen(preChecked, detected)
	header := &tui.HeaderInfo{ProjectDir: "vibepit", SessionID: "setup"}
	w := tui.NewWindow(header, s)
	w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return s, w
}
```

**Step 2: Run all tests**

Run: `go test ./config/ -v`
Expected: ALL PASS

**Step 3: Run full project build check**

Run: `go build ./...`
Expected: No errors

**Step 4: Commit**

```
refactor(setup-ui): consolidate registry creation in newPresetScreen

Avoids creating PresetRegistry twice by passing it through from
newPresetScreen to buildPresetItems. Simplifies the constructor API.
```
