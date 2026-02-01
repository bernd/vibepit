# Compact TUI Header Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Collapse the TUI header to a single styled line when terminal height is below 15 lines.

**Architecture:** `RenderHeader` gains a `height` parameter to choose between the existing 3-row wordmark and a new single-line compact layout. The compact line preserves all info (gradient "VIBEPIT", orange italic tagline, session info) with `╱` field chars filling the gap. `Window` passes its height through to the render call.

**Tech Stack:** Go, lipgloss, charmbracelet/x/ansi, testify

---

### Task 1: Add compact header rendering

**Files:**
- Modify: `tui/header.go` (RenderHeader signature + new compact path)
- Test: `tui/header_test.go`

**Step 1: Write the failing tests**

Add to `tui/header_test.go`:

```go
func TestRenderHeader_CompactWhenShort(t *testing.T) {
	header := tui.RenderHeader(&tui.HeaderInfo{
		SessionID:  "abc123",
		ProjectDir: "myproject",
	}, 80, 10)

	lines := strings.Split(strings.TrimPrefix(header, "\n"), "\n")
	assert.Equal(t, 1, len(lines), "compact header should be a single line")
	assert.Contains(t, header, "VIBEPIT")
	assert.Contains(t, header, "I PITY THE VIBES")
	assert.Contains(t, header, "abc123")
	assert.Contains(t, header, "myproject")
}

func TestRenderHeader_FullWhenTall(t *testing.T) {
	header := tui.RenderHeader(&tui.HeaderInfo{
		SessionID:  "abc123",
		ProjectDir: "myproject",
	}, 80, 30)

	lines := strings.Split(strings.TrimPrefix(header, "\n"), "\n")
	assert.GreaterOrEqual(t, len(lines), 4, "full header should have at least 4 lines")
}

func TestRenderHeader_CompactAtThreshold(t *testing.T) {
	// height=14 should be compact
	compact := tui.RenderHeader(&tui.HeaderInfo{
		SessionID:  "abc123",
		ProjectDir: "myproject",
	}, 80, 14)
	compactLines := strings.Split(strings.TrimPrefix(compact, "\n"), "\n")
	assert.Equal(t, 1, len(compactLines))

	// height=15 should be full
	full := tui.RenderHeader(&tui.HeaderInfo{
		SessionID:  "abc123",
		ProjectDir: "myproject",
	}, 80, 15)
	fullLines := strings.Split(strings.TrimPrefix(full, "\n"), "\n")
	assert.GreaterOrEqual(t, len(fullLines), 4)
}

func TestRenderHeader_CompactFieldFill(t *testing.T) {
	header := tui.RenderHeader(&tui.HeaderInfo{
		SessionID:  "x",
		ProjectDir: "p",
	}, 80, 10)

	// The line should span the full width with field chars filling the gap.
	// Just verify it contains multiple consecutive field chars in the middle.
	assert.Contains(t, header, "╱╱╱")
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./tui/ -run "TestRenderHeader_Compact|TestRenderHeader_FullWhenTall" -v`
Expected: compilation error — `RenderHeader` doesn't accept a height parameter yet.

**Step 3: Update the RenderHeader signature and add compact path**

In `tui/header.go`, change the `RenderHeader` signature to accept height and add the compact rendering branch:

```go
func RenderHeader(info *HeaderInfo, width int, height int) string {
	if width < 40 {
		width = 40
	}

	if height > 0 && height < 15 {
		return renderCompactHeader(info, width)
	}

	// ... existing full header code unchanged ...
}
```

Add the new `renderCompactHeader` function:

```go
// renderCompactHeader produces a single-line header with gradient wordmark,
// orange tagline, and session info separated by diagonal field characters.
func renderCompactHeader(info *HeaderInfo, width int) string {
	fieldChar := lipgloss.NewStyle().Foreground(ColorField).Render("╱")

	name := applyGradient("VIBEPIT", ColorCyan, ColorPurple)
	tagline := lipgloss.NewStyle().Foreground(ColorOrange).Italic(true).Render("I PITY THE VIBES")
	sessionInfo := lipgloss.NewStyle().Foreground(ColorField).Render(
		fmt.Sprintf("%s ╱╱ %s", info.ProjectDir, info.SessionID),
	)

	leftPad := strings.Repeat(fieldChar, 3)
	rightPad := strings.Repeat(fieldChar, 3)

	// Fixed structure: "╱╱╱ VIBEPIT  tagline ╱...╱ session ╱╱╱"
	fixedParts := 3 + 1 + 7 + 2 + 17 + 1 + 1 + ansi.StringWidth(sessionInfo) + 1 + 3
	fill := width - fixedParts
	if fill < 1 {
		fill = 1
	}
	fieldFill := strings.Repeat(fieldChar, fill)

	line := leftPad + " " + name + "  " + tagline + " " + fieldFill + " " + sessionInfo + " " + rightPad

	return "\n" + line
}
```

**Step 4: Fix existing callers and tests**

Update the two existing tests in `tui/header_test.go` to pass a tall height (e.g. `30`) as the third argument so they keep testing the full header:

- `TestRenderHeader_ContainsWordmark`: change `tui.RenderHeader(..., 80)` to `tui.RenderHeader(..., 80, 30)`
- `TestRenderHeader_ContainsSessionInfo`: change `tui.RenderHeader(..., 120)` to `tui.RenderHeader(..., 120, 30)`
- `TestRenderHeader_Print`: change `tui.RenderHeader(..., 100)` to `tui.RenderHeader(..., 100, 30)`

**Step 5: Run all header tests**

Run: `go test ./tui/ -run TestRenderHeader -v`
Expected: all tests PASS.

**Step 6: Commit**

```
feat: add compact single-line header for small terminals
```

---

### Task 2: Wire height through Window

**Files:**
- Modify: `tui/window.go` (pass height to RenderHeader)

**Step 1: Update headerHeight()**

In `tui/window.go`, change `headerHeight()` to pass the terminal height:

```go
func (w *Window) headerHeight() int {
	h := RenderHeader(w.header, w.width, w.height)
	return strings.Count(h, "\n") + 1
}
```

**Step 2: Update View()**

In `tui/window.go`, change the `View()` method:

```go
header := RenderHeader(w.header, w.width, w.height)
```

**Step 3: Build and verify**

Run: `go build ./...`
Expected: compiles with no errors. No other callers of `RenderHeader` exist outside the tui package and tests.

**Step 4: Run full test suite**

Run: `go test ./... -count=1`
Expected: all tests PASS.

**Step 5: Commit**

```
feat: wire terminal height to header rendering
```

---

### Task 3: Add visual check for compact header

**Files:**
- Modify: `tui/header_test.go`

**Step 1: Add a visual print test for compact mode**

Add to `tui/header_test.go`:

```go
func TestRenderHeader_PrintCompact(t *testing.T) {
	t.Skip("visual check only — run with: go test ./tui/ -run TestRenderHeader_PrintCompact -v -count=1")
	fmt.Println(tui.RenderHeader(&tui.HeaderInfo{
		SessionID:  "a1b2c3d4e5f6",
		ProjectDir: "/home/user/myproject",
	}, 100, 10))
}
```

**Step 2: Run the visual check manually**

Run: `go test ./tui/ -run TestRenderHeader_PrintCompact -v -count=1 -skip "^$"`
Expected: prints the compact header to terminal for visual verification.

**Step 3: Commit**

```
test: add visual check for compact header
```
