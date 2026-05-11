package ward

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/bernd/vibepit/tui"
	"github.com/charmbracelet/x/ansi"
)

// DefaultTimeout is the default alert display duration.
const DefaultTimeout = 3 * time.Second

// KeyHint defines an action key shown on the bar during command mode.
type KeyHint struct {
	Key          byte
	Desc         string
	RequireAlert bool
}

// StatusUpdate carries a message, alert flag, and display timeout for the status bar.
type StatusUpdate struct {
	Message  string
	Alert    bool
	Timeout  time.Duration
	Target   string
	KeyHints []KeyHint
}

// barMode represents the display mode of the status bar.
type barMode int

const (
	barHidden barMode = iota
	barStatus
	barAlert
	barCleared
	barCommand
)

type barEventKind int

const (
	barEventStatus barEventKind = iota
	barEventAlert
	barEventDismiss
	barEventEnterCommand
	barEventBeginAction
	barEventAction
	barEventCancelCommand
)

type barEvent struct {
	kind   barEventKind
	update StatusUpdate

	// Command mode fields
	gen    uint64
	respCh chan<- commandResponse // barEventEnterCommand
	ackCh  chan<- bool            // barEventBeginAction
	result actionResult           // barEventAction
}

type barCache struct {
	rendered string
	message  string
	mode     barMode
	target   string
	keyHints []KeyHint
}

var (
	defaultBarStyle = lipgloss.NewStyle().Foreground(tui.ColorCyan)
	alertBarStyle   = lipgloss.NewStyle().Bold(true).Foreground(tui.ColorOrange)

	padChar      = "╱"
	tailChar     = "…"
	gradientName = tui.RenderNameWithGradient()
	prefix       = defaultBarStyle.Render(strings.Repeat(padChar, 3)) + " " + gradientName
	prefixLen    = ansi.StringWidth(prefix)
)

// RenderStatusBar renders a full-width status bar with the given message.
// alert selects the orange alert style; otherwise the cyan default style is used.
func RenderStatusBar(message string, cols int, alert bool) string {
	msg := " " + sanitizeMessage(message) + " "
	style := defaultBarStyle
	if alert {
		style = alertBarStyle
		msg = " " + padChar + padChar + " ALERT:" + msg + padChar + padChar + " press ctrl-] "
	}

	return renderMessage(style, cols, msg)
}

// RenderCommandBar renders the command mode bar with key hints and optional target.
// hasAlert controls whether RequireAlert hints are shown.
func RenderCommandBar(target string, hints []KeyHint, cols int, hasAlert bool) string {
	var parts []string
	if target != "" {
		parts = append(parts, sanitizeMessage(target))
	}
	for _, h := range hints {
		if h.RequireAlert && !hasAlert {
			continue
		}
		parts = append(parts, fmt.Sprintf("[%s] %s", string(h.Key), sanitizeMessage(h.Desc)))
	}
	parts = append(parts, "[esc] cancel")
	msg := " " + strings.Join(parts, "  ") + " "

	return renderMessage(alertBarStyle, cols, msg)
}

// renderMessage renders a styled message into the bar, truncating or filling to fit cols.
func renderMessage(style lipgloss.Style, cols int, msg string) string {
	msgWidth := ansi.StringWidth(msg)
	if (cols - prefixLen) < msgWidth {
		return prefix + style.Render(ansi.Truncate(msg, max(cols-prefixLen, 0), tailChar))
	}
	fill := max(cols-prefixLen-msgWidth, 0) + 1
	return prefix + style.Render(msg) + style.Render(strings.Repeat(padChar, fill))
}

// sanitizeMessage strips control characters (C0, C1, and DEL) from a message
// to prevent terminal escape injection.
func sanitizeMessage(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\t' || (r >= ' ' && r != 0x7F && (r < 0x80 || r > 0x9F)) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
