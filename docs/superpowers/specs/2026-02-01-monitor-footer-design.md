# Monitor Footer Design

## Goal

Add a footer bar pinned to the bottom of the monitor TUI showing status indicators on the left and context-sensitive keybindings on the right.

## Layout

Single line at the bottom of the terminal with two zones:

### Left side — status indicators

- New messages available: `↓ N new` in orange
- Connection error: `connection error: ...` in red
- Flash message: flash text in orange
- Priority: error > flash > new messages
- Empty when no status to show

### Right side — context-sensitive keybindings

- Always shown: `↑/↓ navigate  Home/End jump  q quit`
- On blocked entry (status `none`): append `a allow  A allow+save`
- On temp-allowed entry: append `A save`
- Key names in bright color (colorCyan), descriptions in muted color (colorField)

## New messages tracking

Add `newCount int` to the model. Increment by `len(entries)` in the tick handler when new entries arrive and cursor is not at the end. Reset to 0 when cursor reaches the last item (via G/End, j reaching end, or auto-follow in tick).

## View changes

Remove status/flash/scrollHint lines from between header and log. The footer replaces them. Layout becomes: header + "\n" + logLines + "\n" + footer.

Viewport height calculation stays the same (replacing the status line with the footer line).
