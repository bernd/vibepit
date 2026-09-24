# Shadow Terminal, Step 1: `vt` Package Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the `vt` package: libghostty-vt compiled to WebAssembly and translated to plain Go by wasm2go, built with `CGO_ENABLED=0`, behind a library-neutral Go API, with the feature tests that pin every libghostty behaviour vibepit relies on and the build/upgrade pipeline around the `.wasm` and its generated Go code.

**Architecture:** `vt/internal/ghostty/internal/wasmvt` is the translated module: the committed `.wasm`, its provenance file, the wasm2go output `ghostty_vt.go`, and a hand-written memory-limit setter. `vt/internal/ghostty` holds everything else that is libghostty-specific: one module instance per terminal with thin C-ABI calls run under `recover`, the `write_pty` callback as a Go func in the module's function table, the ABI constants, the build script, the patches and the feature tests. `vt` wraps it with a mutex per terminal, a failed state after a trap, Go types, and region/extras options. Nothing exported from `vt` mentions libghostty, wasm2go or WebAssembly.

**Tech Stack:** Go 1.27, `github.com/ncruces/wasm2go` v0.4.15 (a Go tool dependency, used only to regenerate code), libghostty-vt at ghostty commit `7c40388b2c63b7dcc5d6c9b9804e40fb2574444f`, Zig 0.16 (only for rebuilding the `.wasm`), testify, GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-09-24-ghostty-shadow-terminal-design.md`

**Scope:** Rollout step 1 of the spec. The overlay and `--prompt` in `run` (step 2, which also ports only the needed pieces of `add/kitty-prompt`), `connect` (step 4), and the `session/` swap (step 5) get their own plans.

## Global Constraints

- Builds stay `CGO_ENABLED=0` (the Makefile exports it). No cgo anywhere in `vt/`.
- Pinned ghostty commit: `7c40388b2c63b7dcc5d6c9b9804e40fb2574444f`. Build flags: `zig build -Demit-lib-vt -Dtarget=wasm32-freestanding -Dcpu=generic -Doptimize=ReleaseSmall`. Zig 0.16. `-Dcpu=generic` is required: ghostty enables `simd128` for wasm by default, and wasm2go rejects SIMD (`unsupported opcode (SIMD)`).
- Pinned wasm2go: v0.4.15, as `tool github.com/ncruces/wasm2go` in `go.mod`. Flags: `-unsafe -pkg wasmvt`. The `go:generate` line in `wasmvt/doc.go` is the only place they live.
- The `.wasm` and the generated `ghostty_vt.go` are committed. The `.wasm` is not embedded in the binary. Local builds and `build.yml` never need Zig. Never edit `ghostty_vt.go` by hand.
- `GHOSTTY_COMMIT` records the commit, the Zig version and the sha256 of the `.wasm`. A mismatch fails `make test` (`TestProvenance`), and so does a `ghostty_vt.go` that differs from wasm2go's output (`TestGeneratedCodeIsCurrent`).
- Only `vt` imports `vt/internal/ghostty`, and only `vt/internal/ghostty` imports `wasmvt`. Nothing exported from `vt` mentions libghostty, wasm2go or WebAssembly.
- `vt/internal/ghostty` keeps no state across calls beyond the instance and has no locking. `vt` holds one mutex per terminal.
- One module instance (`wasmvt.New()`) per terminal. There is no engine or runtime object.
- Every call into `wasmvt` runs inside `Instance.guard`, which turns a panic into a `*TrapError`. Never keep the linear memory slice across a call into the module: a call can grow memory and move the slice.
- `vt` always sets `SCROLLBACK_MAX_LINES`, `SCROLLBACK_MAX_BYTES` and `CONTINUATION_MAX_BYTES` explicitly. Defaults: 1000 lines, 32 MiB, 64 KiB.
- A trap (a panic inside the translated module, including one from the `write_pty` callback) marks the `vt.Terminal` failed; later calls return an error wrapping `vt.ErrFailed`, never panic. Runaway recursion is a fatal Go stack overflow and can't be caught; the spec records the risk.
- Go style: `gofmt`, comments explain why, `any` not `interface{}`, table-driven tests with subtests, testify `assert`/`require`.
- The `ghostty-wasm.yml` workflow never pushes to `main`. Permissions: `contents: write`, `pull-requests: write`. No `id-token`.

## Findings From Plan Research (deviations from the spec)

Probed against the pinned commit before writing this plan. Every item is covered by a test below.

1. **Cursor shape has no terminal getter.** `GHOSTTY_TERMINAL_DATA_CURSOR_STYLE` is the SGR pen, not DECSCUSR. `vt.CursorStyle` reads shape and blinking through a render state (`ghostty_render_state_*`, `CURSOR_VISUAL_STYLE`, `CURSOR_BLINKING`).
2. **`DATA_MOUSE_TRACKING` is a bool.** `vt.MouseTracking` derives the level from modes 1003, 1002, 1000 and 9.
3. **The mode table has 43 entries**, not 48. `TestModeTableComplete` scans every mode number and fails when upstream adds or removes one.
4. **The formatter emits a wrong cursor position under origin mode (DECOM).** It writes an absolute CUP, which the terminal reads relative to the scroll region. A second patch, `0002-formatter-origin-mode.patch`, makes the cursor extra relative to the region's top-left when DECOM is on. Propose it upstream with `content_none`.
5. **A snapshot restore into a dirty terminal needs a pen reset first.** The formatter only emits the scroll region, charsets, kitty keyboard flags and the open hyperlink when they differ from the default, and content is drawn with whatever SGR is active. The restore harness writes `CSI r`, `CSI 0m`, OSC 8 close, `ESC ( B ESC ) B ESC * B ESC + B SI` and `CSI =0;1u` before `CSI 2J CSI H`. Leaving the alt screen must use the mode that entered it (`?1049l` after `?1049h`), or the mode bit stays set. The overlay plan must copy `snapshotLeave` from `vt/internal/ghostty/restore_test.go`.
6. **The `pwd` extra emits OSC 7 with a stray NUL**, and the `tabstops` extra moves the cursor. `vt.Extras` offers neither.
7. **`vt_write_until_ground` consumes nothing when the parser is already at ground.** `vt.WriteUntilGround` documents it.
8. **libghostty's default charset designation is UTF-8**, which `ESC ( B` (ASCII) doesn't restore. Real terminals treat `ESC ( B` as the default, so tests compare charsets by printing through them (`probeSuffix`), not by formatter output.
9. `vt` adds `SetMode` (the spec's feature tests use `OPT_MODE` to turn off synchronized output around formatting), `WithScrollbackBytes` (the spec requires both limits to be set explicitly), and `CursorStyle.DECSCUSR`.
10. The spec's provenance and generated-code checks are `TestProvenance` and `TestGeneratedCodeIsCurrent` in `wasmvt`, which run in `make test` and therefore in `build.yml` on every PR.
11. PRs opened with `GITHUB_TOKEN` don't trigger `build.yml`. The reviewer closes and reopens the `ghostty-wasm-update` PR to run CI.
12. **At the memory limit, a large Format fails with OUT_OF_MEMORY.** `format_alloc` builds the whole output in linear memory. With a 4 MiB cap and no effective scrollback limit, formatting about 1,600 rows of history fails on every attempt, while formatting the visible screen still works and nothing traps. `vt` maps it to `vt.ErrOutOfMemory` and does not mark the terminal failed. Phase 2's scrollback replay must handle it (skip the history, keep the screen). The defaults keep the cap (256 MiB) far above the scrollback byte limit (32 MiB). A streaming formatter (`ghostty_formatter_format` with a writer callback) would avoid the large allocation; it's a follow-up.

## Review Focus

1. **Writes larger than the 64 KiB input buffer.** `Write` and `WriteUntilGround` must chunk and still find ground past the first chunk. Test: `TestWriteUntilGround/ground_after_the_first_chunk` (Task 3), `TestWriteLargerThanBuffer` (Task 3).
2. **Zero terminal dimensions.** `NewTerminal(0, 24)` and `Resize(80, 0)` return an error and leave the terminal usable. Test: `TestZeroSize` (Task 8).
3. **Use after `Close` and double `Close`.** Calls return `ErrClosed`, never panic. Test: `TestClose` (Task 8).
4. **A `write_pty` callback that keeps its slice.** Later writes must not change bytes the callback already received. Test: `TestWritePtyDataIsCopied` (Task 6).
5. **Scrollback region with no history.** `Format` with `RegionScrollback` on a fresh terminal returns no content and no error. Test: `TestFormatRegions/scrollback_empty` (Task 8).
6. **Linear memory moves when it grows.** Every memory helper re-reads `*mod.Xmemory().Slice()` after the call it follows. Test: `TestScrollbackLimitsBoundMemory` (Task 4) and `TestMemoryLimit` (Task 7) grow memory well past its initial 448 KiB while reading and writing through the helpers.
7. **A panic in the `write_pty` callback.** It surfaces inside the `VTWrite` that triggered it and must become a `*TrapError`, not crash the caller. Test: `TestWritePtyPanicIsTrap` (Task 6).
8. **Format at the memory limit.** A large Format fails with `ErrOutOfMemory`, the terminal is not failed, and a small Format still works. Test: `TestMemoryLimitOption` (Task 8).

---

## File Structure

```
go.mod, go.sum                            modify: tool github.com/ncruces/wasm2go v0.4.15
Makefile                                  modify: ghostty-wasm target, ./vt/... in test-race
AGENTS.md                                 modify: vt package, make target, workflow
.github/workflows/ghostty-wasm.yml        create: weekly/manual rebuild + PR
.github/scripts/ghostty-wasm-pr.sh        create: open or update the PR
vt/
  terminal.go                             package doc, NewTerminal, Terminal, TerminalOption, errors, getters
  format.go                               FormatOptions, Region, Output, Extras
  types.go                                ModeState, CursorStyle, MouseTracking
  terminal_test.go                        public API tests (package vt_test)
  terminal_internal_test.go               failed-state tests (package vt)
  internal/ghostty/
    doc.go                                package doc
    build-wasm.sh                         builds the .wasm at a commit, regenerates the Go code
    README.md                             upgrading libghostty and wasm2go
    patches/0001-formatter-content-none.patch
    patches/0002-formatter-origin-mode.patch
    abi.go                                enums, struct offsets, mode table
    errors.go                             TrapError, CallError, ErrTrap
    instance.go                           Instance: guard, C-ABI calls, memory helpers
    formatter.go                          FormatterOptions, GridRef, FormatAlloc
    callback.go                           write_pty as a function table entry, SetWritePty
    *_test.go                             feature tests
    testdata/*.golden                     formatter goldens
    internal/wasmvt/
      doc.go                              package doc, the go:generate line
      limit.go                            SetMemoryLimitPages (hand-written)
      ghostty-vt.wasm                     built by build-wasm.sh, committed, not embedded
      GHOSTTY_COMMIT                      written by build-wasm.sh, committed
      ghostty_vt.go                       generated by wasm2go, committed, never edited
      provenance_test.go                  TestProvenance, TestGeneratedCodeIsCurrent
```

---

### Task 1: WASM build pipeline, translation to Go and provenance

**Files:**
- Create: `vt/internal/ghostty/patches/0001-formatter-content-none.patch`
- Create: `vt/internal/ghostty/patches/0002-formatter-origin-mode.patch`
- Create: `vt/internal/ghostty/build-wasm.sh`
- Modify: `Makefile`, `go.mod`, `go.sum`
- Create: `vt/internal/ghostty/internal/wasmvt/doc.go`, `vt/internal/ghostty/internal/wasmvt/limit.go`
- Generate: `vt/internal/ghostty/internal/wasmvt/ghostty-vt.wasm`, `GHOSTTY_COMMIT`, `ghostty_vt.go`
- Test: `vt/internal/ghostty/internal/wasmvt/provenance_test.go`

**Interfaces:**
- Produces: package `wasmvt` with `func New() *Module` and the generated export methods (`Xghostty_terminal_new`, `Xghostty_terminal_vt_write`, …, `Xmemory() Memory`, `X__indirect_function_table() *[]any`); `func (m *Module) SetMemoryLimitPages(n int64)`; `make ghostty-wasm GHOSTTY_COMMIT=<sha>`; `build-wasm.sh` exits 3 when a patch does not apply.

- [ ] **Step 1: Add the formatter patches**

`vt/internal/ghostty/patches/0001-formatter-content-none.patch`:

```diff
Expose ScreenFormatter.Content.none in the C API as content_none, so the
formatter can emit extras without cell content.

Upstream: not yet proposed.

diff --git a/include/ghostty/vt/formatter.h b/include/ghostty/vt/formatter.h
index fd593b139..f5709b5ee 100644
--- a/include/ghostty/vt/formatter.h
+++ b/include/ghostty/vt/formatter.h
@@ -116,6 +116,9 @@ typedef struct {
   /** Optional selection to restrict output to a range.
    *  If NULL, the entire screen is formatted. */
   const GhosttySelection *selection;
+
+  /** Emit no content, only the requested extras. Overrides selection. */
+  bool content_none;
 } GhosttyFormatterTerminalOptions;
 
 /**
diff --git a/src/terminal/c/formatter.zig b/src/terminal/c/formatter.zig
index 4ab41cd47..89ba58e38 100644
--- a/src/terminal/c/formatter.zig
+++ b/src/terminal/c/formatter.zig
@@ -72,6 +72,9 @@ pub const TerminalOptions = extern struct {
     /// If null, the entire screen is formatted.
     selection: ?*const CSelection = null,
 
+    /// Emit no content, only the requested extras. Overrides selection.
+    content_none: bool = false,
+
     /// C: GhosttyFormatterTerminalExtra
     pub const Extra = extern struct {
         size: usize = @sizeOf(Extra),
@@ -152,6 +155,7 @@ fn terminal_new_(
         .selection = sel.toZig() orelse
             return error.InvalidValue,
     };
+    if (opts.content_none) formatter.content = .none;
 
     ptr.* = .{
         .kind = .{ .terminal = formatter },
```

`vt/internal/ghostty/patches/0002-formatter-origin-mode.patch`:

```diff
With origin mode (DECOM) on, CUP is relative to the scrolling region, but
the cursor extra emitted absolute coordinates. Emit them relative to the
region's top-left corner instead.

Upstream: not yet proposed.

diff --git a/src/terminal/formatter.zig b/src/terminal/formatter.zig
index 4feedda50..0396149c5 100644
--- a/src/terminal/formatter.zig
+++ b/src/terminal/formatter.zig
@@ -548,6 +548,10 @@ pub const TerminalFormatter = struct {
         // cursor last.
         screen_formatter.content = .none;
         screen_formatter.extra = self.extra.screen;
+        if (self.terminal.modes.get(.origin)) {
+            const region = &self.terminal.scrolling_region;
+            screen_formatter.cursor_origin = .{ .x = region.left, .y = region.top };
+        }
         try screen_formatter.format(writer);
     }
 };
@@ -567,6 +571,11 @@ pub const ScreenFormatter = struct {
     /// This information is ONLY emitted when the format is "vt".
     extra: Extra,
 
+    /// Cursor positions are emitted relative to this cell. With origin
+    /// mode on, CUP is relative to the scrolling region, so
+    /// TerminalFormatter sets this to the region's top-left corner.
+    cursor_origin: struct { x: usize = 0, y: usize = 0 } = .{},
+
     /// If non-null, then `map` will contain the Pin of every byte
     /// byte written to the writer offset by the byte index. It is the
     /// caller's responsibility to free the map.
@@ -704,7 +713,10 @@ pub const ScreenFormatter = struct {
 
             // If we don't have pending wrap, then we can just use CUP.
             if (!cursor.pending_wrap or cursor.x != self.screen.pages.cols - 1) {
-                try writer.print("\x1b[{d};{d}H", .{ cursor.y + 1, cursor.x + 1 });
+                try writer.print("\x1b[{d};{d}H", .{
+                    cursor.y -| self.cursor_origin.y + 1,
+                    cursor.x -| self.cursor_origin.x + 1,
+                });
                 break :cursor;
             }
 
@@ -717,7 +729,10 @@ pub const ScreenFormatter = struct {
             // Move cursor to the edge.
             try writer.print(
                 "\x1b[{d};{d}H",
-                .{ cursor.y + 1, start_x + 1 },
+                .{
+                    cursor.y -| self.cursor_origin.y + 1,
+                    start_x -| self.cursor_origin.x + 1,
+                },
             );
 
             // Reformat the cell which sets the proper pending wrap state.
```

- [ ] **Step 2: Add the build script**

`vt/internal/ghostty/build-wasm.sh` (then `chmod +x vt/internal/ghostty/build-wasm.sh`):

```bash
#!/usr/bin/env bash
# Builds ghostty-vt.wasm at a pinned ghostty commit with vibepit's patches,
# records its provenance in GHOSTTY_COMMIT, and translates it to Go with
# wasm2go (the go:generate line in internal/wasmvt/doc.go).
#
# Exit codes: 3 = a patch does not apply (the ghostty-wasm workflow reports
# this category separately), anything else nonzero = build failure.
set -euo pipefail

commit="${1:?usage: build-wasm.sh <ghostty-commit>}"
here="$(cd "$(dirname "$0")" && pwd)"
out="$here/internal/wasmvt"
src="${GHOSTTY_SRC:-${TMPDIR:-/tmp}/vibepit-ghostty-src}"

zig_version="$(zig version)"
case "$zig_version" in
  0.16.*) ;;
  *) echo "need Zig 0.16, found $zig_version" >&2; exit 1 ;;
esac

if [ ! -d "$src/.git" ]; then
  git clone --filter=blob:none https://github.com/ghostty-org/ghostty.git "$src"
fi
git -C "$src" fetch --quiet origin
# --force drops the patches applied by an earlier run.
git -C "$src" checkout --quiet --force "$commit"
full="$(git -C "$src" rev-parse HEAD)"

for p in "$here"/patches/*.patch; do
  if ! git -C "$src" apply --check "$p"; then
    echo "patch does not apply: $(basename "$p")" >&2
    exit 3
  fi
  git -C "$src" apply "$p"
done

# ghostty enables simd128 on wasm unless a CPU is given, and wasm2go can't
# translate SIMD instructions.
(cd "$src" && zig build -Demit-lib-vt -Dtarget=wasm32-freestanding -Dcpu=generic -Doptimize=ReleaseSmall)

cp "$src/zig-out/bin/ghostty-vt.wasm" "$out/ghostty-vt.wasm"
if command -v sha256sum >/dev/null; then
  sum="$(sha256sum "$out/ghostty-vt.wasm" | cut -d' ' -f1)"
else
  sum="$(shasum -a 256 "$out/ghostty-vt.wasm" | cut -d' ' -f1)"
fi
printf 'commit=%s\nzig=%s\nsha256=%s\n' "$full" "$zig_version" "$sum" > "$out/GHOSTTY_COMMIT"
(cd "$out" && go generate .)
echo "built ghostty-vt.wasm at $full ($sum) and regenerated ghostty_vt.go"
```

- [ ] **Step 3: Add the Make target**

In `Makefile`, add `ghostty-wasm` to `.PHONY`, add `./vt/...` to `test-race`, and add the target after `test-bats`:

```make
.PHONY: build test test-race test-integration clean release-build release-archive release-publish docs-install docs-build docs-serve ghostty-wasm
```

```make
test-race:
	CGO_ENABLED=1 go test -race ./session ./sshd ./cmd ./vt/...
```

```make
# Rebuilds vt/internal/ghostty/internal/wasmvt/ghostty-vt.wasm and its Go
# translation. Needs Zig 0.16 and network access to github.com,
# deps.files.ghostty.org and codeberg.org.
GHOSTTY_COMMIT ?= $(shell sed -n 's/^commit=//p' vt/internal/ghostty/internal/wasmvt/GHOSTTY_COMMIT 2>/dev/null)

ghostty-wasm:
	@[ -n "$(GHOSTTY_COMMIT)" ] || { echo "GHOSTTY_COMMIT is required"; exit 1; }
	vt/internal/ghostty/build-wasm.sh $(GHOSTTY_COMMIT)
```

- [ ] **Step 4: Add wasm2go and the `wasmvt` package files**

Run: `go get -tool github.com/ncruces/wasm2go@v0.4.15`
Expected: `go.mod` gains `tool github.com/ncruces/wasm2go` and gains `github.com/ncruces/wasm2go v0.4.15 // indirect` and `golang.org/x/tools v0.50.0 // indirect` (wasm2go needs it; neither is in `go.mod` today). `go tool wasm2go -version` prints `wasm2go v0.4.15`.

`vt/internal/ghostty/internal/wasmvt/doc.go`:

```go
// Package wasmvt is libghostty-vt, compiled to WebAssembly and translated
// to Go by wasm2go. ghostty_vt.go is generated from ghostty-vt.wasm by the
// go:generate line below, which is the only place its flags live. Never
// edit it: run make ghostty-wasm, or go generate after a wasm2go bump.
//
// -unsafe lets the generated code load and store through package unsafe.
// Every access is still bounds-checked, and throughput is about 1.8x the
// safe output.
//
// Only package ghostty may import it.
package wasmvt

//go:generate go tool wasm2go -unsafe -pkg wasmvt -o ghostty_vt.go ghostty-vt.wasm
```

`vt/internal/ghostty/internal/wasmvt/limit.go`:

```go
package wasmvt

// SetMemoryLimitPages caps linear memory growth, in 64 KiB pages. Past the
// cap, memory.grow fails and libghostty sees an allocation failure. The
// generated code keeps the limit in an unexported field; a wasm2go upgrade
// that renames it breaks this file's build, not silently the limit.
func (m *Module) SetMemoryLimitPages(n int64) { m.maxMem = n }
```

- [ ] **Step 5: Write the failing test**

`vt/internal/ghostty/internal/wasmvt/provenance_test.go`:

```go
package wasmvt

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// provenance records where ghostty-vt.wasm came from.
type provenance struct {
	Commit string
	Zig    string
	SHA256 string
}

// parseProvenance parses the key=value lines of a GHOSTTY_COMMIT file.
func parseProvenance(s string) (provenance, error) {
	var p provenance
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			return provenance{}, fmt.Errorf("GHOSTTY_COMMIT: malformed line %q", line)
		}
		switch k {
		case "commit":
			p.Commit = v
		case "zig":
			p.Zig = v
		case "sha256":
			p.SHA256 = v
		default:
			return provenance{}, fmt.Errorf("GHOSTTY_COMMIT: unknown key %q", k)
		}
	}
	if p.Commit == "" || p.Zig == "" || p.SHA256 == "" {
		return provenance{}, errors.New("GHOSTTY_COMMIT: missing commit, zig or sha256")
	}
	return p, nil
}

func TestParseProvenance(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    provenance
		wantErr string
	}{
		{
			name: "valid",
			in:   "commit=abc\nzig=0.16.0\nsha256=ff\n",
			want: provenance{Commit: "abc", Zig: "0.16.0", SHA256: "ff"},
		},
		{name: "missing sha256", in: "commit=abc\nzig=0.16.0\n", wantErr: "missing"},
		{name: "unknown key", in: "commit=abc\nfoo=1\n", wantErr: "unknown key"},
		{name: "malformed line", in: "commit abc\n", wantErr: "malformed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseProvenance(tt.in)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestProvenance ties the committed .wasm to its source commit. It runs in
// make test, so build.yml fails on a PR whose .wasm doesn't match
// GHOSTTY_COMMIT.
func TestProvenance(t *testing.T) {
	commitFile, err := os.ReadFile("GHOSTTY_COMMIT")
	require.NoError(t, err)
	p, err := parseProvenance(string(commitFile))
	require.NoError(t, err)
	wasm, err := os.ReadFile("ghostty-vt.wasm")
	require.NoError(t, err)

	sum := sha256.Sum256(wasm)
	assert.Equal(t, hex.EncodeToString(sum[:]), p.SHA256,
		"ghostty-vt.wasm does not match GHOSTTY_COMMIT; rebuild it with make ghostty-wasm")
	assert.Len(t, p.Commit, 40)
	assert.True(t, strings.HasPrefix(p.Zig, "0.16."), "zig version %q", p.Zig)
}

// TestGeneratedCodeIsCurrent ties ghostty_vt.go to the committed .wasm and
// the wasm2go version pinned in go.mod. Translation is deterministic, so
// any difference is a stale or hand-edited file. It runs the go:generate
// line from doc.go, so the flags can't drift.
func TestGeneratedCodeIsCurrent(t *testing.T) {
	out := filepath.Join(t.TempDir(), "ghostty_vt.go")
	cmd := exec.Command("go", generateArgs(t, out)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Run(), "wasm2go: %s", stderr.String())

	want, err := os.ReadFile(out)
	require.NoError(t, err)
	got, err := os.ReadFile("ghostty_vt.go")
	require.NoError(t, err)
	// Not assert.Equal: a diff of a 4 MB file is unreadable.
	assert.True(t, bytes.Equal(want, got),
		"ghostty_vt.go is not wasm2go's output for ghostty-vt.wasm; run go generate ./vt/internal/ghostty/internal/wasmvt")
}

// generateArgs returns the arguments of doc.go's go:generate line after
// "go", with the output redirected to out.
func generateArgs(t *testing.T, out string) []string {
	t.Helper()
	src, err := os.ReadFile("doc.go")
	require.NoError(t, err)
	for _, line := range strings.Split(string(src), "\n") {
		rest, ok := strings.CutPrefix(line, "//go:generate go ")
		if !ok {
			continue
		}
		args := strings.Fields(rest)
		for i := range args[:len(args)-1] {
			if args[i] == "-o" {
				args[i+1] = out
			}
		}
		return args
	}
	t.Fatal("doc.go has no go:generate line")
	return nil
}
```

- [ ] **Step 6: Run the test to verify it fails**

Run: `go test ./vt/internal/ghostty/internal/wasmvt/`
Expected: FAIL to compile: `undefined: Module` (in `limit.go`; the generated code doesn't exist yet).

- [ ] **Step 7: Build and translate the module**

Run: `make ghostty-wasm GHOSTTY_COMMIT=7c40388b2c63b7dcc5d6c9b9804e40fb2574444f`

Expected: `built ghostty-vt.wasm at 7c40388b2c63b7dcc5d6c9b9804e40fb2574444f (<sha>) and regenerated ghostty_vt.go`, a `.wasm` of about 818 KB, a `ghostty_vt.go` of about 4.4 MB starting with `// Code generated by wasm2go. DO NOT EDIT.`, and a `GHOSTTY_COMMIT` like:

```
commit=7c40388b2c63b7dcc5d6c9b9804e40fb2574444f
zig=0.16.0
sha256=<64 hex chars>
```

If wasm2go fails with `unsupported opcode (SIMD)`, the build ran without `-Dcpu=generic`.

Inside a vibepit sandbox, Zig's package fetcher can fail through the proxy (`HttpConnectionClosing`). Then download each missing dependency URL from the error with `curl -LO`, register it with `zig fetch <file>`, and rerun until nothing is missing.

- [ ] **Step 8: Run the tests to verify they pass**

Run: `go test ./vt/internal/ghostty/internal/wasmvt/ && go vet ./vt/internal/ghostty/internal/wasmvt/ && gofmt -l vt`
Expected: PASS; `go vet` and `gofmt -l` print nothing. The first build of the package takes about 5 s, later builds come from the build cache.

- [ ] **Step 9: Commit**

```bash
git add go.mod go.sum Makefile vt/internal/ghostty
git commit -m "Build libghostty-vt as WebAssembly and translate it to Go"
```

---

### Task 2: Instance core and ABI checks

**Files:**
- Create: `vt/internal/ghostty/doc.go`, `abi.go`, `errors.go`, `instance.go`
- Test: `vt/internal/ghostty/helpers_test.go`, `abi_test.go`, `lifecycle_test.go`

**Interfaces:**
- Consumes: package `wasmvt` (Task 1).
- Produces:
  - `type Config struct{ MemoryLimitPages uint32 }`, `func NewInstance(cfg Config) (*Instance, error)`.
  - `Instance` methods: `MemorySize() uint64`, `Close() error`, `Alloc(n uint32) (uint32, error)`, `Free(p, n uint32) error`, `TerminalNew(cols, rows uint16) error`, `TerminalFree() error`, `TerminalReset() error`, `TerminalResize(cols, rows uint16, cellWidthPx, cellHeightPx uint32) error`, `TerminalSetPtr(opt TerminalOption, v uint32) error`, `TerminalSetSize(opt TerminalOption, v uint32) error`, `TerminalSetMode(mode uint16, value bool) error`, `GetU8/GetU16/GetU32/GetBool/GetString(d TerminalData)`, `GetMode(mode uint16) (bool, error)`, `VTWrite(p []byte) error`, `TypeJSON() (string, error)`.
  - Unexported helpers used by Tasks 3–7: `guard`, `call`, `callResult`, `invoke`, `takeOpaque`, `mem`, `read`, `writeMem`, `readU32`, `readByte`, `scratch` offsets `scrOut`, `scrOutPtr`, `scrOutLen`, `scrPoint`, `scrSelection`, `scrFormatter`, `scrMode`; fields `mod`, `term`, `slot`, `render`, `ptyIndex`, `writePty`.
  - Errors: `ErrTrap`, `*TrapError{Func string; Err error}`, `*CallError{Func string; Result Result}`, `Result` implements `error`.
  - ABI: every constant in `abi.go` below; `type ModeEntry`, `func EncodeMode(value uint16, ansi bool) uint16`, `var Modes []ModeEntry`.
  - Test helpers: `newTerm`, `withTerm`, `feed`, `update` flag.

The compiler replaces three checks the wazero design needed at run time: `wasmvt.New()` takes no arguments (the module imports nothing), every export this package calls is a typed method (it exists with the expected signature), and `X__indirect_function_table` exists (the table is exported).

- [ ] **Step 1: Write the test helpers and failing tests**

`vt/internal/ghostty/helpers_test.go`:

```go
package ghostty

import (
	"flag"
	"testing"

	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden")

// newTerm returns a terminal that is closed when the test ends.
func newTerm(t testing.TB, cols, rows uint16) *Instance {
	t.Helper()
	in, err := NewInstance(Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = in.Close() })
	require.NoError(t, in.TerminalNew(cols, rows))
	return in
}

// withTerm runs fn with a terminal that is closed right after, for loops
// that would otherwise keep hundreds of instances (448 KiB of linear memory
// each) alive until the test ends.
func withTerm(t testing.TB, cols, rows uint16, fn func(in *Instance)) {
	t.Helper()
	in, err := NewInstance(Config{})
	require.NoError(t, err)
	defer func() { _ = in.Close() }()
	require.NoError(t, in.TerminalNew(cols, rows))
	fn(in)
}

func feed(t testing.TB, in *Instance, s string) {
	t.Helper()
	require.NoError(t, in.VTWrite([]byte(s)))
}
```

`vt/internal/ghostty/abi_test.go`:

```go
package ghostty

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestABIMatchesTypeJSON compares every hard-coded layout and enum value
// with the module's own description. A ghostty bump that changes one fails
// here, not in production.
func TestABIMatchesTypeJSON(t *testing.T) {
	in, err := NewInstance(Config{})
	require.NoError(t, err)
	defer func() { _ = in.Close() }()
	raw, err := in.TypeJSON()
	require.NoError(t, err)

	var doc struct {
		ABI struct {
			Target      string `json:"target"`
			PointerSize int    `json:"pointer_size"`
			UsizeSize   int    `json:"usize_size"`
			Endian      string `json:"endian"`
		} `json:"abi"`
		Types map[string]struct {
			Size   uint32 `json:"size"`
			Fields map[string]struct {
				Offset uint32 `json:"offset"`
			} `json:"fields"`
			Values map[string]int64 `json:"values"`
		} `json:"types"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &doc))
	assert.Equal(t, "wasm32", doc.ABI.Target)
	assert.Equal(t, 4, doc.ABI.PointerSize)
	assert.Equal(t, 4, doc.ABI.UsizeSize)
	assert.Equal(t, "little", doc.ABI.Endian)

	structs := []struct {
		name   string
		size   uint32
		fields map[string]uint32
	}{
		{"GhosttyString", sizeofString, map[string]uint32{"ptr": offStringPtr, "len": offStringLen}},
		{"GhosttyTerminalModeConfig", sizeofModeConfig, map[string]uint32{"mode": offModeConfigMode, "value": offModeConfigValue}},
		{"GhosttyPoint", sizeofPoint, map[string]uint32{"tag": offPointTag, "value": offPointValue}},
		{"GhosttyPointCoordinate", sizeofCoordinate, map[string]uint32{"x": offCoordinateX, "y": offCoordinateY}},
		{"GhosttyGridRef", sizeofGridRef, map[string]uint32{
			"size": offGridRefSize, "node": offGridRefNode, "x": offGridRefX, "y": offGridRefY,
		}},
		{"GhosttySelection", sizeofSelection, map[string]uint32{
			"size": offSelectionSize, "start": offSelectionStart, "end": offSelectionEnd, "rectangle": offSelectionRectangle,
		}},
		{"GhosttyFormatterTerminalOptions", sizeofFormatterOptions, map[string]uint32{
			"size": offFmtSize, "emit": offFmtEmit, "unwrap": offFmtUnwrap, "trim": offFmtTrim,
			"extra": offFmtExtra, "selection": offFmtSelection, "content_none": offFmtContentNone,
		}},
		{"GhosttyFormatterTerminalExtra", sizeofTerminalExtra, map[string]uint32{
			"size": offTExtraSize, "palette": offTExtraPalette, "modes": offTExtraModes,
			"scrolling_region": offTExtraScrollRegion, "tabstops": offTExtraTabstops,
			"pwd": offTExtraPwd, "keyboard": offTExtraKeyboard, "screen": offTExtraScreen,
		}},
		{"GhosttyFormatterScreenExtra", sizeofScreenExtra, map[string]uint32{
			"size": offSExtraSize, "cursor": offSExtraCursor, "style": offSExtraStyle,
			"hyperlink": offSExtraHyperlink, "protection": offSExtraProtection,
			"kitty_keyboard": offSExtraKittyKeyboard, "charsets": offSExtraCharsets,
		}},
	}
	for _, s := range structs {
		t.Run(s.name, func(t *testing.T) {
			typ, ok := doc.Types[s.name]
			require.True(t, ok, "type missing from ghostty_type_json")
			assert.Equal(t, s.size, typ.Size, "size")
			assert.Len(t, typ.Fields, len(s.fields), "field count")
			for field, off := range s.fields {
				got, ok := typ.Fields[field]
				if assert.True(t, ok, "field %s missing", field) {
					assert.Equal(t, off, got.Offset, "offset of %s", field)
				}
			}
		})
	}

	enums := []struct {
		typ, name string
		want      int64
	}{
		{"GhosttyResult", "SUCCESS", int64(Success)},
		{"GhosttyResult", "OUT_OF_MEMORY", int64(OutOfMemory)},
		{"GhosttyResult", "INVALID_VALUE", int64(InvalidValue)},
		{"GhosttyResult", "OUT_OF_SPACE", int64(OutOfSpace)},
		{"GhosttyResult", "NO_VALUE", int64(NoValue)},
		{"GhosttyResult", "IO_ERROR", int64(IOError)},
		{"GhosttyResult", "LIMIT_EXCEEDED", int64(LimitExceeded)},
		{"GhosttyResult", "REJECTED", int64(Rejected)},
		{"GhosttyTerminalOption", "WRITE_PTY", int64(OptWritePty)},
		{"GhosttyTerminalOption", "SCROLLBACK_MAX_BYTES", int64(OptScrollbackMaxBytes)},
		{"GhosttyTerminalOption", "SCROLLBACK_MAX_LINES", int64(OptScrollbackMaxLines)},
		{"GhosttyTerminalOption", "CONTINUATION_MAX_BYTES", int64(OptContinuationMaxBytes)},
		{"GhosttyTerminalOption", "MODE", int64(OptMode)},
		{"GhosttyTerminalData", "COLS", int64(DataCols)},
		{"GhosttyTerminalData", "ROWS", int64(DataRows)},
		{"GhosttyTerminalData", "CURSOR_X", int64(DataCursorX)},
		{"GhosttyTerminalData", "CURSOR_Y", int64(DataCursorY)},
		{"GhosttyTerminalData", "CURSOR_PENDING_WRAP", int64(DataCursorPendingWrap)},
		{"GhosttyTerminalData", "ACTIVE_SCREEN", int64(DataActiveScreen)},
		{"GhosttyTerminalData", "CURSOR_VISIBLE", int64(DataCursorVisible)},
		{"GhosttyTerminalData", "KITTY_KEYBOARD_FLAGS", int64(DataKittyKeyboardFlags)},
		{"GhosttyTerminalData", "MOUSE_TRACKING", int64(DataMouseTracking)},
		{"GhosttyTerminalData", "TITLE", int64(DataTitle)},
		{"GhosttyTerminalData", "PWD", int64(DataPwd)},
		{"GhosttyTerminalData", "TOTAL_ROWS", int64(DataTotalRows)},
		{"GhosttyTerminalData", "SCROLLBACK_ROWS", int64(DataScrollbackRows)},
		{"GhosttyTerminalData", "SCROLLBACK_MAX_BYTES", int64(DataScrollbackMaxBytes)},
		{"GhosttyTerminalData", "SCROLLBACK_MAX_LINES", int64(DataScrollbackMaxLines)},
		{"GhosttyTerminalData", "CONTINUATION_MAX_BYTES", int64(DataContinuationMaxBytes)},
		{"GhosttyTerminalData", "MODE", int64(DataMode)},
		{"GhosttyTerminalData", "VT_GROUND", int64(DataVTGround)},
		{"GhosttyTerminalScreen", "PRIMARY", int64(ScreenPrimary)},
		{"GhosttyTerminalScreen", "ALTERNATE", int64(ScreenAlternate)},
		{"GhosttyFormatterFormat", "PLAIN", int64(FormatPlain)},
		{"GhosttyFormatterFormat", "VT", int64(FormatVT)},
		{"GhosttyFormatterFormat", "HTML", int64(FormatHTML)},
		{"GhosttyPointTag", "ACTIVE", int64(PointActive)},
		{"GhosttyPointTag", "VIEWPORT", int64(PointViewport)},
		{"GhosttyPointTag", "SCREEN", int64(PointScreen)},
		{"GhosttyPointTag", "HISTORY", int64(PointHistory)},
		{"GhosttyRenderStateData", "CURSOR_VISUAL_STYLE", int64(RenderDataCursorVisualStyle)},
		{"GhosttyRenderStateData", "CURSOR_BLINKING", int64(RenderDataCursorBlinking)},
		{"GhosttyRenderStateCursorVisualStyle", "BAR", int64(CursorVisualBar)},
		{"GhosttyRenderStateCursorVisualStyle", "BLOCK", int64(CursorVisualBlock)},
		{"GhosttyRenderStateCursorVisualStyle", "UNDERLINE", int64(CursorVisualUnderline)},
		{"GhosttyRenderStateCursorVisualStyle", "BLOCK_HOLLOW", int64(CursorVisualBlockHollow)},
	}
	for _, e := range enums {
		v, ok := doc.Types[e.typ].Values[e.name]
		if assert.True(t, ok, "%s.%s missing", e.typ, e.name) {
			assert.Equal(t, e.want, v, "%s.%s", e.typ, e.name)
		}
	}
}
```

`vt/internal/ghostty/lifecycle_test.go`:

```go
package ghostty

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTerminalLifecycle(t *testing.T) {
	in := newTerm(t, 80, 24)
	size := func() (uint16, uint16) {
		cols, err := in.GetU16(DataCols)
		require.NoError(t, err)
		rows, err := in.GetU16(DataRows)
		require.NoError(t, err)
		return cols, rows
	}

	cols, rows := size()
	assert.EqualValues(t, 80, cols)
	assert.EqualValues(t, 24, rows)

	require.NoError(t, in.TerminalResize(100, 30, 0, 0))
	cols, rows = size()
	assert.EqualValues(t, 100, cols)
	assert.EqualValues(t, 30, rows)

	bracketedPaste := EncodeMode(2004, false)
	feed(t, in, "\x1b[?2004h")
	on, err := in.GetMode(bracketedPaste)
	require.NoError(t, err)
	assert.True(t, on)

	require.NoError(t, in.TerminalReset())
	on, err = in.GetMode(bracketedPaste)
	require.NoError(t, err)
	assert.False(t, on, "reset restores mode defaults")

	require.NoError(t, in.TerminalFree())
	require.NoError(t, in.TerminalNew(10, 5), "an instance can hold a new terminal after free")
	cols, _ = size()
	assert.EqualValues(t, 10, cols)

	require.NoError(t, in.Close())
	require.NoError(t, in.Close(), "Close is idempotent")
}

// OPT_MODE sets a mode without a VT sequence. The overlay uses it to turn
// synchronized output off around formatting.
func TestSetModeOption(t *testing.T) {
	in := newTerm(t, 80, 24)
	sync := EncodeMode(2026, false)
	for _, want := range []bool{true, false} {
		require.NoError(t, in.TerminalSetMode(sync, want))
		got, err := in.GetMode(sync)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
}

func TestUnknownModeIsInvalidValue(t *testing.T) {
	in := newTerm(t, 80, 24)
	_, err := in.GetMode(EncodeMode(9999, false))
	assert.ErrorIs(t, err, InvalidValue)
}

func TestWriteLargerThanBuffer(t *testing.T) {
	in := newTerm(t, 80, 24)
	data := make([]byte, 0, inputBufSize*2+100)
	for len(data) < inputBufSize*2 {
		data = append(data, "0123456789abcdef"...)
	}
	data = append(data, "\x1b]2;end\x07"...)
	require.NoError(t, in.VTWrite(data))
	title, err := in.GetString(DataTitle)
	require.NoError(t, err)
	assert.Equal(t, "end", title, "bytes after the first 64 KiB chunk are parsed")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./vt/internal/ghostty/`
Expected: FAIL to compile: `undefined: Instance`, `undefined: NewInstance`.

- [ ] **Step 3: Implement `abi.go`**

```go
package ghostty

import "fmt"

// The values in this file mirror include/ghostty/vt/*.h and
// src/terminal/modes.zig at the commit in GHOSTTY_COMMIT, for wasm32.
// TestABIMatchesTypeJSON and TestModeTableComplete check them against the
// translated module.

// Result is GhosttyResult.
type Result int32

const (
	Success       Result = 0
	OutOfMemory   Result = -1
	InvalidValue  Result = -2
	OutOfSpace    Result = -3
	NoValue       Result = -4
	IOError       Result = -5
	LimitExceeded Result = -6
	Rejected      Result = -7
)

var resultNames = map[Result]string{
	Success:       "SUCCESS",
	OutOfMemory:   "OUT_OF_MEMORY",
	InvalidValue:  "INVALID_VALUE",
	OutOfSpace:    "OUT_OF_SPACE",
	NoValue:       "NO_VALUE",
	IOError:       "IO_ERROR",
	LimitExceeded: "LIMIT_EXCEEDED",
	Rejected:      "REJECTED",
}

func (r Result) String() string {
	if n, ok := resultNames[r]; ok {
		return n
	}
	return fmt.Sprintf("result %d", int32(r))
}

func (r Result) Error() string { return "ghostty: " + r.String() }

// TerminalOption is GhosttyTerminalOption.
type TerminalOption int32

const (
	OptWritePty             TerminalOption = 1
	OptScrollbackMaxBytes   TerminalOption = 27
	OptScrollbackMaxLines   TerminalOption = 28
	OptContinuationMaxBytes TerminalOption = 31
	OptMode                 TerminalOption = 34
)

// TerminalData is GhosttyTerminalData.
type TerminalData int32

const (
	DataCols                 TerminalData = 1
	DataRows                 TerminalData = 2
	DataCursorX              TerminalData = 3
	DataCursorY              TerminalData = 4
	DataCursorPendingWrap    TerminalData = 5
	DataActiveScreen         TerminalData = 6
	DataCursorVisible        TerminalData = 7
	DataKittyKeyboardFlags   TerminalData = 8
	DataMouseTracking        TerminalData = 11
	DataTitle                TerminalData = 12
	DataPwd                  TerminalData = 13
	DataTotalRows            TerminalData = 14
	DataScrollbackRows       TerminalData = 15
	DataScrollbackMaxBytes   TerminalData = 34
	DataScrollbackMaxLines   TerminalData = 35
	DataContinuationMaxBytes TerminalData = 36
	DataMode                 TerminalData = 37
	DataVTGround             TerminalData = 38
)

// GhosttyTerminalScreen values, as returned for DataActiveScreen.
const (
	ScreenPrimary   int32 = 0
	ScreenAlternate int32 = 1
)

// FormatterFormat is GhosttyFormatterFormat.
type FormatterFormat int32

const (
	FormatPlain FormatterFormat = 0
	FormatVT    FormatterFormat = 1
	FormatHTML  FormatterFormat = 2
)

// PointTag is GhosttyPointTag.
type PointTag int32

const (
	PointActive   PointTag = 0
	PointViewport PointTag = 1
	PointScreen   PointTag = 2
	PointHistory  PointTag = 3
)

// RenderStateData is GhosttyRenderStateData.
type RenderStateData int32

const (
	RenderDataCursorVisualStyle RenderStateData = 10
	RenderDataCursorBlinking    RenderStateData = 12
)

// CursorVisualStyle is GhosttyRenderStateCursorVisualStyle.
type CursorVisualStyle int32

const (
	CursorVisualBar         CursorVisualStyle = 0
	CursorVisualBlock       CursorVisualStyle = 1
	CursorVisualUnderline   CursorVisualStyle = 2
	CursorVisualBlockHollow CursorVisualStyle = 3
)

// Struct sizes and field offsets on wasm32.
const (
	sizeofString = 8
	offStringPtr = 0
	offStringLen = 4

	sizeofModeConfig   = 4
	offModeConfigMode  = 0
	offModeConfigValue = 2

	sizeofPoint      = 24
	offPointTag      = 0
	offPointValue    = 8
	sizeofCoordinate = 8
	offCoordinateX   = 0
	offCoordinateY   = 4

	sizeofGridRef  = 12
	offGridRefSize = 0
	offGridRefNode = 4 // opaque; GridRef only writes size
	offGridRefX    = 8
	offGridRefY    = 10

	sizeofSelection       = 32
	offSelectionSize      = 0
	offSelectionStart     = 4
	offSelectionEnd       = 16
	offSelectionRectangle = 28

	sizeofFormatterOptions = 44
	offFmtSize             = 0
	offFmtEmit             = 4
	offFmtUnwrap           = 8
	offFmtTrim             = 9
	offFmtExtra            = 12
	offFmtSelection        = 36
	offFmtContentNone      = 40 // patches/0001-formatter-content-none.patch

	sizeofTerminalExtra   = 24
	offTExtraSize         = 0
	offTExtraPalette      = 4
	offTExtraModes        = 5
	offTExtraScrollRegion = 6
	offTExtraTabstops     = 7
	offTExtraPwd          = 8
	offTExtraKeyboard     = 9
	offTExtraScreen       = 12

	sizeofScreenExtra      = 12
	offSExtraSize          = 0
	offSExtraCursor        = 4
	offSExtraStyle         = 5
	offSExtraHyperlink     = 6
	offSExtraProtection    = 7
	offSExtraKittyKeyboard = 8
	offSExtraCharsets      = 9
)

// ModeEntry is one entry of the mode table in src/terminal/modes.zig.
type ModeEntry struct {
	Value   uint16
	ANSI    bool
	Default bool
}

// Mode returns the entry as a GhosttyMode.
func (m ModeEntry) Mode() uint16 { return EncodeMode(m.Value, m.ANSI) }

// EncodeMode builds a GhosttyMode, like ghostty_mode_new.
func EncodeMode(value uint16, ansi bool) uint16 {
	v := value & 0x7fff
	if ansi {
		v |= 0x8000
	}
	return v
}

// Modes lists every mode libghostty knows, in modes.zig order.
var Modes = []ModeEntry{
	{2, true, false},     // KAM
	{4, true, false},     // IRM
	{12, true, true},     // SRM
	{20, true, false},    // LNM
	{1, false, false},    // DECCKM
	{3, false, false},    // DECCOLM
	{4, false, false},    // slow scroll
	{5, false, false},    // DECSCNM
	{6, false, false},    // DECOM
	{7, false, true},     // DECAWM
	{8, false, false},    // autorepeat
	{9, false, false},    // X10 mouse
	{12, false, false},   // cursor blinking
	{25, false, true},    // DECTCEM
	{40, false, false},   // allow 132 columns
	{45, false, false},   // reverse wrap
	{47, false, false},   // alt screen (legacy)
	{66, false, false},   // DECNKM
	{67, false, false},   // DECBKM
	{69, false, false},   // DECLRMM
	{1000, false, false}, // normal mouse
	{1002, false, false}, // button mouse
	{1003, false, false}, // any mouse
	{1004, false, false}, // focus events
	{1005, false, false}, // UTF-8 mouse
	{1006, false, false}, // SGR mouse
	{1007, false, true},  // alternate scroll
	{1015, false, false}, // urxvt mouse
	{1016, false, false}, // SGR-pixels mouse
	{1035, false, true},  // ignore keypad with NumLock
	{1036, false, true},  // alt sends ESC prefix
	{1039, false, false}, // alt sends escape
	{1045, false, false}, // extended reverse wrap
	{1047, false, false}, // alt screen
	{1048, false, false}, // save cursor
	{1049, false, false}, // alt screen + save cursor + clear
	{2004, false, false}, // bracketed paste
	{2026, false, false}, // synchronized output
	{2027, false, false}, // grapheme clusters
	{2031, false, false}, // color scheme reports
	{2033, false, false}, // visibility reports
	{2048, false, false}, // in-band size reports
	{5522, false, false}, // kitty paste events
}
```

- [ ] **Step 4: Implement `errors.go`**

```go
package ghostty

import (
	"errors"
	"fmt"
)

// ErrTrap matches every *TrapError. After a trap the instance's state can't
// be trusted.
var ErrTrap = errors.New("ghostty: wasm trap")

// TrapError is a WebAssembly trap, which in translated code is a panic
// (including one from the write_pty callback), or a pointer from the module
// that points outside its memory.
type TrapError struct {
	Func string
	Err  error
}

func (e *TrapError) Error() string        { return fmt.Sprintf("%s: wasm trap: %v", e.Func, e.Err) }
func (e *TrapError) Is(target error) bool { return target == ErrTrap }
func (e *TrapError) Unwrap() error        { return e.Err }

// CallError is a C function that returned a GhosttyResult other than
// SUCCESS. errors.Is matches the Result.
type CallError struct {
	Func   string
	Result Result
}

func (e *CallError) Error() string { return fmt.Sprintf("ghostty: %s returned %s", e.Func, e.Result) }
func (e *CallError) Unwrap() error { return e.Result }

var errOutOfBounds = errors.New("memory access out of bounds")
```

- [ ] **Step 5: Implement `doc.go` and `instance.go`**

`vt/internal/ghostty/doc.go`:

```go
// Package ghostty binds libghostty-vt, compiled to WebAssembly and
// translated to Go by wasm2go (package wasmvt). Its functions mirror the C
// API one to one and use its terms. It keeps no state beyond one module
// instance and does no locking.
//
// Only package vt may import it. The feature tests in this package pin
// every libghostty behaviour vibepit relies on; see README.md before
// upgrading the module or wasm2go.
package ghostty
```

`vt/internal/ghostty/instance.go`:

```go
package ghostty

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/bernd/vibepit/vt/internal/ghostty/internal/wasmvt"
)

var le = binary.LittleEndian

const (
	inputBufSize = 64 << 10

	// Scratch space for out-parameters and for structs passed by pointer.
	scrOut       = 0   // 8 bytes: out-parameter of *_get
	scrOutPtr    = 0   // (ptr, len) out-pair of the *_alloc functions
	scrOutLen    = 4   //
	scrPoint     = 16  // GhosttyPoint, 8-byte aligned
	scrSelection = 40  // GhosttySelection
	scrFormatter = 72  // GhosttyFormatterTerminalOptions
	scrMode      = 116 // GhosttyTerminalModeConfig
	scratchSize  = 128
)

var zero8 [8]byte

// Config configures NewInstance.
type Config struct {
	// MemoryLimitPages caps linear memory, in 64 KiB pages. Zero keeps the
	// module's limit of 4 GiB.
	MemoryLimitPages uint32
}

// Instance is one module instance holding at most one terminal. It is not
// safe for concurrent use.
type Instance struct {
	mod      *wasmvt.Module
	term     uint32 // GhosttyTerminal
	input    uint32 // reusable buffer for VT input
	scratch  uint32
	slot     uint32       // opaque out-parameter slot
	render   uint32       // GhosttyRenderState, created on first use
	ptyIndex uint32       // function table index of onWritePty
	writePty func([]byte) // see SetWritePty
}

// NewInstance creates a module instance: about 65 µs, most of it copying
// the data segment into fresh linear memory.
func NewInstance(cfg Config) (*Instance, error) {
	in := &Instance{}
	if err := in.guard("wasmvt.New", func() { in.mod = wasmvt.New() }); err != nil {
		return nil, err
	}
	if cfg.MemoryLimitPages > 0 {
		in.mod.SetMemoryLimitPages(int64(cfg.MemoryLimitPages))
	}
	if err := in.init(); err != nil {
		_ = in.Close()
		return nil, err
	}
	return in, nil
}

func (in *Instance) init() error {
	var err error
	if in.input, err = in.Alloc(inputBufSize); err != nil {
		return err
	}
	if in.scratch, err = in.Alloc(scratchSize); err != nil {
		return err
	}
	slot, err := in.call("ghostty_wasm_alloc_opaque", func() int32 { return in.mod.Xghostty_wasm_alloc_opaque() })
	if err != nil {
		return err
	}
	if slot == 0 {
		return &CallError{Func: "ghostty_wasm_alloc_opaque", Result: OutOfMemory}
	}
	in.slot = slot
	return nil
}

// MemorySize is the current size of linear memory in bytes. It only grows.
func (in *Instance) MemorySize() uint64 { return uint64(len(in.mem())) }

// Close drops the instance. The garbage collector frees its linear memory,
// and the terminal in it. It is idempotent.
func (in *Instance) Close() error {
	in.mod = nil
	in.writePty = nil
	return nil
}

// guard runs f, which calls into the module, and turns a panic into a
// *TrapError. In translated code a WASM trap is a panic: a bounds check,
// unreachable, a function table entry of the wrong type. A panic in the
// write_pty callback surfaces here too. Runaway recursion is a fatal stack
// overflow instead, which no recover catches.
func (in *Instance) guard(name string, f func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &TrapError{Func: name, Err: fmt.Errorf("%v", r)}
		}
	}()
	f()
	return nil
}

// call runs an export that returns an i32: a pointer, a handle or a size.
func (in *Instance) call(name string, f func() int32) (uint32, error) {
	var v int32
	err := in.guard(name, func() { v = f() })
	return uint32(v), err
}

// callResult runs an export that returns a GhosttyResult.
func (in *Instance) callResult(name string, f func() int32) (Result, error) {
	v, err := in.call(name, f)
	return Result(int32(v)), err
}

// invoke runs an export that returns a GhosttyResult and treats anything
// but SUCCESS as an error.
func (in *Instance) invoke(name string, f func() int32) error {
	r, err := in.callResult(name, f)
	if err != nil {
		return err
	}
	if r != Success {
		return &CallError{Func: name, Result: r}
	}
	return nil
}

// takeOpaque is ghostty_wasm_take_opaque on the instance's slot: the handle
// a *_new function just wrote there.
func (in *Instance) takeOpaque() (uint32, error) {
	return in.call("ghostty_wasm_take_opaque", func() int32 {
		return in.mod.Xghostty_wasm_take_opaque(int32(in.slot))
	})
}

// Alloc is ghostty_wasm_alloc.
func (in *Instance) Alloc(n uint32) (uint32, error) {
	p, err := in.call("ghostty_wasm_alloc", func() int32 { return in.mod.Xghostty_wasm_alloc(int32(n)) })
	if err != nil {
		return 0, err
	}
	if p == 0 {
		return 0, &CallError{Func: "ghostty_wasm_alloc", Result: OutOfMemory}
	}
	return p, nil
}

// Free is ghostty_wasm_free.
func (in *Instance) Free(p, n uint32) error {
	return in.guard("ghostty_wasm_free", func() { in.mod.Xghostty_wasm_free(int32(p), int32(n)) })
}

// mem is linear memory. A call can grow it and move the slice, so never
// keep it across a call into the module.
func (in *Instance) mem() []byte { return *in.mod.Xmemory().Slice() }

// span returns n bytes of linear memory at p, or a *TrapError for a
// pointer from the module that points outside it.
func (in *Instance) span(p, n uint32) ([]byte, error) {
	m := in.mem()
	if uint64(p)+uint64(n) > uint64(len(m)) {
		return nil, &TrapError{Func: "memory", Err: errOutOfBounds}
	}
	return m[p : p+n], nil
}

// read copies n bytes out of linear memory. The copy matters: the next
// call may grow memory or reuse the bytes.
func (in *Instance) read(p, n uint32) ([]byte, error) {
	b, err := in.span(p, n)
	if err != nil {
		return nil, err
	}
	return bytes.Clone(b), nil
}

func (in *Instance) writeMem(p uint32, b []byte) error {
	dst, err := in.span(p, uint32(len(b)))
	if err != nil {
		return err
	}
	copy(dst, b)
	return nil
}

func (in *Instance) readU32(p uint32) (uint32, error) {
	b, err := in.span(p, 4)
	if err != nil {
		return 0, err
	}
	return le.Uint32(b), nil
}

func (in *Instance) readByte(p uint32) (byte, error) {
	b, err := in.span(p, 1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

// TerminalNew is ghostty_terminal_new with the default allocator.
func (in *Instance) TerminalNew(cols, rows uint16) error {
	if in.term != 0 {
		return errors.New("ghostty: instance already holds a terminal")
	}
	if err := in.invoke("ghostty_terminal_new", func() int32 {
		return in.mod.Xghostty_terminal_new(0, int32(in.slot), int32(cols), int32(rows))
	}); err != nil {
		return err
	}
	h, err := in.takeOpaque()
	if err != nil {
		return err
	}
	in.term = h
	return nil
}

// TerminalFree is ghostty_terminal_free. It also frees the render state,
// which borrows from the terminal.
func (in *Instance) TerminalFree() error {
	if in.term == 0 {
		return nil
	}
	if in.render != 0 {
		if err := in.guard("ghostty_render_state_free", func() {
			in.mod.Xghostty_render_state_free(int32(in.render))
		}); err != nil {
			return err
		}
		in.render = 0
	}
	err := in.guard("ghostty_terminal_free", func() { in.mod.Xghostty_terminal_free(int32(in.term)) })
	in.term = 0
	return err
}

// TerminalReset is ghostty_terminal_reset (RIS).
func (in *Instance) TerminalReset() error {
	return in.guard("ghostty_terminal_reset", func() { in.mod.Xghostty_terminal_reset(int32(in.term)) })
}

// TerminalResize is ghostty_terminal_resize.
func (in *Instance) TerminalResize(cols, rows uint16, cellWidthPx, cellHeightPx uint32) error {
	return in.invoke("ghostty_terminal_resize", func() int32 {
		return in.mod.Xghostty_terminal_resize(int32(in.term), int32(cols), int32(rows),
			int32(cellWidthPx), int32(cellHeightPx))
	})
}

// TerminalSetPtr is ghostty_terminal_set with a raw value: a callback's
// table index, or 0 for NULL.
func (in *Instance) TerminalSetPtr(opt TerminalOption, v uint32) error {
	return in.invoke("ghostty_terminal_set", func() int32 {
		return in.mod.Xghostty_terminal_set(int32(in.term), int32(opt), int32(v))
	})
}

// TerminalSetSize sets an option whose input type is size_t*.
func (in *Instance) TerminalSetSize(opt TerminalOption, v uint32) error {
	p := in.scratch + scrOut
	b := make([]byte, 4)
	le.PutUint32(b, v)
	if err := in.writeMem(p, b); err != nil {
		return err
	}
	return in.TerminalSetPtr(opt, p)
}

// TerminalSetMode sets GHOSTTY_TERMINAL_OPT_MODE.
func (in *Instance) TerminalSetMode(mode uint16, value bool) error {
	p := in.scratch + scrMode
	if err := in.writeMem(p, modeConfig(mode, value)); err != nil {
		return err
	}
	return in.TerminalSetPtr(OptMode, p)
}

func modeConfig(mode uint16, value bool) []byte {
	b := make([]byte, sizeofModeConfig)
	le.PutUint16(b[offModeConfigMode:], mode)
	if value {
		b[offModeConfigValue] = 1
	}
	return b
}

// get runs ghostty_terminal_get into zeroed scratch and returns where the
// value is.
func (in *Instance) get(data TerminalData) (uint32, error) {
	p := in.scratch + scrOut
	if err := in.writeMem(p, zero8[:]); err != nil {
		return 0, err
	}
	return p, in.invoke("ghostty_terminal_get", func() int32 {
		return in.mod.Xghostty_terminal_get(int32(in.term), int32(data), int32(p))
	})
}

// GetU8 reads a uint8_t value.
func (in *Instance) GetU8(d TerminalData) (uint8, error) {
	p, err := in.get(d)
	if err != nil {
		return 0, err
	}
	return in.readByte(p)
}

// GetU16 reads a uint16_t value.
func (in *Instance) GetU16(d TerminalData) (uint16, error) {
	p, err := in.get(d)
	if err != nil {
		return 0, err
	}
	b, err := in.read(p, 2)
	if err != nil {
		return 0, err
	}
	return le.Uint16(b), nil
}

// GetU32 reads a uint32_t, size_t or enum value.
func (in *Instance) GetU32(d TerminalData) (uint32, error) {
	p, err := in.get(d)
	if err != nil {
		return 0, err
	}
	return in.readU32(p)
}

// GetBool reads a bool value.
func (in *Instance) GetBool(d TerminalData) (bool, error) {
	v, err := in.GetU8(d)
	return v != 0, err
}

// GetString reads a GhosttyString value and copies the borrowed bytes.
func (in *Instance) GetString(d TerminalData) (string, error) {
	p, err := in.get(d)
	if err != nil {
		return "", err
	}
	b, err := in.read(p, sizeofString)
	if err != nil {
		return "", err
	}
	ptr, n := le.Uint32(b[offStringPtr:]), le.Uint32(b[offStringLen:])
	if n == 0 {
		return "", nil
	}
	s, err := in.read(ptr, n)
	return string(s), err
}

// GetMode reads GHOSTTY_TERMINAL_DATA_MODE. An unknown mode is
// InvalidValue.
func (in *Instance) GetMode(mode uint16) (bool, error) {
	p := in.scratch + scrMode
	if err := in.writeMem(p, modeConfig(mode, false)); err != nil {
		return false, err
	}
	if err := in.invoke("ghostty_terminal_get", func() int32 {
		return in.mod.Xghostty_terminal_get(int32(in.term), int32(DataMode), int32(p))
	}); err != nil {
		return false, err
	}
	v, err := in.readByte(p + offModeConfigValue)
	return v != 0, err
}

// VTWrite is ghostty_terminal_vt_write, in chunks of the input buffer.
func (in *Instance) VTWrite(p []byte) error {
	for len(p) > 0 {
		n := min(len(p), inputBufSize)
		if err := in.writeMem(in.input, p[:n]); err != nil {
			return err
		}
		if err := in.guard("ghostty_terminal_vt_write", func() {
			in.mod.Xghostty_terminal_vt_write(int32(in.term), int32(in.input), int32(n))
		}); err != nil {
			return err
		}
		p = p[n:]
	}
	return nil
}

// TypeJSON is ghostty_type_json.
func (in *Instance) TypeJSON() (string, error) {
	p, err := in.call("ghostty_type_json", func() int32 { return in.mod.Xghostty_type_json() })
	if err != nil {
		return "", err
	}
	b, err := in.span(p, uint32(len(in.mem()))-p)
	if err != nil {
		return "", err
	}
	end := bytes.IndexByte(b, 0)
	if end < 0 {
		return "", errors.New("ghostty: type json is not NUL-terminated")
	}
	return string(b[:end]), nil
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./vt/internal/ghostty/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add vt/internal/ghostty
git commit -m "Add C-ABI instance for the translated libghostty-vt module"
```

---

### Task 3: Parsing, ground detection and state getters

**Files:**
- Modify: `vt/internal/ghostty/instance.go` (add `VTWriteUntilGround`, `RenderStateCursor`)
- Test: `vt/internal/ghostty/parse_test.go`, `vt/internal/ghostty/getters_test.go`

**Interfaces:**
- Consumes: Task 2 instance core.
- Produces: `func (in *Instance) VTWriteUntilGround(p []byte) (consumed int, ground bool, err error)`; `func (in *Instance) RenderStateCursor() (CursorVisualStyle, bool, error)`.

- [ ] **Step 1: Write the failing tests**

`vt/internal/ghostty/parse_test.go`:

```go
package ghostty

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRISResetsModesAndKittyFlags(t *testing.T) {
	in := newTerm(t, 80, 24)
	feed(t, in, "\x1b[?2004h\x1b[>5u\x1b[?1049hX\x1bc")

	paste, err := in.GetMode(EncodeMode(2004, false))
	require.NoError(t, err)
	assert.False(t, paste)
	flags, err := in.GetU8(DataKittyKeyboardFlags)
	require.NoError(t, err)
	assert.Zero(t, flags)
	screen, err := in.GetU32(DataActiveScreen)
	require.NoError(t, err)
	assert.Equal(t, ScreenPrimary, int32(screen))
}

func TestVTGround(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		ground bool
	}{
		{"at rest", "", true},
		{"after text", "hello", true},
		{"after a complete CSI", "\x1b[31m", true},
		{"lone ESC", "\x1b", false},
		{"inside CSI", "\x1b[3", false},
		{"inside OSC", "\x1b]2;tit", false},
		{"inside DCS", "\x1bP+q54", false},
		{"inside UTF-8", "\xe2\x82", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := newTerm(t, 80, 24)
			feed(t, in, tt.input)
			got, err := in.GetBool(DataVTGround)
			require.NoError(t, err)
			assert.Equal(t, tt.ground, got)
		})
	}
}

func TestWriteUntilGround(t *testing.T) {
	big := strings.Repeat("x", inputBufSize+10)
	tests := []struct {
		name         string
		before       string
		input        string
		wantConsumed int
		wantGround   bool
		wantTitle    string
	}{
		{name: "already at ground consumes nothing", input: "abc", wantConsumed: 0, wantGround: true},
		{name: "rest of a UTF-8 character", before: "abc\xe2", input: "\x82\xacX", wantConsumed: 2, wantGround: true},
		{name: "unfinished OSC stays unfinished", before: "\x1b]2;tit", input: "le", wantConsumed: 2, wantGround: false},
		{name: "OSC terminator", before: "\x1b]2;tit", input: "\x07after", wantConsumed: 1, wantGround: true, wantTitle: "tit"},
		// libghostty drops a title this long, so only the counts are checked.
		{
			name: "ground after the first chunk", before: "\x1b]2;", input: big + "\x07tail",
			wantConsumed: len(big) + 1, wantGround: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := newTerm(t, 80, 24)
			feed(t, in, tt.before)
			n, ground, err := in.VTWriteUntilGround([]byte(tt.input))
			require.NoError(t, err)
			assert.Equal(t, tt.wantConsumed, n)
			assert.Equal(t, tt.wantGround, ground)
			if tt.wantTitle != "" {
				title, err := in.GetString(DataTitle)
				require.NoError(t, err)
				assert.Equal(t, tt.wantTitle, title)
			}
		})
	}
}

// A cut at ground keeps the real terminal and the shadow in the same
// parser state: "abc€X" stays intact.
func TestWriteUntilGroundThenRest(t *testing.T) {
	in := newTerm(t, 20, 2)
	feed(t, in, "abc\xe2")
	rest := []byte("\x82\xacX")
	n, ground, err := in.VTWriteUntilGround(rest)
	require.NoError(t, err)
	require.True(t, ground)
	require.NoError(t, in.VTWrite(rest[n:]))

	out, err := in.GetU16(DataCursorX)
	require.NoError(t, err)
	assert.EqualValues(t, 5, out, "a, b, c, €, X")
}

// TestModeTableComplete scans every mode number. It fails when upstream
// adds or removes a mode, or changes a default, so Modes stays exact.
func TestModeTableComplete(t *testing.T) {
	in := newTerm(t, 80, 24)
	known := map[uint16]ModeEntry{}
	for _, m := range Modes {
		known[m.Mode()] = m
	}

	found := map[uint16]bool{}
	for _, ansi := range []bool{true, false} {
		for v := uint16(0); v < 10000; v++ {
			mode := EncodeMode(v, ansi)
			on, err := in.GetMode(mode)
			if err != nil {
				require.ErrorIs(t, err, InvalidValue)
				continue
			}
			found[mode] = true
			entry, ok := known[mode]
			if assert.True(t, ok, "mode %d (ansi=%v) is not in Modes", v, ansi) {
				assert.Equal(t, entry.Default, on, "default of mode %d (ansi=%v)", v, ansi)
			}
		}
	}
	for mode, m := range known {
		assert.True(t, found[mode], "mode %d (ansi=%v) in Modes is unknown to libghostty", m.Value, m.ANSI)
	}
}
```

`vt/internal/ghostty/getters_test.go`:

```go
package ghostty

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The restore sequences depend on each of these values.
func TestStateGetters(t *testing.T) {
	type check func(t *testing.T, in *Instance)
	screen := func(want int32) check {
		return func(t *testing.T, in *Instance) {
			v, err := in.GetU32(DataActiveScreen)
			require.NoError(t, err)
			assert.Equal(t, want, int32(v))
		}
	}
	mode := func(value uint16, want bool) check {
		return func(t *testing.T, in *Instance) {
			v, err := in.GetMode(EncodeMode(value, false))
			require.NoError(t, err)
			assert.Equal(t, want, v)
		}
	}
	kitty := func(want uint8) check {
		return func(t *testing.T, in *Instance) {
			v, err := in.GetU8(DataKittyKeyboardFlags)
			require.NoError(t, err)
			assert.Equal(t, want, v)
		}
	}
	boolean := func(d TerminalData, want bool) check {
		return func(t *testing.T, in *Instance) {
			v, err := in.GetBool(d)
			require.NoError(t, err)
			assert.Equal(t, want, v)
		}
	}
	str := func(d TerminalData, want string) check {
		return func(t *testing.T, in *Instance) {
			v, err := in.GetString(d)
			require.NoError(t, err)
			assert.Equal(t, want, v)
		}
	}
	cursorAt := func(x, y uint16) check {
		return func(t *testing.T, in *Instance) {
			gx, err := in.GetU16(DataCursorX)
			require.NoError(t, err)
			gy, err := in.GetU16(DataCursorY)
			require.NoError(t, err)
			assert.Equal(t, [2]uint16{x, y}, [2]uint16{gx, gy})
		}
	}
	cursorStyle := func(style CursorVisualStyle, blinking bool) check {
		return func(t *testing.T, in *Instance) {
			s, b, err := in.RenderStateCursor()
			require.NoError(t, err)
			assert.Equal(t, style, s, "style")
			assert.Equal(t, blinking, b, "blinking")
		}
	}

	tests := []struct {
		name  string
		input string
		check check
	}{
		{"primary screen", "", screen(ScreenPrimary)},
		{"alt screen via 1049", "\x1b[?1049h", screen(ScreenAlternate)},
		{"alt screen via 1047", "\x1b[?1047h", screen(ScreenAlternate)},
		{"alt screen via 47", "\x1b[?47h", screen(ScreenAlternate)},
		{"back to primary", "\x1b[?1049h\x1b[?1049l", screen(ScreenPrimary)},
		{"mode 25 off", "\x1b[?25l", mode(25, false)},
		{"mode 1000", "\x1b[?1000h", mode(1000, true)},
		{"mode 1002", "\x1b[?1002h", mode(1002, true)},
		{"mode 1003", "\x1b[?1003h", mode(1003, true)},
		{"mode 1006", "\x1b[?1006h", mode(1006, true)},
		{"mode 1004", "\x1b[?1004h", mode(1004, true)},
		{"mode 2004", "\x1b[?2004h", mode(2004, true)},
		{"mode 2026", "\x1b[?2026h", mode(2026, true)},
		{"kitty push", "\x1b[>5u", kitty(5)},
		{"kitty push push pop", "\x1b[>5u\x1b[>1u\x1b[<u", kitty(5)},
		{"kitty set", "\x1b[=3;1u", kitty(3)},
		{"cursor fresh", "", cursorStyle(CursorVisualBlock, false)},
		{"DECSCUSR 0", "\x1b[0 q", cursorStyle(CursorVisualBlock, false)},
		{"DECSCUSR 1", "\x1b[1 q", cursorStyle(CursorVisualBlock, true)},
		{"DECSCUSR 2", "\x1b[2 q", cursorStyle(CursorVisualBlock, false)},
		{"DECSCUSR 3", "\x1b[3 q", cursorStyle(CursorVisualUnderline, true)},
		{"DECSCUSR 4", "\x1b[4 q", cursorStyle(CursorVisualUnderline, false)},
		{"DECSCUSR 5", "\x1b[5 q", cursorStyle(CursorVisualBar, true)},
		{"DECSCUSR 6", "\x1b[6 q", cursorStyle(CursorVisualBar, false)},
		{"blinking off by mode 12", "\x1b[5 q\x1b[?12l", cursorStyle(CursorVisualBar, false)},
		{"cursor visible", "", boolean(DataCursorVisible, true)},
		{"cursor hidden", "\x1b[?25l", boolean(DataCursorVisible, false)},
		{"no mouse tracking", "", boolean(DataMouseTracking, false)},
		{"mouse tracking", "\x1b[?1000h", boolean(DataMouseTracking, true)},
		{"title via OSC 0", "\x1b]0;zero\x07", str(DataTitle, "zero")},
		{"title via OSC 2", "\x1b]2;two\x1b\\", str(DataTitle, "two")},
		{"pwd via OSC 7 has no trailing NUL", "\x1b]7;file://host/tmp\x07", str(DataPwd, "file://host/tmp")},
		{"cursor position", "\x1b[3;7H", cursorAt(6, 2)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := newTerm(t, 80, 24)
			feed(t, in, tt.input)
			tt.check(t, in)
		})
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./vt/internal/ghostty/`
Expected: FAIL to compile: `in.VTWriteUntilGround undefined`, `in.RenderStateCursor undefined`.

- [ ] **Step 3: Implement**

Append to `vt/internal/ghostty/instance.go`:

```go
// VTWriteUntilGround feeds p only up to the byte at which the parser
// reaches ground. It returns how many bytes it consumed and whether the
// parser is at ground. A parser already at ground consumes nothing.
func (in *Instance) VTWriteUntilGround(p []byte) (int, bool, error) {
	const name = "ghostty_terminal_vt_write_until_ground"
	consumed := 0
	for {
		n := min(len(p), inputBufSize)
		if err := in.writeMem(in.input, p[:n]); err != nil {
			return consumed, false, err
		}
		out := in.scratch + scrOut
		r, err := in.callResult(name, func() int32 {
			return in.mod.Xghostty_terminal_vt_write_until_ground(int32(in.term), int32(in.input), int32(n), int32(out))
		})
		if err != nil {
			return consumed, false, err
		}
		switch r {
		case Success:
			c, err := in.readU32(out)
			return consumed + int(c), true, err
		case NoValue:
			consumed += n
			p = p[n:]
			if len(p) == 0 {
				return consumed, false, nil
			}
		default:
			return consumed, false, &CallError{Func: name, Result: r}
		}
	}
}

// RenderStateCursor reads the cursor's DECSCUSR shape and blinking state.
// GhosttyTerminalData has no getter for them (its CURSOR_STYLE is the SGR
// pen), so this goes through a render state.
func (in *Instance) RenderStateCursor() (CursorVisualStyle, bool, error) {
	if in.render == 0 {
		if err := in.invoke("ghostty_render_state_new", func() int32 {
			return in.mod.Xghostty_render_state_new(0, int32(in.slot))
		}); err != nil {
			return 0, false, err
		}
		h, err := in.takeOpaque()
		if err != nil {
			return 0, false, err
		}
		in.render = h
	}
	if err := in.invoke("ghostty_render_state_update", func() int32 {
		return in.mod.Xghostty_render_state_update(int32(in.render), int32(in.term))
	}); err != nil {
		return 0, false, err
	}
	renderGet := func(d RenderStateData) (uint32, error) {
		p := in.scratch + scrOut
		if err := in.writeMem(p, zero8[:]); err != nil {
			return 0, err
		}
		if err := in.invoke("ghostty_render_state_get", func() int32 {
			return in.mod.Xghostty_render_state_get(int32(in.render), int32(d), int32(p))
		}); err != nil {
			return 0, err
		}
		return in.readU32(p)
	}
	style, err := renderGet(RenderDataCursorVisualStyle)
	if err != nil {
		return 0, false, err
	}
	blink, err := renderGet(RenderDataCursorBlinking)
	return CursorVisualStyle(int32(style)), blink&0xff != 0, err
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./vt/internal/ghostty/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add vt/internal/ghostty
git commit -m "Pin libghostty parsing, ground detection and state getters"
```

---

### Task 4: Continuation and scrollback limits

**Files:**
- Modify: `vt/internal/ghostty/instance.go` (add `ContinuationAlloc`, `takeAlloc`)
- Test: `vt/internal/ghostty/continuation_test.go`, `vt/internal/ghostty/limits_test.go`

**Interfaces:**
- Consumes: Task 2 core.
- Produces: `func (in *Instance) ContinuationAlloc() ([]byte, error)` (returns `InvalidValue` via `errors.Is` when tracking is off or the continuation is unavailable); `func (in *Instance) takeAlloc(out uint32) ([]byte, error)` used by Task 5.

- [ ] **Step 1: Write the failing tests**

`vt/internal/ghostty/continuation_test.go`:

```go
package ghostty

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withContinuation(t *testing.T, in *Instance, limit uint32) {
	t.Helper()
	require.NoError(t, in.TerminalSetSize(OptContinuationMaxBytes, limit))
}

// The C API's default is 0, tracking off. vt must always set it.
func TestContinuationOffByDefault(t *testing.T) {
	in := newTerm(t, 80, 24)
	limit, err := in.GetU32(DataContinuationMaxBytes)
	require.NoError(t, err)
	assert.Zero(t, limit)

	feed(t, in, "abc\x1b[3")
	_, err = in.ContinuationAlloc()
	assert.ErrorIs(t, err, InvalidValue)
}

func TestContinuationBytes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty at ground", "abc", ""},
		{"CSI", "abc\x1b[3", "\x1b[3"},
		{"OSC", "\x1b]2;tit", "\x1b]2;tit"},
		{"DCS", "\x1bP+q54", "\x1bP+q54"},
		{"APC", "\x1b_Gf=1;AAA", "\x1b_Gf=1;AAA"},
		{"lone ESC", "\x1b", "\x1b"},
		{"UTF-8", "abc\xe2\x82", "\xe2\x82"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := newTerm(t, 80, 24)
			withContinuation(t, in, 64<<10)
			feed(t, in, tt.input)
			got, err := in.ContinuationAlloc()
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

func TestContinuationOverLimit(t *testing.T) {
	in := newTerm(t, 80, 24)
	withContinuation(t, in, 1024)
	feed(t, in, "\x1b]2;"+strings.Repeat("x", 1100))

	_, err := in.ContinuationAlloc()
	require.ErrorIs(t, err, InvalidValue, "too long to reconstruct")
	assert.NotErrorIs(t, err, ErrTrap)

	feed(t, in, "\x07")
	got, err := in.ContinuationAlloc()
	require.NoError(t, err, "tracking recovers at ground")
	assert.Empty(t, got)
}
```

The replay tests need the formatter and live in Task 5 (`TestContinuationReplay`, `TestContinuationReplayEverySplit`).

`vt/internal/ghostty/limits_test.go`:

```go
package ghostty

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// styledLines returns about n bytes of coloured lines of varying length.
func styledLines(n int) []byte {
	var buf bytes.Buffer
	for i := 0; buf.Len() < n; i++ {
		fmt.Fprintf(&buf, "\x1b[3%dmline %d \x1b[1mbold\x1b[0m plain %s\r\n", i%8, i, strings.Repeat("x", i%120))
	}
	return buf.Bytes()[:n]
}

// Both scrollback limits bound history and memory. Limits apply per page,
// so the row count only has an upper bound.
func TestScrollbackLimitsBoundMemory(t *testing.T) {
	total := 100 << 20
	if testing.Short() {
		total = 16 << 20
	}
	in := newTerm(t, 80, 24)
	require.NoError(t, in.TerminalSetSize(OptScrollbackMaxLines, 1000))
	require.NoError(t, in.TerminalSetSize(OptScrollbackMaxBytes, 4<<20))

	lines, err := in.GetU32(DataScrollbackMaxLines)
	require.NoError(t, err)
	assert.EqualValues(t, 1000, lines)

	chunk := styledLines(1 << 20)
	for written := 0; written < total; written += len(chunk) {
		require.NoError(t, in.VTWrite(chunk))
	}

	rows, err := in.GetU32(DataScrollbackRows)
	require.NoError(t, err)
	assert.LessOrEqual(t, rows, uint32(2000))
	assert.LessOrEqual(t, in.MemorySize(), uint64(16<<20))
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./vt/internal/ghostty/`
Expected: FAIL to compile: `in.ContinuationAlloc undefined`.

- [ ] **Step 3: Implement**

Append to `vt/internal/ghostty/instance.go`:

```go
// ContinuationAlloc is ghostty_terminal_continuation_alloc: the bytes that
// recreate an unfinished escape sequence or UTF-8 character in another
// terminal. It is InvalidValue while tracking is off or when the sequence
// outgrew the limit.
func (in *Instance) ContinuationAlloc() ([]byte, error) {
	out := in.scratch + scrOutPtr
	if err := in.writeMem(out, zero8[:]); err != nil {
		return nil, err
	}
	if err := in.invoke("ghostty_terminal_continuation_alloc", func() int32 {
		return in.mod.Xghostty_terminal_continuation_alloc(int32(in.term), 0, int32(out), int32(in.scratch+scrOutLen))
	}); err != nil {
		return nil, err
	}
	return in.takeAlloc(out)
}

// takeAlloc copies the (ptr, len) out-pair at out and releases the buffer
// with ghostty_free. Empty output is (NULL, 0).
func (in *Instance) takeAlloc(out uint32) ([]byte, error) {
	b, err := in.read(out, 8)
	if err != nil {
		return nil, err
	}
	ptr, n := le.Uint32(b[scrOutPtr:]), le.Uint32(b[scrOutLen:])
	if ptr == 0 {
		return nil, nil
	}
	data, err := in.read(ptr, n)
	if err != nil {
		return nil, err
	}
	if err := in.guard("ghostty_free", func() { in.mod.Xghostty_free(0, int32(ptr), int32(n)) }); err != nil {
		return nil, err
	}
	return data, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./vt/internal/ghostty/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add vt/internal/ghostty
git commit -m "Pin libghostty continuation and scrollback limits"
```

---

### Task 5: Formatter, round trips and restore into a dirty terminal

**Files:**
- Create: `vt/internal/ghostty/formatter.go`
- Test: `vt/internal/ghostty/format_test.go`, `vt/internal/ghostty/restore_test.go`, `vt/internal/ghostty/testdata/format-*.golden`

**Interfaces:**
- Consumes: Task 2 core, `takeAlloc` (Task 4).
- Produces:
  - `type Point struct{ Tag PointTag; X uint16; Y uint32 }`, `type Selection struct{ Start, End Point; Rectangle bool }`
  - `type ScreenExtra struct{ Cursor, Style, Hyperlink, Protection, KittyKeyboard, Charsets bool }`
  - `type TerminalExtra struct{ Palette, Modes, ScrollingRegion, Tabstops, Pwd, Keyboard bool; Screen ScreenExtra }`
  - `type FormatterOptions struct{ Emit FormatterFormat; Unwrap, Trim bool; Extra TerminalExtra; Selection *Selection; ContentNone bool }`
  - `func (in *Instance) GridRef(pt Point, out uint32) error`, `func (in *Instance) FormatAlloc(opts FormatterOptions) ([]byte, error)`
  - Test helpers used by Tasks 6–7: `scenarios`, `scenarioCols`, `scenarioRows`, `activeArea`, `format`, `plainScreen`, `snapshot`, `restoreExtras`, `state`, `capture`, `probeSuffix`, `snapshotLeave`, `penReset`, `skipReconcile`, `dirtyState`.

- [ ] **Step 1: Write the failing tests**

`vt/internal/ghostty/format_test.go`:

```go
package ghostty

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const scenarioCols, scenarioRows = 40, 10

// scenarios feed the round-trip, chunking, golden and fuzz tests.
var scenarios = []struct{ name, input string }{
	{"primary-agent", "\x1b[>1u\x1b[?2004h\x1b[?1004h\x1b[?25lhello \x1b[1;32mgreen\x1b[0m \x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\\r\n> \x1b[2;3H"},
	{"alt-screen-mouse", "shell$ \x1b[?1049h\x1b[?1002h\x1b[?1006h\x1b[Htop line\x1b[3;4Hcontent"},
	{"scroll-region", "a\r\nb\r\nc\x1b[2;4r\x1b[4;1Hin region\r\nscrolled"},
	{"charsets-g0", "abc\x1b(0lqk"},
	{"charsets-g1", "xyz\x1b)0\x0elqk"},
	{"sgr", "\x1b[38;5;123mA\x1b[38;2;1;2;3mB\x1b[4:3mC\x1b[58;5;9mD\x1b[1;3;7mE\x1b[0mF\x1b[45m"},
	{"wide", "漢字 👍🏽 é x"},
	{"pending-wrap", "\x1b[H0123456789012345678901234567890123456789"},
	{"insert-mode", "abcdef\x1b[1;3H\x1b[4h"},
	{"origin-mode", "\x1b[3;8r\x1b[?6h\x1b[2;2Hx"},
}

func scenarioInput(t *testing.T, name string) string {
	t.Helper()
	for _, s := range scenarios {
		if s.name == name {
			return s.input
		}
	}
	t.Fatalf("no scenario %q", name)
	return ""
}

func activeArea(t testing.TB, in *Instance) *Selection {
	t.Helper()
	cols, err := in.GetU16(DataCols)
	require.NoError(t, err)
	rows, err := in.GetU16(DataRows)
	require.NoError(t, err)
	return &Selection{
		Start: Point{Tag: PointActive},
		End:   Point{Tag: PointActive, X: cols - 1, Y: uint32(rows) - 1},
	}
}

func format(t testing.TB, in *Instance, opts FormatterOptions) string {
	t.Helper()
	out, err := in.FormatAlloc(opts)
	require.NoError(t, err)
	return string(out)
}

func plainScreen(t testing.TB, in *Instance) string {
	t.Helper()
	return format(t, in, FormatterOptions{Emit: FormatPlain, Trim: true, Selection: activeArea(t, in)})
}

// restoreExtras is every extra a restore uses. Palette, tab stops and pwd
// stay off; see known_gaps_test.go.
var restoreExtras = TerminalExtra{
	Modes: true, ScrollingRegion: true, Keyboard: true,
	Screen: ScreenExtra{Cursor: true, Style: true, Hyperlink: true, Protection: true, KittyKeyboard: true, Charsets: true},
}

// snapshot is the visible screen with every restore extra.
func snapshot(t testing.TB, in *Instance) string {
	t.Helper()
	return format(t, in, FormatterOptions{Emit: FormatVT, Unwrap: true, Extra: restoreExtras, Selection: activeArea(t, in)})
}

// state is what a restore must reproduce.
type state struct {
	Screen      string
	Alt         bool
	CursorX     uint16
	CursorY     uint16
	PendingWrap bool
	Kitty       uint8
	Modes       []bool // indexed like Modes
}

func capture(t testing.TB, in *Instance) state {
	t.Helper()
	extra := restoreExtras
	// libghostty's default designation is UTF-8, which ESC ( B (ASCII)
	// doesn't restore, while real terminals treat ESC ( B as the default.
	// probeSuffix checks charsets by printing through them instead.
	extra.Screen.Charsets = false
	s := state{Screen: format(t, in, FormatterOptions{Emit: FormatVT, Unwrap: true, Extra: extra, Selection: activeArea(t, in)})}
	screen, err := in.GetU32(DataActiveScreen)
	require.NoError(t, err)
	s.Alt = int32(screen) == ScreenAlternate
	s.CursorX, err = in.GetU16(DataCursorX)
	require.NoError(t, err)
	s.CursorY, err = in.GetU16(DataCursorY)
	require.NoError(t, err)
	s.PendingWrap, err = in.GetBool(DataCursorPendingWrap)
	require.NoError(t, err)
	s.Kitty, err = in.GetU8(DataKittyKeyboardFlags)
	require.NoError(t, err)
	for _, m := range Modes {
		v, err := in.GetMode(m.Mode())
		require.NoError(t, err)
		s.Modes = append(s.Modes, v)
	}
	return s
}

// probeSuffix shows state the capture can't: the active charset (q draws a
// line in DEC graphics), LNM, and the scroll region (DL).
const probeSuffix = "q\r\nw\x1b[Mz"

func TestFormatRoundTrip(t *testing.T) {
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			a := newTerm(t, scenarioCols, scenarioRows)
			b := newTerm(t, scenarioCols, scenarioRows)
			feed(t, a, sc.input)
			feed(t, b, snapshot(t, a))
			assert.Equal(t, capture(t, a), capture(t, b))

			feed(t, a, probeSuffix)
			feed(t, b, probeSuffix)
			assert.Equal(t, capture(t, a), capture(t, b), "after probe suffix")
		})
	}
}

// The state must not depend on how the input was chunked.
func TestChunkingInvariance(t *testing.T) {
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			var want state
			withTerm(t, scenarioCols, scenarioRows, func(in *Instance) {
				feed(t, in, sc.input)
				want = capture(t, in)
			})
			for k := 1; k < len(sc.input); k++ {
				withTerm(t, scenarioCols, scenarioRows, func(in *Instance) {
					feed(t, in, sc.input[:k])
					feed(t, in, sc.input[k:])
					assert.Equal(t, want, capture(t, in), "split at %d", k)
				})
			}
		})
	}
}

// Golden files make formatter output changes visible in review. Run with
// -update after reviewing a diff.
func TestFormatGolden(t *testing.T) {
	for _, name := range []string{"primary-agent", "alt-screen-mouse", "scroll-region", "origin-mode"} {
		t.Run(name, func(t *testing.T) {
			in := newTerm(t, scenarioCols, scenarioRows)
			feed(t, in, scenarioInput(t, name))
			got := snapshot(t, in)
			path := filepath.Join("testdata", "format-"+name+".golden")
			if *update {
				require.NoError(t, os.MkdirAll("testdata", 0o755))
				require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
			}
			want, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, string(want), got)
		})
	}
}

func TestFormatScrollbackOnly(t *testing.T) {
	in := newTerm(t, 10, 3)
	feed(t, in, "l1\r\nl2\r\n\x1b[31ml3\x1b[0m\r\nl4 long line wraps\r\nl5")
	rows, err := in.GetU32(DataScrollbackRows)
	require.NoError(t, err)
	require.EqualValues(t, 3, rows)

	got := format(t, in, FormatterOptions{
		Emit: FormatVT,
		Selection: &Selection{
			Start: Point{Tag: PointHistory},
			End:   Point{Tag: PointHistory, X: 9, Y: rows - 1},
		},
	})
	assert.Equal(t, "l1\r\nl2\r\n\x1b[0m\x1b[38;5;1ml3\x1b[0m", got, "content and styles only")
}

// A soft-wrapped line stays one logical line and reflows after a resize.
func TestFormatSoftWrap(t *testing.T) {
	a := newTerm(t, 10, 3)
	feed(t, a, "l4 long line wraps")
	out := snapshot(t, a)
	assert.Contains(t, out, "l4 long line wraps")

	b := newTerm(t, 10, 3)
	feed(t, b, out)
	require.NoError(t, a.TerminalResize(20, 3, 0, 0))
	require.NoError(t, b.TerminalResize(20, 3, 0, 0))
	assert.Equal(t, "l4 long line wraps", strings.Split(plainScreen(t, b), "\n")[0])
	assert.Equal(t, capture(t, a), capture(t, b))
}

func TestFormatOSC133(t *testing.T) {
	t.Run("formatter drops prompt marks", func(t *testing.T) {
		in := newTerm(t, 20, 5)
		feed(t, in, "\x1b]133;A\x07$ \x1b]133;B\x07ls\r\n\x1b]133;C\x07out\r\n\x1b]133;D;0\x07")
		assert.NotContains(t, snapshot(t, in), "\x1b]133")
	})
	t.Run("resize keeps prompt lines by default", func(t *testing.T) {
		in := newTerm(t, 20, 3)
		feed(t, in, "out\r\n\x1b]133;A\x07$ cmd")
		require.NoError(t, in.TerminalResize(10, 3, 0, 0))
		assert.Contains(t, plainScreen(t, in), "$ cmd")
	})
	t.Run("resize clears prompt lines after redraw=1", func(t *testing.T) {
		in := newTerm(t, 20, 3)
		feed(t, in, "out\r\n\x1b]133;A;redraw=1\x07$ cmd")
		require.NoError(t, in.TerminalResize(10, 3, 0, 0))
		got := plainScreen(t, in)
		assert.NotContains(t, got, "$ cmd")
		assert.Contains(t, got, "out")
	})
}

func TestFormatExtrasOnly(t *testing.T) {
	extrasOnly := func(t *testing.T, in *Instance) string {
		return format(t, in, FormatterOptions{Emit: FormatVT, Extra: restoreExtras, ContentNone: true})
	}

	t.Run("no content", func(t *testing.T) {
		in := newTerm(t, 20, 5)
		feed(t, in, "hello\x1b[2;4r\x1b[31m")
		got := extrasOnly(t, in)
		assert.NotContains(t, got, "hello")
		assert.Contains(t, got, "\x1b[2;4r")
	})

	t.Run("order", func(t *testing.T) {
		in := newTerm(t, 20, 5)
		feed(t, in, "x\x1b[2;4r\x1b[3;5H\x1b[31m\x1b(0")
		got := extrasOnly(t, in)
		region := strings.Index(got, "\x1b[2;4r")
		cup := regexp.MustCompile(`\x1b\[\d+;\d+H`).FindStringIndex(got)
		sgr := strings.Index(got, "\x1b[38;5;1m")
		charset := strings.Index(got, "\x1b(0")
		require.NotNil(t, cup)
		assert.Less(t, region, cup[0], "scroll region before the cursor")
		assert.Less(t, cup[0], sgr, "pen after the cursor")
		assert.Less(t, cup[0], charset, "charsets after the cursor")
	})

	t.Run("pending wrap is reprinted", func(t *testing.T) {
		a := newTerm(t, 10, 3)
		feed(t, a, "0123456789")
		b := newTerm(t, 10, 3)
		feed(t, b, "0123456789\x1b[H")
		feed(t, b, extrasOnly(t, a))
		wrap, err := b.GetBool(DataCursorPendingWrap)
		require.NoError(t, err)
		assert.True(t, wrap)
		assert.Equal(t, capture(t, a), capture(t, b))
	})

	t.Run("restores cursor, pen and region in a dirty terminal", func(t *testing.T) {
		a := newTerm(t, 20, 5)
		feed(t, a, "x\x1b[2;4r\x1b[3;5H\x1b[31m\x1b(0")
		b := newTerm(t, 20, 5)
		feed(t, b, "x\x1b[1;5r\x1b[32m\x1b[5;1H")
		feed(t, b, extrasOnly(t, a))
		assert.Equal(t, extrasOnly(t, a), extrasOnly(t, b))
	})
}

// An attach between "abc ESC[3" and "1mRED" must show a red RED, not
// "1mRED": the continuation restores the parser state.
func TestContinuationReplay(t *testing.T) {
	const first, rest = "abc\x1b[3", "1mRED\x1b[0m"

	whole := newTerm(t, 20, 2)
	feed(t, whole, first+rest)

	cut := newTerm(t, 20, 2)
	require.NoError(t, cut.TerminalSetSize(OptContinuationMaxBytes, 64<<10))
	feed(t, cut, first)
	cont, err := cut.ContinuationAlloc()
	require.NoError(t, err)

	replay := newTerm(t, 20, 2)
	feed(t, replay, snapshot(t, cut)+string(cont)+rest)
	assert.Equal(t, capture(t, whole), capture(t, replay))
	assert.NotContains(t, plainScreen(t, replay), "1mRED")
}

// Snapshot, then continuation, then the rest of the stream reproduces the
// uncut stream at every split point. This is vibed's attach path.
func TestContinuationReplayEverySplit(t *testing.T) {
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			var want state
			withTerm(t, scenarioCols, scenarioRows, func(in *Instance) {
				feed(t, in, sc.input)
				want = capture(t, in)
			})
			for k := 1; k < len(sc.input); k++ {
				withTerm(t, scenarioCols, scenarioRows, func(cut *Instance) {
					require.NoError(t, cut.TerminalSetSize(OptContinuationMaxBytes, 64<<10))
					feed(t, cut, sc.input[:k])
					cont, err := cut.ContinuationAlloc()
					require.NoError(t, err)
					withTerm(t, scenarioCols, scenarioRows, func(replay *Instance) {
						feed(t, replay, snapshot(t, cut)+string(cont)+sc.input[k:])
						assert.Equal(t, want, capture(t, replay), "split at %d", k)
					})
				})
			}
		})
	}
}
```

`vt/internal/ghostty/restore_test.go`:

```go
package ghostty

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// skipReconcile are DEC modes a restore must not write: DECCOLM clears the
// screen, 1048 saves the cursor, and the screen modes belong to the screen
// switch.
var skipReconcile = map[uint16]bool{3: true, 47: true, 1047: true, 1048: true, 1049: true}

// penReset clears state that the formatter only emits when it differs from
// the default, so a dirty terminal would keep it: scroll region, SGR (also
// used while drawing content), an open hyperlink, charsets and GL, and the
// kitty keyboard flags.
const penReset = "\x1b[r\x1b[0m\x1b]8;;\x1b\\\x1b(B\x1b)B\x1b*B\x1b+B\x0f\x1b[=0;1u"

// snapshotLeave restores shadow a into b, a stand-in for the real terminal,
// without assuming b was reset. The overlay's snapshot leave must emit the
// same parts in the same order: match the screen, reconcile every mode,
// reset the pen, clear, then the formatter output.
func snapshotLeave(t testing.TB, a, b *Instance) string {
	t.Helper()
	var s strings.Builder
	isAlt := func(in *Instance) bool {
		v, err := in.GetU32(DataActiveScreen)
		require.NoError(t, err)
		return int32(v) == ScreenAlternate
	}
	decMode := func(in *Instance, v uint16) bool {
		on, err := in.GetMode(EncodeMode(v, false))
		require.NoError(t, err)
		return on
	}
	if isAlt(b) && !isAlt(a) {
		// Leave with the mode that entered, or its mode bit stays set.
		switch {
		case decMode(b, 1049):
			s.WriteString("\x1b[?1049l")
		case decMode(b, 1047):
			s.WriteString("\x1b[?1047l")
		default:
			s.WriteString("\x1b[?47l")
		}
	}
	for _, m := range Modes {
		if !m.ANSI && skipReconcile[m.Value] {
			continue
		}
		on, err := a.GetMode(m.Mode())
		require.NoError(t, err)
		prefix, suffix := "?", "l"
		if m.ANSI {
			prefix = ""
		}
		if on {
			suffix = "h"
		}
		fmt.Fprintf(&s, "\x1b[%s%d%s", prefix, m.Value, suffix)
	}
	s.WriteString(penReset)
	s.WriteString("\x1b[2J\x1b[H")
	s.WriteString(snapshot(t, a))
	return s.String()
}

// dirtyState puts a terminal into random state from the mode table, the alt
// screen, kitty keyboard flags, a scroll region, charsets, SGR and an open
// hyperlink.
func dirtyState(r *rand.Rand) string {
	var s strings.Builder
	for _, m := range Modes {
		if !m.ANSI && skipReconcile[m.Value] {
			continue
		}
		if r.Intn(3) != 0 {
			continue
		}
		prefix := "?"
		if m.ANSI {
			prefix = ""
		}
		fmt.Fprintf(&s, "\x1b[%s%d%c", prefix, m.Value, "hl"[r.Intn(2)])
	}
	if r.Intn(2) == 0 {
		s.WriteString("\x1b[?1049h")
	}
	fmt.Fprintf(&s, "\x1b[>%du", r.Intn(32))
	fmt.Fprintf(&s, "\x1b[%d;%dr", 1+r.Intn(3), 5+r.Intn(5))
	s.WriteString([]string{"\x1b(0", "\x1b)0\x0e", "\x1b(A", ""}[r.Intn(4)])
	fmt.Fprintf(&s, "\x1b[%d;%dm", 1+r.Intn(8), 30+r.Intn(8))
	s.WriteString("\x1b]8;;http://dirty\x1b\\dirty text\x1b[5;5H")
	return s.String()
}

// The snapshot path must never assume a reset terminal.
func TestSnapshotRestoreIntoDirtyTerminal(t *testing.T) {
	for _, sc := range scenarios {
		for seed := int64(1); seed <= 20; seed++ {
			t.Run(fmt.Sprintf("%s/seed-%d", sc.name, seed), func(t *testing.T) {
				a := newTerm(t, scenarioCols, scenarioRows)
				b := newTerm(t, scenarioCols, scenarioRows)
				feed(t, a, sc.input)
				feed(t, b, dirtyState(rand.New(rand.NewSource(seed))))
				feed(t, b, snapshotLeave(t, a, b))
				assert.Equal(t, capture(t, a), capture(t, b))

				feed(t, a, probeSuffix)
				feed(t, b, probeSuffix)
				assert.Equal(t, capture(t, a), capture(t, b), "after probe suffix")
			})
		}
	}
}

// Without the pen reset, state that equals the default in a but not in b
// survives the restore. This pins why penReset exists.
func TestFormatterOnlyRestoreLeavesDirtyState(t *testing.T) {
	a := newTerm(t, 20, 5)
	b := newTerm(t, 20, 5)
	feed(t, a, "hi there")
	feed(t, b, "\x1b[2;4r\x1b[>5u")
	feed(t, b, "\x1b[2J\x1b[H"+snapshot(t, a))
	assert.NotEqual(t, capture(t, a), capture(t, b))
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./vt/internal/ghostty/`
Expected: FAIL to compile: `undefined: FormatterOptions`.

- [ ] **Step 3: Implement `formatter.go`**

```go
package ghostty

// Point is GhosttyPoint with a coordinate value.
type Point struct {
	Tag PointTag
	X   uint16
	Y   uint32
}

// Selection is GhosttySelection, given as two points. FormatAlloc turns
// them into grid references.
type Selection struct {
	Start, End Point
	Rectangle  bool
}

// ScreenExtra is GhosttyFormatterScreenExtra.
type ScreenExtra struct {
	Cursor, Style, Hyperlink, Protection, KittyKeyboard, Charsets bool
}

// TerminalExtra is GhosttyFormatterTerminalExtra.
type TerminalExtra struct {
	Palette, Modes, ScrollingRegion, Tabstops, Pwd, Keyboard bool
	Screen                                                   ScreenExtra
}

// FormatterOptions is GhosttyFormatterTerminalOptions.
type FormatterOptions struct {
	Emit         FormatterFormat
	Unwrap, Trim bool
	Extra        TerminalExtra
	Selection    *Selection // nil formats the whole screen, scrollback included
	ContentNone  bool       // extras only; overrides Selection
}

// GridRef is ghostty_terminal_grid_ref, writing the GhosttyGridRef to out.
func (in *Instance) GridRef(pt Point, out uint32) error {
	p := make([]byte, sizeofPoint)
	le.PutUint32(p[offPointTag:], uint32(pt.Tag))
	le.PutUint16(p[offPointValue+offCoordinateX:], pt.X)
	le.PutUint32(p[offPointValue+offCoordinateY:], pt.Y)
	if err := in.writeMem(in.scratch+scrPoint, p); err != nil {
		return err
	}
	ref := make([]byte, sizeofGridRef)
	le.PutUint32(ref[offGridRefSize:], sizeofGridRef)
	if err := in.writeMem(out, ref); err != nil {
		return err
	}
	return in.invoke("ghostty_terminal_grid_ref", func() int32 {
		return in.mod.Xghostty_terminal_grid_ref(int32(in.term), int32(in.scratch+scrPoint), int32(out))
	})
}

// FormatAlloc creates a formatter with opts, formats the terminal's current
// state once, and frees the formatter.
func (in *Instance) FormatAlloc(opts FormatterOptions) ([]byte, error) {
	var sel uint32
	if opts.Selection != nil && !opts.ContentNone {
		sel = in.scratch + scrSelection
		b := make([]byte, sizeofSelection)
		le.PutUint32(b[offSelectionSize:], sizeofSelection)
		if opts.Selection.Rectangle {
			b[offSelectionRectangle] = 1
		}
		if err := in.writeMem(sel, b); err != nil {
			return nil, err
		}
		if err := in.GridRef(opts.Selection.Start, sel+offSelectionStart); err != nil {
			return nil, err
		}
		if err := in.GridRef(opts.Selection.End, sel+offSelectionEnd); err != nil {
			return nil, err
		}
	}
	optPtr := in.scratch + scrFormatter
	if err := in.writeMem(optPtr, encodeFormatterOptions(opts, sel)); err != nil {
		return nil, err
	}
	if err := in.invoke("ghostty_formatter_terminal_new", func() int32 {
		return in.mod.Xghostty_formatter_terminal_new(0, int32(in.slot), int32(in.term), int32(optPtr))
	}); err != nil {
		return nil, err
	}
	f, err := in.takeOpaque()
	if err != nil {
		return nil, err
	}
	out := in.scratch + scrOutPtr
	if err := in.writeMem(out, zero8[:]); err != nil {
		return nil, err
	}
	err = in.invoke("ghostty_formatter_format_alloc", func() int32 {
		return in.mod.Xghostty_formatter_format_alloc(int32(f), 0, int32(out), int32(in.scratch+scrOutLen))
	})
	var data []byte
	if err == nil {
		data, err = in.takeAlloc(out)
	}
	if ferr := in.guard("ghostty_formatter_free", func() { in.mod.Xghostty_formatter_free(int32(f)) }); err == nil {
		err = ferr
	}
	return data, err
}

func encodeFormatterOptions(o FormatterOptions, sel uint32) []byte {
	b := make([]byte, sizeofFormatterOptions)
	le.PutUint32(b[offFmtSize:], sizeofFormatterOptions)
	le.PutUint32(b[offFmtEmit:], uint32(o.Emit))
	b[offFmtUnwrap] = b2u(o.Unwrap)
	b[offFmtTrim] = b2u(o.Trim)

	x := b[offFmtExtra:]
	le.PutUint32(x[offTExtraSize:], sizeofTerminalExtra)
	x[offTExtraPalette] = b2u(o.Extra.Palette)
	x[offTExtraModes] = b2u(o.Extra.Modes)
	x[offTExtraScrollRegion] = b2u(o.Extra.ScrollingRegion)
	x[offTExtraTabstops] = b2u(o.Extra.Tabstops)
	x[offTExtraPwd] = b2u(o.Extra.Pwd)
	x[offTExtraKeyboard] = b2u(o.Extra.Keyboard)

	s := x[offTExtraScreen:]
	le.PutUint32(s[offSExtraSize:], sizeofScreenExtra)
	s[offSExtraCursor] = b2u(o.Extra.Screen.Cursor)
	s[offSExtraStyle] = b2u(o.Extra.Screen.Style)
	s[offSExtraHyperlink] = b2u(o.Extra.Screen.Hyperlink)
	s[offSExtraProtection] = b2u(o.Extra.Screen.Protection)
	s[offSExtraKittyKeyboard] = b2u(o.Extra.Screen.KittyKeyboard)
	s[offSExtraCharsets] = b2u(o.Extra.Screen.Charsets)

	le.PutUint32(b[offFmtSelection:], sel)
	b[offFmtContentNone] = b2u(o.ContentNone)
	return b
}

func b2u(v bool) byte {
	if v {
		return 1
	}
	return 0
}
```

- [ ] **Step 4: Generate the goldens and review them**

Run: `go test ./vt/internal/ghostty/ -run TestFormatGolden -update`
Then: `cat -v vt/internal/ghostty/testdata/format-origin-mode.golden`
Expected: the cursor CUP after `\x1b[3;8r` is `\x1b[2;3H` (relative to the region, from patch 0002), not `\x1b[4;3H`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./vt/internal/ghostty/`
Expected: PASS. (If `origin-mode` fails in `TestFormatRoundTrip`, patch 0002 is missing from the build: rerun Task 1 Step 4.)

- [ ] **Step 6: Commit**

```bash
git add vt/internal/ghostty
git commit -m "Pin libghostty formatter round trips and dirty-terminal restore"
```

---

### Task 6: `write_pty` callback and query effects

**Files:**
- Create: `vt/internal/ghostty/callback.go`
- Test: `vt/internal/ghostty/callback_test.go`, `vt/internal/ghostty/effects_test.go`

**Interfaces:**
- Consumes: Task 2 core (`mod`, `mem`, `guard`, `TerminalSetPtr`, fields `ptyIndex`, `writePty`).
- Produces: `func (in *Instance) SetWritePty(fn func([]byte)) error` (nil removes the callback; `fn` runs synchronously inside `VTWrite` and must not call the instance; a panic in `fn` becomes a `*TrapError` from that `VTWrite`).

A C callback is an index into the module's function table. wasm2go exposes the table as `X__indirect_function_table() *[]any`, and an indirect call is a type assertion on the entry, so the callback is a Go method value of exactly `func(int32, int32, int32, int32)` appended to that slice. No shim module is needed.

- [ ] **Step 1: Write the failing tests**

`vt/internal/ghostty/callback_test.go`:

```go
package ghostty

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWritePtyInstalledInTable(t *testing.T) {
	in := newTerm(t, 80, 24)
	before := len(*in.mod.X__indirect_function_table())
	require.NoError(t, in.SetWritePty(func([]byte) {}))
	assert.EqualValues(t, before, in.ptyIndex, "the callback is the new last entry")
	assert.Len(t, *in.mod.X__indirect_function_table(), before+1)

	require.NoError(t, in.SetWritePty(func([]byte) {}))
	assert.Len(t, *in.mod.X__indirect_function_table(), before+1, "a second SetWritePty reuses the entry")
}

func TestWritePtyReachesItsOwnInstance(t *testing.T) {
	a := newTerm(t, 80, 24)
	b := newTerm(t, 80, 24)
	var gotA, gotB []byte
	require.NoError(t, a.SetWritePty(func(p []byte) { gotA = append(gotA, p...) }))
	require.NoError(t, b.SetWritePty(func(p []byte) { gotB = append(gotB, p...) }))

	feed(t, a, "\x1b[6n")
	feed(t, b, "x\x1b[5n")
	assert.Equal(t, "\x1b[1;1R", string(gotA))
	assert.Equal(t, "\x1b[0n", string(gotB))
}

func TestWritePtyRemoved(t *testing.T) {
	in := newTerm(t, 80, 24)
	calls := 0
	require.NoError(t, in.SetWritePty(func([]byte) { calls++ }))
	require.NoError(t, in.SetWritePty(nil))
	feed(t, in, "\x1b[6n")
	assert.Zero(t, calls)
}

func TestWritePtyDataIsCopied(t *testing.T) {
	in := newTerm(t, 80, 24)
	var kept [][]byte
	require.NoError(t, in.SetWritePty(func(p []byte) { kept = append(kept, p) }))
	feed(t, in, "\x1b[6n")
	feed(t, in, "\x1b[5;5H\x1b[6n")
	require.Len(t, kept, 2)
	assert.Equal(t, "\x1b[1;1R", string(kept[0]), "later writes must not change earlier answers")
	assert.Equal(t, "\x1b[5;5R", string(kept[1]))
}

// The callback runs inside the module call, so its panic unwinds through
// translated code and leaves the instance in an unknown state.
func TestWritePtyPanicIsTrap(t *testing.T) {
	in := newTerm(t, 80, 24)
	require.NoError(t, in.SetWritePty(func([]byte) { panic("callback failed") }))
	err := in.VTWrite([]byte("\x1b[6n"))
	require.ErrorIs(t, err, ErrTrap)
	assert.ErrorContains(t, err, "callback failed")
}
```

`vt/internal/ghostty/effects_test.go`:

```go
package ghostty

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The overlay forwards these answers to the app while a prompt shows. An
// upgrade that starts answering a new query, especially with made-up
// colours or sizes, needs a decision before it is adopted.
func TestQueryAnswers(t *testing.T) {
	tests := []struct {
		name, query, want string
	}{
		{"DA1", "\x1b[c", "\x1b[?62;22c"},
		{"DA2", "\x1b[>c", "\x1b[>1;0;0c"},
		{"DSR 5n", "\x1b[5n", "\x1b[0n"},
		{"CPR 6n", "\x1b[6n", "\x1b[1;1R"},
		{"kitty keyboard query", "\x1b[?u", "\x1b[?0u"},
		{"DECRQM", "\x1b[?2004$p", "\x1b[?2004;2$y"},
		{"XTVERSION", "\x1b[>q", "\x1bP>|libghostty\x1b\\"},
		{"OSC 10 query", "\x1b]10;?\x07", ""},
		{"OSC 11 query", "\x1b]11;?\x07", ""},
		{"XTWINOPS 14t", "\x1b[14t", ""},
		{"XTWINOPS 16t", "\x1b[16t", ""},
		{"XTWINOPS 18t", "\x1b[18t", ""},
		{"ENQ", "\x05", ""},
		{"colour scheme 996n", "\x1b[?996n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := newTerm(t, 80, 24)
			var got []byte
			require.NoError(t, in.SetWritePty(func(p []byte) { got = append(got, p...) }))
			feed(t, in, tt.query)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

func TestQueriesWithoutCallbackAreDropped(t *testing.T) {
	in := newTerm(t, 80, 24)
	feed(t, in, "\x1b[c\x1b[6n\x1b[?u\x1b[?2004$phello")
	x, err := in.GetU16(DataCursorX)
	require.NoError(t, err)
	assert.EqualValues(t, 5, x, "processing continues after unanswered queries")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./vt/internal/ghostty/`
Expected: FAIL to compile: `in.SetWritePty undefined`.

- [ ] **Step 3: Implement `callback.go`**

```go
package ghostty

import "bytes"

// SetWritePty installs fn as the terminal's write_pty callback, which
// receives the answers to queries in the stream. nil removes it. fn runs
// synchronously inside VTWrite and must not call this instance. A panic in
// fn becomes a *TrapError from that VTWrite.
func (in *Instance) SetWritePty(fn func([]byte)) error {
	in.writePty = fn
	if fn == nil {
		return in.TerminalSetPtr(OptWritePty, 0)
	}
	if in.ptyIndex == 0 {
		// A C callback is a function table index, and an indirect call is a
		// type assertion on the entry, so the entry's type must match
		// GhosttyTerminalWritePtyFn exactly: (i32 i32 i32 i32) -> ().
		tab := in.mod.X__indirect_function_table()
		in.ptyIndex = uint32(len(*tab))
		*tab = append(*tab, in.onWritePty)
	}
	return in.TerminalSetPtr(OptWritePty, in.ptyIndex)
}

// onWritePty is the function table entry. Its arguments are the terminal,
// the userdata, and the answer's pointer and length in linear memory.
func (in *Instance) onWritePty(_, _, data, n int32) {
	if in.writePty == nil {
		return
	}
	// The bytes live in linear memory that the next call may reuse. A bad
	// pointer panics here, inside the guarded call.
	b := in.mem()[uint32(data) : uint32(data)+uint32(n)]
	in.writePty(bytes.Clone(b))
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./vt/internal/ghostty/`
Expected: PASS. The callback gets table index 160 at the pinned commit.

- [ ] **Step 5: Commit**

```bash
git add vt/internal/ghostty
git commit -m "Install write_pty in the function table and pin query answers"
```

---

### Task 7: Known gaps, robustness and benchmark

**Files:**
- Test: `vt/internal/ghostty/known_gaps_test.go`, `vt/internal/ghostty/robustness_test.go`, `vt/internal/ghostty/bench_test.go`

**Interfaces:**
- Consumes: `scenarios`, `restoreExtras`, `snapshot`, `format`, `activeArea` (Task 5), `styledLines` (Task 4), `newTerm`, `feed`.
- Produces: `FuzzVTWrite`, `BenchmarkVTWrite` (used by the `ghostty-wasm.yml` workflow).

- [ ] **Step 1: Write the tests**

`vt/internal/ghostty/known_gaps_test.go`:

```go
package ghostty

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

// These tests describe what the formatter does NOT do today. When an
// upgrade makes one fail, upstream closed a gap: drop vibepit's own
// emission of that state so it isn't emitted twice.
func TestKnownGaps(t *testing.T) {
	full := func(t *testing.T, in *Instance, extra TerminalExtra) string {
		return format(t, in, FormatterOptions{Emit: FormatVT, Extra: extra})
	}

	t.Run("no per-cell OSC 8 hyperlinks", func(t *testing.T) {
		in := newTerm(t, 20, 5)
		feed(t, in, "\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\")
		assert.NotContains(t, full(t, in, restoreExtras), "\x1b]8;")
	})
	t.Run("no DECSCUSR cursor shape", func(t *testing.T) {
		in := newTerm(t, 20, 5)
		feed(t, in, "\x1b[5 q")
		assert.NotContains(t, full(t, in, restoreExtras), " q")
	})
	t.Run("no title", func(t *testing.T) {
		in := newTerm(t, 20, 5)
		feed(t, in, "\x1b]2;TITLE\x07")
		assert.NotContains(t, full(t, in, restoreExtras), "TITLE")
	})
	t.Run("pwd extra emits a stray NUL", func(t *testing.T) {
		in := newTerm(t, 20, 5)
		feed(t, in, "\x1b]7;file://h/tmp\x07")
		extra := restoreExtras
		extra.Pwd = true
		assert.Contains(t, full(t, in, extra), "\x1b]7;file://h/tmp\x00")
	})
	t.Run("only the active screen", func(t *testing.T) {
		in := newTerm(t, 20, 5)
		feed(t, in, "SHELL\x1b[?1049hVIM")
		assert.NotContains(t, full(t, in, restoreExtras), "SHELL")
	})
	t.Run("no saved cursor", func(t *testing.T) {
		in := newTerm(t, 20, 5)
		feed(t, in, "ab\x1b7\x1b[3;3H")
		got := full(t, in, restoreExtras)
		assert.NotContains(t, got, "\x1b7")
		assert.NotContains(t, got, "\x1b[?1048h")
	})
	t.Run("tab-stop extra moves the cursor", func(t *testing.T) {
		in := newTerm(t, 20, 5)
		feed(t, in, "\x1b[3;5Hx")
		got := format(t, in, FormatterOptions{Emit: FormatVT, ContentNone: true, Extra: TerminalExtra{Tabstops: true}})
		assert.Contains(t, got, "\x1b[3g")
		assert.Regexp(t, regexp.MustCompile(`\x1b\[\d+G\x1bH`), got)
	})
}
```

`vt/internal/ghostty/robustness_test.go`:

```go
package ghostty

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func FuzzVTWrite(f *testing.F) {
	for _, sc := range scenarios {
		f.Add([]byte(sc.input))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		in := newTerm(t, 40, 10)
		require.NoError(t, in.TerminalSetSize(OptContinuationMaxBytes, 1024))
		require.NoError(t, in.VTWrite(data))
		_, err := in.FormatAlloc(FormatterOptions{Emit: FormatVT, Extra: restoreExtras, Selection: activeArea(t, in)})
		require.NoError(t, err)
		_, err = in.ContinuationAlloc()
		assert.NotErrorIs(t, err, ErrTrap)
	})
}

// Hitting the memory limit must not trap or panic.
func TestMemoryLimit(t *testing.T) {
	const pages = 64
	in, err := NewInstance(Config{MemoryLimitPages: pages})
	require.NoError(t, err)
	defer func() { _ = in.Close() }()
	require.NoError(t, in.TerminalNew(200, 60))
	require.NoError(t, in.TerminalSetPtr(OptScrollbackMaxBytes, 0), "no byte limit")
	require.NoError(t, in.TerminalSetPtr(OptScrollbackMaxLines, 0), "no line limit")

	chunk := styledLines(1 << 20)
	for range 32 {
		require.NoError(t, in.VTWrite(chunk))
	}
	assert.LessOrEqual(t, in.MemorySize(), uint64(pages*64<<10))
	_, err = in.FormatAlloc(FormatterOptions{Emit: FormatVT})
	assert.NotErrorIs(t, err, ErrTrap)
}

func TestTrapBecomesError(t *testing.T) {
	in := newTerm(t, 80, 24)
	// A pointer past the end of linear memory fails the generated code's
	// bounds check, which panics.
	err := in.guard("ghostty_terminal_vt_write", func() {
		in.mod.Xghostty_terminal_vt_write(int32(in.term), -256, 1000)
	})
	require.ErrorIs(t, err, ErrTrap)
	var te *TrapError
	require.True(t, errors.As(err, &te))
	assert.Equal(t, "ghostty_terminal_vt_write", te.Func)
}
```

`vt/internal/ghostty/bench_test.go`:

```go
package ghostty

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// BenchmarkVTWrite is the spike's workload E: styled lines into a 200x60
// terminal. It isn't a pass/fail gate; upgrade PRs record it before and
// after.
func BenchmarkVTWrite(b *testing.B) {
	in := newTerm(b, 200, 60)
	require.NoError(b, in.TerminalSetSize(OptScrollbackMaxLines, 10000))
	chunk := styledLines(64 << 10)
	b.SetBytes(int64(len(chunk)))
	b.ResetTimer()
	for b.Loop() {
		if err := in.VTWrite(chunk); err != nil {
			b.Fatal(err)
		}
	}
}
```

- [ ] **Step 2: Run the tests**

Run: `go test ./vt/internal/ghostty/ && go test -run '^$' -fuzz FuzzVTWrite -fuzztime 30s ./vt/internal/ghostty/ && go test -run '^$' -bench BenchmarkVTWrite -benchtime 3x ./vt/internal/ghostty/`
Expected: PASS; fuzzing finds no failure; the benchmark reports about 150 MB/s or more on a current desktop CPU (158 MB/s on a Ryzen 7 7700 in the evaluation). Much less means the module was generated without `-unsafe`.

These tests pin existing behaviour, so there is no failing step. If a known-gap test fails on the pinned build, the build differs from the pinned commit: check `GHOSTTY_COMMIT`. If the fuzzer process dies with `fatal error: stack overflow`, that is the unrecoverable case from the spec: keep the input, report it upstream, and stop to ask the user. If the fuzzer finds a trap, keep the corpus entry under `testdata/fuzz/FuzzVTWrite/`, report it upstream, and stop to ask the user before continuing.

- [ ] **Step 3: Commit**

```bash
git add vt/internal/ghostty
git commit -m "Pin libghostty known gaps, robustness and write throughput"
```

---

### Task 8: Public `vt` package

**Files:**
- Create: `vt/terminal.go`, `vt/format.go`, `vt/types.go`
- Test: `vt/terminal_test.go` (package `vt_test`), `vt/terminal_internal_test.go` (package `vt`)

**Interfaces:**
- Consumes: everything exported from `vt/internal/ghostty` (Tasks 2–6).
- Produces (the API later plans build on). There is no runtime object: the emulator is compiled Go code with nothing to set up, so `NewTerminal` is a plain function.

```go
func NewTerminal(cols, rows uint16, opts ...TerminalOption) (*Terminal, error)
func WithMemoryLimit(bytes uint64) TerminalOption
func WithScrollbackLines(n uint) TerminalOption
func WithScrollbackBytes(n uint) TerminalOption
func WithContinuationMaxBytes(n uint) TerminalOption
func WithWritePty(fn func([]byte)) TerminalOption
func (t *Terminal) Write(p []byte) (int, error)
func (t *Terminal) WriteUntilGround(p []byte) (n int, ground bool, err error)
func (t *Terminal) Resize(cols, rows uint16) error
func (t *Terminal) AltScreen() (bool, error)
func (t *Terminal) Mode(mode uint16, ansi bool) (bool, error)
func (t *Terminal) SetMode(mode uint16, ansi, value bool) error
func (t *Terminal) Modes() ([]ModeState, error)
func (t *Terminal) KittyKeyboardFlags() (uint8, error)
func (t *Terminal) CursorStyle() (CursorStyle, error)
func (t *Terminal) CursorVisible() (bool, error)
func (t *Terminal) MouseTracking() (MouseTracking, error)
func (t *Terminal) Title() (string, error)
func (t *Terminal) Pwd() (string, error)
func (t *Terminal) AtGround() (bool, error)
func (t *Terminal) Continuation() ([]byte, error)
func (t *Terminal) Format(opts FormatOptions) ([]byte, error)
func (t *Terminal) Close() error
var ErrFailed, ErrClosed, ErrContinuationUnavailable, ErrOutOfMemory error
const DefaultMemoryLimit, DefaultScrollbackLines, DefaultScrollbackBytes, DefaultContinuationMaxBytes
type ModeState struct{ Mode uint16; ANSI, Value, Default bool }
type CursorShape uint8 // CursorBlock, CursorBar, CursorUnderline, CursorBlockHollow
type CursorStyle struct{ Shape CursorShape; Blinking bool }; func (s CursorStyle) DECSCUSR() string
type MouseTracking uint8 // MouseNone, MouseX10, MouseNormal, MouseButton, MouseAny
type Region uint8 // RegionScreen, RegionScrollback, RegionNone
type Output uint8 // OutputVT, OutputPlain
type Extras struct{ Modes, ScrollRegion, Keyboard, Cursor, Style, Hyperlink, Protection, KittyKeyboard, Charsets bool }
var AllExtras Extras
type FormatOptions struct{ Output Output; Unwrap, Trim bool; Extras Extras; Region Region }
```

- [ ] **Step 1: Write the failing tests**

`vt/terminal_test.go`:

```go
package vt_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/bernd/vibepit/vt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTerminal(t *testing.T, cols, rows uint16, opts ...vt.TerminalOption) *vt.Terminal {
	t.Helper()
	term, err := vt.NewTerminal(cols, rows, opts...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = term.Close() })
	return term
}

func write(t *testing.T, term *vt.Terminal, s string) {
	t.Helper()
	n, err := term.Write([]byte(s))
	require.NoError(t, err)
	require.Equal(t, len(s), n)
}

func TestFormatRegions(t *testing.T) {
	const input = "l1\r\nl2\r\n\x1b[31ml3\x1b[0m\r\nl4 long line wraps\r\nl5\x1b[?2004h"
	tests := []struct {
		name     string
		input    string
		opts     vt.FormatOptions
		contains []string
		excludes []string
		empty    bool
	}{
		{
			name:     "screen",
			input:    input,
			opts:     vt.FormatOptions{Unwrap: true, Extras: vt.AllExtras},
			contains: []string{"\x1b[?2004h", "l4 long line wraps", "l5"},
			excludes: []string{"l1"},
		},
		{
			name:     "scrollback",
			input:    input,
			opts:     vt.FormatOptions{Region: vt.RegionScrollback},
			contains: []string{"l1\r\nl2", "l3"},
			excludes: []string{"l5", "\x1b[?2004h"},
		},
		{
			name:     "none",
			input:    input,
			opts:     vt.FormatOptions{Region: vt.RegionNone, Extras: vt.AllExtras},
			contains: []string{"\x1b[?2004h"},
			excludes: []string{"l1", "l5"},
		},
		{
			name:  "scrollback empty",
			input: "only screen",
			opts:  vt.FormatOptions{Region: vt.RegionScrollback},
			empty: true,
		},
		{
			name:     "plain",
			input:    "\x1b[31mred\x1b[0m",
			opts:     vt.FormatOptions{Output: vt.OutputPlain, Trim: true},
			contains: []string{"red"},
			excludes: []string{"\x1b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			term := newTerminal(t, 10, 3)
			write(t, term, tt.input)
			got, err := term.Format(tt.opts)
			require.NoError(t, err)
			if tt.empty {
				assert.Empty(t, got)
			}
			for _, s := range tt.contains {
				assert.Contains(t, string(got), s)
			}
			for _, s := range tt.excludes {
				assert.NotContains(t, string(got), s)
			}
		})
	}
}

func TestCursorStyle(t *testing.T) {
	tests := []struct {
		input string
		want  vt.CursorStyle
	}{
		{"", vt.CursorStyle{Shape: vt.CursorBlock}},
		{"\x1b[0 q", vt.CursorStyle{Shape: vt.CursorBlock}},
		{"\x1b[1 q", vt.CursorStyle{Shape: vt.CursorBlock, Blinking: true}},
		{"\x1b[2 q", vt.CursorStyle{Shape: vt.CursorBlock}},
		{"\x1b[3 q", vt.CursorStyle{Shape: vt.CursorUnderline, Blinking: true}},
		{"\x1b[4 q", vt.CursorStyle{Shape: vt.CursorUnderline}},
		{"\x1b[5 q", vt.CursorStyle{Shape: vt.CursorBar, Blinking: true}},
		{"\x1b[6 q", vt.CursorStyle{Shape: vt.CursorBar}},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.input), func(t *testing.T) {
			term := newTerminal(t, 80, 24)
			write(t, term, tt.input)
			got, err := term.CursorStyle()
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)

			again := newTerminal(t, 80, 24)
			write(t, again, got.DECSCUSR())
			round, err := again.CursorStyle()
			require.NoError(t, err)
			assert.Equal(t, got, round, "DECSCUSR round trip")
		})
	}
}

func TestMouseTracking(t *testing.T) {
	tests := []struct {
		input string
		want  vt.MouseTracking
	}{
		{"", vt.MouseNone},
		{"\x1b[?9h", vt.MouseX10},
		{"\x1b[?1000h", vt.MouseNormal},
		{"\x1b[?1002h", vt.MouseButton},
		{"\x1b[?1003h", vt.MouseAny},
		{"\x1b[?1000h\x1b[?1003h", vt.MouseAny},
		{"\x1b[?1003h\x1b[?1003l\x1b[?1000h", vt.MouseNormal},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.input), func(t *testing.T) {
			term := newTerminal(t, 80, 24)
			write(t, term, tt.input)
			got, err := term.MouseTracking()
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestModes(t *testing.T) {
	term := newTerminal(t, 80, 24)
	write(t, term, "\x1b[?2004h\x1b[4h")
	modes, err := term.Modes()
	require.NoError(t, err)
	require.Len(t, modes, 43)
	assert.Contains(t, modes, vt.ModeState{Mode: 2004, Value: true})
	assert.Contains(t, modes, vt.ModeState{Mode: 4, ANSI: true, Value: true})
	assert.Contains(t, modes, vt.ModeState{Mode: 7, Value: true, Default: true})

	on, err := term.Mode(2004, false)
	require.NoError(t, err)
	assert.True(t, on)
	require.NoError(t, term.SetMode(2004, false, false))
	on, err = term.Mode(2004, false)
	require.NoError(t, err)
	assert.False(t, on)
}

func TestScreenGetters(t *testing.T) {
	term := newTerminal(t, 80, 24)
	write(t, term, "\x1b[?1049h\x1b[>5u\x1b[?25l\x1b]2;title\x07\x1b]7;file://h/tmp\x07")

	alt, err := term.AltScreen()
	require.NoError(t, err)
	assert.True(t, alt)
	flags, err := term.KittyKeyboardFlags()
	require.NoError(t, err)
	assert.EqualValues(t, 5, flags)
	visible, err := term.CursorVisible()
	require.NoError(t, err)
	assert.False(t, visible)
	title, err := term.Title()
	require.NoError(t, err)
	assert.Equal(t, "title", title)
	pwd, err := term.Pwd()
	require.NoError(t, err)
	assert.Equal(t, "file://h/tmp", pwd)
}

func TestWriteUntilGround(t *testing.T) {
	term := newTerminal(t, 80, 24)
	n, ground, err := term.WriteUntilGround([]byte("abc"))
	require.NoError(t, err)
	assert.Equal(t, 0, n, "already at ground")
	assert.True(t, ground)

	write(t, term, "abc\xe2")
	atGround, err := term.AtGround()
	require.NoError(t, err)
	assert.False(t, atGround)
	n, ground, err = term.WriteUntilGround([]byte("\x82\xacX"))
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.True(t, ground)
}

func TestContinuation(t *testing.T) {
	term := newTerminal(t, 80, 24)
	write(t, term, "abc\x1b[3")
	got, err := term.Continuation()
	require.NoError(t, err)
	assert.Equal(t, "\x1b[3", string(got), "tracking is on by default")

	small := newTerminal(t, 80, 24, vt.WithContinuationMaxBytes(1024))
	write(t, small, "\x1b]2;"+strings.Repeat("x", 1100))
	_, err = small.Continuation()
	assert.ErrorIs(t, err, vt.ErrContinuationUnavailable)
}

func TestWritePty(t *testing.T) {
	var answers []byte
	term := newTerminal(t, 80, 24, vt.WithWritePty(func(p []byte) { answers = append(answers, p...) }))
	write(t, term, "\x1b[6n")
	assert.Equal(t, "\x1b[1;1R", string(answers))
}

func TestResize(t *testing.T) {
	term := newTerminal(t, 10, 3)
	write(t, term, "l4 long line wraps")
	require.NoError(t, term.Resize(20, 3))
	got, err := term.Format(vt.FormatOptions{Output: vt.OutputPlain, Trim: true})
	require.NoError(t, err)
	assert.Equal(t, "l4 long line wraps", strings.Split(string(got), "\n")[0])
}

func TestZeroSize(t *testing.T) {
	_, err := vt.NewTerminal(0, 24)
	assert.Error(t, err)

	term := newTerminal(t, 80, 24)
	assert.Error(t, term.Resize(80, 0))
	write(t, term, "still usable")
}

func TestClose(t *testing.T) {
	term, err := vt.NewTerminal(80, 24)
	require.NoError(t, err)
	require.NoError(t, term.Close())
	require.NoError(t, term.Close(), "idempotent")
	_, err = term.Write([]byte("x"))
	assert.ErrorIs(t, err, vt.ErrClosed)
	_, err = term.Format(vt.FormatOptions{})
	assert.ErrorIs(t, err, vt.ErrClosed)
}

func TestConcurrentWriteAndFormat(t *testing.T) {
	term := newTerminal(t, 80, 24)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 200 {
				_, _ = term.Write([]byte("\x1b[31mline\x1b[0m\r\n"))
			}
		}()
		go func() {
			defer wg.Done()
			for range 200 {
				_, _ = term.Format(vt.FormatOptions{Extras: vt.AllExtras})
			}
		}()
	}
	wg.Wait()
}

// With generous scrollback limits, the memory limit is what stops growth.
// Hitting it is not an error for Write. A large Format then fails with
// ErrOutOfMemory, which must not fail the terminal: a small Format still
// works. vibed's scrollback replay (phase 2) relies on this to fall back.
func TestMemoryLimitOption(t *testing.T) {
	term := newTerminal(t, 200, 24,
		vt.WithMemoryLimit(4<<20), vt.WithScrollbackLines(1<<20), vt.WithScrollbackBytes(1<<30))
	line := strings.Repeat("x", 190) + "\r\n"
	_, err := term.Write([]byte(strings.Repeat(line, 50000)))
	require.NoError(t, err, "hitting the memory limit is not an error for Write")

	_, err = term.Format(vt.FormatOptions{Region: vt.RegionScrollback})
	require.ErrorIs(t, err, vt.ErrOutOfMemory, "about 1,600 rows of history don't fit")
	assert.NotErrorIs(t, err, vt.ErrFailed)

	write(t, term, "\x1b[2J\x1b[Hstill here")
	got, err := term.Format(vt.FormatOptions{Output: vt.OutputPlain, Trim: true})
	require.NoError(t, err)
	assert.Contains(t, string(got), "still here")
}
```

`vt/terminal_internal_test.go`:

```go
package vt

import (
	"errors"
	"testing"

	"github.com/bernd/vibepit/vt/internal/ghostty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newInternalTerminal(t *testing.T) *Terminal {
	t.Helper()
	term, err := NewTerminal(80, 24)
	require.NoError(t, err)
	t.Cleanup(func() { _ = term.Close() })
	return term
}

func TestTrapMarksTerminalFailed(t *testing.T) {
	term := newInternalTerminal(t)
	trap := &ghostty.TrapError{Func: "ghostty_terminal_vt_write", Err: errors.New("unreachable")}
	err := term.do(func(*ghostty.Instance) error { return trap })
	require.ErrorIs(t, err, ErrFailed)

	_, err = term.Write([]byte("x"))
	assert.ErrorIs(t, err, ErrFailed)
	_, err = term.Format(FormatOptions{})
	assert.ErrorIs(t, err, ErrFailed)
	_, err = term.AltScreen()
	assert.ErrorIs(t, err, ErrFailed)
	assert.NoError(t, term.Close(), "a failed terminal still closes")
}

func TestOtherErrorsDoNotFail(t *testing.T) {
	term := newInternalTerminal(t)
	err := term.do(func(*ghostty.Instance) error { return errors.New("plain") })
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrFailed)
	_, err = term.Write([]byte("x"))
	assert.NoError(t, err)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./vt/`
Expected: FAIL to compile: `undefined: vt.NewTerminal`.

- [ ] **Step 3: Implement `vt/types.go`**

```go
package vt

import (
	"fmt"

	"github.com/bernd/vibepit/vt/internal/ghostty"
)

// ModeState is one terminal mode: its current and its default value.
type ModeState struct {
	Mode    uint16
	ANSI    bool // an ANSI mode (CSI n h) rather than a DEC mode (CSI ? n h)
	Value   bool
	Default bool
}

// CursorShape is the shape selected by DECSCUSR.
type CursorShape uint8

const (
	CursorBlock CursorShape = iota
	CursorBar
	CursorUnderline
	CursorBlockHollow
)

// CursorStyle is the cursor's DECSCUSR state.
type CursorStyle struct {
	Shape    CursorShape
	Blinking bool
}

// DECSCUSR returns the sequence that selects s. A hollow block has no
// DECSCUSR code and maps to a block.
func (s CursorStyle) DECSCUSR() string {
	n := 2
	switch s.Shape {
	case CursorUnderline:
		n = 4
	case CursorBar:
		n = 6
	}
	if s.Blinking {
		n--
	}
	return fmt.Sprintf("\x1b[%d q", n)
}

func cursorShape(v ghostty.CursorVisualStyle) CursorShape {
	switch v {
	case ghostty.CursorVisualBar:
		return CursorBar
	case ghostty.CursorVisualUnderline:
		return CursorUnderline
	case ghostty.CursorVisualBlockHollow:
		return CursorBlockHollow
	default:
		return CursorBlock
	}
}

// MouseTracking is the most inclusive mouse tracking mode that is on.
type MouseTracking uint8

const (
	MouseNone   MouseTracking = iota
	MouseX10                  // mode 9
	MouseNormal               // mode 1000
	MouseButton               // mode 1002
	MouseAny                  // mode 1003
)
```

- [ ] **Step 4: Implement `vt/format.go`**

```go
package vt

import (
	"fmt"

	"github.com/bernd/vibepit/vt/internal/ghostty"
)

// Region selects the content Format emits.
type Region uint8

const (
	// RegionScreen is the visible screen.
	RegionScreen Region = iota
	// RegionScrollback is the history above the visible screen.
	RegionScrollback
	// RegionNone emits no content, only the requested extras.
	RegionNone
)

// Output selects the output format.
type Output uint8

const (
	OutputVT Output = iota
	OutputPlain
)

// Extras selects the terminal state Format emits besides the content. The
// emulator's palette, tab-stop and working-directory extras are left out:
// the tab-stop extra moves the cursor and the working-directory extra
// emits a stray NUL.
type Extras struct {
	Modes         bool // modes that differ from their defaults
	ScrollRegion  bool
	Keyboard      bool // modifyOtherKeys
	Cursor        bool // position, including a pending wrap
	Style         bool // the SGR pen
	Hyperlink     bool // the pen's open OSC 8 link
	Protection    bool
	KittyKeyboard bool
	Charsets      bool
}

// AllExtras emits every supported extra.
var AllExtras = Extras{
	Modes: true, ScrollRegion: true, Keyboard: true, Cursor: true, Style: true,
	Hyperlink: true, Protection: true, KittyKeyboard: true, Charsets: true,
}

// FormatOptions configures Format.
type FormatOptions struct {
	Output Output
	// Unwrap joins soft-wrapped lines, so a replay at another width
	// reflows them.
	Unwrap bool
	// Trim drops trailing whitespace on non-blank lines.
	Trim   bool
	Extras Extras
	Region Region
}

// Format serializes the terminal's current state. Extras are emitted after
// the content, in an order that restores them: modes first, then the
// scroll region, then the cursor, then the pen.
func (t *Terminal) Format(opts FormatOptions) ([]byte, error) {
	var out []byte
	err := t.do(func(in *ghostty.Instance) error {
		o, err := formatterOptions(in, opts)
		if err != nil {
			return err
		}
		out, err = in.FormatAlloc(o)
		return err
	})
	return out, err
}

func formatterOptions(in *ghostty.Instance, opts FormatOptions) (ghostty.FormatterOptions, error) {
	x := opts.Extras
	o := ghostty.FormatterOptions{
		Emit:   ghostty.FormatVT,
		Unwrap: opts.Unwrap,
		Trim:   opts.Trim,
		Extra: ghostty.TerminalExtra{
			Modes:           x.Modes,
			ScrollingRegion: x.ScrollRegion,
			Keyboard:        x.Keyboard,
			Screen: ghostty.ScreenExtra{
				Cursor:        x.Cursor,
				Style:         x.Style,
				Hyperlink:     x.Hyperlink,
				Protection:    x.Protection,
				KittyKeyboard: x.KittyKeyboard,
				Charsets:      x.Charsets,
			},
		},
	}
	if opts.Output == OutputPlain {
		o.Emit = ghostty.FormatPlain
	}
	cols, err := in.GetU16(ghostty.DataCols)
	if err != nil {
		return o, err
	}
	switch opts.Region {
	case RegionNone:
		o.ContentNone = true
	case RegionScreen:
		rows, err := in.GetU16(ghostty.DataRows)
		if err != nil {
			return o, err
		}
		o.Selection = &ghostty.Selection{
			Start: ghostty.Point{Tag: ghostty.PointActive},
			End:   ghostty.Point{Tag: ghostty.PointActive, X: cols - 1, Y: uint32(rows) - 1},
		}
	case RegionScrollback:
		rows, err := in.GetU32(ghostty.DataScrollbackRows)
		if err != nil {
			return o, err
		}
		if rows == 0 {
			// No history: a selection can't be empty.
			o.ContentNone = true
			break
		}
		o.Selection = &ghostty.Selection{
			Start: ghostty.Point{Tag: ghostty.PointHistory},
			End:   ghostty.Point{Tag: ghostty.PointHistory, X: cols - 1, Y: rows - 1},
		}
	default:
		return o, fmt.Errorf("vt: unknown region %d", opts.Region)
	}
	return o, nil
}
```

- [ ] **Step 5: Implement `vt/terminal.go`**

```go
// Package vt emulates a terminal as a shadow of a byte stream: it parses
// everything a program writes, reports the resulting terminal state, and
// serializes that state back to VT sequences.
package vt

import (
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/bernd/vibepit/vt/internal/ghostty"
)

var (
	// ErrFailed wraps the fault that stopped a terminal. Every later call
	// returns it; callers fall back to working without the emulator.
	ErrFailed = errors.New("vt: terminal failed")
	// ErrClosed is returned by calls after Close.
	ErrClosed = errors.New("vt: terminal closed")
	// ErrContinuationUnavailable means the unfinished sequence outgrew
	// the continuation limit, or tracking is off.
	ErrContinuationUnavailable = errors.New("vt: continuation unavailable")
	// ErrOutOfMemory means the emulator couldn't allocate under its memory
	// limit, typically for a large Format. The terminal stays usable, and
	// smaller requests (RegionScreen) still succeed.
	ErrOutOfMemory = errors.New("vt: emulator out of memory")

	errSize = errors.New("vt: terminal size must be at least 1x1")
)

// Defaults for NewTerminal. The emulator's own defaults are not relied on.
const (
	DefaultMemoryLimit          = 256 << 20
	DefaultScrollbackLines      = 1000
	DefaultScrollbackBytes      = 32 << 20
	DefaultContinuationMaxBytes = 64 << 10
)

const pageSize = 64 << 10

type terminalConfig struct {
	memoryLimit     uint64
	scrollbackLines uint
	scrollbackBytes uint
	continuationMax uint
	writePty        func([]byte)
}

// TerminalOption configures NewTerminal.
type TerminalOption func(*terminalConfig)

// WithMemoryLimit caps the terminal's emulator memory, rounded up to 64 KiB.
// Past the cap, Write keeps working and the emulator drops history instead
// of growing, but a Format whose output doesn't fit fails with
// ErrOutOfMemory (a RegionScrollback of a long history, for example). Keep
// the cap well above WithScrollbackBytes. Zero means no cap below the
// emulator's 4 GiB address space.
func WithMemoryLimit(bytes uint64) TerminalOption {
	return func(c *terminalConfig) { c.memoryLimit = bytes }
}

// WithScrollbackLines limits history to about n lines. The limit applies
// per page of rows, so a little less may be kept.
func WithScrollbackLines(n uint) TerminalOption {
	return func(c *terminalConfig) { c.scrollbackLines = n }
}

// WithScrollbackBytes limits history memory to about n bytes. Zero keeps
// no history.
func WithScrollbackBytes(n uint) TerminalOption {
	return func(c *terminalConfig) { c.scrollbackBytes = n }
}

// WithContinuationMaxBytes bounds how much of an unfinished escape
// sequence Continuation can reproduce. Zero turns tracking off.
func WithContinuationMaxBytes(n uint) TerminalOption {
	return func(c *terminalConfig) { c.continuationMax = n }
}

// WithWritePty receives the terminal's answers to queries in the stream.
// fn runs synchronously inside Write while the terminal is locked, so it
// must not call the terminal. It may keep the slice.
func WithWritePty(fn func([]byte)) TerminalOption {
	return func(c *terminalConfig) { c.writePty = fn }
}

// Terminal is one emulated terminal. It is safe for concurrent use.
type Terminal struct {
	mu     sync.Mutex
	inst   *ghostty.Instance
	failed error
}

// NewTerminal creates a terminal of cols x rows cells. It takes about
// 65 µs; there is nothing to set up beforehand.
func NewTerminal(cols, rows uint16, opts ...TerminalOption) (*Terminal, error) {
	if cols == 0 || rows == 0 {
		return nil, errSize
	}
	cfg := terminalConfig{
		memoryLimit:     DefaultMemoryLimit,
		scrollbackLines: DefaultScrollbackLines,
		scrollbackBytes: DefaultScrollbackBytes,
		continuationMax: DefaultContinuationMaxBytes,
	}
	for _, o := range opts {
		o(&cfg)
	}
	pages := min((cfg.memoryLimit+pageSize-1)/pageSize, 65536)
	in, err := ghostty.NewInstance(ghostty.Config{MemoryLimitPages: uint32(pages)})
	if err != nil {
		return nil, fmt.Errorf("vt: %w", err)
	}
	if err := configure(in, cols, rows, cfg); err != nil {
		_ = in.Close()
		return nil, fmt.Errorf("vt: %w", err)
	}
	return &Terminal{inst: in}, nil
}

func configure(in *ghostty.Instance, cols, rows uint16, cfg terminalConfig) error {
	if err := in.TerminalNew(cols, rows); err != nil {
		return err
	}
	for _, o := range []struct {
		opt ghostty.TerminalOption
		v   uint
	}{
		{ghostty.OptScrollbackMaxLines, cfg.scrollbackLines},
		{ghostty.OptScrollbackMaxBytes, cfg.scrollbackBytes},
		{ghostty.OptContinuationMaxBytes, cfg.continuationMax},
	} {
		if err := in.TerminalSetSize(o.opt, uint32(min(o.v, math.MaxUint32))); err != nil {
			return err
		}
	}
	if cfg.writePty != nil {
		return in.SetWritePty(cfg.writePty)
	}
	return nil
}

// do runs f under the lock. A trap marks the terminal failed, because the
// emulator's state can't be trusted after it. An allocation failure under
// the memory limit does not: libghostty reports it and stays consistent.
func (t *Terminal) do(f func(in *ghostty.Instance) error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failed != nil {
		return t.failed
	}
	if t.inst == nil {
		return ErrClosed
	}
	err := f(t.inst)
	switch {
	case errors.Is(err, ghostty.ErrTrap):
		t.failed = fmt.Errorf("%w: %w", ErrFailed, err)
		return t.failed
	case errors.Is(err, ghostty.OutOfMemory):
		return fmt.Errorf("%w: %w", ErrOutOfMemory, err)
	}
	return err
}

// Write feeds p to the emulator. It implements io.Writer.
func (t *Terminal) Write(p []byte) (int, error) {
	if err := t.do(func(in *ghostty.Instance) error { return in.VTWrite(p) }); err != nil {
		return 0, err
	}
	return len(p), nil
}

// WriteUntilGround feeds p only up to the byte at which the parser is at
// ground: outside any escape sequence and UTF-8 character. n is the number
// of bytes consumed and ground whether the parser is at ground after
// them. A parser already at ground consumes nothing.
func (t *Terminal) WriteUntilGround(p []byte) (n int, ground bool, err error) {
	err = t.do(func(in *ghostty.Instance) error {
		var err error
		n, ground, err = in.VTWriteUntilGround(p)
		return err
	})
	return n, ground, err
}

// Resize changes the size. The primary screen reflows soft-wrapped lines.
func (t *Terminal) Resize(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return errSize
	}
	return t.do(func(in *ghostty.Instance) error { return in.TerminalResize(cols, rows, 0, 0) })
}

// AltScreen reports whether the alternate screen is active.
func (t *Terminal) AltScreen() (bool, error) {
	var alt bool
	err := t.do(func(in *ghostty.Instance) error {
		v, err := in.GetU32(ghostty.DataActiveScreen)
		alt = int32(v) == ghostty.ScreenAlternate
		return err
	})
	return alt, err
}

// Mode reports a mode's current value. ansi selects ANSI modes (CSI n h)
// over DEC modes (CSI ? n h).
func (t *Terminal) Mode(mode uint16, ansi bool) (bool, error) {
	var v bool
	err := t.do(func(in *ghostty.Instance) error {
		var err error
		v, err = in.GetMode(ghostty.EncodeMode(mode, ansi))
		return err
	})
	return v, err
}

// SetMode sets a mode without a VT sequence and without its side effects,
// for example to turn synchronized output off before formatting.
func (t *Terminal) SetMode(mode uint16, ansi, value bool) error {
	return t.do(func(in *ghostty.Instance) error {
		return in.TerminalSetMode(ghostty.EncodeMode(mode, ansi), value)
	})
}

// Modes returns every mode the emulator knows.
func (t *Terminal) Modes() ([]ModeState, error) {
	var out []ModeState
	err := t.do(func(in *ghostty.Instance) error {
		out = make([]ModeState, 0, len(ghostty.Modes))
		for _, m := range ghostty.Modes {
			v, err := in.GetMode(m.Mode())
			if err != nil {
				return err
			}
			out = append(out, ModeState{Mode: m.Value, ANSI: m.ANSI, Value: v, Default: m.Default})
		}
		return nil
	})
	return out, err
}

// KittyKeyboardFlags returns the active kitty keyboard protocol flags.
func (t *Terminal) KittyKeyboardFlags() (uint8, error) {
	var v uint8
	err := t.do(func(in *ghostty.Instance) error {
		var err error
		v, err = in.GetU8(ghostty.DataKittyKeyboardFlags)
		return err
	})
	return v, err
}

// CursorStyle returns the cursor's DECSCUSR shape and blinking state.
func (t *Terminal) CursorStyle() (CursorStyle, error) {
	var s CursorStyle
	err := t.do(func(in *ghostty.Instance) error {
		v, blink, err := in.RenderStateCursor()
		s = CursorStyle{Shape: cursorShape(v), Blinking: blink}
		return err
	})
	return s, err
}

// CursorVisible reports DECTCEM.
func (t *Terminal) CursorVisible() (bool, error) {
	return t.getBool(ghostty.DataCursorVisible)
}

// MouseTracking returns the most inclusive mouse tracking mode that is on.
func (t *Terminal) MouseTracking() (MouseTracking, error) {
	mt := MouseNone
	err := t.do(func(in *ghostty.Instance) error {
		for _, c := range []struct {
			mode uint16
			mt   MouseTracking
		}{{1003, MouseAny}, {1002, MouseButton}, {1000, MouseNormal}, {9, MouseX10}} {
			on, err := in.GetMode(ghostty.EncodeMode(c.mode, false))
			if err != nil {
				return err
			}
			if on {
				mt = c.mt
				return nil
			}
		}
		return nil
	})
	return mt, err
}

// Title returns the window title set by OSC 0 or OSC 2.
func (t *Terminal) Title() (string, error) { return t.getString(ghostty.DataTitle) }

// Pwd returns the working directory as the program sent it (OSC 7 is a
// file:// URI).
func (t *Terminal) Pwd() (string, error) { return t.getString(ghostty.DataPwd) }

// AtGround reports whether the parser is outside any escape sequence and
// UTF-8 character.
func (t *Terminal) AtGround() (bool, error) { return t.getBool(ghostty.DataVTGround) }

// Continuation returns the bytes that recreate the unfinished escape
// sequence or UTF-8 character in another terminal. It is empty at ground.
func (t *Terminal) Continuation() ([]byte, error) {
	var out []byte
	err := t.do(func(in *ghostty.Instance) error {
		b, err := in.ContinuationAlloc()
		if errors.Is(err, ghostty.InvalidValue) {
			return ErrContinuationUnavailable
		}
		out = b
		return err
	})
	return out, err
}

// Close frees the terminal and its memory. It is idempotent.
func (t *Terminal) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.inst == nil {
		return nil
	}
	err := t.inst.Close()
	t.inst = nil
	return err
}

func (t *Terminal) getBool(d ghostty.TerminalData) (bool, error) {
	var v bool
	err := t.do(func(in *ghostty.Instance) error {
		var err error
		v, err = in.GetBool(d)
		return err
	})
	return v, err
}

func (t *Terminal) getString(d ghostty.TerminalData) (string, error) {
	var v string
	err := t.do(func(in *ghostty.Instance) error {
		var err error
		v, err = in.GetString(d)
		return err
	})
	return v, err
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./vt/... && CGO_ENABLED=1 go test -race ./vt/`
Expected: PASS, no race reports.

- [ ] **Step 7: Commit**

```bash
git add vt
git commit -m "Add vt package: library-neutral shadow terminal API"
```

---

### Task 9: Upgrade workflow and documentation

**Files:**
- Create: `.github/workflows/ghostty-wasm.yml`, `.github/scripts/ghostty-wasm-pr.sh`
- Create: `vt/internal/ghostty/README.md`
- Modify: `AGENTS.md`

**Interfaces:**
- Consumes: `make ghostty-wasm` and exit code 3 (Task 1), `BenchmarkVTWrite`, `FuzzVTWrite` (Task 7).
- Produces: nothing code depends on.

- [ ] **Step 1: Add the PR script**

`.github/scripts/ghostty-wasm-pr.sh` (then `chmod +x`):

```bash
#!/usr/bin/env bash
# Opens or updates the ghostty-wasm-update pull request. Never pushes to
# main.
#
# Env: GHOSTTY_OLD, GHOSTTY_NEW (commits), STATUS (pass|fail), CATEGORY
# (failure category), SIZE_OLD, GEN_SIZE_OLD, GH_TOKEN. Reads bench-old.txt,
# bench-new.txt and test-output.txt from the working directory when present.
set -euo pipefail

branch="ghostty-wasm-update"
label="ghostty-wasm-failing"
title="Update libghostty-vt to ${GHOSTTY_NEW:0:12}"
dir="vt/internal/ghostty/internal/wasmvt"
wasm="$dir/ghostty-vt.wasm"
gen="$dir/ghostty_vt.go"
size_new="$(stat -c %s "$wasm")"
gen_size_new="$(stat -c %s "$gen")"

git config user.name "github-actions[bot]"
git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
git checkout -B "$branch"
git add "$wasm" "$gen" "$dir/GHOSTTY_COMMIT"
# A failed build changes nothing; the PR still carries the failure report.
git commit --allow-empty -m "$title"
git push --force origin "$branch"

body="$(mktemp)"
{
  echo "Upstream: https://github.com/ghostty-org/ghostty/compare/${GHOSTTY_OLD}...${GHOSTTY_NEW}"
  echo
  echo "| | before | after |"
  echo "|---|---|---|"
  echo "| \`.wasm\` size | ${SIZE_OLD} | ${size_new} |"
  echo "| \`ghostty_vt.go\` size | ${GEN_SIZE_OLD} | ${gen_size_new} |"
  echo
  echo "## BenchmarkVTWrite"
  echo '```'
  echo "before:"; cat bench-old.txt 2>/dev/null || echo "(none)"
  echo "after:"; cat bench-new.txt 2>/dev/null || echo "(none)"
  echo '```'
  if [ "$STATUS" = "fail" ]; then
    echo
    echo "## Failing: ${CATEGORY}"
    echo '```'
    tail -c 60000 test-output.txt 2>/dev/null || true
    echo '```'
  fi
  echo
  echo "Opened by the ghostty-wasm workflow. See vt/internal/ghostty/README.md for what each failure category means."
  echo "\`ghostty_vt.go\` is generated by wasm2go: review the upstream range and the test results, not its diff."
  echo "PRs opened by this workflow don't trigger build.yml: close and reopen to run CI."
} > "$body"

number="$(gh pr list --head "$branch" --state open --json number --jq '.[0].number // empty')"
if [ -z "$number" ]; then
  draft=()
  [ "$STATUS" = "fail" ] && draft=(--draft)
  gh pr create --base main --head "$branch" --title "$title" --body-file "$body" "${draft[@]}"
  number="$(gh pr list --head "$branch" --state open --json number --jq '.[0].number')"
else
  gh pr edit "$number" --title "$title" --body-file "$body"
fi

if [ "$STATUS" = "fail" ]; then
  gh label create "$label" --color B60205 --force
  gh pr edit "$number" --add-label "$label"
  gh pr ready "$number" --undo || true
else
  gh pr edit "$number" --remove-label "$label" || true
  gh pr ready "$number" || true
fi
```

- [ ] **Step 2: Add the workflow**

`.github/workflows/ghostty-wasm.yml`:

```yaml
name: "ghostty-wasm"

on:
  workflow_dispatch:
    inputs:
      ghostty_commit:
        description: "ghostty commit to build (default: head of main)"
        required: false
        default: ""
  schedule:
    - cron: "17 4 * * 1"

permissions:
  contents: "write"
  pull-requests: "write"

defaults:
  run:
    shell: "bash"

jobs:
  update:
    runs-on: "ubuntu-latest"

    steps:
      - name: "Checkout repository"
        uses: "actions/checkout@v7"
        with:
          fetch-depth: 0

      - name: "Setup Golang"
        uses: "actions/setup-go@v7"
        with:
          go-version-file: "go.mod"

      - name: "Setup Zig"
        uses: "mlugg/setup-zig@v2"
        with:
          version: "0.16.0"

      - name: "Resolve ghostty commit"
        id: "resolve"
        env:
          WANT: "${{ inputs.ghostty_commit }}"
        run: |
          want="$WANT"
          if [ -z "$want" ]; then
            want="$(git ls-remote https://github.com/ghostty-org/ghostty.git refs/heads/main | cut -f1)"
          fi
          dir="vt/internal/ghostty/internal/wasmvt"
          current="$(sed -n 's/^commit=//p' "$dir/GHOSTTY_COMMIT")"
          echo "want=$want" >> "$GITHUB_OUTPUT"
          echo "current=$current" >> "$GITHUB_OUTPUT"
          echo "size=$(stat -c %s "$dir/ghostty-vt.wasm")" >> "$GITHUB_OUTPUT"
          echo "gen_size=$(stat -c %s "$dir/ghostty_vt.go")" >> "$GITHUB_OUTPUT"
          if [[ "$current" == "$want"* ]]; then
            echo "skip=true" >> "$GITHUB_OUTPUT"
          fi

      - name: "Benchmark current module"
        if: "steps.resolve.outputs.skip != 'true'"
        run: |
          go test -run '^$' -bench BenchmarkVTWrite -benchtime 3x -count 5 ./vt/internal/ghostty | tee bench-old.txt

      - name: "Build module"
        id: "build"
        if: "steps.resolve.outputs.skip != 'true'"
        run: |
          set +e
          make ghostty-wasm GHOSTTY_COMMIT="${{ steps.resolve.outputs.want }}" 2>&1 | tee test-output.txt
          code="${PIPESTATUS[0]}"
          set -e
          case "$code" in
            0)
              echo "status=pass" >> "$GITHUB_OUTPUT"
              # The script records the full sha even for a short input.
              echo "commit=$(sed -n 's/^commit=//p' vt/internal/ghostty/internal/wasmvt/GHOSTTY_COMMIT)" >> "$GITHUB_OUTPUT"
              ;;
            3) echo "status=fail" >> "$GITHUB_OUTPUT"; echo "category=patch does not apply" >> "$GITHUB_OUTPUT" ;;
            *) echo "status=fail" >> "$GITHUB_OUTPUT"; echo "category=build failed" >> "$GITHUB_OUTPUT" ;;
          esac

      - name: "Feature tests"
        id: "test"
        if: "steps.build.outputs.status == 'pass'"
        run: |
          set +e
          {
            go test ./vt/internal/ghostty/... &&
            go test -run '^$' -fuzz FuzzVTWrite -fuzztime 2m ./vt/internal/ghostty &&
            go test ./...
          } 2>&1 | tee test-output.txt
          code="${PIPESTATUS[0]}"
          set -e
          if [ "$code" = "0" ]; then
            echo "status=pass" >> "$GITHUB_OUTPUT"
          else
            echo "status=fail" >> "$GITHUB_OUTPUT"
            echo "category=feature tests failed" >> "$GITHUB_OUTPUT"
          fi

      - name: "Benchmark new module"
        if: "steps.test.outputs.status == 'pass'"
        run: |
          go test -run '^$' -bench BenchmarkVTWrite -benchtime 3x -count 5 ./vt/internal/ghostty | tee bench-new.txt

      - name: "Open or update pull request"
        if: "steps.resolve.outputs.skip != 'true'"
        env:
          GH_TOKEN: "${{ github.token }}"
          GHOSTTY_OLD: "${{ steps.resolve.outputs.current }}"
          GHOSTTY_NEW: "${{ steps.build.outputs.commit || steps.resolve.outputs.want }}"
          SIZE_OLD: "${{ steps.resolve.outputs.size }}"
          GEN_SIZE_OLD: "${{ steps.resolve.outputs.gen_size }}"
          STATUS: "${{ steps.test.outputs.status || steps.build.outputs.status }}"
          CATEGORY: "${{ steps.test.outputs.category || steps.build.outputs.category }}"
        run: ".github/scripts/ghostty-wasm-pr.sh"

      - name: "Fail on test failure"
        if: "steps.build.outputs.status == 'fail' || steps.test.outputs.status == 'fail'"
        run: "exit 1"
```

- [ ] **Step 3: Validate the workflow and script syntax**

Run: `bash -n .github/scripts/ghostty-wasm-pr.sh vt/internal/ghostty/build-wasm.sh && ruby -ryaml -e 'YAML.load_file(".github/workflows/ghostty-wasm.yml")' && echo ok`
Expected: `ok`.

- [ ] **Step 4: Write `vt/internal/ghostty/README.md`**

```markdown
# libghostty-vt (WebAssembly, translated to Go)

`internal/wasmvt/ghostty-vt.wasm` is libghostty-vt built for
`wasm32-freestanding` with `ReleaseSmall` and `-Dcpu=generic` at the commit
in `internal/wasmvt/GHOSTTY_COMMIT`, plus the patches in `patches/`.
`GHOSTTY_COMMIT` also records the Zig version and the sha256 of the
`.wasm`; `TestProvenance` fails if they don't match.

`internal/wasmvt/ghostty_vt.go` is wasm2go's translation of that `.wasm`,
generated by the `go:generate` line in `internal/wasmvt/doc.go` with the
wasm2go version pinned in `go.mod`. Never edit it.
`TestGeneratedCodeIsCurrent` regenerates it and fails on any difference.
The `.wasm` is not embedded in the binary; it is the input and the
provenance record.

`-Dcpu=generic` matters: without it ghostty enables `simd128`, and wasm2go
fails with `unsupported opcode (SIMD)`.

## Patches

| Patch | Why | Upstream |
|---|---|---|
| `0001-formatter-content-none.patch` | Extras-only formatting (`content_none`) | not yet proposed |
| `0002-formatter-origin-mode.patch` | Cursor position relative to the scroll region under DECOM | not yet proposed |

Delete a patch once an upstream commit includes it.

## Upgrading

Normally the `ghostty-wasm` workflow does this weekly and opens a PR on
the `ghostty-wasm-update` branch. By hand:

1. `make ghostty-wasm GHOSTTY_COMMIT=<sha>` (needs Zig 0.16). It also
   regenerates `ghostty_vt.go`.
2. `go test ./vt/internal/ghostty/...`. Failure categories:
   - **Compile error in this package**: an export was removed or changed
     its signature, or the module gained an import (`wasmvt.New` then
     takes arguments). Adapt `instance.go`.
   - **Layout or enum** (`TestABIMatchesTypeJSON`): update `abi.go`.
   - **Mode table** (`TestModeTableComplete`): update `Modes` in `abi.go`,
     then decide whether the overlay must reconcile the new mode.
   - **Round trip or getter**: a regression. Don't adopt the build, or
     adapt `vt` and document why.
   - **Golden diff** (`TestFormatGolden`): review the change, then rerun
     with `-update`.
   - **Known gap** (`TestKnownGaps`): upstream closed a gap. Remove
     vibepit's own emission of that state.
   - **Effects** (`TestQueryAnswers`): decide whether the new answer is
     safe to forward to the app while a prompt shows.
   - **Patch does not apply** (`build-wasm.sh` exits 3): rebase the patch,
     or drop it if upstream merged it.
3. `go test -run '^$' -fuzz FuzzVTWrite -fuzztime 5m ./vt/internal/ghostty`
4. `go test ./...`
5. Record `BenchmarkVTWrite` before and after, and the sizes of the
   `.wasm` and `ghostty_vt.go`, in the PR.

## Upgrading wasm2go

1. `go get -tool github.com/ncruces/wasm2go@<version>`
2. `go generate ./vt/internal/ghostty/internal/wasmvt`
3. Steps 2–5 above. If `limit.go` stops compiling, the generated code
   renamed its memory-limit field: follow the rename.

A dependency update that bumps wasm2go without step 2 fails
`TestGeneratedCodeIsCurrent`.

## Known limit

Runaway recursion in the module is a fatal Go stack overflow that no
`recover` catches, so it kills the process. The fuzz test is the guard. If
it ever triggers, report it upstream with the input.

## Building inside a vibepit sandbox

Zig's package fetcher can fail through the proxy with
`HttpConnectionClosing`. Download each missing dependency with `curl -LO`,
register it with `zig fetch <file>`, and build again until nothing is
missing. It needs github.com, deps.files.ghostty.org and codeberg.org.
```

- [ ] **Step 5: Update `AGENTS.md`**

Under `## Build, Run, and Test`, in the Make block, add:

```bash
make ghostty-wasm      # rebuild libghostty-vt .wasm and its Go translation (needs Zig 0.16)
```

Under `## Architecture`, after `### SSH key generation (\`keygen/\`)`, add:

```markdown
### Terminal emulator (`vt/`)

Shadow terminal emulator: parses a byte stream, reports terminal state, and
serializes it back to VT sequences. Backed by libghostty-vt compiled to
WebAssembly and translated to Go by wasm2go, so builds stay
`CGO_ENABLED=0` and need no WebAssembly runtime.
`vt/internal/ghostty/internal/wasmvt` holds the `.wasm` and the generated
`ghostty_vt.go` (never edit it); `vt/internal/ghostty` holds the C-ABI
bindings and the feature tests that pin libghostty behaviour; only `vt` may
import it. See `vt/internal/ghostty/README.md` before upgrading the module
or wasm2go.
```

Under `## CI and Release Notes`, change "Three CI workflows" to "Four CI workflows" and add:

```markdown
- `ghostty-wasm.yml` -- weekly and on demand: rebuilds the libghostty-vt
  `.wasm` at a newer ghostty commit, regenerates its Go translation, runs
  the feature tests, and opens or updates a PR on `ghostty-wasm-update`.
  Never pushes to `main`.
```

- [ ] **Step 6: Run the full verification**

Run: `make test && make test-integration && CGO_ENABLED=1 go test -race ./vt/... && gofmt -l vt`
Expected: all tests pass; `gofmt -l` prints nothing.

- [ ] **Step 7: Commit**

```bash
git add .github AGENTS.md vt/internal/ghostty/README.md
git commit -m "Add ghostty-wasm upgrade workflow and document the vt package"
```

---

## Manual follow-ups (not tasks; need the user)

- Propose `0001-formatter-content-none.patch` and `0002-formatter-origin-mode.patch` upstream (ghostty-org/ghostty). Opening PRs on another project is the user's call.
- Plan 2 (overlay, `--prompt`, selective port from `add/kitty-prompt`) starts from `vt` and copies `snapshotLeave`'s order.
