# Monitor Header Graphic Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a cyberpunk block-art "VIBEPIT" header to the monitor command, styled with the project's theme colors.

**Architecture:** A new `cmd/logo.go` file renders the header as a styled string using lipgloss. The monitor command calls it once after session discovery, before the log polling loop. The header is a 3-row block-character wordmark with a cyan-to-purple gradient, an orange tagline, diagonal line fields, and right-aligned session metadata.

**Tech Stack:** `github.com/charmbracelet/lipgloss` (already in go.mod as indirect dep), `github.com/charmbracelet/x/ansi` (already in go.mod), Go standard library `os` for terminal width.

---

### Task 1: Create block-art letter definitions

**Files:**
- Create: `cmd/logo.go`

**Step 1: Create `cmd/logo.go` with letter glyphs**

Each letter is defined as 3 strings (top/mid/bot rows). Use Unicode half-block characters (`▄`, `▀`, `█`) and spaces. Letters are ~5 columns wide with 1 column spacing.

```go
package cmd

// letterGlyph holds the three rows of a block-art character.
type letterGlyph struct {
	Top string
	Mid string
	Bot string
}

// glyphs maps rune to its block-art representation.
// Each glyph is 3 rows tall, designed for the VIBEPIT wordmark.
var glyphs = map[rune]letterGlyph{
	'V': {
		Top: `█   █`,
		Mid: `▀▄ ▄▀`,
		Bot: ` ▀█▀ `,
	},
	'I': {
		Top: `▀█▀`,
		Mid: ` █ `,
		Bot: `▄█▄`,
	},
	'B': {
		Top: `█▀▀▄`,
		Mid: `█▄▄▀`,
		Bot: `█▄▄▀`,
	},
	'E': {
		Top: `█▀▀▀`,
		Mid: `█▄▄ `,
		Bot: `█▄▄▄`,
	},
	'P': {
		Top: `█▀▀▄`,
		Mid: `█▄▄▀`,
		Bot: `█   `,
	},
	'T': {
		Top: `▀▀█▀▀`,
		Mid: `  █  `,
		Bot: `  █  `,
	},
}
```

**Step 2: Run `go vet ./cmd/...`**

Expected: passes (file compiles).

**Step 3: Commit**

```bash
git add cmd/logo.go
git commit -m "feat: add block-art letter glyphs for VIBEPIT wordmark"
```

---

### Task 2: Build the header renderer

**Files:**
- Modify: `cmd/logo.go`

**Step 1: Write test for `RenderHeader`**

Create `cmd/logo_test.go` with a basic test that verifies the header contains the wordmark text and session info.

```go
package cmd

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRenderHeader_ContainsWordmark(t *testing.T) {
	header := RenderHeader(&SessionInfo{
		SessionID:  "abc123",
		ProjectDir: "/home/user/myproject",
	}, 80)

	// The raw text (ignoring ANSI codes) should contain block chars from the glyphs.
	// Just verify it has multiple lines and contains the tagline.
	lines := strings.Split(header, "\n")
	assert.GreaterOrEqual(t, len(lines), 3, "header should have at least 3 lines")

	assert.Contains(t, header, "I pity the vibes")
}

func TestRenderHeader_ContainsSessionInfo(t *testing.T) {
	header := RenderHeader(&SessionInfo{
		SessionID:  "abc123",
		ProjectDir: "/home/user/myproject",
	}, 120)

	assert.Contains(t, header, "abc123")
	assert.Contains(t, header, "myproject")
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./cmd/ -run TestRenderHeader -v
```

Expected: FAIL — `RenderHeader` undefined.

**Step 3: Implement `RenderHeader`**

Add to `cmd/logo.go`:

```go
import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Theme colors from docs/THEME.md
var (
	colorCyan    = lipgloss.Color("#00d4ff")
	colorPurple  = lipgloss.Color("#8b5cf6")
	colorOrange  = lipgloss.Color("#f97316")
	colorField   = lipgloss.Color("#1a2744")
	colorMuted   = lipgloss.Color("#4a90a4")
)

// buildWordmark assembles the 3-row block text for a given word.
func buildWordmark(word string) [3]string {
	var rows [3]string
	for i, ch := range word {
		g, ok := glyphs[ch]
		if !ok {
			continue
		}
		if i > 0 {
			rows[0] += " "
			rows[1] += " "
			rows[2] += " "
		}
		rows[0] += g.Top
		rows[1] += g.Mid
		rows[2] += g.Bot
	}
	return rows
}

// applyGradient colors a string with a linear gradient from colorA to colorB.
func applyGradient(s string, colorA, colorB lipgloss.Color) string {
	runes := []rune(s)
	n := len(runes)
	if n == 0 {
		return s
	}

	aR, aG, aB, _ := lipgloss.ColorProfile().Convert(colorA).RGBA()
	bR, bG, bB, _ := lipgloss.ColorProfile().Convert(colorB).RGBA()

	var out strings.Builder
	for i, r := range runes {
		if r == ' ' {
			out.WriteRune(r)
			continue
		}
		t := float64(i) / float64(max(n-1, 1))
		cr := uint8(float64(aR>>8)*(1-t) + float64(bR>>8)*t)
		cg := uint8(float64(aG>>8)*(1-t) + float64(bG>>8)*t)
		cb := uint8(float64(aB>>8)*(1-t) + float64(bB>>8)*t)
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", cr, cg, cb)))
		out.WriteString(style.Render(string(r)))
	}
	return out.String()
}

// RenderHeader produces the styled monitor header with wordmark, tagline,
// session info, and diagonal line field.
func RenderHeader(session *SessionInfo, width int) string {
	if width < 40 {
		width = 40
	}

	rows := buildWordmark("VIBEPIT")
	wordmarkWidth := ansi.StringWidth(rows[0])

	// Style the tagline
	tagline := lipgloss.NewStyle().Foreground(colorOrange).Italic(true).Render("I pity the vibes.")

	// Session info: right-aligned
	projectName := session.ProjectDir
	if idx := strings.LastIndex(projectName, "/"); idx >= 0 {
		projectName = projectName[idx+1:]
	}
	sessionInfo := lipgloss.NewStyle().Foreground(colorMuted).Render(
		fmt.Sprintf("%s  %s", session.SessionID[:min(len(session.SessionID), 8)], projectName),
	)

	// Build diagonal line field to fill remaining width on each wordmark row
	fieldChar := lipgloss.NewStyle().Foreground(colorField).Render("╱")

	var lines []string
	for i := 0; i < 3; i++ {
		coloredRow := applyGradient(rows[i], colorCyan, colorPurple)
		remaining := width - wordmarkWidth - 2 // 2 for spacing
		if remaining < 0 {
			remaining = 0
		}
		field := strings.Repeat(fieldChar, remaining)
		lines = append(lines, coloredRow+"  "+field)
	}

	// Tagline row with session info right-aligned
	taglineWidth := ansi.StringWidth(tagline)
	sessionWidth := ansi.StringWidth(sessionInfo)
	gap := width - taglineWidth - sessionWidth
	if gap < 2 {
		gap = 2
	}
	lines = append(lines, tagline+strings.Repeat(" ", gap)+sessionInfo)

	return strings.Join(lines, "\n")
}
```

Note: The gradient implementation uses lipgloss color profile conversion. If that turns out to be awkward with the lipgloss v1 API, fall back to manual hex parsing. The key idea: iterate over runes, interpolate RGB, apply per-character foreground style.

**Step 4: Run tests**

```bash
go test ./cmd/ -run TestRenderHeader -v
```

Expected: PASS. If gradient color conversion API differs, adjust to parse hex directly.

**Step 5: Visual test — run manually**

Add a temporary `TestRenderHeader_Print` that prints to stdout so you can eyeball it:

```go
func TestRenderHeader_Print(t *testing.T) {
	t.Skip("visual check only — run with: go test ./cmd/ -run TestRenderHeader_Print -v -count=1")
	fmt.Println(RenderHeader(&SessionInfo{
		SessionID:  "a1b2c3d4e5f6",
		ProjectDir: "/home/user/myproject",
	}, 100))
}
```

**Step 6: Commit**

```bash
git add cmd/logo.go cmd/logo_test.go
git commit -m "feat: implement RenderHeader with gradient wordmark and session info"
```

---

### Task 3: Integrate header into monitor command

**Files:**
- Modify: `cmd/monitor.go:35`

**Step 1: Replace the plain "Connecting to proxy..." with the header**

In `cmd/monitor.go`, after the `client` is created (line 34), replace:

```go
fmt.Println("Connecting to proxy...")
```

with:

```go
width, _, _ := term.GetSize(os.Stdout.Fd())
if width <= 0 {
    width = 80
}
fmt.Println(RenderHeader(session, width))
fmt.Println()
```

Add the necessary imports: `"os"` and `"golang.org/x/term"` (or `"github.com/charmbracelet/x/term"`).

Check which term package is available — the project already has `github.com/charmbracelet/x/term` in go.mod. Use that.

**Step 2: Build and verify**

```bash
go vet ./cmd/...
go build ./...
```

Expected: compiles without errors.

**Step 3: Commit**

```bash
git add cmd/monitor.go
git commit -m "feat: show header graphic when monitor connects"
```

---

### Task 4: Polish — tune letter shapes and test visually

**Files:**
- Modify: `cmd/logo.go` (glyphs)

**Step 1: Run monitor against a live session (or use the visual test)**

Unskip the visual test temporarily:

```bash
go test ./cmd/ -run TestRenderHeader_Print -v -count=1
```

**Step 2: Adjust glyph shapes as needed**

The block-art letters in Task 1 are a starting point. Tune widths and shapes so:
- All letters have consistent visual height
- Spacing between letters is even
- The gradient reads smoothly left-to-right

**Step 3: Verify no test regressions**

```bash
go test ./cmd/ -v
```

Expected: all pass.

**Step 4: Commit**

```bash
git add cmd/logo.go
git commit -m "fix: polish block-art letter shapes"
```
