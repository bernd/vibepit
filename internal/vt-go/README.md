# vt-go

Go virtual terminal emulator. Fork of [charmbracelet/x/vt](https://github.com/charmbracelet/x/tree/main/vt) with fixes for running interactive terminal applications.

## Why the fork

The upstream emulator doesn't implement Insert/Replace Mode (IRM, `\x1b[4h`/`\x1b[4l`). When an application like nano enables insert mode and types a character, the character overwrites in place instead of shifting existing content to the right. This breaks any editor or TUI that uses ncurses insert mode for text editing.

We needed this for [latch](https://github.com/unixshells/latch), a terminal multiplexer that uses a VT emulator for server-side rendering. Without IRM support, editors like nano, and tools like Claude Code, display incorrectly when inserting text.

## Changes from upstream

- **IRM support**: characters written while Insert/Replace Mode is enabled now insert blank cells before writing, shifting existing content right. This matches the behavior of xterm, iTerm2, and other terminals.

## Usage

```go
import vt "github.com/unixshells/vt-go"

emu := vt.NewSafeEmulator(80, 24)
emu.Write(data)
cell := emu.CellAt(0, 0)
```

## License

MIT. Same license as the upstream project.
