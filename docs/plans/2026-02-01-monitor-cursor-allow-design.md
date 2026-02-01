# Monitor Cursor & Allow Design

## Goal

Add a selectable cursor to the monitor log view so users can navigate log entries with arrow keys or vim keys (j/k) and press `a` to temporarily allow a blocked domain or `A` to allow and persist to project config.

## Architecture

### Custom log list model

Replace the `bubbles/viewport` with a custom list renderer in `monitor_ui.go`. New model fields:

- `cursor int` — index into log items of the highlighted line
- `[]logItem` slice (replaces `[]string`) where each item holds the `proxy.LogEntry` plus an allow status (`none`, `temp`, `saved`)

The list renderer draws visible lines based on scroll offset and terminal height. The cursor line gets a subtle background highlight using `colorField`. Navigation:

- `j` / `↓` — cursor down
- `k` / `↑` — cursor up
- `G` / `End` — jump to bottom
- `g` / `Home` — jump to top
- Page up / Page down

When new log entries arrive via tick, they append to the slice. If the cursor was on the last line (auto-follow mode), it advances to stay at the bottom.

### Allow actions

`a` on a blocked entry (`x`, status `none`):
- Call `client.Allow([]string{entry.Domain})` via async `tea.Cmd`
- On success: set status to `temp`, symbol becomes `a`
- On already-allowed or non-blocked entry: show brief flash message

`A` on a blocked entry:
- Same as `a`, plus call `config.AppendAllow()` to persist to project config
- On success: set status to `saved`, symbol becomes `A`

Both actions are async (tea.Cmd returning a result message) so the UI doesn't freeze.

### Rendering

```
[15:04:05] x proxy example.com:443 not in allowlist
[15:04:06] + dns   github.com
[15:04:07] a proxy evil.com         ← user allowed temporarily
[15:04:08] A proxy another.com      ← user allowed and saved
```

Symbol colors:
- `+` cyan (colorCyan)
- `x` red (#ef4444)
- `a` orange (colorOrange) — temporary allow
- `A` orange bold — saved allow

Cursor line gets `lipgloss.NewStyle().Background(colorField)`.

Allow target is domain-only (no port), since users typically want to unblock a domain entirely.

Cursor highlights all lines, but `a`/`A` only acts on blocked entries (status `none`). Pressing `a`/`A` on an already-allowed line shows "already allowed" briefly.
