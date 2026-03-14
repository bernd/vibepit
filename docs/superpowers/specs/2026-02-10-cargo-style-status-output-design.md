# Cargo-Style Status Output

## Context

CLI status messages currently use plain `fmt.Printf("+ ...")` calls with no
styling. The TUI already has a consistent color theme (lipgloss) but these
startup/command messages don't participate in it. The "+" prefix was originally
used to visually distinguish status lines from other output — with colors, the
prefix becomes redundant.

## Design

Replace all `+ ...` status messages with cargo-style output: a **bold cyan
verb**, right-aligned to 12 characters, followed by the message.

```
   Creating network vibepit-abc123
   Starting proxy container
  Attaching to running session in /home/user/project
```

Errors get the same treatment with **bold purple** (the existing `ColorError`),
printed to stderr:

```
      error volume: permission denied
```

### Color Behavior

Lipgloss color detection is per-renderer and derived from the output stream.
Use separate renderers for stdout and stderr so color stripping is correct when
only one stream is a TTY (e.g. `cmd 2>err.log` or `cmd | less`):

```go
var (
    stdoutRenderer = lipgloss.NewRenderer(os.Stdout)
    stderrRenderer = lipgloss.NewRenderer(os.Stderr)
)
```

Both renderers respect the `NO_COLOR` environment variable. No `--color` flag
needed.

When colors are stripped, output degrades gracefully to plain right-aligned
text, which is still readable and grep-friendly:

```
   Creating network vibepit-abc123
      error volume: permission denied
```

### Helper API (`tui/status.go`)

Two exported functions that print directly, backed by an unexported
writer-based helper for testability:

```go
// writeStatus formats and writes a cargo-style status line to w.
// verb is right-padded to 12 chars, styled with style, followed by message.
func writeStatus(w io.Writer, verb string, style lipgloss.Style, format string, args ...any)

// Status prints a right-aligned bold cyan verb + message to stdout.
func Status(verb string, format string, args ...any)

// Error prints a right-aligned bold purple "error" + message to stderr.
func Error(format string, args ...any)
```

Implementation: pad the plain verb string with `fmt.Sprintf("%12s", verb)`,
then apply the lipgloss style to the padded result. Write
`fmt.Fprintf(w, "%s %s\n", styledVerb, message)`.

Status calls `writeStatus(os.Stdout, verb, statusStyle, ...)`.
Error calls `writeStatus(os.Stderr, "error", errorStyle, ...)`.

### Edge Cases

- **Verbs longer than 12 characters:** `%12s` does not truncate — the verb
  prints at full length and column alignment breaks. All current verbs are ≤ 10
  characters (`Generating` is the longest), so this is acceptable. No
  truncation or enforcement.
- **Multiline messages:** Not handled. If a format string produces newlines, only
  the first line aligns with the verb. Callers are responsible for keeping
  messages single-line. No runtime enforcement.

### Verb Mapping

**cmd/run.go:**

| Verb | Message |
|---|---|
| `Attaching` | `to running session in %s` |
| `Creating` | `network %s` |
| `Removing` | `network %s` |
| `Generating` | `mTLS credentials` |
| `Session` | `%s (credentials in %s)` |
| `Starting` | `proxy container` |
| `Listening` | `control API on 127.0.0.1:%s` |
| `Stopping` | `proxy container` |
| `Starting` | `dev container in %s` |
| `Stopping` | `dev container` |

**cmd/allow.go:**

| Verb | Message |
|---|---|
| `Allowed` | `%s` |
| `Saved` | `to %s` |

**container/client.go:**

| Verb | Message |
|---|---|
| `Pulling` | `image %s` |

**main.go (error):**

| Verb | Message |
|---|---|
| `error` | `%v` |

### Out of Scope

- `proxy/server.go` messages — these run inside the proxy container with their
  own `proxy:` prefix and are not user-facing.

## Files to Modify

- **New:** `tui/status.go` — render, Status, Error functions
- **New:** `tui/status_test.go` — unit tests
- **Edit:** `cmd/run.go` — replace 10 printf calls
- **Edit:** `cmd/allow.go` — replace 4 printf calls
- **Edit:** `container/client.go` — replace 1 printf call
- **Edit:** `main.go` — replace error fprintf with `tui.Error`

## Verification

1. `make test` — all tests pass including new status_test.go
2. `go build .` — compiles cleanly
3. `tui/status_test.go` must cover:
   - Right-alignment: short verb is padded to 12 chars
   - Format args: `writeStatus` with format string and args produces expected
     output
   - Writer routing: test via `bytes.Buffer` that `writeStatus` writes to the
     provided writer (verifies both stdout and stderr paths)
   - No-color degradation: use an unstyled `lipgloss.Style` and verify plain
     aligned text without ANSI escapes
4. Grep repo-wide for remaining `"+ "` status patterns:
   `rg 'fmt\.(Print|Printf|Println).*"\+ ' -g '*.go'` — must return zero
   matches outside proxy/
