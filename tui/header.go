package tui

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

// Theme colors
var (
	ColorCyan      = lipgloss.Color("#00d4ff")
	ColorPurple    = lipgloss.Color("#8b5cf6")
	ColorOrange    = lipgloss.Color("#f97316")
	ColorField     = lipgloss.Color("#0099cc")
	ColorError     = ColorPurple // lipgloss.Color("#ef4444") - this one is too similar to the orange
	ColorHighlight = lipgloss.Color("#1e2d3d")
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

// CompactHeaderThreshold is the terminal height below which the header
// collapses to a single line.
const CompactHeaderThreshold = 15

const defaultBannerWidth = 80

type HeaderInfo struct {
	ProjectDir string
	SessionID  string
}

// ProjectDirWithHome replaces the home directory path in the project dir
// with "$HOME" to shorten the value.
func (info HeaderInfo) ProjectDirWithHome() string {
	home := os.Getenv("HOME")
	if home != "" && strings.HasPrefix(info.ProjectDir, home) {
		return fmt.Sprintf("$HOME%s", strings.TrimPrefix(info.ProjectDir, home))
	}
	return info.ProjectDir
}

// renderCompactHeader produces a single-line header with gradient wordmark,
// orange tagline, and session info separated by diagonal field characters.
func renderCompactHeader(info *HeaderInfo, width int) string {
	fieldChar := lipgloss.NewStyle().Foreground(ColorField).Render("╱")

	name := applyGradient("VIBEPIT", ColorCyan, ColorPurple)
	tagline := lipgloss.NewStyle().Foreground(ColorOrange).Italic(true).Render("I pity the vibes")
	sessionInfo := lipgloss.NewStyle().Foreground(ColorField).Render(
		fmt.Sprintf("%s ╱╱ %s", info.ProjectDirWithHome(), info.SessionID))

	leftPad := strings.Repeat(fieldChar, 3)
	rightPad := strings.Repeat(fieldChar, 3)

	// Fixed structure: "╱╱╱ VIBEPIT  tagline ╱...╱ session ╱╱╱"
	// Calculate fill based on visual widths
	nameWidth := ansi.StringWidth("VIBEPIT")
	taglineWidth := ansi.StringWidth("I PITY THE VIBES")
	fixedWidth := 3 + 1 + nameWidth + 2 + taglineWidth + 1 + 1 + ansi.StringWidth(sessionInfo) + 1 + 3
	fill := width - fixedWidth
	if fill < 1 {
		fill = 1
	}
	fieldFill := strings.Repeat(fieldChar, fill)

	line := leftPad + " " + name + "  " + tagline + " " + fieldFill + " " + sessionInfo + " " + rightPad

	return line
}

// PrintHeader prints a branding header (wordmark + tagline) to stdout.
// It detects terminal size and uses the compact layout on short terminals.
func PrintHeader() {
	width, height, err := term.GetSize(int(os.Stdout.Fd()))
	width, height = normalizeBannerSize(width, height, err)
	writeBanner(os.Stdout, width, height)
}

func writeBanner(w io.Writer, width, height int) {
	fmt.Fprintln(w, RenderBanner(width, height))
	fmt.Fprintln(w)
}

func normalizeBannerSize(width int, height int, err error) (int, int) {
	if err != nil || width <= 0 {
		width = defaultBannerWidth
	}
	if err != nil || height <= 0 {
		height = 0
	}
	return width, height
}

// RenderBanner produces a branding-only header string (wordmark + tagline,
// no session info). It uses the compact layout when height < CompactHeaderThreshold.
func RenderBanner(width, height int) string {
	if width < 40 {
		width = 40
	}
	if height > 0 && height < CompactHeaderThreshold {
		return renderCompactBanner(width)
	}
	return renderFullBanner(width)
}

// renderCompactBanner produces a single-line branding banner without session info.
func renderCompactBanner(width int) string {
	fieldChar := lipgloss.NewStyle().Foreground(ColorField).Render("╱")

	name := applyGradient("VIBEPIT", ColorCyan, ColorPurple)
	tagline := lipgloss.NewStyle().Foreground(ColorOrange).Italic(true).Render("I pity the vibes")

	leftPad := strings.Repeat(fieldChar, 3)
	rightPad := strings.Repeat(fieldChar, 3)

	nameWidth := ansi.StringWidth("VIBEPIT")
	taglineWidth := ansi.StringWidth("I pity the vibes")
	fixedWidth := 3 + 1 + nameWidth + 2 + taglineWidth + 1 + 3
	fill := width - fixedWidth
	if fill < 1 {
		fill = 1
	}
	fieldFill := strings.Repeat(fieldChar, fill)

	return leftPad + " " + name + "  " + tagline + " " + fieldFill + rightPad
}

// renderFullBanner produces the 3-row block-art wordmark with tagline, no session info.
func renderFullBanner(width int) string {
	rows := buildWordmark("VIBEPIT")
	wordmarkWidth := ansi.StringWidth(rows[0])

	tagline := lipgloss.NewStyle().Foreground(ColorOrange).Italic(true).Render("I PITY THE VIBES")

	fieldChar := lipgloss.NewStyle().Foreground(ColorField).Render("╱")
	leftFieldCharLen := 3
	leftPadLen := leftFieldCharLen + 2

	var lines []string
	for i := 0; i < 3; i++ {
		coloredRow := applyGradient(rows[i], ColorCyan, ColorPurple)
		leftPad := strings.Repeat(fieldChar, leftFieldCharLen)
		remaining := width - wordmarkWidth - leftPadLen
		if remaining < 0 {
			remaining = 0
		}
		field := strings.Repeat(fieldChar, remaining)
		lines = append(lines, leftPad+coloredRow+"  "+field)
	}

	lines = append(lines, strings.Repeat(" ", leftPadLen)+tagline)

	return strings.Join(lines, "\n")
}

// RenderHeader produces the styled monitor header with wordmark, tagline,
// session info, and diagonal line field.
func RenderHeader(info *HeaderInfo, width int, height int) string {
	if width < 40 {
		width = 40
	}

	if height > 0 && height < CompactHeaderThreshold {
		return renderCompactHeader(info, width)
	}

	rows := buildWordmark("VIBEPIT")
	wordmarkWidth := ansi.StringWidth(rows[0])

	tagline := lipgloss.NewStyle().Foreground(ColorOrange).Italic(true).Render("I PITY THE VIBES")

	sessionInfo := lipgloss.NewStyle().Foreground(ColorField).Render(
		fmt.Sprintf("%s ╱╱ %s", info.ProjectDirWithHome(), info.SessionID),
	)

	fieldChar := lipgloss.NewStyle().Foreground(ColorField).Render("╱")
	leftFieldCharLen := 3              // left field chars
	leftPadLen := leftFieldCharLen + 2 // spacing

	var lines []string
	for i := 0; i < 3; i++ {
		coloredRow := applyGradient(rows[i], ColorCyan, ColorPurple)
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

	return strings.Join(lines, "\n")
}
