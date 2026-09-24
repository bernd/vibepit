# Shadow Terminal: libghostty-vt (WASM) for the Prompt and vibed

## Overview

This design revises the output side of
`2026-09-06-inline-overlay-prompt-design.md` and later replaces the vt-go
emulator in `vibed`.

The earlier design pauses container output at a safe point, buffers it,
tracks terminal modes with a hand-written parser, and replays the buffer
after the prompt. A proof of concept showed that getting the terminal
handling right this way is hard.

This design takes the approach of [zmx](https://github.com/neurosnap/zmx)
instead. A full terminal emulator runs as a **shadow**: it sees every byte
the container writes, but it never sits between the container and the
screen. Showing a prompt becomes a detach: vibepit stops forwarding output,
the shadow keeps absorbing it, the prompt runs, and on reattach the screen
is rebuilt from the shadow's state. The emulator is libghostty-vt,
compiled to WebAssembly and translated to plain Go source by
[wasm2go](https://github.com/ncruces/wasm2go). There is no WebAssembly
runtime at run time, and the build stays `CGO_ENABLED=0`.

Two phases:

1. **Phase 1:** the host-side overlay for `vibepit run`, and later for
   `connect`.
2. **Phase 2:** replace `github.com/unixshells/vt-go` in `session/`, the
   package behind `vibed`'s persistent sessions.

Both phases use one internal wrapper package, `vt`. The package name
leaves out the library, because the library is an implementation detail.

## Background

### How zmx works

zmx is a session attach/detach tool written in Zig on top of
`libghostty-vt`. We checked out and read upstream commit `878a8b0`.

- One daemon per session owns the PTY. Clients connect over a unix socket.
- Every PTY output chunk goes to the attached clients and also into a
  `ghostty_vt.Terminal`. The emulator is a side copy of the stream: live
  output is never re-rendered.
- Detach (`src/main.zig:1608`): the client writes `ESC c` and OSC 110/111/112
  resets to its terminal, then closes the socket.
- Reattach (`src/loop.zig`, `handleInit`, and `src/util.zig:769`,
  `serializeTerminalState`):
  - Resize the emulator to the client's size before serializing, so the
    snapshot has the width the client will draw it at.
  - Turn off synchronized output (mode 2026) while serializing, so the
    client doesn't hold back rendering.
  - Emit colour overrides (OSC 10/11/12).
  - Phase 1: the scrollback as styled text, with no terminal state.
  - Then `CSI 2J CSI H CSI 0m`.
  - Phase 2: the visible screen, with modes, scroll region, keyboard state
    and cursor position. `screen = .all` turns on every screen-level extra.
    It does not serialize both screen buffers. The formatter only formats
    the active screen.
  - Emit OSC 7 (working directory) and OSC 2 (title) by hand. The formatter
    leaves them out, and its OSC 7 carries a stray NUL.
  - Rewrite OSC 133;A to add `redraw=0`, so the outer terminal doesn't clear
    prompt lines on resize.
  - Send SIGWINCH to the PTY's foreground process group, so Ink-style apps
    like Claude Code repaint.
- When resizing its emulator, zmx turns off the emulator's own
  "shell redraws prompt" line clearing, so the snapshot isn't corrupted.
- If the app asked for focus reports (mode 1004), zmx sends focus-out when
  the last client leaves and focus-in when one returns.

### What vibepit already has

`session/` already works like zmx for `connect`. vt-go is a side copy of
the output. On `Attach` it replays `ESC c`, then the scrollback, then the
screen, then the cursor position (`session/doc.go`).

Two findings shape this design:

- **Don't copy zmx's reattach as is for prompts.** `ESC c` plus a full
  scrollback replay suits a new terminal window. Done for every prompt in
  the same window, it would duplicate the history in the user's scrollback
  each time. `session.Attach` has the same property.
- **"Detach" in `vibepit run` happens inside the vibepit process.** Closing
  the Docker attach and re-attaching would lose the output produced in
  between. The attach stays open. Only forwarding to the local terminal
  stops.

## Goals

- Prompt for blocked connections in any terminal, in `vibepit run` first.
- Keep passthrough byte-for-byte while no prompt is showing.
- Don't stall the agent during a prompt.
- Restore the active screen exactly afterwards, whether it is the primary
  or the alternate screen, including every terminal mode. The inactive
  screen is kept only as far as the real terminal already has it (see
  Screen Buffers).
- No cgo. One `.wasm` file, translated once to one Go package, for every
  platform.
- Phase 2: better reattach fidelity and throughput in `vibed`.

## Non-Goals

- Holding the blocked connection open until the user decides. Unchanged
  from the earlier spec.
- Windows.
- Restoring kitty graphics images or per-cell OSC 8 hyperlinks. The
  formatter doesn't emit them yet.
- A persistent status bar.
- Prompts tied to one terminal emulator. The kitty overlay from the PoC is
  removed, not kept as an option.

## Relationship to the 2026-09-06 Spec

| Earlier spec | This design |
|---|---|
| `InputMux` | **Kept**: one goroutine reads stdin and hands it to the container or to the prompt. The 10 ms silence heuristic is **replaced** by a barrier query at detach and an atomic switch at reattach (see Ownership Transitions). |
| `OutputMux` pausing at a safe point, 500 ms timeout, hand-written scanner | **Replaced.** Detach at the exact byte where the shadow's parser reaches ground (`vt_write_until_ground`), usually within the current chunk. If that fails within a budget, cut anyway and repair with a snapshot. |
| 1 MiB buffer that stalls the container when full | **Replaced.** The shadow absorbs output without limit. A raw log with a cap is only an optimisation. It never applies backpressure. |
| `ModeTracker` (hand-written parser) | **Replaced** by the shadow's state getters and extras-only formatting. |
| Enter with `?1049h`, restore "whatever Bubble Tea enabled" | **Replaced** by the Restoration Contract: Bubble Tea's output is filtered, enter changes only set D with `?1047h`, and each leave path restores a defined list. |
| Alt-screen resize bounce to force a redraw | **Replaced** by redrawing the screen from the shadow. |
| Terminal queries lost while paused | **Improved.** The shadow answers them while the prompt shows. Every query gets at most one answer (see Ownership Transitions). |
| `approveScreen`, `blockWatcher`, `runBlockPrompter`, `prompt.log`, attach wiring | **Kept** unchanged. |
| `Prompter` interface with `kittyPrompter` and `inlinePrompter` | **Dropped.** There is one concrete type, `prompter`, with no interface and no variant name. The poller calls `prompter.Show` directly. |
| `kittyPrompter`, `kitty.go`, `approve.go` (`vibepit approve`, the child process started in the kitty overlay) | **Removed.** |
| `--prompt MODE` | **Back to `--prompt`**, a plain on/off flag. With a single mode there's nothing to choose. |

## Architecture

```
 cmd/run.go
   poller (blockWatcher) → prompter.Show(ctx, entry)
                              └── overlay.Terminal.Show(approveScreen)

 overlay package
 ┌──────────────────────────────────────────────────────────────────┐
 │ Terminal                                                          │
 │   InputMux     os.Stdin ──► container | prompt pipe               │
 │   output pump  container ──► shadow.Write ──► stdout (attached)    │
 │                                          └─► raw log (detached)   │
 │   shadow       *vt.Terminal                                       │
 │   Show()       detach, enter, run program, leave, reattach        │
 └──────────────────────────────────────────────────────────────────┘

 vt package (phase 1 and phase 2): public, library-neutral API
 ┌──────────────────────────────────────────────────────────────────┐
 │ NewTerminal  creates terminals                                    │
 │ Terminal     mutex, failed state, Go types for modes/format opts  │
 └───────────────────────────────┬──────────────────────────────────┘
                                 │ only importer
 vt/internal/ghostty: everything that knows about libghostty
 ┌───────────────────────────────▼──────────────────────────────────┐
 │ Instance  one module instance: C-ABI calls, memory helpers,       │
 │           panic → trap error                                      │
 │ ABI       struct layouts, option/data enums, type_json check      │
 │ callback  write_pty as a Go func in the module's function table   │
 └───────────────────────────────┬──────────────────────────────────┘
                                 │ only importer
 vt/internal/ghostty/internal/wasmvt: the translated module
 ┌───────────────────────────────▼──────────────────────────────────┐
 │ ghostty-vt.wasm + GHOSTTY_COMMIT (committed, not embedded)        │
 │ ghostty_vt.go   generated by wasm2go (committed)                  │
 │ limit.go        memory page limit (hand-written)                  │
 └──────────────────────────────────────────────────────────────────┘

 session package (phase 2)
   Session.vte: vt-go → *vt.Terminal
```

### Packages `vt`, `vt/internal/ghostty` and `wasmvt`

The code is split into three packages:

- **`vt`** is the public API. It exposes only what vibepit needs. Nothing
  exported mentions libghostty, wasm2go or WebAssembly. Callers depend on a
  terminal emulator, not on a particular one.
- **`vt/internal/ghostty`** holds everything specific to libghostty. Go's
  `internal` rule lets only `vt` and its subpackages import it, so the
  compiler enforces the boundary. Contents:
  - One module instance per terminal, with thin Go functions that match
    the C API one to one (`TerminalNew`, `VTWrite`, `TerminalSet`,
    `TerminalGet`, `FormatterNew`, `FormatAlloc`, `ContinuationAlloc`, …).
  - The wasm memory helpers (`ghostty_wasm_alloc` and friends).
  - Struct layouts and enum values, plus the test that checks them against
    `ghostty_type_json()`.
  - The `write_pty` callback entry in the module's function table.
  - Turning a trap (a panic inside the translated module) into an error.
  - The build script, the patches, and the feature tests.

  Its types use C API terms. It keeps no state across calls beyond the
  instance and has no locking.
- **`vt/internal/ghostty/internal/wasmvt`** is the translated module. Only
  `vt/internal/ghostty` can import it. Contents:
  - `ghostty-vt.wasm` and `GHOSTTY_COMMIT`, committed. The `.wasm` is the
    input to wasm2go and the provenance record. It is not embedded in the
    binary.
  - `ghostty_vt.go`, the output of wasm2go, committed.
  - `limit.go`, a hand-written method that sets the memory page limit.
- **`vt`** holds everything else:
  - The mutex around each terminal.
  - The "failed" state after a trap.
  - The option types.
  - Converting between vibepit's Go types (`CursorStyle`, `MouseTracking`,
    `FormatOptions`) and the internal package's C-ABI values.
  - The `io.Writer` adapter.

Replacing libghostty later means replacing `vt/internal/ghostty`, not any
caller of `vt`.

Public API of `vt`:

```go
type Terminal struct { /* *ghostty.Instance, mutex, failed */ }

func NewTerminal(cols, rows uint16, opts ...TerminalOption) (*Terminal, error)
func WithMemoryLimit(bytes uint64) TerminalOption
func WithScrollbackLines(n uint) TerminalOption
func WithContinuationMaxBytes(n uint) TerminalOption
func WithWritePty(fn func([]byte)) TerminalOption          // query answers

func (t *Terminal) Write(p []byte) (int, error)             // io.Writer
func (t *Terminal) WriteUntilGround(p []byte) (n int, ground bool, err error)
func (t *Terminal) Resize(cols, rows uint16) error
func (t *Terminal) AltScreen() (bool, error)
func (t *Terminal) Mode(mode uint16, ansi bool) (bool, error)
func (t *Terminal) Modes() ([]ModeState, error)             // every known mode: current and default value
func (t *Terminal) KittyKeyboardFlags() (uint8, error)
func (t *Terminal) CursorStyle() (CursorStyle, error)
func (t *Terminal) CursorVisible() (bool, error)
func (t *Terminal) MouseTracking() (MouseTracking, error)
func (t *Terminal) Title() (string, error)
func (t *Terminal) Pwd() (string, error)
func (t *Terminal) AtGround() (bool, error)                 // DATA_VT_GROUND
func (t *Terminal) Continuation() ([]byte, error)           // ErrContinuationUnavailable past the limit
func (t *Terminal) Format(opts FormatOptions) ([]byte, error)
func (t *Terminal) Close() error
```

There is no runtime object. The emulator is ordinary compiled Go code, so
there's nothing to compile or cache at startup, and `NewTerminal` is a
plain function.

`Modes` walks the mode table, which the internal package mirrors from
`modes.zig` (43 modes as of the pinned commit; `TestModeTableComplete`
scans every mode number and fails when upstream adds or removes one). The restore paths use it to
emit every mode explicitly instead of relying on the formatter's
"differs from default" output.

`FormatOptions` holds the output format, unwrap, trim, the extra state to
emit (modes, scroll region, keyboard, cursor, style, kitty keyboard,
charsets), and a region: the scrollback, the visible screen, or none
(extras only, which needs the formatter patch). `vt` turns
it into the internal package's `GhosttyFormatterTerminalOptions` layout.

#### Runtime details

These all live in `vt/internal/ghostty` and its `wasmvt` package, except
where marked `vt`.

- **Translation.** wasm2go turns the ReleaseSmall build (818 KB) into one
  Go file of about 4.4 MB (126k lines), `wasmvt/ghostty_vt.go`. It is
  generated with `-unsafe`: memory loads and stores go through `unsafe`
  but stay bounds-checked, and throughput is about 1.8× that of the safe
  output. The pinned ghostty commit is recorded in `wasmvt/GHOSTTY_COMMIT`.
  - The generated file starts with `// Code generated by wasm2go. DO NOT
    EDIT.`, so linters and reviewers skip it.
  - A cold build of the package takes about 5 s. The Go build cache makes
    later builds free.
  - Translation is deterministic, so CI can regenerate the file and
    require an identical result.
- **No SIMD.** wasm2go doesn't support the SIMD proposal, and ghostty
  enables `simd128` for wasm by default. The build passes `-Dcpu=generic`,
  ghostty's documented opt-out. The translated code is still faster than
  the SIMD build under wazero (see wasm2go Evaluation).
- **One instance per terminal.** `wasmvt.New()` costs about 65 µs: it
  allocates the initial 448 KiB of linear memory and copies the data
  segment into it. An instance is not safe for concurrent calls. Each
  `vt.Terminal` holds a mutex, so there is no contention between sessions.
- **Memory.** Each instance has its own linear memory, an ordinary Go byte
  slice:
  - **While the terminal lives,** that memory only grows. Its size is the
    peak so far. libghostty reuses freed pages inside it, but never gives
    them back.
  - **`Terminal.Close`** drops the instance, and the garbage collector
    frees its memory. Measured on one terminal with 32 MiB of output and
    no scrollback limits: 571 MiB of linear memory and 693 MiB of heap,
    the same under wazero and wasm2go. Heap went back to about 1 MiB once
    the instances were unreachable.
  - **There is no shared compiled module.** The code is part of the
    binary.
  - **Limits:** bound each terminal's peak twice. First with the
    scrollback limits: `SCROLLBACK_MAX_LINES`, and `SCROLLBACK_MAX_BYTES`,
    which libghostty enforces in steps of pages of about 400 KB. Second
    with the module's memory page limit, set from `vt.WithMemoryLimit`.
    The generated code keeps the limit in an unexported field, so
    `wasmvt/limit.go` adds `SetMemoryLimitPages`. A wasm2go upgrade that
    renames the field fails to compile. It doesn't fail silently. With a 16 MiB cap, a
    terminal with no scrollback limits stayed at 16 MiB through 512 MiB of
    output, the same as wazero's page limit.
  - **At the cap,** writes keep working: libghostty drops history instead
    of growing. Allocations can fail, though, and `format_alloc` builds
    the whole output in linear memory. Tested with a 4 MiB cap and no
    effective scrollback limit: formatting about 1,600 rows of history
    failed with `OUT_OF_MEMORY` on every attempt, while formatting the
    visible screen still worked, and nothing trapped. `vt` returns
    `vt.ErrOutOfMemory` and does not mark the terminal failed. The
    defaults keep the cap (256 MiB) far above the scrollback byte limit
    (32 MiB). Follow-up: a streaming formatter (`ghostty_formatter_format`
    with a writer callback) would avoid the large allocation.
  - **Sizing:** libghostty stores a full-width row of 8-byte cells, even
    for short lines. That's about 1.7 KB per row at 200 columns and about
    0.7 KB at 80. Measured WASM memory per terminal:

    | Columns | Scrollback limit | WASM memory |
    |---|---|---|
    | 80 | 1,000 | 1.8 MiB |
    | 200 | 1,000 | 2.7 MiB |
    | 80 | 10,000 | 7.5 MiB |
    | 200 | 10,000 | 17.6 MiB |
    | 200 | none | 1.4 MiB |

    For comparison, vt-go at its default of 10,000 lines, which is what
    `vibed` uses today, measured 66–68 MiB of Go heap at 80 or 200
    columns. That makes libghostty 4–9× smaller for the same history.
  - **Page granularity:** limits apply per page, so a 1,000-line limit kept
    595–857 rows.
  - **Default byte limit:** libghostty also sets a byte limit by default,
    which kept the spike terminals near 1 MiB. `vt` always sets both
    limits explicitly, so it doesn't depend on that default.
  - **`vibed`** closes the terminal when a session ends. Only live sessions
    hold emulator memory.
- **Imports.** A ReleaseFast/ReleaseSmall build of the module imports
  nothing. wasm2go turns imports into interface parameters of `New`, so
  a build that gains an import fails to compile. It can't fail at run time.
- **Exports.** Every export is a method on `wasmvt.Module`
  (`Xghostty_terminal_vt_write`, …). A missing export or a changed
  signature is a compile error, not a runtime lookup failure.
- **ABI.** wasm32: pointers and `size_t` are 4 bytes, little-endian. Struct
  layouts come from `ghostty_type_json()`. A unit test compares the offsets
  the wrapper hard-codes with the JSON of the translated module. A ghostty
  bump that changes a layout fails that test, not production.
- **Memory helpers.** Use `ghostty_wasm_alloc`, `ghostty_wasm_free`,
  `ghostty_wasm_alloc_opaque` and `ghostty_wasm_take_opaque` for scratch
  space and out-parameters. Keep one reusable input buffer per instance
  (64 KiB) for `Write`. Linear memory is `*m.Xmemory().Slice()`. A call
  can grow it and move the slice, so re-read it after every call.
- **Traps.** In translated code a WASM trap is a Go panic: a bounds check,
  `unreachable`, a failed type assertion on a function table entry, or an
  integer division by zero. The internal package runs every call under
  `recover` and turns the panic into a `*TrapError`. `vt` then marks the
  `Terminal` as failed. Later calls return an error, and callers fall back
  (see Error Handling). A panic in the `write_pty` callback happens inside
  such a call, so it is treated the same way.
  - Tested: a wild pointer into `ghostty_terminal_vt_write` panics with
    `index out of range`, and `recover` catches it.
  - **Not recoverable:** runaway recursion in the module grows the
    goroutine stack until Go aborts with `fatal error: stack overflow`,
    which kills the process. wazero would have returned an error. Tested
    with a module whose export calls itself. See Gaps and Risks.
  - Linear memory is a separate slice, and every access is
    bounds-checked, so a parser bug can't corrupt Go memory.

#### Callbacks (`write_pty`)

C callbacks are function pointers. In WASM they are indexes into the
module's exported `__indirect_function_table`. wasm2go exposes that table
as `X__indirect_function_table() *[]any`, a slice of Go funcs, and an
indirect call is a type assertion on an entry. So installing a callback is:

- Append a Go method value of type `func(int32, int32, int32, int32)` to
  the table. Its index is the old length (160 at the pinned commit).
- Pass the index to `ghostty_terminal_set(term, OPT_WRITE_PTY, idx)`.

The method belongs to the `Instance`, so it knows its own Go callback. No
shim module, no host module and no name-based dispatch are needed. If
libghostty changes the callback's signature, the type assertion panics on
the first query. The effects tests turn that into a failure.

Only `write_pty` is needed, and only for the overlay. `vibed` needs no
callbacks. `vt.WithWritePty` is the only public trace of this mechanism.

#### Building the module

- `make ghostty-wasm` runs
  `zig build -Demit-lib-vt -Dtarget=wasm32-freestanding -Dcpu=generic -Doptimize=ReleaseSmall`
  in a ghostty checkout at the pinned commit. It copies `zig-out/bin/ghostty-vt.wasm`
  into `vt/internal/ghostty/internal/wasmvt/`, updates `GHOSTTY_COMMIT`,
  and regenerates `ghostty_vt.go` with `go generate`.
- The `go:generate` line in `wasmvt/doc.go` is the only place the wasm2go
  flags live: `go tool wasm2go -unsafe -pkg wasmvt -o ghostty_vt.go
  ghostty-vt.wasm`.
- wasm2go is pinned as a Go tool dependency (`tool
  github.com/ncruces/wasm2go` in `go.mod`, v0.4.15). It is pure Go, so
  regenerating needs no Zig, and `go tool` fetches it like any other
  module.
- The Zig build needs Zig 0.16 and ghostty's dependencies (github.com,
  deps.files.ghostty.org, codeberg.org).
- Takes the commit as a variable: `make ghostty-wasm GHOSTTY_COMMIT=<sha>`.
- Applies `vt/internal/ghostty/patches/*.patch` before building. Today
  there are two:
  - `0001-formatter-content-none.patch`: `content_none` in
    `GhosttyFormatterTerminalOptions` (6 lines), which gives extras-only
    formatting.
  - `0002-formatter-origin-mode.patch`: with origin mode (DECOM) on, the
    formatter emitted an absolute CUP, which the terminal reads relative to
    the scroll region, so the cursor landed in the wrong row. The patch
    emits the cursor relative to the region's top-left corner.

  The patches stay local for now; proposing them upstream is deferred.
  A patch is deleted once an upstream commit includes it. The
  `ghostty-wasm.yml` workflow fails with the category "patch does not
  apply" if a new upstream commit conflicts. ghostty-web carries WASM API
  patches in the same way.
- The `.wasm` and the generated Go file are committed. Local builds and
  `build.yml` never need Zig. Only the `ghostty-wasm.yml` workflow (below)
  does.
- Inside a vibepit sandbox, Zig's package fetcher fails through the proxy
  (`HttpConnectionClosing`). Download each dependency with `curl`, register
  it with `zig fetch <file>`, and build again until nothing is missing.

#### Upgrading libghostty

1. `make ghostty-wasm GHOSTTY_COMMIT=<sha>`.
2. `go test ./vt/internal/ghostty/...`, the feature tests:
   - **Compile failure in `vt/internal/ghostty`:** an export was removed or
     changed its signature, or the module gained an import. Adapt the
     bindings.
   - **Layout or enum failure:** update the hard-coded ABI values.
   - **Round-trip or getter failure:** a regression. Don't adopt the build,
     or adapt `vt` and document why.
   - **Golden diff:** review the output change, then run `-update`.
   - **Known-gap failure:** upstream closed a gap. Remove vibepit's own
     emission.
   - **Effects failure:** decide whether the new answer is safe to forward
     to the app.
   - **Patch does not apply:** rebase the patch, or drop it if upstream
     merged it.
3. `go test -fuzz=FuzzVTWrite -fuzztime=5m ./vt/internal/ghostty`.
4. `go test ./vt/... ./overlay/... ./session/...`.
5. Benchmark before and after, recorded in the PR together with the
   `.wasm` size and the generated file's size.

Normally the `ghostty-wasm.yml` workflow runs these steps. Doing them by
hand is for local debugging.

**Upgrading wasm2go** is a separate change: `go get -tool
github.com/ncruces/wasm2go@<version>`, `go generate
./vt/internal/ghostty/internal/wasmvt`, then steps 2–5. A dependency
update that bumps wasm2go without regenerating fails
`TestGeneratedCodeIsCurrent`. If the generated code changes the
memory-limit field, `limit.go` fails to compile.

#### Workflow `ghostty-wasm.yml`

A new GitHub Actions workflow in `.github/workflows/`. It updates and
builds the WASM file, regenerates the Go code, and runs the feature tests.

- **Triggers:**
  - `workflow_dispatch`, with an optional `ghostty_commit` input. The
    default is the head of ghostty's `main`.
  - A weekly `schedule`, which checks for new upstream commits.
- **Job:**
  1. Check out vibepit and set up Go, as `build.yml` does.
  2. Install the pinned Zig version, 0.16. On GitHub's runners Zig's
     package fetcher needs none of the sandbox workaround.
  3. Check out ghostty at the chosen commit.
  4. Stop early if that commit equals `GHOSTTY_COMMIT`, so scheduled runs
     are no-ops without upstream changes.
  5. Run `make ghostty-wasm GHOSTTY_COMMIT=<sha>`, which also regenerates
     the Go code.
  6. Run the feature tests `go test ./vt/internal/ghostty/...`, a short fuzz run
     (`-fuzztime=2m`), and `go test ./vt/... ./overlay/... ./session/...`.
  7. Run `BenchmarkVTWrite` against the old and the new module.
- **Result:**
  - **Tests pass:** open or update a pull request on a fixed branch
    (`ghostty-wasm-update`). It contains the new `.wasm`, the regenerated
    `ghostty_vt.go` and `GHOSTTY_COMMIT`. The PR body lists the upstream
    commit range, the benchmark before and after, and the sizes of the
    `.wasm` and the generated file before and after. The generated file's
    diff is large, so reviewers read the upstream range and the feature
    test results, not the diff.
  - **Tests fail:** open or update a draft PR with the same content and
    the failing test output, labelled `ghostty-wasm-failing`. The failure
    categories in Upgrading libghostty tell the reviewer what to do. The
    run is marked failed.
  - A human always reviews and merges. The workflow never pushes to
    `main`.
- **Permissions:** `contents: write` and `pull-requests: write`. No
  `id-token`.
- **Provenance:** the generated Go code comes from a committed binary and
  runs on user machines and inside the sandbox, so every change must be
  traceable to a source commit. The chain is ghostty commit → `.wasm` →
  `ghostty_vt.go`:
  - `GHOSTTY_COMMIT` also records the Zig version and the sha256 of the
    `.wasm`. `TestProvenance` fails if they don't match.
  - `TestGeneratedCodeIsCurrent` runs the pinned wasm2go on the committed
    `.wasm` and requires output identical to `ghostty_vt.go`. It runs in
    `make test`, so `build.yml` rejects a hand-edited or stale file on
    every PR.
  - If the Zig build turns out to be reproducible, `ghostty-wasm.yml` can
    also rebuild at the recorded commit and require an identical file.
    Checking that is a follow-up.

### Package `overlay` (phase 1)

#### Output pump

One goroutine reads the container output. For each chunk, while holding
the terminal mutex `mu`:

1. `shadow.Write(chunk)`.
2. If attached: write the chunk to stdout.
3. If detached: append it to the raw log while the log is under its cap
   (4 MiB). Past the cap, drop the log and mark it overflowed.

Attach and detach switch while holding `mu`.

**Detaching at ground.** A request to detach sets `detachPending`. The pump
then handles chunks this way:

- If the shadow is already at ground, it switches before the chunk.
- Otherwise it calls `shadow.WriteUntilGround(chunk)`, forwards the
  consumed prefix to stdout, switches, and gives the rest of the chunk to
  the shadow and the raw log.

At the switch, the real terminal is at the same clean parser state as the
shadow: no half-written escape sequence and no half-written UTF-8
character. No `CAN` and no continuation are needed at the cut, and the raw
log starts exactly where forwarding stopped.

Ground is normally reached within a few bytes. There is a budget: 64 KiB
of forwarded bytes, or 250 ms without reaching ground (for example, a
container stalled in the middle of a huge OSC or APC). If it runs out, the
pump makes a **forced cut**: it switches anyway and marks the cut
`forced`. `Show` then writes `CAN` (0x18) to abort the half-received
sequence. A forced cut rules out the raw replay path, because
the real terminal may already have drawn a replacement character that only
a full repaint removes. Tested: cutting `abc€X` after the first byte of `€`
and replaying gives `abc�€X`. Detaching at ground gives `abc€X`.

**Resync after reattach.** If the snapshot path can't provide a
continuation (see `Show`), the pump sets `resyncing`. For each chunk it
calls `shadow.WriteUntilGround`, drops the consumed prefix instead of
forwarding it, and forwards from the first byte after ground. The sequence
that was in flight at reattach is lost for the real terminal, usually a
title or clipboard OSC. The screen stays clean. Tested: an unfinished
1,100-byte OSC followed by `yyy BEL after` shows `after`, not `yyyafter`.

#### Ownership Transitions

Three resources change hands between the app and the prompt:

- **Output**: where container bytes go.
- **Replies**: who answers the app's terminal queries.
- **Input**: where stdin goes.

They must not change hands independently, or a reply or keystroke ends up
with the wrong owner. The overlay is a state machine with four states.
Every transition names what moves and why that is safe.

| State | Container output | Answers to app queries | Stdin | Real terminal state |
|---|---|---|---|---|
| **Attached** | → real terminal | real terminal | → container | owned by the app |
| **Draining** | → shadow + raw log | queries sent before the cut: real terminal (reply in flight). Queries sent after the cut: shadow. | → container | frozen at the cut; vibepit writes only `CAN` (after a forced cut), `CSI ?2026l` and the barrier query |
| **Prompting** | → shadow + raw log | shadow | → prompt | owned by vibepit, limited to the set in the Restoration Contract |
| **Attached** (again) | → real terminal | real terminal | → container | owned by the app |

An **outstanding reply** is the reply to a query that the real terminal
has received but not yet answered. The design assumes the terminal
processes its input in order and answers in order. Every common terminal
does.

**T1 Attached → Draining** (at the cut, holding `mu`):

- Output moves at the parser-ground byte (see Output pump). Answers for
  queries after the cut move with it: the shadow answers them, and its
  answers go to the container.
- Input stays with the container, because everything still arriving on
  stdin belongs to the app: replies to queries sent before the cut, and
  keys typed before the user can see a prompt.
- vibepit writes the **barrier query** DSR `CSI 5n` to the real terminal.

**T2 Draining → Prompting** (when the barrier reply arrives):

- `InputMux` parses stdin while draining, across read boundaries and
  interleaved with other input. When it sees the barrier reply `CSI 0n`,
  it consumes it and hands input to the prompt at that byte.
- Terminals answer in order, so every reply that was outstanding at T1
  has already reached the container.
- Only after T2 does vibepit draw the prompt (the enter sequence). Keys
  typed after the prompt appears always go to the prompt.
- An app query for DSR 5n that is in flight at T1 is harmless: both
  replies are the same `CSI 0n`, so the app still receives exactly one.

**T3 Prompting → Attached** (the program has exited, or was cancelled):

- Holding `mu` and the `InputMux` lock at the same time, vibepit:
  1. writes the leave sequence (raw replay or snapshot)
  2. sets `attached = true`
  3. hands input back to the container

  All three moves happen atomically. No container output interleaves with
  the restore, and no input is routed in between.
- No reply is outstanding at T3 that the app could receive by mistake:
  - The prompt sent no queries. The prompt output filter drops them (see
    Restoration Contract).
  - Queries the app sent while detached were either answered by the
    shadow, and those answers are already queued to the container, or not
    answered. Unanswered queries reach the real terminal on raw replay, and
    their replies go to the container after T3. Snapshot replay contains
    no queries.
  - Every app query therefore gets exactly one answer, or none, the same
    as a terminal that doesn't support it.
- If the shadow answered anything while detached (`answeredWhileDetached`),
  raw replay is ruled out. Replaying the query would make the real
  terminal answer a second time. Tested: `CSI 6n` while detached got
  `CSI 1;1R` from the shadow, and again from the real terminal on replay.
- Keys typed between the program's exit and T3 go to the prompt's sink and
  are dropped. The window is a few milliseconds.

**Barrier failure** (T2 doesn't happen within 500 ms):

- **Abort the prompt.** Nothing has been drawn yet. vibepit takes T3
  directly:
  - After a normal cut, the leave is the raw log alone. The real terminal
    is exactly in its cut state, so this is exact.
  - After a forced cut, the leave is a snapshot.

  Input never left the container, so no keys are lost.
- **Late barrier reply.** For 10 s, `InputMux` strips the next `CSI 0n`,
  so a late barrier reply doesn't reach the app.
- `Show` returns `ErrBarrierTimeout`. The poller logs the blocked request
  to `prompt.log` and retries the prompt on the next block.
- After two consecutive barrier timeouts, the session is marked
  `barrierUnsupported`. Later prompts use a degraded T2: switch input after
  100 ms of input silence. The risk that a stray reply crosses to the
  prompt, or a keystroke to the app, is logged once. No common terminal is
  expected to take this path. The manual terminal matrix records it.

**Other exits:**

- Container exit or stdin EOF in Draining or Prompting: go straight to T3.
  The raw path or a snapshot is chosen as usual.
- A detach budget overrun (no ground within 64 KiB or 250 ms): T1 happens
  as a forced cut (see Output pump).

#### Resize

On SIGWINCH, resize the shadow before the container, so the shadow's
layout matches what the app redraws for. A resize while detached forces
the snapshot restore path.

#### `Show`

```go
func (t *Terminal) Show(ctx context.Context, model tea.Model) error
```

1. **Serialize.** `showMu` makes concurrent `Show` calls wait.
2. **T1**, at ground (see Output pump), holding `mu`:
   - `attached = false`.
   - Capture the cut state (see Restoration Contract):
     - `cut.modes`: the modes in set D
     - `cut.screen`: the active screen
     - `cut.extras`: `shadow.Format(content: none, scroll region, cursor,
       style, hyperlink, protection, charsets)`
     - whether the cut was `forced`
   - Reset the raw log and `answeredWhileDetached`.
   - Write `CAN` if the cut was forced, `CSI ?2026l` if synchronized
     output was on, then the barrier query.
3. **T2**: `in, err := InputMux.AwaitBarrier(ctx)`. On a timeout, abort as
   described under Barrier failure.
4. **Enter** (see Restoration Contract).
5. **Run** `tea.NewProgram(model, tea.WithInput(in),
   tea.WithOutput(filter(stdout)))` without its own alt screen.
   Cancelling `ctx` kills the program.
6. **T3**: the leave sequence, raw replay or snapshot (see Restoration
   Contract), then `attached = true` and input back to the container, all
   under `mu` and the `InputMux` lock.

Step 6 always runs, even if the program fails or panics. Restore is
unconditional.

#### Restoration Contract

This contract defines which terminal state the prompt may change, what
each leave path must restore, and when the raw path is not allowed.

**Principle: the prompt changes only a closed, known set of state.** Two
mechanisms guarantee it:

1. **Prompt output filter.** Bubble Tea's output passes through a
   whitelist filter, built on the `charmbracelet/x/ansi` parser, before it
   reaches stdout.
   - **Allowed:**
     - printable text
     - BS, HT, LF, CR
     - cursor movement: CUP, CUU, CUD, CUF, CUB, CHA, VPA, CNL, CPL
     - erasing and editing: ED, EL, ECH, ICH, DCH, IL, DL
     - SGR
     - DECSTBM
     - `CSI ?25h/l`
     - `CSI ?2026h/l`
   - **Dropped:** everything else. That includes:
     - every query (DA, DSR, DECRQM, XTVERSION, OSC colour queries)
     - every other mode change
     - kitty keyboard and modifyOtherKeys sequences
     - OSC 0/2 (title), OSC 8 (hyperlinks), OSC 52 (clipboard)
     - DECSC/DECRC and `CSI s`/`CSI u`
     - charset designation
     - DECSCUSR (cursor style)
     - DCS and APC
     - RIS and DECSTR
   - A test checks that `approveScreen` renders the same through the
     filter and handles keys with every query dropped. Bubble Tea sends its
     queries asynchronously and falls back when no reply arrives. That's
     an assumption, and the test verifies it.
2. **Enter changes only set D, and only in ways the leave can undo.**

**Set D**, the state vibepit itself changes on enter:

| State | Enter writes | Why |
|---|---|---|
| Active screen | `CSI ?1047h` + `CSI 2J`, only when the cut was on the primary screen | Draw the prompt without touching primary content. `1047` rather than `1049`, because `1049` saves the cursor into the same slot as the app's DECSC and overwrites it. Tested: after an app's `ESC 7`, a `1049` round trip moved the app's later `ESC 8` to the wrong column. `1047` leaves the slot alone. |
| Kitty keyboard | push `CSI >0u` on the prompt's screen | Legacy key encoding for the prompt. The leave pops with `CSI <u`, so the app's stack on both screens is unchanged, whatever its depth. |
| Modes IRM (4), LNM (20), DECSCNM (5), DECOM (6), DECAWM (7), DECTCEM (25), 2026 | IRM, LNM, DECSCNM, DECOM off; DECAWM, DECTCEM on; 2026 off | Modes that change how text is drawn. Everything else (mouse, focus, bracketed paste, …) is left alone. The prompt ignores mouse and focus events, and Bubble Tea handles bracketed paste. |
| Scroll region | `CSI r` | Full-screen drawing. |
| Pen: SGR, OSC 8, charsets | `CSI 0m`, OSC 8 close, `ESC ( B`, `SI` | So the prompt isn't drawn in the app's colours, charset or link. |
| Cursor position | moved by drawing | — |
| Alt screen content (when the cut was on the alt screen) | overwritten by the prompt | Not restorable by replay: the raw path isn't allowed. |

Allowed prompt output (the filter) can only change state inside D, plus
screen content on the prompt's screen.

**What each leave path restores:**

| State | Raw replay | Snapshot |
|---|---|---|
| Active screen | `CSI <u` pop, then `CSI ?1047l` | pop, `?1047l` if entered, then switch to the shadow's active screen |
| Primary content + scrollback | untouched by the prompt; the log carries the app's changes | visible screen from the formatter; lines that scrolled off during the prompt are lost (known) |
| Modes in D | `cut.modes`, written explicitly | the shadow's current values, written explicitly |
| Modes outside D | untouched by the prompt; the log carries changes | every mode in the table is written explicitly from the shadow (full reconciliation, see below) |
| Kitty keyboard stack | balanced push/pop; the log carries changes | balanced push/pop, then the formatter's current flags (`CSI =flags;1u`). Stack depth changes made while detached are lost (known). |
| Scroll region, margins | `cut.extras` | formatter extras |
| Cursor position, pending wrap | `cut.extras`. The formatter reprints the right-edge cell to restore pending wrap. Tested. | formatter extras |
| Pen: SGR, hyperlink, protection, charsets | `cut.extras` | formatter extras |
| Saved cursor (DECSC slot) | untouched: enter uses `1047`, and the filter drops DECSC | untouched on the real terminal. If the app saved the cursor while detached, the real slot is stale (known gap: no getter, no formatter extra). |
| Title, icon, working directory, colours (OSC 4/10/11/12), cursor style | untouched by the prompt; the log carries changes | written from the shadow's getters, only where they differ from the values captured at the cut |
| Tab stops | untouched; the log carries changes | known gap: the formatter's tab-stop extra moves the cursor (zmx). Changes while detached are lost. |
| Actions (bell, clipboard, notifications) | replayed from the log, late | dropped |
| Images (kitty graphics, sixel) | primary untouched | lost (known gap) |

**Order inside a leave:**

- DECOM and DECSTBM home the cursor, so they come before the cursor
  restore.
- IRM comes after it, because the pending-wrap reprint must not insert.
- The written order is: pop kitty, leave the prompt screen, modes except
  IRM, extras (scroll region, cursor, pen), IRM, then the log or the
  snapshot extras. Tested for raw replay: after restoring scroll region,
  IRM, DEC graphics charset, red SGR, pending wrap and the saved cursor,
  then replaying the log, the real terminal and the shadow produced the
  same formatter output.

**Full reconciliation on the snapshot path.** The formatter only emits
modes that differ from their defaults, and never turns a mode back off.
Tested: formatter-only restore left the real terminal on the alt screen,
with insert on and wrapping off. So the snapshot path writes the shadow's
current value of every mode in the table explicitly, except:

- Modes with side effects that aren't state: DECCOLM (3), which clears the
  screen, and 1048 (save cursor).
- The screen modes 47, 1047 and 1049, handled by the screen switch.
- **Report-triggering modes**, such as 2048 (in-band resize) and 1004
  (focus). Enabling them makes some terminals send a report right away.
  They are written only when they differ from the real terminal's known
  value (the cut value, since the prompt doesn't touch them). The shadow
  has no size or focus callbacks installed, so the app gets the report
  exactly once, from the real terminal.

Then comes the **pen reset**, then `CSI 2J CSI H`, and then the formatter
output with content and extras. The formatter also emits the scroll
region, charsets, kitty keyboard flags and the open hyperlink only when
they differ from the default, and it draws content with whatever SGR is
active. So before clearing, the snapshot path writes `CSI r`, `CSI 0m`,
an OSC 8 close, `ESC ( B ESC ) B ESC * B ESC + B SI` and `CSI =0;1u`.
Tested: without it, a dirty terminal kept its scroll region and kitty
flags; with it, 10 scenarios × 20 random dirty terminals restored exactly.

When the real terminal is on the alt screen and the shadow isn't, leave
the alt screen with the mode that entered it (`?1049l` after `?1049h`).
Otherwise that mode's bit stays set. The reference implementation is
`snapshotLeave` in `vt/internal/ghostty/restore_test.go`.

**When the raw path is required to fall back to a snapshot:**

| Condition | Why |
|---|---|
| Cut on the alt screen | The prompt overwrote the app's content. |
| Forced cut | The real terminal may show a replacement character. |
| Raw log overflow | Bytes are missing. |
| Resize while detached | The log was written for a different geometry than the real terminal has reflowed to. |
| `answeredWhileDetached` | A replayed query would be answered twice. |
| The shadow failed while detached | Its state can't be trusted. The leave is `ESC c` + SIGWINCH instead (see Error Handling). |

Everything else stays on the raw path. For the state in D, raw replay is
exact: `cut.modes` and `cut.extras` capture it at the cut, and the log
carries every later change in the app's own bytes.

**Formatter support.** The C API always formats content: a NULL selection
means the whole screen. The Zig formatter already supports extras without
content (`Content.none`). vibepit carries a six-line patch that exposes it
as `content_none` in `GhosttyFormatterTerminalOptions`, kept local for
now (see Building the module). Verified with a patched build.

#### Screen Buffers

The formatter only serializes the active screen. The C API has no way to
format the inactive one. Tested: `SHELL`, then the alt screen, then `VIM`,
restored into a fresh terminal. After leaving the alt screen, `SHELL` is
missing. So the guarantee is:

- The **active screen** at reattach is restored exactly.
- The **inactive screen** is whatever the real terminal holds:
  - When the prompt covered the primary screen with `CSI ?1047h` and the
    app stayed on it, the real primary screen is untouched.
  - When the app was on the alt screen, the prompt drew over the alt
    screen, and the real primary screen is untouched.
  - What the app changed on the inactive screen while detached is lost.
    Example: the app leaves the alt screen, prints, and enters it again
    during the prompt. Rare. A later redraw by the app fixes it.
- Follow-up: ask upstream for a formatter option that selects the screen.
  A known-gap feature test watches for it.

Known loss on the snapshot path: lines that scrolled off the screen during
the prompt are missing from the real terminal's scrollback. A follow-up can
track the history row at the cut (tracked grid refs) and emit only the new
history rows. Claude Code runs on the primary screen, so it normally takes
the raw replay path.

#### Shadow configuration on the host

- Size: the local terminal's size.
- Scrollback: 1000 lines. The snapshot path only uses the visible screen.
- Continuation limit: 64 KiB. The C API's default is 0, which turns
  tracking off. The limit only sets how much of an unfinished sequence is
  kept. 1 KiB was too small for OSC 52 clipboard writes and kitty graphics
  APCs.
- `write_pty` installed.

### `connect` (follow-up)

The same `overlay.Terminal` wraps the SSH session's stdin and stdout, with
`Resize` mapped to the SSH window-change request. The overlay runs on the
host. Prompts don't need `vibed`'s replay, so history is never duplicated.

## Phase 2: Replace vt-go in `vibed`

### Current use of vt-go (`session/session.go`)

- `vte.Write` in the pump, synchronously while holding `s.mu`.
- `Resize`, `IsAltScreen`, `Render`, `CursorPosition`.
- `renderVTEScrollback`: walks scrollback cells and writes SGR diffs.
- `drainVTE` and `closeVTEPipe`: throw away the emulator's query answers so
  its pipe never fills.

### Mapping

| vt-go | vt |
|---|---|
| `NewSafeEmulator` | `vt.NewTerminal` (one module instance per session) |
| `Write` | `Terminal.Write` |
| `Resize` | `Terminal.Resize`. The C API already turns off prompt-redraw clearing by default, the flag zmx sets by hand. A shell can turn it back on with `OSC 133;A;redraw=1`. The feature tests cover this. |
| `IsAltScreen` | `Terminal.AltScreen` |
| `Render` + `CursorPosition` | `Format`: visible-screen selection plus extras |
| `renderVTEScrollback` | `Format`: scrollback selection, content only |
| `drainVTE`, `closeVTEPipe` | Deleted. With no `write_pty` callback, answers are dropped. |

### New replay in `Attach`

- **Primary screen:**
  1. `ESC c`, for a fresh client window
  2. the scrollback (content only)
  3. `CSI 2J CSI H CSI 0m`
  4. the visible screen with extras
  5. OSC 7 and OSC 2
- **Alt screen:** `ESC c`, then the formatter output, which enters the alt
  screen and draws its content. The Ctrl-L that `Attach` writes into the
  PTY today goes away. The primary screen underneath is not replayed (see
  Screen Buffers). Today's replay doesn't have it either.
- **Both:** the replay ends with `shadow.Continuation()`. `vibed` enables
  continuation tracking (64 KiB), because the C API's default is off.
  Without it, an attach between `abc ESC[3` and `1mRED` shows `abc1mRED`,
  as tested. `Attach` already builds the replay and adds the client under
  `s.mu`, so the continuation and the first live chunk are in order. If
  the continuation is unavailable, the new client starts in resync mode.
  The pump calls `WriteUntilGround` on the next chunk. Resyncing clients
  receive only the bytes after ground, and other clients receive the whole
  chunk.
- `ESC c` is a correct baseline here, because an attach is a fresh client
  window. The prompt overlay can't use it (see `Show`).
- **Geometry stays with the writer.** The existing rule holds: only a
  client that becomes the writer (`Attach` with no current writer,
  `TakeOver`, `Resize`) resizes the PTY, and with it the shadow. The shadow
  is resized before formatting only in that case. Observers get the
  replay at the writer's geometry, as today. A smaller observer window
  wraps or clips.
  - Later: give observers a reflowed replay. Copy the shadow into a scratch
    terminal with the snapshot encode/decode API, resize the copy to the
    observer's size, and format the copy.
- Send SIGWINCH to the foreground process group after a writer attach,
  even when the size is unchanged (the kernel skips it then), so Ink apps
  repaint.
- `ToCRLF` goes away. The formatter emits explicit positioning.

### Gains

- Terminal modes survive a reattach. Today bracketed paste, kitty keyboard,
  mouse modes, cursor visibility and the scroll region are lost.
- Alt-screen apps come back exactly, without fake input. The primary
  screen underneath is still not replayed, as today.
- Soft-wrapped lines are marked as such, so reattaching at a different
  width reflows them.
- Less code: the scrollback renderer, the drain goroutine and the CRLF
  rewrite are deleted. The IRM fork of vt-go is no longer needed.
- Throughput, measured on the same workload (below):

  | Emulator | Throughput |
  |---|---|
  | vt-go | 1.6 MiB/s |
  | libghostty (WASM on wazero, spike) | 91–100 MiB/s |
  | libghostty (wasm2go `-unsafe`, ReleaseSmall) | 158 MB/s |

  The pump holds `s.mu` while writing to the emulator, so vt-go probably
  caps session output today. Not yet confirmed end to end.
- Later: libghostty's binary snapshot API
  (`ghostty_snapshot_encode`/`decode`) could let sessions survive a `vibed`
  restart through the state file.

### Costs

- The translated module adds about 2.8 MB to a stripped binary. That is
  less than the wazero variant: the runtime plus the embedded `.wasm`
  added 4.3 MB. The binary is shared with the host CLI, so the cost is
  paid once. There is no startup compile.
- `session/` imports vt-go under the name `vt`. That import is removed in
  the same change, so the new `vt` package takes the name without an alias.
- Rewrite the tests that pin vt-go behaviour: `vt_irm_test.go`,
  `vt_scrollback_test.go`, and the replay assertions in `manager_test.go`.

## Error Handling

- **WASM trap in the host shadow.** Mark the overlay unavailable for the
  session and keep passthrough running. The poller then only logs blocked
  requests to `prompt.log`. The user can still allow them with `allow-http`,
  `allow-dns` or `monitor`.
- **Trap in `Show` after the detach.** Take the raw replay path if it's
  allowed. `cut.modes` and `cut.extras` were captured at T1, so it doesn't
  need the shadow. Otherwise write `ESC c` and send a SIGWINCH nudge.
  Either way, finish T3.
- **Trap in `vibed`.** Drop the shadow for that session. Reattach then
  sends `ESC c` and relies on the app to redraw, which is today's alt-screen
  behaviour.
- **Container exits or stdin reaches EOF during a prompt.** Unchanged from
  the earlier spec: cancel the program, restore, return.
- **`NewTerminal` fails** (for example, out of memory under the limit).
  Treat it as "overlay unavailable", with the same fallback as a trap.
- **`Format` fails with `vt.ErrOutOfMemory`.** The terminal stays usable.
  In `vibed`, replay without the scrollback and keep the visible screen.
  In the overlay, a snapshot this large can't happen at the host's
  settings; if it does, fall back as for a trap in `Show`.
- **Stack overflow in the module.** Not recoverable: the process exits.
  In `vibed` that ends every session. On the host it ends `vibepit run`,
  and the container keeps running as after any vibepit crash. No
  fallback is possible in-process. Mitigations are in Gaps and Risks.

## Security

- Container output is attacker-controlled. The shadow parses it inside
  the instance's linear memory, a separate Go slice. Every load and store
  is bounds-checked, including in the `-unsafe` output, so a
  memory-safety bug in the parser stays inside that instance and shows up
  as a trap.
- Containment is weaker than under wazero in one way: input that drives
  the parser into runaway recursion crashes the process instead of
  returning an error. That is a denial of service, not an escape.
- Prompt decisions, the control API and mTLS credentials stay on the host.
  Unchanged.
- `approveScreen` still strips C0, C1 and DEL from domain and reason
  strings.
- The shadow answers queries while a prompt shows. Those answers describe
  only the shadow's own state, never the host terminal.

## Testing

### `vt/internal/ghostty`: feature tests

This package pins the libghostty behaviour vibepit relies on. Every
behaviour listed here has a test that runs against the translated module.
A new WASM build is safe to adopt when `go test ./vt/internal/ghostty/...`
passes. Tests are table-driven. Expected VT output lives in
`testdata/*.golden`, and an `-update` flag rewrites it.

Where possible, tests check **behaviour, not bytes**. The main check is a
round trip:

1. Feed input into terminal A.
2. Format A.
3. Feed the formatted output into a fresh terminal B of the same size.
4. Compare A and B: plain-text screen, active screen, cursor position,
   modes, kitty keyboard flags, and title.

A second variant restores into a **dirty** terminal B, not a fresh one.
B first gets randomized state: modes from the mode table, the alt screen,
kitty keyboard flags, a scroll region, charsets and SGR. B then gets the
snapshot leave sequence from `overlay` (full mode reconciliation, then the
formatter output), and must match A. This pins the rule that the snapshot
path never assumes a reset terminal.

A harmless change in the formatter's output then doesn't fail the suite,
while a real regression does. Golden files are kept for a few
representative scenarios, so output changes show up in review.

**Module and ABI**

- Provenance: the `.wasm` matches the sha256 in `GHOSTTY_COMMIT`.
- The committed `ghostty_vt.go` is exactly what the pinned wasm2go
  generates from the committed `.wasm` (`TestGeneratedCodeIsCurrent`).
- No imports, every C function the package calls is exported with the
  expected signature, and the function table is exported. The compiler
  checks all three: `wasmvt.New()` takes no arguments, and the calls are
  typed methods.
- `ghostty_type_json()` reports wasm32 with 4-byte pointers, and every
  struct offset, struct size and enum value the package hard-codes matches
  it.

**Terminal lifecycle and options**

- Create and free a terminal, and reset it.
- Resize: the reported cols and rows change.
- `SCROLLBACK_MAX_LINES` and `SCROLLBACK_MAX_BYTES` bound
  `SCROLLBACK_ROWS` and WASM memory. After 100 MiB of output, memory stays
  under the budget.
- `CONTINUATION_MAX_BYTES` is honoured.
- `OPT_MODE` can set and clear a mode. We use it to turn synchronized
  output off around formatting.

**Parsing**

- The resulting state doesn't depend on how the input is chunked. Feeding
  every scenario split at every byte offset gives the same state as one
  write.
- RIS (`ESC c`) resets the modes and the kitty keyboard flags.
- `VT_GROUND` is true at rest and false inside CSI, OSC, DCS and a
  multi-byte UTF-8 character.
- `vt_write_until_ground` consumes exactly the bytes up to ground. It
  returns NO_VALUE when the input ends before ground. The tested cases are
  an unfinished UTF-8 character (`abc\xe2` followed by `\x82\xacX`
  consumes 2) and an unfinished OSC.
- Mode table: every mode in the internal table is known to `DATA_MODE`,
  and after a reset each one reads its recorded default.

**State getters** (one table row per value; the restore sequence depends on
them):

- `ACTIVE_SCREEN` for 1049, 1047 and 47.
- `MODE` for 25, 1000, 1002, 1003, 1006, 1004, 2004 and 2026.
- `KITTY_KEYBOARD_FLAGS` after push (`CSI >n u`), pop (`CSI <u`) and set
  (`CSI =n;m u`).
- The cursor shape and blinking after DECSCUSR, and `CURSOR_VISIBLE`.
  `GHOSTTY_TERMINAL_DATA_CURSOR_STYLE` is the SGR pen, not the shape, so
  the shape comes from a render state (`CURSOR_VISUAL_STYLE`,
  `CURSOR_BLINKING`).
- `MOUSE_TRACKING`, which is only a bool. `vt.MouseTracking` derives the
  level from modes 1003, 1002, 1000 and 9.
- `TITLE` after OSC 0 and OSC 2.
- `PWD` after OSC 7, with no trailing NUL (zmx issue 222).
- `CURSOR_X` and `CURSOR_Y`.

**Continuation**

- Empty at ground.
- Exact bytes for an unfinished CSI, OSC, DCS and UTF-8 character.
- Replay: continuation plus the remaining bytes, fed into a fresh terminal,
  gives the same state as the uncut stream.
- An unfinished sequence over the limit gives INVALID_VALUE without
  trapping. Tested: a 1,100-byte OSC with a 1,024-byte limit.
- The default limit is 0 (tracking off), so `vt` always sets it
  explicitly.

**Formatter** (round trip plus golden)

- Visible screen with all extras. Scenarios:
  - a primary-screen agent with kitty keyboard, bracketed paste, focus
    reporting and a hidden cursor
  - an alt-screen app with mouse modes (the output enters the alt screen
    itself)
  - a scroll region
  - charsets
  - SGR styles: 256-colour, truecolour, underline styles
  - wide characters and emoji
- Scrollback only, with no extras: plain content and styles, no mode or
  cursor sequences.
- Soft wraps: a wrapped line stays one logical line after the round trip,
  and after a resize it reflows.
- OSC 133 prompts: whether the formatter emits them, since the replay
  rewrites `OSC 133;A` to add `redraw=0`, and whether a resize clears
  prompt lines with the default flag and after `OSC 133;A;redraw=1`.

**Extras-only formatting** (patched)

- `content_none` produces no cell content, only the requested extras.
- Scroll region and origin-mode effects come before the cursor position.
- Pending wrap is restored by reprinting the right-edge cell.
- SGR, hyperlink, protection and charsets come after the cursor.
- A round trip of extras-only output into a dirty terminal restores the
  cursor, pen and scroll region.

**Known gaps** (tests that describe current behaviour)

- The formatter doesn't emit OSC 8 hyperlinks, cursor style (DECSCUSR),
  title or working directory.
- The formatter formats only the active screen. There is no option to
  select the inactive one.
- There's no getter and no formatter extra for the saved cursor (DECSC).
- The formatter's tab-stop extra moves the cursor.
- These tests assert today's output. If an upgrade starts emitting any of
  them, a test fails. That is the signal to drop vibepit's own emission and
  avoid emitting the sequence twice.

**Effects** (`write_pty`)

- Which queries are answered, and with what:
  - DA1, DA2
  - DSR 5n, CPR 6n
  - `CSI ?u`
  - DECRQM
  - XTVERSION
  - OSC 10/11 colour queries
  - XTWINOPS size queries
- The overlay forwards these answers to the app while a prompt shows. An
  upgrade that starts answering a new query, especially with made-up
  colours or sizes, needs a decision first.
- With no callback installed, queries produce no answer and don't block.

**Callbacks**

- Installing `write_pty` appends one entry to the function table and
  returns its index.
- With two instances, each `write_pty` reaches its own instance's callback.
- The callback's bytes are a copy. Later writes don't change them.

**Robustness**

- A fuzz test (`FuzzVTWrite`, with the scenario inputs as seeds) never
  traps.
- Hitting the memory limit returns an error, not a panic.
- A trap (a panic in translated code) turns into an error.
- A panic in the `write_pty` callback turns into a trap error.
- Runaway recursion can't be tested, because it kills the test process.
  The fuzz test is the only guard.

**Benchmark**

- `BenchmarkVTWrite` uses the spike's workload (E). It isn't a pass/fail
  gate. An upgrade PR includes the before and after numbers.

### `vt`, through the public API only

- Conversions between vibepit's types and the internal values.
- Locking: concurrent `Write` and `Format` pass under `-race`.
- A failed terminal returns errors and doesn't panic.
- Most behaviour is already covered below this package. `vt` tests only
  what it adds.

### Other packages

`overlay`, with fake streams (see the earlier spec for `InputMux`):

- Detach happens at ground. A detach requested inside an escape sequence
  or a UTF-8 character splits the chunk at the ground byte.
- `abc€X` cut after the first byte of `€` ends up as `abc€X` on the real
  terminal.
- A budget overrun makes a forced cut. It writes `CAN` and rules out raw
  replay.
- Raw replay order: pop, leave the screen, `cut.modes` without IRM,
  `cut.extras`, IRM, log.
- Snapshot order: pop, leave the screen, match the screen, full mode
  reconciliation, pen reset, clear, formatter output, getter-based extras,
  continuation.
- Restore into a dirty terminal: the app leaves the alt screen and resets
  insert mode and line wrapping while detached. The real terminal ends on
  the primary screen with insert off and wrapping on. The stand-in
  terminal for these tests is a second `vt.Terminal`.
- The log overflow forces the snapshot path.
- A resize while detached forces the snapshot path.
- A query answered while detached forces the snapshot path and is
  answered exactly once.
- Query answers go to the container only while detached.
- Ownership transitions, table-driven over the state machine:
  - Draining: replies and keys before the barrier reply go to the
    container, and the barrier reply is consumed. The barrier reply is
    recognized when it is split across reads and interleaved with keys.
  - T3 is atomic: no container output and no input between the leave
    bytes and the switch.
  - Barrier timeout: the prompt is aborted, nothing is drawn, the leave is
    raw (or a snapshot after a forced cut), and a late `CSI 0n` is
    stripped. Two timeouts set `barrierUnsupported`.
  - An app DSR 5n in flight at T1 still gets exactly one `CSI 0n`.
- Prompt output filter: every allowed sequence passes, every other one is
  dropped (table over the categories). `approveScreen` renders the same
  and accepts keys with queries dropped.
- Restoration contract:
  - Enter writes exactly set D. Each leave path restores each row of the
    contract table.
  - Leave order: DECOM and DECSTBM before the cursor, IRM after it.
  - The app's DECSC slot survives a prompt.
  - The kitty keyboard stack depth survives a prompt on both screens.
  - Report-triggering modes are written only when they changed.
  - Raw replay into the stand-in terminal matches the shadow's formatter
    output (the tested scenario, generalized).
- When the continuation is unavailable, the pump resyncs. Forwarding
  resumes after ground, and no sequence tail shows up as text.
- Restore runs when the program fails.

`session` (phase 2):

- The replay tests, rewritten for the new replay sequence.
- Attach between `abc ESC[3` and `1mRED`: the new client shows red `RED`,
  not `1mRED`.
- Attach during an unfinished sequence over the limit: that client resyncs,
  and other clients get every byte.
- A smaller observer attaching doesn't resize the PTY or the shadow.

Manual host matrix: the same as in the earlier spec (kitty, ghostty,
wezterm, tmux, xterm × shell, vim, Claude Code, `yes`).

## Spike Evidence (2026-09-24)

Both spikes ran these scenarios:

- A. Primary screen: `CSI >1u`, `?2004h`, `?1004h`, `?25l`, styled text,
  an OSC 8 link, a cursor move.
- B. Alt screen: `?1049h`, `?1002h`, `?1006h`, content, a cursor move.
- C. `abc ESC[3`, then `1mRED ESC[0m` in a second write.
- D. Queries: `CSI c`, `CSI 6n`, `CSI ?u`, `CSI ?2004$p`.
- E. 256 MiB of styled lines on a 200×60 terminal.

**cgo**, using `go.mitchellh.com/libghostty` through
`github.com/ehsanul/libghostty-vt-static` (prebuilt, May 2026):

- A: modes, kitty keyboard state (`CSI =1;1u`) and the cursor were
  restored. The OSC 8 link was dropped.
- B: mouse modes, `?1049h` and the alt-screen content were restored.
- C: formatted correctly.
- D: DA1, CPR, `?u` and DECRQM were answered.
- E: 98 MiB/s.

**WASM on wazero** (superseded by wasm2go, below), ghostty main `7c40388`
built with Zig 0.16, running on wazero main `cfd684f`, with
`CGO_ENABLED=0`:

- A–D: the same output as cgo.
- C: continuation was `"\x1b[3"` after the cut and empty after the rest.
- D: answered through the shim callback, which got table index 160.
- E: 100 MiB/s ReleaseFast, 91 MiB/s ReleaseSmall.
- Size: 4.6 MB ReleaseFast, 814 KB ReleaseSmall.
- Compile: 309 ms cold (153 ms ReleaseSmall), 88 ms with a warm cache.
- WASM linear memory after E: 4 MiB.
- Cross-compiles to darwin/arm64, darwin/amd64 and linux/arm64.

**vt-go** v0.2.0 on workload E: 1.6 MiB/s (64 MiB in 40 s).

### wasm2go Evaluation (2026-09-24)

wasm2go v0.4.15 on the same ghostty commit with the formatter patch, Go
1.27.1, AMD Ryzen 7 7700. The harness ran each scenario against every
variant through the same C-ABI calls.

- **SIMD:** the default build fails with `unsupported opcode (SIMD)`.
  `-Dcpu=generic` builds a module without SIMD (818 KB) that translates
  in 1.5 s.
  - hauntty, the other libghostty user of wasm2go, uses a fork with SIMD
    support. It needs `GOEXPERIMENT=simd` and, on amd64, AVX2. That's
    not an option for vibepit's plain builds.
- **Correctness:** 24 outputs were byte-identical across wazero with SIMD,
  wazero without SIMD, and wasm2go (plain, `-unsafe`, ReleaseFast
  `-unsafe`):
  - spike scenarios A–D
  - the known gaps: title, pwd, OSC 8, tab stops, DECSC
  - alt screen, OSC 133, resize reflow
  - 14 query answers
- **Callbacks:** appending a Go func to the function table got index 160,
  the same index the wazero shim got. Two instances each reached their
  own callback.
- **Traps:** a wild pointer panicked with `index out of range` and was
  recovered, with `-unsafe` too. So was a panic in the callback. Runaway
  recursion (a hand-built module whose export calls itself) ended in
  `fatal error: stack overflow`. wazero returned `stack overflow` as an
  error.
- **Memory:** the 16 MiB page limit held through 512 MiB of output with no
  scrollback limits, the same as wazero. Heap use was identical and was
  reclaimed after the instances were dropped.
- **Startup:** `New()` takes 65 µs, with no compile step. wazero took
  140 ms to compile without a cache.
- **Throughput and binary size**, where binary growth is measured against
  an empty program (linux/amd64, `-s -w`):

  | Variant | Workload E | Format, 200×60 | Binary growth |
  |---|---|---|---|
  | wazero, SIMD build | 120 MB/s | 440 µs | +4.3 MB |
  | wazero, `-Dcpu=generic` | 86 MB/s | 449 µs | +4.3 MB |
  | wasm2go | 89 MB/s | 350 µs | — |
  | wasm2go `-unsafe`, ReleaseSmall | **158 MB/s** | 258 µs | +2.8 MB |
  | wasm2go `-unsafe`, ReleaseFast | 186 MB/s | 235 µs | +4.1 MB |

  ReleaseSmall is the choice. It is already about 100× vt-go, and
  ReleaseFast costs 1.3 MB of binary and 2.4 MB of generated code for
  18% more throughput.
- **Build:** the generated file is 4.4 MB (600 KB gzipped). A cold build
  takes 5.7 s, `go vet` takes 1 s, and a `-race` test build takes 12 s.
  It cross-compiles to linux/arm64, darwin/arm64 and darwin/amd64. arm64
  was built, not run.

**Design review reproductions** against the same WASM. A second
libghostty terminal stood in for the real terminal.

| Finding | Before the fix | With the fix |
|---|---|---|
| Formatter-only restore into a changed terminal | Stayed on the alt screen, insert on, wrapping off | — |
| Cut inside `€` + `CAN` + continuation replay | `abc�€X` | Detach at ground: `abc€X` |
| Query while detached + raw replay | Answered twice | Snapshot path only |
| Alt screen restored into a fresh terminal, then left | Primary screen empty | Guarantee narrowed |
| `vibed` attach between `ESC[3` and `1mRED` | `abc1mRED` | Continuation appended |
| Unfinished 1,100-byte OSC with a 1,024-byte limit | INVALID_VALUE; tail shown as `yyyafter` | Resync: `after` |

Follow-up reproductions for the Restoration Contract, against a patched
ReleaseSmall build with `content_none`:

| Check | Result |
|---|---|
| App `ESC 7` at column 3, prompt entered with `?1049h`/`l`, app `ESC 8` | Cursor at column 8: the saved cursor was overwritten |
| Same with `?1047h`/`l` | Cursor at column 3: the saved cursor survives |
| Extras-only output at a cut with scroll region, red SGR, DEC graphics, pending wrap | `CSI 2;4r CSI 1;10H … 3 … CSI 38;5;1m ESC ( 0` (reprint for pending wrap) |
| Raw replay under the contract, then more output and scrolling in the region | Real terminal and shadow: same screen, cursor, IRM and full formatter output |

The first and fourth rows of the first table show the problem only. Their fixes are covered
by the dirty-terminal round trip and by Screen Buffers.

## Gaps and Risks

- The formatter doesn't emit per-cell OSC 8 hyperlinks in VT output, cursor
  shape, title, working directory or kitty graphics. The wrapper adds
  cursor shape, title and working directory itself, as zmx does.
- If libghostty and the real terminal disagree on character widths (emoji,
  grapheme clusters, mode 2027), a snapshot restore can misalign. This only
  shows at restore time, and a SIGWINCH fixes it.
- The libghostty C API is not stable yet. Pin the commit, and only upgrade
  through the feature tests in `vt/internal/ghostty` (see Upgrading
  libghostty).
- **Stack overflow is fatal.** Runaway recursion in the translated module
  kills the process instead of returning an error. libghostty's parser is
  a state machine and no recursive path is known, so this is unlikely.
  Mitigations:
  - `FuzzVTWrite` runs on every upgrade.
  - Upstream fixes any crash the fuzzer finds.
  - If this ever happens in practice, switching back to wazero means
    replacing `vt/internal/ghostty/internal/wasmvt` and the instance glue.
    The `vt` API doesn't change.
- **No SIMD.** The build depends on ghostty keeping `-Dcpu=generic`
  working. If upstream makes SIMD mandatory, vibepit needs wasm2go SIMD
  support upstream, or wazero again.
- **wasm2go is young** (v0.4.x, active development). It is pinned in
  `go.mod`, the generated file is checked in CI, and each bump reruns the
  feature tests.
- **Generated code in the repository.** Each ghostty bump rewrites a
  4.4 MB file. Review happens through the upstream range and the feature
  tests.
- `limit.go` depends on an unexported field name in the generated code.
  A rename breaks the build, which is loud but needs a manual fix.
- Only the active screen can be serialized (see Screen Buffers).
- The barrier depends on the terminal answering DSR 5n in order. Every
  common terminal does. Without it, the prompt aborts, and after two
  timeouts it runs in a degraded mode that switches input on input silence.
- The saved cursor (DECSC) and tab stops changed while detached are lost
  on the snapshot path.
- The extras-only formatter patch has to be carried until upstream accepts
  it.
- The prompt output filter relies on Bubble Tea working without replies to
  its queries.
- A forced cut or a resync loses one in-flight sequence for the real
  terminal.
- The shadow and the real terminal disagree after a resize until the app
  redraws. Mitigation: always take the snapshot path after a resize while
  detached.

## Rollout

1. `vt` and `vt/internal/ghostty`: the `.wasm` and its wasm2go
   translation, the internal bindings and the callback, the public API,
   the feature tests, `make ghostty-wasm` with the formatter patches, the
   `ghostty-wasm.yml` workflow, and the provenance and generated-code
   checks in `make test`. The upstream PRs for the formatter patches are
   deferred; the patches stay local.
2. `overlay` with the shadow and `InputMux`, and `--prompt` in `run`.
   Remove the kitty PoC (`kittyPrompter`, `kitty.go`, `approve.go`) in the
   same change.
3. The manual terminal matrix.
4. `connect` support.
5. Phase 2: the `session/` swap, rewritten replay tests, and removal of the
   vt-go dependency.

## Implementation Notes (2026-09-24)

Found while writing the step 1 plan, by probing the pinned commit. Each
has a test in the plan. Items already folded into the sections above:
the mode count, patch 0002, the pen reset, the cursor-shape and mouse
getters, and the out-of-memory behaviour.

- The formatter's `pwd` extra emits OSC 7 with a stray NUL, and its
  `tabstops` extra moves the cursor. `vt.Extras` offers neither. vibepit
  emits OSC 7 itself, as zmx does.
- `vt_write_until_ground` consumes nothing when the parser is already at
  ground. `vt.WriteUntilGround` documents it, and the pump checks
  `AtGround` first.
- libghostty's default charset designation is UTF-8, which `ESC ( B`
  (ASCII) doesn't restore. Real terminals treat `ESC ( B` as the default,
  so the tests compare charsets by printing through them, not by the
  formatter's charset output.
- libghostty dropped a 64 KiB OSC title entirely (the title stayed
  empty). Don't rely on very long titles surviving.
- `vt` adds `SetMode` (to turn synchronized output off around formatting
  without a VT sequence), `WithScrollbackBytes` (both scrollback limits are
  set explicitly), `CursorStyle.DECSCUSR`, and `ErrOutOfMemory`.
- PRs opened by `ghostty-wasm.yml` with `GITHUB_TOKEN` don't trigger
  `build.yml`. The reviewer closes and reopens the PR to run CI.
