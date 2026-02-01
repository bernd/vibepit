# Expandable Preset Details in Network Setup UI

## Problem

Users selecting network presets cannot see which domains a preset includes.
They must guess whether a preset covers their needs or inspect the YAML source.

## Solution

Add expand/collapse to the preset selector. Left/right arrow keys toggle
detail visibility on both section headers and preset rows. All lines
(headers, presets, domain details) are visitable by the cursor. Actions
(toggle, expand/collapse) only fire on the appropriate line types.

## Visual Design

### Collapsed preset (default)

```
  ▸ [x] pkg-go           Go
```

### Expanded regular preset (has own domains)

```
  ▾ [x] pkg-go           Go
         proxy.golang.org:443
         sum.golang.org:443
         pkg.go.dev:443
```

### Expanded meta-preset (includes other presets)

```
  ▾ [x] default           Anthropic services and GitHub
         anthropic:
           api.anthropic.com:443
           statsig.anthropic.com:443
         vcs-github:
           github.com:443
           api.github.com:443
```

### Collapsed section header

```
▸ ── Infrastructure ──
```

### Expanded section header (default)

```
▾ ── Detected ──
  ▸ [x] pkg-go           Go
  ▸ [x] pkg-node          Node.js
```

## Interaction

- **Right arrow** on a section header or preset: expand (show children/domains)
- **Left arrow** on a section header or preset: collapse (hide children/domains)
- **Up/down arrows**: move cursor through all visible lines (headers, presets,
  domain details). Domain detail lines are visitable for scrolling but inert
  (space/enter/left/right do nothing on them).
- **Space**: toggle checkbox (only on preset lines)
- Section headers default to expanded; presets default to collapsed
- Collapsing a section hides all its presets and their domain details
- Expanding/collapsing works regardless of dimmed/checked state
- Footer key hints include `←/→ details`

## State

`presetScreen` gains an `expanded map[string]bool` field. Keys are section
names for headers and preset names for presets. Sections start expanded;
presets start collapsed.

## View Rendering

`View()` builds visible lines dynamically from the `items` slice:

1. For each section header: emit header line with `▾`/`▸`. If collapsed,
   skip all items until the next header.
2. For each preset (if section is expanded): emit preset line with `▾`/`▸`.
   If expanded, emit domain detail lines:
   - Regular presets: indented domain list
   - Meta-presets: sub-group headers (included preset name + colon) followed
     by indented domains for each included preset

## Cursor and Viewport

The cursor moves through all visible lines. `ItemCount` is recomputed after
each expand/collapse toggle. `EnsureVisible()` is called after each toggle
to keep the cursor in the viewport. Domain detail lines and section headers
are visitable but only preset lines respond to space; only presets and headers
respond to left/right.

## Files to Change

- **`config/setup_ui.go`**: Add `expanded` map, handle left/right in
  `Update()`, build visible lines in `View()`, update `FooterKeys()`,
  adjust cursor/viewport math
- **`proxy/presets.go`**: No changes needed (`Registry.Get()` already
  provides access to `Preset.Domains` and `Preset.Includes`)
