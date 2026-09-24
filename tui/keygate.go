package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// KeyGate wraps a model that asks the user something and drops key input
// until the user deliberately presses an answer key: a key the answers
// predicate accepts, with no other key for a quiet period before it,
// counted from when the model starts, and none for a settle period after
// it. Someone typing into another program when the question appears,
// holding a key, or pasting text cannot answer it by accident; that input
// is dropped. Once an answer got through, the user knows the question is
// there and the gate stays open. Other keys never open it, even pressed on
// their own: after Enter, "and" would still answer with its a.
//
// Modifier keys pressed on their own, which terminals report under the
// kitty keyboard protocol, neither count as a key nor restart the quiet
// period, so Shift+A still makes a single key press.
//
// A held key repeats only after the terminal's repeat delay. Where the
// terminal reports key releases, the gate asks for them, and a key still
// down when the settle period ends is held, not pressed. Elsewhere, only a
// settle period longer than the repeat delay tells the two apart.
type KeyGate struct {
	model   tea.Model
	answers func(tea.KeyPressMsg) bool
	quiet   time.Duration
	settle  time.Duration
	now     func() time.Time

	open     bool
	last     time.Time // the last key or paste
	held     *tea.KeyPressMsg
	heldUp   bool // held was released
	releases bool // the terminal reports key releases
	seq      int  // identifies the settle tick for held
}

// settledMsg ends the settle period of the held key with the same seq.
type settledMsg struct {
	gate *KeyGate
	seq  int
}

// NewKeyGate returns model behind a gate that opens for the keys answers
// accepts, with the given quiet and settle periods.
func NewKeyGate(model tea.Model, answers func(tea.KeyPressMsg) bool, quiet, settle time.Duration) *KeyGate {
	return &KeyGate{model: model, answers: answers, quiet: quiet, settle: settle, now: time.Now}
}

func (g *KeyGate) Init() tea.Cmd {
	g.last = g.now()
	return g.model.Init()
}

func (g *KeyGate) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if g.open {
		return g.forward(msg)
	}
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if isModifierKey(msg.Code) {
			return g, nil
		}
		now := g.now()
		deliberate := g.held == nil && now.Sub(g.last) >= g.quiet && g.answers(msg)
		g.last = now
		g.held = nil
		if !deliberate {
			return g, nil
		}
		g.held, g.heldUp = &msg, false
		g.seq++
		seq := g.seq
		return g, tea.Tick(g.settle, func(time.Time) tea.Msg { return settledMsg{gate: g, seq: seq} })
	case tea.KeyReleaseMsg:
		g.releases = true
		if g.held != nil && msg.Code == g.held.Code {
			g.heldUp = true
		}
		return g, nil
	case tea.PasteMsg, tea.PasteStartMsg, tea.PasteEndMsg:
		g.last = g.now()
		g.held = nil
		return g, nil
	case settledMsg:
		if msg.gate != g || msg.seq != g.seq || g.held == nil {
			return g, nil
		}
		key := *g.held
		g.held = nil
		if g.releases && !g.heldUp {
			// Still down: held, not pressed. Its repeats restart the quiet
			// period anyway.
			g.last = g.now()
			return g, nil
		}
		g.open = true
		return g.forward(key)
	}
	return g.forward(msg)
}

func (g *KeyGate) forward(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := g.model.Update(msg)
	g.model = next
	return g, cmd
}

func (g *KeyGate) View() tea.View {
	v := g.model.View()
	v.KeyboardEnhancements.ReportEventTypes = true
	return v
}

// isModifierKey reports whether code is a modifier or lock key pressed on
// its own.
func isModifierKey(code rune) bool {
	switch code {
	case tea.KeyCapsLock, tea.KeyScrollLock, tea.KeyNumLock:
		return true
	}
	return code >= tea.KeyLeftShift && code <= tea.KeyIsoLevel5Shift
}
