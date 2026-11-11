package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// TickInterval is the base tick rate for the TUI event loop.
const TickInterval = 250 * time.Millisecond

// TickMsg is sent on every tick interval. Screens receive it after Window
// increments the frame counter and expires flash messages.
type TickMsg struct{}

// Clock is the time source of a Window. The demo cast recorder replaces it to
// drive the window on virtual time.
type Clock interface {
	Now() time.Time
	// Tick returns a command that sends TickMsg after TickInterval.
	Tick() tea.Cmd
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

func (wallClock) Tick() tea.Cmd {
	return tea.Tick(TickInterval, func(time.Time) tea.Msg {
		return TickMsg{}
	})
}

// Window is the top-level tea.Model. It owns the shared frame (header,
// footer, sizing, tick, flash/error) and delegates content to the active Screen.
type Window struct {
	header    *HeaderInfo
	screen    Screen
	clock     Clock
	width     int
	height    int
	vpHeight  int
	tickFrame int
	flash     string
	flashExp  time.Time
	err       error
}

func NewWindow(header *HeaderInfo, screen Screen) *Window {
	return &Window{
		header: header,
		screen: screen,
		clock:  wallClock{},
	}
}

// Accessors for screens to read shared state.
func (w *Window) Width() int     { return w.width }
func (w *Window) Height() int    { return w.height }
func (w *Window) VpHeight() int  { return w.vpHeight }
func (w *Window) TickFrame() int { return w.tickFrame }
func (w *Window) Flash() string  { return w.flash }
func (w *Window) Err() error     { return w.err }

// Now returns the current time of the window's clock. Screens use it instead
// of time.Now so they follow a replaced clock.
func (w *Window) Now() time.Time { return w.clock.Now() }

// IntervalElapsed returns true if the given interval has elapsed since the last tick.
func (w *Window) IntervalElapsed(interval time.Duration) bool {
	n := int(interval / TickInterval)
	if n <= 1 {
		return true
	}
	return w.tickFrame%n == 0
}

// Mutators for screens to update shared state.
func (w *Window) SetFlash(msg string) {
	w.SetFlashFor(msg, 2*time.Second)
}

// SetFlashFor shows msg for d, for messages that take longer to read.
func (w *Window) SetFlashFor(msg string, d time.Duration) {
	w.flash = msg
	w.flashExp = w.Now().Add(d)
}
func (w *Window) SetHeader(info *HeaderInfo) {
	w.header = info
}
func (w *Window) SetClock(c Clock)   { w.clock = c }
func (w *Window) SetError(err error) { w.err = err }
func (w *Window) ClearError()        { w.err = nil }

func (w *Window) headerHeight() int {
	h := RenderHeader(w.header, w.width, w.height)
	return strings.Count(h, "\n") + 1
}

func (w *Window) Init() tea.Cmd {
	return w.clock.Tick()
}

func (w *Window) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.width = msg.Width
		w.height = msg.Height
		headerH := w.headerHeight()
		w.vpHeight = max(w.height-headerH-1, 1) // 1 footer line

	case TickMsg:
		w.tickFrame++
		if w.flash != "" && w.Now().After(w.flashExp) {
			w.flash = ""
		}
		cmds = append(cmds, w.clock.Tick())
	}

	// Forward all messages to the active screen.
	newScreen, cmd := w.screen.Update(msg, w)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	if newScreen != w.screen {
		w.screen = newScreen
		// Notify the new screen of the current terminal dimensions so it
		// can initialize its viewport height.
		sizeMsg := tea.WindowSizeMsg{Width: w.width, Height: w.height}
		newScreen2, cmd2 := w.screen.Update(sizeMsg, w)
		if cmd2 != nil {
			cmds = append(cmds, cmd2)
		}
		if newScreen2 != w.screen {
			w.screen = newScreen2
		}
	}

	return w, tea.Batch(cmds...)
}

func (w *Window) View() tea.View {
	view := tea.NewView("Starting...")
	view.AltScreen = true
	if w.width == 0 {
		return view
	}

	header := RenderHeader(w.header, w.width, w.height)
	content := w.screen.View(w)
	footer := w.renderFooter()

	view.SetContent(header + "\n" + content + "\n" + footer)
	return view
}

func (w *Window) renderFooter() string {
	keyStyle := lipgloss.NewStyle().Foreground(ColorCyan)
	descStyle := lipgloss.NewStyle().Foreground(ColorField)

	// Left: screen indicator + window status (error > flash)
	screenStatus := w.screen.FooterStatus(w)
	var windowStatus string
	if w.err != nil {
		windowStatus = lipgloss.NewStyle().Foreground(ColorError).
			Render(fmt.Sprintf("error: %v", w.err))
	} else if w.flash != "" && w.Now().Before(w.flashExp) {
		windowStatus = lipgloss.NewStyle().Foreground(ColorOrange).Render(w.flash)
	}

	var left string
	if screenStatus != "" && windowStatus != "" {
		left = screenStatus + " " + windowStatus
	} else {
		left = screenStatus + windowStatus
	}

	// Right: screen keys + base keys. Screens list their most important keys
	// first, so drop from the end until the line fits and "q quit" stays visible.
	var keys []string
	for _, fk := range w.screen.FooterKeys(w) {
		keys = append(keys, keyStyle.Render(fk.Key)+" "+descStyle.Render(fk.Desc))
	}
	quit := keyStyle.Render("q") + " " + descStyle.Render("quit")

	leftWidth := ansi.StringWidth(left)
	right := strings.Join(append(keys, quit), "  ")
	for len(keys) > 0 && leftWidth+2+ansi.StringWidth(right) > w.width {
		keys = keys[:len(keys)-1]
		right = strings.Join(append(keys, quit), "  ")
	}
	rightWidth := ansi.StringWidth(right)
	gap := max(w.width-leftWidth-rightWidth, 2)

	line := left + strings.Repeat(" ", gap) + right
	return ansi.Truncate(line, w.width, "")
}
