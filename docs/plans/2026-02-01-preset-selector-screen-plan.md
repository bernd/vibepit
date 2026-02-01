# Preset Selector Screen Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace the `huh.MultiSelect` preset selector with a TUI screen using the `tui.Screen`/`tui.Window` pattern, with grouped section headers and space-to-toggle multi-select.

**Architecture:** New `presetScreen` implements `tui.Screen`, embeds `tui.Cursor` for navigation. Items are a mixed slice of section headers (non-selectable) and preset entries (toggleable). Cursor navigation skips header rows. Runs as a standalone temporary TUI.

**Tech Stack:** Go, Bubbletea, Lipgloss, `proxy.PresetRegistry`

**Design doc:** `docs/plans/2026-02-01-preset-selector-screen-design.md`

---

### Task 1: Create presetItem type and item builder

**Files:**
- Create: `config/setup_ui.go`
- Create: `config/setup_ui_test.go`

**Step 1: Write the failing test**

```go
// config/setup_ui_test.go
package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildPresetItems(t *testing.T) {
	preChecked := map[string]bool{"default": true, "pkg-go": true}
	detected := []string{"pkg-go"}

	items, checked := buildPresetItems(preChecked, detected)

	// Should have section headers and preset entries.
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

	// pkg-go should appear in the Detected section, not Package Managers.
	// Find pkg-go's position and verify it's after "Detected" header.
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

	// Pre-checked state should match.
	assert.True(t, checked["default"])
	assert.True(t, checked["pkg-go"])
	assert.False(t, checked["pkg-node"])

	// Should have more than just the detected/default presets.
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

	// No "Detected" section when nothing detected.
	assert.NotContains(t, headers, "Detected")
	assert.Contains(t, headers, "Defaults")
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./config/ -run TestBuildPresetItems -v`
Expected: compile error — `buildPresetItems` not defined.

**Step 3: Write the implementation**

```go
// config/setup_ui.go
package config

import (
	"fmt"
	"strings"

	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// presetItem is either a section header or a toggleable preset entry.
type presetItem struct {
	isHeader    bool
	section     string // header text when isHeader
	presetName  string // e.g. "pkg-go"
	description string // e.g. "Go"
}

// buildPresetItems creates the ordered item list and initial checked state
// from the preset registry.
func buildPresetItems(preChecked map[string]bool, detected []string) ([]presetItem, map[string]bool) {
	reg := proxy.NewPresetRegistry()
	allPresets := reg.All()

	detectedSet := make(map[string]bool, len(detected))
	for _, d := range detected {
		detectedSet[d] = true
	}

	type entry struct {
		preset proxy.Preset
	}

	var detectedEntries, defaultEntries, pkgEntries, infraEntries []entry

	for _, p := range allPresets {
		if detectedSet[p.Name] {
			detectedEntries = append(detectedEntries, entry{preset: p})
		} else if p.Name == "default" {
			defaultEntries = append(defaultEntries, entry{preset: p})
		} else if p.Group == "Package Managers" {
			pkgEntries = append(pkgEntries, entry{preset: p})
		} else if p.Group == "Infrastructure" {
			infraEntries = append(infraEntries, entry{preset: p})
		}
	}

	var items []presetItem

	addSection := func(name string, entries []entry) {
		if len(entries) == 0 {
			return
		}
		items = append(items, presetItem{isHeader: true, section: name})
		for _, e := range entries {
			items = append(items, presetItem{
				presetName:  e.preset.Name,
				description: e.preset.Description,
			})
		}
	}

	addSection("Detected", detectedEntries)
	addSection("Defaults", defaultEntries)
	addSection("Package Managers", pkgEntries)
	addSection("Infrastructure", infraEntries)

	checked := make(map[string]bool)
	for k, v := range preChecked {
		checked[k] = v
	}

	return items, checked
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./config/ -run TestBuildPresetItems -v`
Expected: PASS

**Step 5: Commit**

```
git add config/setup_ui.go config/setup_ui_test.go
git commit -m "Add presetItem type and buildPresetItems builder"
```

---

### Task 2: Create presetScreen struct with cursor-skipping navigation

**Files:**
- Modify: `config/setup_ui.go`
- Modify: `config/setup_ui_test.go`

**Step 1: Write the failing tests**

Add to `config/setup_ui_test.go`:

```go
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
	// Should start on first selectable item, not a header.
	assert.False(t, s.items[s.Pos].isHeader)
}

func TestPresetScreen_NavigationSkipsHeaders(t *testing.T) {
	s, w := makePresetTestSetup()

	// Move down through items. Cursor should never land on a header.
	for i := 0; i < 20; i++ {
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}, w)
		if s.Pos < len(s.items) {
			assert.False(t, s.items[s.Pos].isHeader, "cursor on header at pos %d", s.Pos)
		}
	}

	// Move back up. Same rule.
	for i := 0; i < 20; i++ {
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}, w)
		assert.False(t, s.items[s.Pos].isHeader, "cursor on header at pos %d", s.Pos)
	}
}

func TestPresetScreen_GJumpsSkipHeaders(t *testing.T) {
	s, w := makePresetTestSetup()

	// G should land on last selectable item.
	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}}, w)
	assert.False(t, s.items[s.Pos].isHeader)
	assert.Equal(t, len(s.items)-1, s.Pos) // last item should be a preset, not header

	// g should land on first selectable item.
	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}}, w)
	assert.False(t, s.items[s.Pos].isHeader)
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./config/ -run TestPresetScreen -v`
Expected: compile error — `newPresetScreen` not defined.

**Step 3: Write the presetScreen struct with navigation**

Add to `config/setup_ui.go`:

```go
// presetScreen implements tui.Screen for multi-select preset selection.
type presetScreen struct {
	tui.Cursor
	items    []presetItem
	checked  map[string]bool
	selected []string // set on enter
}

func newPresetScreen(items []presetItem, checked map[string]bool) *presetScreen {
	s := &presetScreen{
		Cursor:  tui.Cursor{ItemCount: len(items)},
		items:   items,
		checked: checked,
	}
	// Start on first selectable item.
	s.skipToSelectable(1)
	return s
}

// skipToSelectable moves the cursor forward (dir=1) or backward (dir=-1)
// until it lands on a non-header item.
func (s *presetScreen) skipToSelectable(dir int) {
	for s.Pos >= 0 && s.Pos < len(s.items) && s.items[s.Pos].isHeader {
		s.Pos += dir
	}
	if s.Pos < 0 {
		s.Pos = 0
		s.skipToSelectable(1)
	}
	if s.Pos >= len(s.items) {
		s.Pos = len(s.items) - 1
		s.skipToSelectable(-1)
	}
}

func (s *presetScreen) Update(msg tea.Msg, w *tui.Window) (tui.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case " ":
			if s.Pos >= 0 && s.Pos < len(s.items) && !s.items[s.Pos].isHeader {
				name := s.items[s.Pos].presetName
				s.checked[name] = !s.checked[name]
			}
		case "enter":
			var selected []string
			for _, item := range s.items {
				if !item.isHeader && s.checked[item.presetName] {
					selected = append(selected, item.presetName)
				}
			}
			s.selected = selected
			return s, tea.Quit
		case "q", "ctrl+c":
			return s, tea.Quit
		default:
			oldPos := s.Pos
			if s.HandleKey(msg) {
				if s.Pos < len(s.items) && s.items[s.Pos].isHeader {
					dir := 1
					if s.Pos < oldPos {
						dir = -1
					}
					s.skipToSelectable(dir)
				}
				s.EnsureVisible()
			}
		}

	case tea.WindowSizeMsg:
		s.VpHeight = w.VpHeight()
		s.EnsureVisible()
	}

	return s, nil
}

func (s *presetScreen) View(w *tui.Window) string {
	var lines []string
	note := lipgloss.NewStyle().Foreground(tui.ColorField).
		Render("Select network presets. Space to toggle, Enter to confirm.")
	lines = append(lines, note, "")

	end := s.Offset + s.VpHeight
	end = min(end, len(s.items))
	for i := s.Offset; i < end; i++ {
		lines = append(lines, renderPresetLine(s.items[i], i == s.Pos, s.checked))
	}
	for len(lines) < s.VpHeight {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func renderPresetLine(item presetItem, highlighted bool, checked map[string]bool) string {
	if item.isHeader {
		header := fmt.Sprintf("── %s ──", item.section)
		return lipgloss.NewStyle().Foreground(tui.ColorField).Render(header)
	}

	base := lipgloss.NewStyle()
	marker := "  "
	if highlighted {
		base = base.Background(tui.ColorHighlight)
		marker = lipgloss.NewStyle().Foreground(tui.ColorCyan).Background(tui.ColorHighlight).Render("➔") + base.Render(" ")
	}

	checkbox := base.Foreground(tui.ColorField).Render("[ ]")
	if checked[item.presetName] {
		checkbox = base.Foreground(tui.ColorCyan).Render("[x]")
	}

	name := base.Foreground(tui.ColorCyan).Render(fmt.Sprintf("%-16s", item.presetName))
	desc := base.Foreground(tui.ColorField).Render(item.description)
	sp := base.Render(" ")

	return marker + checkbox + sp + name + sp + desc
}

func (s *presetScreen) FooterKeys(w *tui.Window) []tui.FooterKey {
	keys := []tui.FooterKey{
		{Key: "space", Desc: "toggle"},
		{Key: "enter", Desc: "confirm"},
	}
	keys = append(keys, s.Cursor.FooterKeys()...)
	return keys
}

func (s *presetScreen) FooterStatus(w *tui.Window) string {
	count := 0
	for _, v := range s.checked {
		if v {
			count++
		}
	}
	return lipgloss.NewStyle().Foreground(tui.ColorField).
		Render(fmt.Sprintf("%d selected", count))
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./config/ -run TestPresetScreen -v`
Expected: PASS

**Step 5: Commit**

```
git add config/setup_ui.go config/setup_ui_test.go
git commit -m "Add presetScreen with cursor-skipping navigation"
```

---

### Task 3: Test toggle, confirm, and view

**Files:**
- Modify: `config/setup_ui_test.go`

**Step 1: Write the tests**

Add to `config/setup_ui_test.go`:

```go
func TestPresetScreen_Toggle(t *testing.T) {
	s, w := makePresetTestSetup()

	// Find a pre-checked item and toggle it off.
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
	// Should contain the instruction note.
	assert.Contains(t, view, "Select network presets")
	// Should contain section headers.
	assert.Contains(t, view, "Detected")
	// Should contain preset names.
	assert.Contains(t, view, "pkg-go")
	assert.Contains(t, view, "default")
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
```

**Step 2: Run tests**

Run: `go test ./config/ -run TestPresetScreen -v`
Expected: all PASS.

**Step 3: Commit**

```
git add config/setup_ui_test.go
git commit -m "Add tests for preset toggle, confirm, view, and footer"
```

---

### Task 4: Wire presetScreen into runPresetSelector and remove huh

**Files:**
- Modify: `config/setup_ui.go`
- Modify: `config/setup.go:55-129`

**Step 1: Add runPresetSelectorTUI to setup_ui.go**

Append to `config/setup_ui.go`:

```go
// runPresetSelectorTUI runs the full-screen preset selector and returns the
// selected preset names. Returns nil if the user quit without confirming.
func runPresetSelectorTUI(preChecked map[string]bool, detected []string) ([]string, error) {
	items, checked := buildPresetItems(preChecked, detected)
	s := newPresetScreen(items, checked)
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

**Step 2: Replace runPresetSelector in setup.go**

Replace the entire `runPresetSelector` function (lines 55-129) in `config/setup.go` with:

```go
// runPresetSelector builds and runs the TUI preset selector. preChecked
// controls which options are initially selected; detected lists preset names
// that were auto-detected from the project directory.
func runPresetSelector(preChecked map[string]bool, detected []string) ([]string, error) {
	return runPresetSelectorTUI(preChecked, detected)
}
```

Remove the `"github.com/charmbracelet/huh"` import from `config/setup.go`. The import block should become:

```go
import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)
```

Note: `proxy` import is no longer needed in setup.go since `buildPresetItems` (in setup_ui.go) handles the registry. Check if `proxy` is used elsewhere in setup.go — it is not, so remove it.

**Step 3: Verify it compiles**

Run: `go build ./...`
Expected: success.

**Step 4: Run all tests**

Run: `go test ./...`
Expected: all PASS.

**Step 5: Commit**

```
git add config/setup_ui.go config/setup.go
git commit -m "Wire presetScreen into runPresetSelector, remove huh from setup.go"
```

---

## Summary of tasks

| Task | Description | Files |
|------|-------------|-------|
| 1 | `presetItem` type + `buildPresetItems` builder with tests | `config/setup_ui.go`, `config/setup_ui_test.go` |
| 2 | `presetScreen` struct with cursor-skipping nav, View, Footer | `config/setup_ui.go`, `config/setup_ui_test.go` |
| 3 | Tests for toggle, confirm, quit, view, footer | `config/setup_ui_test.go` |
| 4 | Wire into `runPresetSelector`, remove `huh` from setup.go | `config/setup_ui.go`, `config/setup.go` |
