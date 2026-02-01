# BubbleTea Code Review

Reviewed: 2026-02-01

## Architecture Overview

The TUI follows a `Window` -> `Screen` delegation pattern. `Window`
(`tui/window.go`) is the top-level `tea.Model` that owns the shared frame
(header, footer, tick, flash/error) and delegates content to a polymorphic
`Screen` interface. Three screens implement this: `monitorScreen`,
`sessionScreen`, and `presetScreen`. A shared `Cursor` struct handles
vim-style navigation.

## Issues

### 1. Styles created in View/render functions (Medium)

Lipgloss styles are created via `lipgloss.NewStyle()` inside View and render
functions, allocating new style objects on every render cycle.

Locations:
- `tui/window.go:138-148` -- `renderFooter()`
- `tui/header.go:122-168` -- `renderCompactHeader()`, `RenderHeader()`
- `config/setup_ui.go:313-421` -- `View()` and all `render*Line()` methods
- `cmd/monitor_ui.go:175-246` -- `FooterStatus()`, `renderLogLine()`
- `cmd/session_ui.go:98-127` -- `View()`, `renderSessionLine()`

Extract these to package-level `var` declarations. The per-character style in
`applyGradient()` is an acceptable exception since colors are computed
dynamically.

### 2. `buildVisibleLines()` called repeatedly per cycle (Low-Medium)

In `config/setup_ui.go`, `buildVisibleLines()` is called in `Update()` (line
208), `View()` (line 317), and `FooterKeys()` (line 427). During a single
render cycle the list is rebuilt 2-3 times. Cache the result and invalidate
when expand/collapse/check state changes.

### 3. `time.Now()` called in View path (Low)

- `tui/window.go:147` -- flash expiry check in `renderFooter()`. Redundant
  since `Update` already expires flashes on tick.
- `cmd/session_ui.go:104` -- uptime formatting. More justifiable but still an
  impurity.

### 4. `onSelect` callback does synchronous work (Low)

In `cmd/monitor.go:37-40`, `NewControlClient(info)` runs synchronously inside
`sessionScreen.Update()` when the user presses Enter. If the connection is
slow the UI freezes. Wrap in a `tea.Cmd` that returns the new screen via a
message.

### 5. No `key.Matches` / `help.KeyMap` usage (Low)

All key handling uses `msg.String()` string comparisons. The codebase has its
own `FooterKeys` system which serves a similar purpose, so this is stylistic
rather than a bug.

### 6. Pointer receiver on Window (Info)

`Window` uses `*Window` for `Update()`, departing from the standard value-
receiver pattern. This is intentional -- `Window` acts as shared context
mutated by screens via setter methods. Correct for this architecture.

## Passing Checks

- No blocking I/O in Update (one minor exception in `onSelect`)
- Commands used correctly (`tea.Batch`, `tea.Tick`, `tea.Quit`)
- WindowSizeMsg handled in all three screens
- Init returns `tea.Batch(doTick(), tea.WindowSize())`
- View functions are mostly pure
- Screen transitions send synthetic WindowSizeMsg to new screens
- Cursor/scroll logic keeps viewport consistent
- No Huh `.Run()` calls

## Summary Table

| Category                    | Status |
|-----------------------------|--------|
| No blocking I/O in Update   | Pass (minor `onSelect` exception) |
| Commands for async ops      | Pass |
| Model immutability          | Pass (pointer Window is intentional) |
| WindowSizeMsg handled       | Pass |
| Styles defined once         | Fail -- styles created in render functions |
| View purity                 | Pass (minor `time.Now()` calls) |
| Key bindings with help.KeyMap | Not used (custom footer system) |
| Component composition       | Pass |
