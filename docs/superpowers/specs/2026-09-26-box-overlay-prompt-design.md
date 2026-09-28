# Box Overlay Prompt with a Dimmed Backdrop

## Overview

Today the approve prompt fills the terminal. It clears the prompt's screen
and draws `tui.Window`'s full-screen layout: header, content and footer.
While it is open, the session's screen is gone.

This design shows the prompt as a small bordered box, centred over a
dimmed, static copy of what the session showed at the cut: the
**backdrop**. The user sees where they were while deciding, and the
prompt still clearly reads as vibepit's, not the app's.

It builds on `2026-09-24-ghostty-shadow-terminal-design.md` and doesn't
change how the overlay cuts, hands off input or leaves.

## Goals

- The prompt is a box sized to its content, not the whole terminal.
- Around the box, the terminal shows the session's screen as it was at the
  cut, in one muted colour.
- The screen restore is unchanged: the same leave paths, the same bytes.
- Terminals too small for the box keep today's full-screen layout.

## Non-Goals

- A live backdrop. It is the screen at the cut and doesn't change while the
  prompt is open.
- Keeping the app's colours or attributes in the backdrop (see
  [Dimming](#dimming)).
- Changes to `monitor` or other `tui.Window` users.
- Mouse support in the box.

## Why the restore doesn't change

The prompt already draws on a screen of its own:

- App on the primary screen: `enterSeq` switches to the alternate screen
  with 1047. The primary screen isn't touched, and the raw leave replays the
  log onto it.
- App on the alternate screen: the prompt draws over it, and
  `leaveBytesLocked` already takes the snapshot path (`drawn && c.alt`).

The backdrop is text and SGR, drawn with cursor movement, on that same
screen. `NewFilter` already lets all of that through, and the pen is part
of the prompt's state, which `penReset` restores. So `restore.go` and the
leave logic in `detach.go` stay as they are.

## Design

### Backdrop capture (`overlay`)

The backdrop is captured in `captureCut`, together with the rest of the
cut state. At the cut the shadow has processed exactly the bytes the real
terminal got, so its screen matches what the user sees:

```go
// Backdrop is the session's screen at the cut as plain text: one string
// per row, trailing whitespace trimmed, without control characters.
type Backdrop struct {
	Lines []string
}
```

- Source: `sh.Format(vt.FormatOptions{Output: vt.OutputPlain, Trim: true, Region: vt.RegionScreen})`,
  split on `\n`.
- Each row is cleaned: tabs become spaces, other control characters and
  `utf8.RuneError` are dropped. The plain formatter shouldn't emit them;
  this is defence in depth, because the text comes from the sandbox.
- `vt.ErrOutOfMemory`: an empty backdrop, logged with `logf`. The prompt
  still works, over a blank screen. Any other error fails the shadow, like
  the other `captureCut` reads.
- Forced cut or misaligned shadow: the backdrop may differ slightly from the
  real screen. It is only shown, never used to restore anything, so this is
  acceptable.

### Handoff to the prompt (`overlay.Show`)

`Show` takes a constructor instead of a model:

```go
func (t *Terminal) Show(ctx context.Context, newModel func(Backdrop) tea.Model) error
```

`runPrompt` calls `newModel(t.cut.backdrop)` right before
`tea.NewProgram`. The alternative, sending the backdrop as a message once
the program has started, lets the first frame render without it and races
with `p.Send`. The constructor avoids both, and it makes the dependency
visible at the call site. There is one production caller
(`cmd/prompt.go`) and a handful of tests.

### Box layout (`tui.Window`)

`tui.Window` gets an optional backdrop. The `Screen` interface is
unchanged, so `approveScreen` keeps its `Update`/`View` code:

```go
func (w *Window) SetBackdrop(lines []string)
```

A call to `SetBackdrop` turns on the box layout, also with no lines, which
gives a blank backdrop. A window without the call renders exactly as it
does today. With the box layout on, `View` picks one of two layouts on
every render:

- **Box**, when the terminal is at least 40 columns wide and the box fits
  vertically with one row to spare above and below:
  - Width: `min(cols-4, 76)` columns including the border. It never takes
    the full width, so the box always reads as floating over the backdrop.
    `Width()` and `VpHeight()` report the box's inner size (width minus
    border and padding), so screens lay out for the space they get.
  - Border: rounded, in `tui.ColorCyan`, with one column of padding.
  - Content, top to bottom: a title line (`vibepit` and
    `HeaderInfo.ProjectDirWithHome()`), `screen.View(w)`, the flash or
    error line when there is one, and the screen's footer keys. The
    multi-line header logo, the footer status and the base `q quit` hint
    are left out. `q` still dismisses: `approveScreen` handles it.
  - Nothing a decision depends on is cut off. A truncated target could read
    as another host, e.g. `github.com.evil.example` cut to `github.com.ev`.
    So content wraps instead:
    - `approveScreen.View` hard-wraps the target and reason to
      `w.Width()`, with continuation lines indented under the value.
    - `Window` wraps the key hints by whole hint (`a allow` never
      splits), and wraps the error line.
  - Height: the wrapped content plus the border. Longer content makes the
    box taller, not wider.
  - Position: centred horizontally and vertically. With an odd leftover,
    the extra column or row goes to the right and bottom.
- **Full screen**, the current layout, when the box doesn't fit. The
  backdrop isn't drawn. `Window` puts a blank line above the screen's
  content and indents it by two columns, because screens drawn for the box
  leave those out. `Width()` is the terminal's width minus that indent, so
  the target wraps here too. Today's full-screen prompt cuts a long target
  off at the terminal's edge.

Sizes with a short target (today's content: 7 lines from `approveScreen`,
the title, the key hints, two blank lines and the border):

| Terminal | Box | Inner width | Key hints | Box height | Needs rows |
|---|---|---|---|---|---|
| 120×40 | 76 wide | 72 | 1 line | 13 | 15 |
| 80×24 | 76 wide | 72 | 1 line | 13 | 15 |
| 50×20 | 46 wide | 42 | 1 line | 13 | 15 |
| 40×16 | 36 wide | 32 | 2 lines | 14 | 16 |
| 39×any | full screen | — | — | — | — |
| 80×14 | full screen | — | — | — | — |

The key hints are 42 columns on one line (`a allow  A allow+save  n deny
esc dismiss`). An error, the `applying...` line or a wrapped target adds
rows. If that pushes the box past the terminal's height, the layout
switches to full screen until it fits again.

Composition uses lipgloss v2: a `lipgloss.NewCanvas(cols, rows)` with the
backdrop layer at z 0 and the box layer at z 1. The canvas clips at its
width, so a backdrop row can never wrap.

On `tea.WindowSizeMsg`, the backdrop is fitted to the new size: rows past
the height are dropped, and each row is cut to the width with
`ansi.Truncate`. It isn't refreshed from the shadow: the backdrop stays
static, and reading the shadow from the model would need locking across
packages. The leave repaints the real screen anyway.

### Dimming

Every backdrop row is drawn in one muted foreground, a new `tui.ColorDim`
(`#4b5563`), with no other attributes and the default background.

A dimmed copy of the app's own colours was considered and rejected:

- SGR 2 (faint) is ignored or drawn inconsistently by several terminals,
  especially on truecolor text.
- Blending each cell's colours towards the background needs the terminal's
  background colour. That takes an OSC 11 query before the prompt, and the
  filter drops queries.
- Parsing the VT formatter output back into cells adds code for a purely
  cosmetic result.
- With a single colour, the sandbox has no say in how its text looks next
  to the prompt (see [Security](#security)).

The colour profile decides whether the backdrop is drawn. `Window` records
it from `tea.ColorProfileMsg`. With `colorprofile.NoTTY`, `Ascii` or
`NoColor`, the backdrop can't be dimmed, so it isn't drawn: the box shows
over a blank screen. For 256 and 16 colours, Bubble Tea downsamples
`ColorDim`.

### Caller (`cmd/prompt.go`)

```go
return t.Show(ctx, func(bd overlay.Backdrop) tea.Model {
	w := tui.NewWindow(header, newApproveScreen(session, cc, entry))
	w.SetBackdrop(bd.Lines)
	return w
})
```

`approveScreen.View` drops the leading blank line and the two-space
indent, which were for the full-screen layout. The box's padding takes
their place. The full-screen fallback adds both back, so there it looks
the same as today.

## Security

The backdrop is sandbox output, shown right next to the prompt. Today the
prompt clears the screen, so the sandbox's text disappears while the user
decides. With the backdrop, a sandbox could print text meant to be read
together with the prompt, e.g. a fake "target: github.com" line or
"press a to continue".

What still holds:

- Keystrokes still reach the prompt only after the barrier. The sandbox
  can't receive or inject the decision.
- The sandbox can't cause a prompt without a blocked request.

Mitigations:

- The backdrop is one muted colour, with no attributes or colours from the
  sandbox. Only the box uses the accent colour and the border.
- The box's content is drawn on top at z 1 and covers the backdrop
  completely inside its bounds. The target and reason shown in the box come
  from the proxy's log entry, as today.
- Without colour support there is no backdrop, because undimmed sandbox
  text next to the prompt is the case the dimming exists to avoid.

Residual risk: grey text around the box can still say something
misleading. This is accepted. It is comparable to the sandbox text the
user saw just before the prompt appeared.

## Edge cases

- **Wide characters:** ghostty and lipgloss/uv may disagree on the width
  of some graphemes. A row's tail can then shift by a column. This only
  affects how the backdrop looks.
- **Hyperlinks:** plain output has none. Backdrop links are plain text.
- **Empty backdrop** (OOM, blank screen): the box over a blank screen.
- **Resize below 40 columns, or below the box height plus two rows:** switches to the full-screen
  layout. Resizing back up returns to the box.
- **Shadow unavailable:** `Show` returns `ErrUnavailable` as today. No
  constructor call.

## Testing

- `overlay`:
  - The backdrop matches the session's screen at the cut: the session
    prints known lines, and the constructor records the `Backdrop` it gets.
  - Output after the cut doesn't change the backdrop.
  - Control characters and tabs in the shadow's cells are cleaned.
  - The leave tests pass unchanged. This is the proof that the restore
    didn't move.
  - Harness end to end: during the prompt, the fake real terminal shows
    the backdrop text and the box. After the leave, its screen equals the
    one before the prompt.
- `tui`, table-driven `View` tests:
  - The box is centred over the dimmed rows.
  - Too narrow or too short falls back to full screen.
  - A `NoColor` profile draws no backdrop.
  - A resize crops the backdrop.
  - A long target and a long reason wrap inside the box, in full, also in the full-screen fallback.
  - The key hints wrap by whole hint at 40 columns.
  - Without `SetBackdrop`, the output is unchanged (monitor, session UI).
- `cmd`: `approve_ui_test.go` updated for the box content. `prompt_test.go`
  updated for the constructor.
- `make test`, `make test-integration`.

## Files

| File | Change |
|---|---|
| `overlay/restore.go` | `Backdrop` type; `captureCut` captures and cleans it |
| `overlay/show.go` | `Show` takes `func(Backdrop) tea.Model`; `runPrompt` calls it |
| `overlay/doc.go` | Mention the backdrop in the package overview |
| `overlay/*_test.go` | Constructor call sites; backdrop tests |
| `tui/window.go` | `SetBackdrop`, box layout, fallback, colour profile |
| `tui/header.go` | `ColorDim` |
| `cmd/prompt.go` | Constructor that sets the backdrop |
| `cmd/approve_ui.go` | Content without the full-screen indent; wraps target and reason to `w.Width()` |
| `container/terminal_test.go` | `Show(ctx, nil)` still returns `ErrUnavailable` |
| `AGENTS.md` | Overlay section: the prompt is a box over a backdrop |

## Decisions

- The box is centred.
- The box is at most 76 columns wide.
- The title line leaves out the session ID. It can be added later.
