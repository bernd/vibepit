package cmd

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
)

// logView renders the raw chronological log tail. Without a selection it
// tails; a selection stays on its entry while new entries arrive below.
type logView struct {
	items      []proxy.LogEntry
	cursor     tui.Cursor
	selecting  bool // Pos is a selection the user made; see selectionTimeout
	pollCursor uint64
	loaded     bool // first poll done; later polls use the strict cursor
	newCount   int
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

func (v *logView) clearNewIfAtBottom() {
	if len(v.items) <= v.cursor.Offset+v.cursor.VpHeight {
		v.newCount = 0
	}
}

// appendEntries adds polled entries. Without a selection the view keeps
// tailing; otherwise new entries are counted so the footer can announce them.
func (v *logView) appendEntries(entries []proxy.LogEntry) {
	v.items = append(v.items, entries...)
	v.loaded = true
	if len(entries) > 0 {
		v.pollCursor = entries[len(entries)-1].ID
	}
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

func (v *logView) render(allows allowState) string {
	var lines []string
	end := min(v.cursor.Offset+v.cursor.VpHeight, len(v.items))
	for i := v.cursor.Offset; i < end; i++ {
		lines = append(lines, renderLogLine(v.items[i], allows.entryStatus(v.items[i]), v.selecting && i == v.cursor.Pos))
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

func renderLogLine(e proxy.LogEntry, status allowStatus, highlighted bool) string {
	base, marker := tui.LineStyle(highlighted)

	var symbol string
	var sourceColor color.Color
	switch {
	case status == statusTemp:
		symbol = base.Foreground(tui.ColorOrange).Render("a")
		sourceColor = tui.ColorOrange
	case status == statusSaved:
		symbol = base.Foreground(tui.ColorOrange).Bold(true).Render("A")
		sourceColor = tui.ColorOrange
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
	reasonStr := base.Render(tui.SanitizeText(e.Reason))
	sp := base.Render(" ")
	return marker + base.Render("[") + ts + base.Render("]") + sp + symbol + sp + src + sp + hostStr + sp + reasonStr
}
