# Compact TUI Header Design

## Problem

Users run vibepit's monitor in a small tmux pane alongside the sandbox. The
current 5-line header (leading blank + 3-row wordmark + tagline/info row) wastes
too much vertical space in tight panes, leaving few lines for actual log content.

## Design

When the terminal height is below 15 lines, the header collapses to a single
line while preserving all information and the visual identity.

### Full header (height >= 15, current behavior)

```
╱╱╱  █   █ ▀█▀ █▀▀▄ █▀▀▀ █▀▀▄ ▀█▀ ▀▀█▀▀  ╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱
╱╱╱  ▀▄ ▄▀  █  █▄▄▀ █▄▄  █▄▄▀  █    █    ╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱
╱╱╱   ▀█▀  ▄█▄ █▄▄▀ █▄▄▄ █   ▄█▄   █    ╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱
     I PITY THE VIBES                              myproject ╱╱ abc123
```

### Compact header (height < 15)

```
╱╱╱ VIBEPIT  I PITY THE VIBES ╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱ myproject ╱╱ abc123 ╱╱╱
```

Layout structure:

    [3 field chars] [space] VIBEPIT [2 spaces] tagline [space] [fill field chars] [space] project ╱╱ session [space] [3 field chars]

### Styling

| Element              | Style                                |
|----------------------|--------------------------------------|
| `VIBEPIT`            | Cyan-to-purple gradient (same as wordmark) |
| `I PITY THE VIBES`  | Orange italic                        |
| `myproject ╱╱ abc123`| Field color (#0099cc)                |
| All `╱` characters   | Field color (#0099cc)                |

### Implementation

`RenderHeader` gains a `height` parameter. When `height < 15`:

1. Build the compact line instead of the 3-row wordmark.
2. Apply gradient to the plain-text "VIBEPIT" string.
3. Calculate fill width: `width - fixed_parts`. If negative, set to 0.
4. Return the single line prefixed with `"\n"` (matching the existing leading
   newline convention).

`Window.headerHeight()` and `Window.Update` pass `w.height` to `RenderHeader`.
The viewport height calculation (`max(height - headerHeight - 2, 1)`) works
unchanged since `headerHeight` will naturally shrink.

### Edge cases

- Very narrow terminals (< 40 width): minimum width of 40 still applies.
- Fill gap goes to 0 if there isn't enough room -- tagline and session info
  touch but nothing is dropped.
