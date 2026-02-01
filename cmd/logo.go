package cmd

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Theme colors
var (
	colorCyan      = lipgloss.Color("#00d4ff")
	colorPurple    = lipgloss.Color("#8b5cf6")
	colorOrange    = lipgloss.Color("#f97316")
	colorField     = lipgloss.Color("#0099cc")
	colorError     = colorPurple // lipgloss.Color("#ef4444") - this one is too similar to the orange
	colorHighlight = lipgloss.Color("#1e2d3d")
)

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
		rows[0] += "  " + g.Top
		rows[1] += "  " + g.Mid
		rows[2] += "  " + g.Bot
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

	aR, aG, aB, _ := colorA.RGBA()
	bR, bG, bB, _ := colorB.RGBA()

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

	tagline := lipgloss.NewStyle().Foreground(colorOrange).Italic(true).Render("I PITY THE VIBES")

	sessionInfo := lipgloss.NewStyle().Foreground(colorField).Render(
		fmt.Sprintf("%s ╱╱ %s", session.ProjectDir, session.SessionID),
	)

	fieldChar := lipgloss.NewStyle().Foreground(colorField).Render("╱")
	leftFieldCharLen := 3              // left field chars
	leftPadLen := leftFieldCharLen + 2 // spacing

	var lines []string
	for i := 0; i < 3; i++ {
		coloredRow := applyGradient(rows[i], colorCyan, colorPurple)
		leftPad := strings.Repeat(fieldChar, leftFieldCharLen)
		remaining := width - wordmarkWidth - leftPadLen
		if remaining < 0 {
			remaining = 0
		}
		field := strings.Repeat(fieldChar, remaining)
		lines = append(lines, leftPad+coloredRow+"  "+field)
	}

	taglineWidth := ansi.StringWidth(tagline)
	sessionWidth := ansi.StringWidth(sessionInfo)
	gap := width - leftPadLen - taglineWidth - sessionWidth
	if gap < 2 {
		gap = 2
	}
	lines = append(lines, strings.Repeat(" ", leftPadLen)+tagline+strings.Repeat(" ", gap)+sessionInfo)

	return "\n" + strings.Join(lines, "\n")
}
