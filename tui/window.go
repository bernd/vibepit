package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	tickInterval = 250 * time.Millisecond
)

// TickMsg is sent on every tick interval. Screens receive it after Window
// increments the frame counter and expires flash messages.
type TickMsg struct{}

// Window is the top-level tea.Model. It owns the shared frame (header,
// footer, sizing, tick, flash/error) and delegates content to the active Screen.
type Window struct {
	header    *HeaderInfo
	screen    Screen
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
	}
}

// Accessors for screens to read shared state.
func (w *Window) Width() int     { return w.width }
func (w *Window) Height() int    { return w.height }
func (w *Window) VpHeight() int  { return w.vpHeight }
func (w *Window) TickFrame() int { return w.tickFrame }
func (w *Window) Flash() string  { return w.flash }
func (w *Window) Err() error     { return w.err }

// IntervalElapsed returns true if the given interval has elapsed since the last tick.
func (w *Window) IntervalElapsed(interval time.Duration) bool {
	n := int(interval / tickInterval)
	if n <= 1 {
		return true
	}
	return w.tickFrame%n == 0
}

// Mutators for screens to update shared state.
func (w *Window) SetFlash(msg string) {
	w.flash = msg
	w.flashExp = time.Now().Add(2 * time.Second)
}
func (w *Window) SetHeader(info *HeaderInfo) {
	w.header = info
}
func (w *Window) SetError(err error) { w.err = err }
func (w *Window) ClearError()        { w.err = nil }

func (w *Window) headerHeight() int {
	h := RenderHeader(w.header, w.width, w.height)
	return strings.Count(h, "\n") + 1
}

func doTick() tea.Cmd {
	return tea.Tick(tickInterval, func(time.Time) tea.Msg {
		return TickMsg{}
	})
}

func (w *Window) Init() tea.Cmd {
	return tea.Batch(doTick(), tea.WindowSize())
}

func (w *Window) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.width = msg.Width
		w.height = msg.Height
		headerH := w.headerHeight()
		w.vpHeight = max(w.height-headerH-2, 1) // 1 separator after header + 1 footer line

	case TickMsg:
		w.tickFrame++
		if w.flash != "" && time.Now().After(w.flashExp) {
			w.flash = ""
		}
		cmds = append(cmds, doTick())
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

func (w *Window) View() string {
	if w.width == 0 {
		return "Starting..."
	}

	header := RenderHeader(w.header, w.width, w.height)
	content := w.screen.View(w)
	footer := w.renderFooter()

	return header + "\n" + content + "\n" + footer
}

func (w *Window) renderFooter() string {
	keyStyle := lipgloss.NewStyle().Foreground(ColorCyan)
	descStyle := lipgloss.NewStyle().Foreground(ColorField)

	// Left: screen indicator + window status (error > flash)
	screenStatus := w.screen.FooterStatus(w)
	var windowStatus string
	if w.err != nil {
		windowStatus = lipgloss.NewStyle().Foreground(ColorError).
			Render(fmt.Sprintf("connection error: %v", w.err))
	} else if w.flash != "" && time.Now().Before(w.flashExp) {
		windowStatus = lipgloss.NewStyle().Foreground(ColorOrange).Render(w.flash)
	}

	var left string
	if screenStatus != "" && windowStatus != "" {
		left = screenStatus + " " + windowStatus
	} else {
		left = screenStatus + windowStatus
	}

	// Right: screen keys + base keys
	var keys []string
	for _, fk := range w.screen.FooterKeys(w) {
		keys = append(keys, keyStyle.Render(fk.Key)+" "+descStyle.Render(fk.Desc))
	}
	keys = append(keys,
		keyStyle.Render("q")+" "+descStyle.Render("quit"),
	)

	right := strings.Join(keys, "  ")

	leftWidth := ansi.StringWidth(left)
	rightWidth := ansi.StringWidth(right)
	gap := max(w.width-leftWidth-rightWidth, 2)

	return left + strings.Repeat(" ", gap) + right
}
