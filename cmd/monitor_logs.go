package cmd

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
)

// logView renders the raw chronological log tail. Without a selection it
// tails; a selection stays on its entry while new entries arrive below.
type logView struct {
	items     []proxy.LogEntry
	cursor    tui.Cursor
	selecting bool // Pos is a selection the user made; see selectionTimeout
	newCount  int
	// unsupported is set for a proxy that predates the event stream; the
	// log can't follow it.
	unsupported bool
}

func (v *logView) selected() (proxy.LogEntry, bool) {
	if !v.selecting || v.cursor.Pos < 0 || v.cursor.Pos >= len(v.items) {
		return proxy.LogEntry{}, false
	}
	return v.items[v.cursor.Pos], true
}

func (v *logView) setHeight(h int) {
	v.cursor.VpHeight = h
	if !v.selecting {
		v.tail()
		return
	}
	v.cursor.EnsureVisible()
	v.clearNewIfAtBottom()
}

// tail shows the newest entries.
func (v *logView) tail() {
	v.cursor.Pos = max(len(v.items)-1, 0)
	v.cursor.Offset = max(len(v.items)-v.cursor.VpHeight, 0)
	v.newCount = 0
}

// deselect ends the selection and resumes tailing.
func (v *logView) deselect() {
	if v.selecting {
		v.selecting = false
		v.tail()
	}
}

// clear drops every line and the selection: the IDs of a restarted proxy's
// entries start over, so its lines can't be told apart from the old ones.
func (v *logView) clear() {
	v.items = nil
	v.cursor.ItemCount = 0
	v.selecting = false
	v.tail()
}

func (v *logView) clearNewIfAtBottom() {
	if len(v.items) <= v.cursor.Offset+v.cursor.VpHeight {
		v.newCount = 0
	}
}

// appendEntries adds new entries. Without a selection the view keeps
// tailing; otherwise new entries are counted so the footer can announce them.
func (v *logView) appendEntries(entries []proxy.LogEntry) {
	v.items = append(v.items, entries...)
	v.cursor.ItemCount = len(v.items)
	if !v.selecting {
		v.tail()
		return
	}
	if len(entries) > 0 && len(v.items) > v.cursor.Offset+v.cursor.VpHeight {
		v.newCount += len(entries)
	}
}

func (v *logView) handleKey(msg tea.KeyPressMsg) bool {
	if !v.selecting {
		if len(v.items) == 0 || !startSelection(&v.cursor, len(v.items)-1, msg) {
			return false
		}
		v.selecting = true
	} else if !v.cursor.HandleKey(msg) {
		return false
	}
	v.clearNewIfAtBottom()
	return true
}

func (v *logView) render(allows allowState, held *heldTargets, now time.Time) string {
	if v.unsupported {
		return lipgloss.NewStyle().Foreground(tui.ColorOrange).
			Render("proxy does not support the live log, restart the session")
	}
	var lines []string
	end := min(v.cursor.Offset+v.cursor.VpHeight, len(v.items))
	for i := v.cursor.Offset; i < end; i++ {
		lines = append(lines, renderLogLine(v.items[i], allows.entryStatus(v.items[i]), v.selecting && i == v.cursor.Pos, held, now))
	}
	for len(lines) < v.cursor.VpHeight {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func (v *logView) footerKeys(target func(proxy.LogEntry) allowTarget) []tui.FooterKey {
	e, ok := v.selected()
	if !ok {
		return nil
	}
	return allowFooterKeys(target(e))
}

// renderLogLine renders one entry. A hold line counts down while the hold is
// open and shows how it ended afterwards, in place of the reason.
func renderLogLine(e proxy.LogEntry, status allowStatus, highlighted bool, held *heldTargets, now time.Time) string {
	base, marker := tui.LineStyle(highlighted)

	var symbol, reason, outcome string
	var sourceColor color.Color
	reasonStyle := base
	switch {
	case e.Action == proxy.ActionHold:
		if oh, ok := held.Open(e.HostKey(), now); ok && oh.ID == e.ID {
			sourceColor = tui.ColorYellow
			symbol = base.Foreground(sourceColor).Bold(true).Render("?")
			reason = fmt.Sprintf("held · waiting for a decision (%ds)", secondsLeft(oh.Deadline, now))
		} else {
			sourceColor = tui.ColorField
			symbol = base.Foreground(sourceColor).Render("?")
			o := holdEnding(e, held, now)
			reason, outcome = "held · ", holdOutcomeStatus(o).style(base).Render(holdOutcomeLabel(o))
		}
		reasonStyle = base.Foreground(sourceColor)
	case status == statusTemp:
		symbol = base.Foreground(tui.ColorOrange).Render("a")
		sourceColor = tui.ColorOrange
	case status == statusSaved:
		symbol = base.Foreground(tui.ColorOrange).Bold(true).Render("A")
		sourceColor = tui.ColorOrange
	case e.Action == proxy.ActionBlock && e.Code == proxy.BlockDenied:
		symbol = base.Foreground(tui.ColorDenied).Render("-")
		sourceColor = tui.ColorDenied
	case e.Action == proxy.ActionBlock:
		symbol = base.Foreground(tui.ColorError).Render("x")
		sourceColor = tui.ColorError
	default:
		symbol = base.Foreground(tui.ColorCyan).Render("+")
		sourceColor = tui.ColorCyan
	}
	host := tui.SanitizeText(allowValue(e.HostKey()))
	ts := base.Foreground(tui.ColorField).Render(e.Time.Format(time.TimeOnly))
	src := base.Foreground(sourceColor).Render(fmt.Sprintf("%-5s", string(e.Source)))
	hostStr := base.Render(host)
	if e.Action != proxy.ActionHold {
		reason = tui.SanitizeText(e.Reason)
	}
	reasonStr := reasonStyle.Render(reason) + outcome
	sp := base.Render(" ")
	return marker + base.Render("[") + ts + base.Render("]") + sp + symbol + sp + src + sp + hostStr + sp + reasonStr
}

// holdOutcomeStatus is the hosts-tab status a hold's outcome usually leaves
// its host in, so both tabs color it alike. An allowed hold can still end
// blocked by a later check, which only the following log entry shows.
func holdOutcomeStatus(o proxy.HoldOutcome) hostStatus {
	switch o {
	case proxy.HoldAllowed:
		return hostAllowed
	case proxy.HoldRejected:
		return hostRejected
	case proxy.HoldDenied:
		return hostDenied
	case proxy.HoldAbandoned:
		return hostHoldEnded
	default:
		return hostBlocked
	}
}

// holdEnding is how the hold of entry e ended. The entry's own outcome
// covers holds the table no longer tracks.
func holdEnding(e proxy.LogEntry, held *heldTargets, now time.Time) proxy.HoldOutcome {
	if o, ok := held.Outcome(e.ID, now); ok {
		return o
	}
	return e.Outcome
}
