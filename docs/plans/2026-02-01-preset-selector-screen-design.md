# Preset Selector Screen Design

## Goal

Replace the `charmbracelet/huh` multi-select in `runPresetSelector()` with a
full TUI screen using the `tui.Screen`/`tui.Window` pattern. Provides visual
consistency with the session selector and monitor, supports grouped section
headers, and uses the same keyboard navigation.

## Behavior

The preset selector is a standalone temporary TUI (same pattern as
`selectSession`). It runs during first-time setup or `--reconfigure`, before
any session exists.

- **Space** toggles a preset on/off
- **Enter** confirms selection and exits
- **q/ctrl+c** quits without selection
- **j/k/arrows** navigate, automatically skipping section headers

## Item Model

A single `[]presetItem` slice with mixed section headers and preset entries:

```go
type presetItem struct {
    isHeader    bool
    section     string  // header text (when isHeader)
    presetName  string  // e.g. "pkg-go"
    description string  // e.g. "Go"
}
```

## Sections

Items are ordered by group with non-selectable section header rows:

1. **Detected** — auto-detected presets (only if any detected)
2. **Defaults** — the "default" meta-preset
3. **Package Managers** — remaining package manager presets
4. **Infrastructure** — infrastructure presets

Section headers render as `── Detected ──` in field color.

## Architecture

### presetScreen (config/setup_ui.go)

Implements `tui.Screen`. Embeds `tui.Cursor` for navigation.

```go
type presetScreen struct {
    tui.Cursor
    items    []presetItem
    checked  map[string]bool
    selected []string  // set on enter
}
```

- `Update`: space toggles `checked[name]`, enter collects checked names into
  `selected` and returns `tea.Quit`. Navigation wraps cursor to skip headers.
- `View`: note line at top, then visible items — headers in field color,
  presets with `[x]`/`[ ]` checkbox + name + description.
- `FooterKeys`: space/toggle, enter/confirm, plus cursor nav keys.
- `FooterStatus`: "N selected" count.

### Cursor skipping

After any cursor movement, if the cursor lands on a header row, advance one
more step in the same direction. The reusable `tui.Cursor` stays unchanged.

### Data flow

```
RunFirstTimeSetup / RunReconfigure
  → builds preChecked map + detected list
  → calls runPresetSelector(preChecked, detected)
    → builds ordered presetItem slice with section headers
    → creates presetScreen{items, checked}
    → tui.NewWindow(header, screen)
    → tea.NewProgram runs
    → returns screen.selected []string
  → writes project config
```

### Header

Full vibepit wordmark header with placeholder session info (same as session
selector standalone mode).

## Files

| File | Change |
|------|--------|
| `config/setup_ui.go` | New — `presetScreen`, `presetItem`, rendering, `runPresetSelector` TUI |
| `config/setup_ui_test.go` | New — tests |
| `config/setup.go` | Modify — `runPresetSelector` calls new TUI, remove `huh` import |

## Testing

- Navigation: j/k skip headers, G/g land on selectable items
- Toggle: space toggles checked state, headers not toggleable
- Confirm: enter collects checked names, quits
- Quit: q sets selected to nil
- View: contains section headers, preset names, checkboxes
- Footer: shows "N selected", toggle/confirm keys
- Pre-checked: initial state renders correctly
