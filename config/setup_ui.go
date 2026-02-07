package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// presetItem is either a section header or a toggleable preset entry.
type presetItem struct {
	isHeader    bool
	section     string   // header text when isHeader
	presetName  string   // e.g. "pkg-go"
	description string   // e.g. "Go"
	includes    []string // other preset names this one includes (meta-presets only)
}

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

// buildPresetItems creates the ordered item list and initial checked state
// from the preset registry.
func buildPresetItems(reg *proxy.PresetRegistry, preChecked map[string]bool, detected []string) ([]presetItem, map[string]bool) {
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
	expanded map[string]bool // key: section name or preset name
	registry *proxy.PresetRegistry
	selected []string
}

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

func (s *presetScreen) buildVisibleLines() []visibleLine {
	var lines []visibleLine
	var currentSection string

	for i, item := range s.items {
		if item.isHeader {
			currentSection = item.section
			lines = append(lines, visibleLine{kind: lineSection, itemIdx: i})
			continue
		}

		if !s.expanded[currentSection] {
			continue
		}

		lines = append(lines, visibleLine{kind: linePreset, itemIdx: i})

		if !s.expanded[item.presetName] {
			continue
		}

		if len(item.includes) > 0 {
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

// includedBy returns the name of a checked meta-preset that includes the given
// preset, or "" if none does. This is used to show "(via default)" indicators.
func (s *presetScreen) includedBy(name string) string {
	for _, item := range s.items {
		if item.isHeader || !s.checked[item.presetName] {
			continue
		}
		if slices.Contains(item.includes, name) {
			return item.presetName
		}
	}
	return ""
}

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
				case lineDomain, lineSubGroup:
					// Find parent preset by scanning backwards.
					for j := s.Pos - 1; j >= 0; j-- {
						if lines[j].kind == linePreset {
							s.expanded[s.items[lines[j].itemIdx].presetName] = false
							s.Pos = j
							break
						}
					}
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
		s.VpHeight = max(w.VpHeight()-2, 1) // 2 lines reserved for instruction note
		s.syncCursor()
	}

	return s, nil
}

func (s *presetScreen) syncCursor() {
	lines := s.buildVisibleLines()
	s.ItemCount = len(lines)
	if s.Pos >= s.ItemCount {
		s.Pos = s.ItemCount - 1
	}
	if s.Pos < 0 {
		s.Pos = 0
	}
	// Pull offset back when content no longer fills the viewport.
	if maxOffset := s.ItemCount - s.VpHeight; maxOffset > 0 {
		if s.Offset > maxOffset {
			s.Offset = maxOffset
		}
	} else {
		s.Offset = 0
	}
	s.EnsureVisible()
}

func (s *presetScreen) View(w *tui.Window) string {
	note := lipgloss.NewStyle().Foreground(tui.ColorField).
		Render("Select network presets. Space to toggle, Enter to confirm.")

	var content []string
	lines := s.buildVisibleLines()
	end := min(s.Offset+s.VpHeight, len(lines))
	for i := s.Offset; i < end; i++ {
		content = append(content, s.renderLine(lines[i], i == s.Pos))
	}
	for len(content) < s.VpHeight {
		content = append(content, "")
	}

	return note + "\n\n" + strings.Join(content, "\n")
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

	base, marker := tui.LineStyle(highlighted)

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

	base, marker := tui.LineStyle(highlighted)

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
	base, marker := tui.LineStyle(highlighted)

	indent := base.Render("        ")
	text := base.Foreground(tui.ColorField).Render(l.text + ":")
	return marker + indent + text
}

func (s *presetScreen) renderDomainLine(l visibleLine, highlighted bool) string {
	base, marker := tui.LineStyle(highlighted)

	indent := base.Render("          ")
	text := base.Faint(true).Render(l.text)
	return marker + indent + text
}

func (s *presetScreen) FooterKeys(w *tui.Window) []tui.FooterKey {
	var keys []tui.FooterKey
	lines := s.buildVisibleLines()
	if s.Pos >= 0 && s.Pos < len(lines) && lines[s.Pos].kind == linePreset {
		keys = append(keys, tui.FooterKey{Key: "space", Desc: "toggle"})
	}
	keys = append(keys,
		tui.FooterKey{Key: "←/→", Desc: "details"},
		tui.FooterKey{Key: "enter", Desc: "confirm"},
	)
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
