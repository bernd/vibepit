package config

import (
	"fmt"
	"maps"
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
	includes    []string // other preset names this one includes (meta-presets only)
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
				includes:    e.preset.Includes,
			})
		}
	}

	addSection("Detected", detectedEntries)
	addSection("Defaults", defaultEntries)
	addSection("Package Managers", pkgEntries)
	addSection("Infrastructure", infraEntries)

	checked := make(map[string]bool, len(preChecked))
	maps.Copy(checked, preChecked)

	return items, checked
}

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

// includedBy returns the name of a checked meta-preset that includes the given
// preset, or "" if none does. This is used to show "(via default)" indicators.
func (s *presetScreen) includedBy(name string) string {
	for _, item := range s.items {
		if item.isHeader || !s.checked[item.presetName] {
			continue
		}
		for _, inc := range item.includes {
			if inc == name {
				return item.presetName
			}
		}
	}
	return ""
}

func (s *presetScreen) Update(msg tea.Msg, w *tui.Window) (tui.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case " ":
			if s.Pos >= 0 && s.Pos < len(s.items) && !s.items[s.Pos].isHeader {
				name := s.items[s.Pos].presetName
				if parent := s.includedBy(name); parent != "" {
					w.SetFlash(fmt.Sprintf("included via %s", parent))
				} else {
					s.checked[name] = !s.checked[name]
				}
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
					// Also include presets referenced by this meta-preset.
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
		via := ""
		if !s.items[i].isHeader {
			via = s.includedBy(s.items[i].presetName)
		}
		lines = append(lines, renderPresetLine(s.items[i], i == s.Pos, s.checked, via))
	}
	for len(lines) < s.VpHeight {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func renderPresetLine(item presetItem, highlighted bool, checked map[string]bool, via string) string {
	if item.isHeader {
		header := fmt.Sprintf("── %s ──", item.section)
		return lipgloss.NewStyle().Foreground(tui.ColorField).Render(header)
	}

	dimmed := via != ""

	base := lipgloss.NewStyle()
	marker := "  "
	if highlighted {
		base = base.Background(tui.ColorHighlight)
		marker = lipgloss.NewStyle().Foreground(tui.ColorCyan).Background(tui.ColorHighlight).Render("➔") + base.Render(" ")
	}

	if dimmed {
		checkbox := base.Faint(true).Render("[·]")
		name := base.Faint(true).Render(fmt.Sprintf("%-16s", item.presetName))
		desc := base.Faint(true).Render(fmt.Sprintf("%s (via %s)", item.description, via))
		sp := base.Render(" ")
		return marker + checkbox + sp + name + sp + desc
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
	seen := make(map[string]bool)
	for _, item := range s.items {
		if item.isHeader {
			continue
		}
		if s.checked[item.presetName] {
			seen[item.presetName] = true
			for _, inc := range item.includes {
				seen[inc] = true
			}
		}
	}
	return lipgloss.NewStyle().Foreground(tui.ColorField).
		Render(fmt.Sprintf("%d selected", len(seen)))
}

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
