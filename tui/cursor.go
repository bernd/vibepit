package tui

import tea "github.com/charmbracelet/bubbletea"

// Cursor provides scrollable list navigation. Screens that need a navigable
// list embed this struct and call HandleKey in their Update method.
type Cursor struct {
	Pos       int // highlighted item index
	Offset    int // scroll offset (first visible item)
	VpHeight  int // visible rows
	ItemCount int // total items
}

// AtEnd reports whether the cursor is on the last item.
func (c *Cursor) AtEnd() bool {
	return c.ItemCount > 0 && c.Pos == c.ItemCount-1
}

// EnsureVisible adjusts Offset so Pos is within the visible window.
func (c *Cursor) EnsureVisible() {
	if c.Pos < c.Offset {
		c.Offset = c.Pos
	}
	if c.Pos >= c.Offset+c.VpHeight {
		c.Offset = c.Pos - c.VpHeight + 1
	}
}

// HandleKey processes navigation keys (j/k/G/g/arrows/pgdn/pgup).
// Returns true if the key was handled.
func (c *Cursor) HandleKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "j", "down":
		if c.Pos < c.ItemCount-1 {
			c.Pos++
			c.EnsureVisible()
		}
	case "k", "up":
		if c.Pos > 0 {
			c.Pos--
			c.EnsureVisible()
		}
	case "G", "end":
		if c.ItemCount > 0 {
			c.Pos = c.ItemCount - 1
			c.EnsureVisible()
		}
	case "g", "home":
		c.Pos = 0
		c.EnsureVisible()
	case "pgdown":
		c.Pos += c.VpHeight
		if c.Pos >= c.ItemCount {
			c.Pos = c.ItemCount - 1
		}
		if c.Pos < 0 {
			c.Pos = 0
		}
		c.EnsureVisible()
	case "pgup":
		c.Pos -= c.VpHeight
		if c.Pos < 0 {
			c.Pos = 0
		}
		c.EnsureVisible()
	default:
		return false
	}
	return true
}

// FooterKeys returns the standard navigation keybinding hints.
func (c *Cursor) FooterKeys() []FooterKey {
	return []FooterKey{
		{Key: "↑/↓ k/j", Desc: "navigate"},
		{Key: "Home/End g/G", Desc: "jump"},
	}
}
