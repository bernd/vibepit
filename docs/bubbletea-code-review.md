# BubbleTea Code Review

Reviewed: 2026-02-22  
Supersedes: 2026-02-01 review in this same file

## Architecture Overview

The TUI follows a `Window` -> `Screen` delegation model. `Window`
(`tui/window.go`) is the top-level `tea.Model` and owns shared state (header,
footer, tick, flash/error, dimensions). Concrete screens (`monitorScreen`,
`sessionScreen`, `presetScreen`) implement `tui.Screen`, and `tui.Cursor`
provides shared list navigation behavior.

## Current Findings

### 1. Blocking network I/O in `Update()` (Resolved)

Status: fixed on 2026-02-22.

`monitorScreen` now polls via a command and handles results as messages:

- `cmd/monitor_ui.go:105` -- `pollLogsCmd(...)` performs `LogsAfter` in `tea.Cmd`
- `cmd/monitor_ui.go:159` -- `logsPollResultMsg` applies fetched entries
- `cmd/monitor_ui.go:189` -- `TickMsg` returns `pollLogsCmd` instead of calling network I/O inline

### 2. Synchronous `onSelect` work in key handling path (Resolved)

Status: fixed on 2026-02-22.

`sessionScreen` now executes `onSelect` asynchronously:

- `cmd/session_ui.go:70` -- `enter` returns a command that runs `s.onSelect(...)`
- `cmd/session_ui.go:90` -- `sessionSelectResultMsg` updates screen/header after command completion

### 3. Styles are still created during render (Informational)

`lipgloss.NewStyle()` is still allocated in render paths:

- `tui/window.go:136`
- `tui/header.go:137`
- `config/setup_ui.go:313`
- `cmd/monitor_ui.go:216`
- `cmd/session_ui.go:113`

This is a performance/style improvement opportunity, not a correctness bug.

### 4. `buildVisibleLines()` is rebuilt multiple times per cycle (Informational)

`presetScreen` still reconstructs the visible list in several paths:

- `config/setup_ui.go:208` -- `Update()`
- `config/setup_ui.go:317` -- `View()`
- `config/setup_ui.go:407` -- `FooterKeys()`

Caching and invalidation could reduce repeated work on large preset sets.

### 5. `time.Now()` in view-related path (Informational)

- `tui/window.go:145` -- flash visibility check in footer rendering
- `cmd/session_ui.go:119` -- uptime timestamp for each render

This is acceptable today, but it does make rendering time-dependent.

### 6. `key.Matches`/`help.KeyMap` still not used (Informational)

Key handling is still based on `msg.String()` checks, and the project uses
custom footer hints instead of `help.KeyMap`. This remains a style decision,
not a bug.

## Revalidation of Previous Findings (2026-02-01)

| Previous finding | 2026-02-22 status | Notes |
|---|---|---|
| Styles created in render functions | Still valid | Informational only |
| `buildVisibleLines()` rebuilt repeatedly | Still valid | Informational only |
| `time.Now()` in view path | Still valid | Informational only |
| `onSelect` synchronous work | Fixed | Converted to async cmd/message flow |
| No `key.Matches` / `help.KeyMap` | Still true | Explicitly style-only |
| Pointer receiver on `Window` | Not an issue | Intentional architecture choice |

Also revalidated: blocking `LogsAfter` call in `Update()` is now fixed via
`pollLogsCmd`.

## Checklist Status

- No blocking I/O in `Update()`: Pass
- Commands used for async operations: Pass
- `WindowSizeMsg` handled in all screens: Pass
- `Init()` returns initial commands: Pass (`tui/window.go:80`)
- Screen transitions preserve sizing: Pass (`tui/window.go:110`)
- Cursor/viewport consistency: Pass
- No blocking Huh `.Run()` integration: Pass (none present)

## Summary Table

| Category | Status |
|---|---|
| No blocking I/O in `Update()` | Pass |
| Commands for async operations | Pass |
| Model/state architecture | Pass |
| `WindowSizeMsg` handling | Pass |
| Styles defined once | Improvement needed (Informational) |
| View purity | Mostly pass (Informational `time.Now()`) |
| Key map/help integration | Custom approach (Informational) |
| Component composition | Pass |
