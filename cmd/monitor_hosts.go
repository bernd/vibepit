package cmd

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/bernd/vibepit/config"
	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	"github.com/charmbracelet/x/ansi"
	"github.com/dustin/go-humanize"
)

type hostSort int

const (
	sortNewest hostSort = iota
	sortHost
	sortCount
	sortModes // number of sort modes, not a mode
)

func (s hostSort) String() string {
	switch s {
	case sortCount:
		return "count"
	case sortHost:
		return "host"
	default:
		return "newest"
	}
}

// compare falls back to the host key on ties. The proxy returns hosts in map
// order, so without a total order equal rows would swap places between polls.
func (s hostSort) compare(a, b proxy.HostStats) int {
	newest := b.LastSeen.Compare(a.LastSeen)
	byKey := cmp.Or(cmp.Compare(a.Domain, b.Domain), cmp.Compare(a.Source, b.Source), cmp.Compare(a.Port, b.Port))
	switch s {
	case sortCount:
		return cmp.Or(cmp.Compare(b.Allowed+b.Blocked, a.Allowed+a.Blocked), newest, byKey)
	case sortHost:
		return byKey
	default:
		return cmp.Or(newest, byKey)
	}
}

const (
	hostsChromeLines = 2 // summary + column header
	hostsTimeWidth   = 12
	hostsSourceWidth = 8
	hostsCountWidth  = 7
	hostsStatusWidth = 8
	hostsMinHostW    = 10
	hostsFreshFor    = 3 * time.Second
)

// hostsView renders the aggregated per-host overview.
type hostsView struct {
	hosts       []proxy.HostStats
	cursor      tui.Cursor // Offset is the first visible row
	selecting   bool       // Pos is a selection the user made; see selectionTimeout
	sort        hostSort
	expanded    *proxy.HostKey
	unsupported bool
	err         error // last failed poll that was not a connection failure

	// Cached per host list so frames do not walk every host.
	longestHost int
	countW      int
	blocked     int
}

func (v *hostsView) selected() (proxy.HostStats, bool) {
	if !v.selecting || v.cursor.Pos < 0 || v.cursor.Pos >= len(v.hosts) {
		return proxy.HostStats{}, false
	}
	return v.hosts[v.cursor.Pos], true
}

func (v *hostsView) isExpandedHost(k proxy.HostKey) bool {
	return v.expanded != nil && *v.expanded == k
}

// indexOf returns the row index of the host, or -1.
func (v *hostsView) indexOf(k proxy.HostKey) int {
	return slices.IndexFunc(v.hosts, func(h proxy.HostStats) bool { return h.HostKey == k })
}

// setHeight sizes the list area; h includes the summary and column header.
// render narrows VpHeight while details are expanded.
func (v *hostsView) setHeight(h int) {
	v.cursor.VpHeight = max(h-hostsChromeLines, 1)
}

// setHosts replaces the host list. Selection and expansion follow the host,
// not the row index, because new traffic re-sorts rows constantly. The allow
// keys act on the selection, so it never moves to another host: when its
// host is gone, the selection ends. The view takes ownership of hosts and
// sorts it in place; each poll decodes a fresh slice.
func (v *hostsView) setHosts(hosts []proxy.HostStats) {
	sel, hadSel := v.selected()
	v.hosts = hosts
	v.measure()
	if !v.resort(sel, hadSel) {
		v.deselect()
	}
	if v.expandedIndex() < 0 {
		v.expanded = nil
	}
}

// deselect ends the selection and shows the newest rows again.
func (v *hostsView) deselect() {
	v.selecting, v.expanded = false, nil
	v.cursor.Offset = 0
}

// selectionExpired ends an idle selection. A selection on the expanded row
// keeps it, because reading the details takes longer than the timeout; a
// selection that moved off the expanded row does not.
func (v *hostsView) selectionExpired() {
	if sel, ok := v.selected(); ok && v.isExpandedHost(sel.HostKey) {
		return
	}
	v.deselect()
}

// markAllowlisted applies a successful allow until the next poll confirms it.
func (v *hostsView) markAllowlisted(k proxy.HostKey) {
	if i := v.indexOf(k); i >= 0 {
		v.hosts[i].Allowlisted = true
		v.measure()
	}
}

func (v *hostsView) measure() {
	v.longestHost, v.countW, v.blocked = 0, hostsCountWidth, 0
	for _, h := range v.hosts {
		v.longestHost = max(v.longestHost, ansi.StringWidth(hostLabel(h.HostKey)))
		v.countW = max(v.countW, len(formatCount(h.Allowed+h.Blocked)))
		if isBlocked(h) {
			v.blocked++
		}
	}
}

func (v *hostsView) cycleSort() {
	sel, hadSel := v.selected()
	v.sort = (v.sort + 1) % sortModes
	v.resort(sel, hadSel)
}

// resort sorts the rows and moves the cursor to the selected host. It
// reports false when there was a selection and its host is gone.
func (v *hostsView) resort(sel proxy.HostStats, hadSel bool) bool {
	slices.SortStableFunc(v.hosts, v.sort.compare)
	v.cursor.ItemCount = len(v.hosts)
	found := false
	if hadSel {
		if i := v.indexOf(sel.HostKey); i >= 0 {
			v.cursor.Pos, found = i, true
		}
	}
	v.cursor.Pos = max(min(v.cursor.Pos, len(v.hosts)-1), 0)
	return found || !hadSel
}

func (v *hostsView) handleKey(msg tea.KeyPressMsg) bool {
	switch msg.String() {
	case "enter", "space":
		if sel, ok := v.selected(); ok {
			if v.isExpandedHost(sel.HostKey) {
				v.expanded = nil
			} else {
				k := sel.HostKey
				v.expanded = &k
			}
		}
		return true
	case "s":
		v.cycleSort()
		return true
	}
	if !v.selecting {
		if len(v.hosts) == 0 || !startSelection(&v.cursor, v.cursor.Offset, msg) {
			return false
		}
		v.selecting = true
		return true
	}
	return v.cursor.HandleKey(msg)
}

func (v *hostsView) footerKeys(allows allowState) []tui.FooterKey {
	var keys []tui.FooterKey
	if sel, ok := v.selected(); ok {
		keys = append(keys, allowFooterKeys(allows.forHost(sel))...)
		desc := "details"
		if v.isExpandedHost(sel.HostKey) {
			desc = "collapse"
		}
		keys = append(keys, tui.FooterKey{Key: "enter", Desc: desc})
	}
	return append(keys, tui.FooterKey{Key: "s", Desc: "sort: " + v.sort.String()})
}

// render returns exactly height lines, each at most width cells wide.
func (v *hostsView) render(allows allowState, width, height int, now time.Time) string {
	var lines []string
	if v.unsupported {
		lines = append(lines, lipgloss.NewStyle().Foreground(tui.ColorOrange).
			Render("proxy does not support the hosts view, restart the session"))
	} else {
		listH := max(height-hostsChromeLines, 1)
		cols := v.columns(width)
		lines = append(lines, v.renderSummary(), v.renderColumnHeader(width, cols))
		lines = append(lines, v.renderList(allows, width, cols, listH, now)...)
	}

	for len(lines) < height {
		lines = append(lines, "")
	}
	lines = lines[:height]
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, width, "")
	}
	return strings.Join(lines, "\n")
}

// hostColumns is the column layout for one render.
type hostColumns struct {
	hostW     int
	countW    int
	showCount bool
}

// columns lays out the table for width. The host column comes first: the
// count column is dropped before a host gets truncated, and the inline allow
// hint only gets what is left over (the footer shows the same keys anyway).
// Sorting by count keeps the count column, since rows ordered by a hidden
// value look shuffled.
func (v *hostsView) columns(width int) hostColumns {
	longest := max(v.longestHost, ansi.StringWidth("host ↑"))
	// A row is marker and dot (4), time, host, source, the optional count
	// plus 2, at least a 1-cell gap, and status.
	avail := width - 4 - hostsTimeWidth - hostsSourceWidth - 1 - hostsStatusWidth
	cols := hostColumns{hostW: longest + 2, countW: v.countW, showCount: true}
	switch {
	case cols.hostW <= avail-cols.countW-2:
	case v.sort == sortCount:
		cols.hostW = avail - cols.countW - 2
	default:
		cols.showCount = false
		cols.hostW = min(cols.hostW, avail)
	}
	cols.hostW = max(cols.hostW, hostsMinHostW)
	return cols
}

func (v *hostsView) renderSummary() string {
	if v.err != nil {
		return lipgloss.NewStyle().Foreground(tui.ColorOrange).Render("cannot load hosts: " + v.err.Error())
	}
	allowed, blocked := len(v.hosts)-v.blocked, v.blocked
	return lipgloss.NewStyle().Foreground(tui.ColorField).Render(fmt.Sprintf("%d hosts", len(v.hosts))) + "  " +
		lipgloss.NewStyle().Foreground(tui.ColorCyan).Render(fmt.Sprintf("● %d allowed", allowed)) + "  " +
		lipgloss.NewStyle().Foreground(tui.ColorError).Render(fmt.Sprintf("● %d blocked", blocked))
}

var sortArrowStyle = lipgloss.NewStyle().Foreground(tui.ColorCyan).Bold(true)

func (v *hostsView) renderColumnHeader(width int, cols hostColumns) string {
	field := lipgloss.NewStyle().Foreground(tui.ColorField).Bold(true)
	title := func(s hostSort, name string) string {
		switch {
		case v.sort != s:
			return field.Render(name)
		case s == sortHost:
			return field.Render(name+" ") + sortArrowStyle.Render("↑")
		case s == sortCount:
			// Count is right-aligned; a leading arrow keeps the label in place.
			return sortArrowStyle.Render("↓") + field.Render(" "+name)
		default:
			return field.Render(name+" ") + sortArrowStyle.Render("↓")
		}
	}
	left := "    " + padRight(title(sortNewest, "last seen"), hostsTimeWidth) +
		padRight(title(sortHost, "host"), cols.hostW) + padRight(field.Render("source"), hostsSourceWidth)
	right := padRight(field.Render("status"), hostsStatusWidth)
	if cols.showCount {
		right = padLeft(title(sortCount, "count"), cols.countW) + "  " + right
	}
	gap := max(width-ansi.StringWidth(left)-ansi.StringWidth(right), 1)
	return left + strings.Repeat(" ", gap) + right
}

// expandedIndex returns the row index of the expanded host, or -1.
func (v *hostsView) expandedIndex() int {
	if v.expanded == nil {
		return -1
	}
	return v.indexOf(*v.expanded)
}

// renderList renders only the rows in view. Only the expanded host spans more
// than one line, so the layout is known without rendering every row.
func (v *hostsView) renderList(allows allowState, width int, cols hostColumns, listH int, now time.Time) []string {
	expandedAt := v.expandedIndex()
	var details []string
	if expandedAt >= 0 {
		h := v.hosts[expandedAt]
		details = renderHostDetails(h, allows.hostStatus(h), width)
	}
	// Page keys move by the rows that fit next to the details.
	v.cursor.VpHeight = max(listH-len(details), 1)
	if v.selecting {
		v.scroll(expandedAt, len(details), listH)
	}

	var lines []string
	for i := v.cursor.Offset; i < len(v.hosts) && len(lines) < listH; i++ {
		lines = append(lines, renderHostRow(v.hosts[i], allows, v.selecting && i == v.cursor.Pos, width, cols, now))
		if i == expandedAt {
			lines = append(lines, details...)
		}
	}
	return lines[:min(len(lines), listH)]
}

// scroll sets cursor.Offset so the selected row and its details are in view
// where they fit, without blank lines below the last row.
func (v *hostsView) scroll(expandedAt, detailsH, listH int) {
	rowH := func(i int) int {
		if i == expandedAt {
			return 1 + detailsH
		}
		return 1
	}
	off := max(min(v.cursor.Offset, v.cursor.Pos), 0)
	used := 0
	for i := off; i <= v.cursor.Pos; i++ {
		used += rowH(i)
	}
	// Details taller than the list area: keep the row itself in view.
	for used > listH && off < v.cursor.Pos {
		used -= rowH(off)
		off++
	}

	used = 0
	for i := off; i < len(v.hosts) && used <= listH; i++ {
		used += rowH(i)
	}
	for off > 0 && used+rowH(off-1) <= listH {
		off--
		used += rowH(off)
	}
	v.cursor.Offset = off
}

func renderHostRow(h proxy.HostStats, allows allowState, selected bool, width int, cols hostColumns, now time.Time) string {
	base, marker := tui.LineStyle(selected)
	st := allows.hostStatus(h)

	timeColor := tui.ColorField
	if now.Sub(h.LastSeen) < hostsFreshFor {
		timeColor = tui.ColorOrange
	}
	hostStyle := base
	switch {
	case st == hostBlocked:
		hostStyle = hostStyle.Bold(true)
	case !selected:
		hostStyle = hostStyle.Faint(true)
	}
	host := ansi.Truncate(hostLabel(h.HostKey), cols.hostW-1, "…")

	left := marker +
		base.Foreground(st.color()).Render("●") + base.Render(" ") +
		base.Foreground(timeColor).Render(padRight(h.LastSeen.Format(time.TimeOnly), hostsTimeWidth)) +
		hostStyle.Render(host) + base.Render(strings.Repeat(" ", cols.hostW-ansi.StringWidth(host))) +
		base.Foreground(tui.ColorField).Render(padRight(string(h.Source), hostsSourceWidth))

	var hint string
	if selected {
		for _, k := range allowFooterKeys(allows.forHost(h)) {
			hint += base.Foreground(tui.ColorCyan).Bold(true).Render(k.Key) +
				base.Foreground(tui.ColorField).Render(" "+k.Desc+"  ")
		}
	}
	right := base.Foreground(st.color()).Bold(true).Render(padRight(st.label(), hostsStatusWidth))
	if cols.showCount {
		right = base.Render(padLeft(formatCount(h.Allowed+h.Blocked), cols.countW)+"  ") + right
	}
	if ansi.StringWidth(left)+ansi.StringWidth(hint)+ansi.StringWidth(right) < width {
		right = hint + right
	}

	gap := max(width-ansi.StringWidth(left)-ansi.StringWidth(right), 1)
	return left + base.Render(strings.Repeat(" ", gap)) + right
}

func renderHostDetails(h proxy.HostStats, st hostStatus, width int) []string {
	field := lipgloss.NewStyle().Foreground(tui.ColorField)
	sep := field.Render("    " + strings.Repeat("╌", max(width-8, 1)))
	label := func(s string) string { return "      " + field.Render(padRight(s, 12)) }
	action := func(a proxy.Action) string {
		st := hostBlocked
		if a == proxy.ActionAllow {
			st = hostAllowed
		}
		return lipgloss.NewStyle().Foreground(st.color()).Render(st.label())
	}

	status := lipgloss.NewStyle().Foreground(st.color()).Bold(true).Render(st.label())
	switch st {
	case hostAllowedTemp:
		status += field.Render("  (this session only)")
	case hostSaved:
		status += field.Render("  (saved to " + config.ProjectConfigDirName + "/network.yaml)")
	}
	port := h.Port
	if port == "" {
		port = "-"
	}
	source := "HTTP proxy"
	if h.Source == proxy.SourceDNS {
		source = "DNS query"
	}

	lines := []string{
		sep,
		"",
		label("status:") + status,
		label("host:") + lipgloss.NewStyle().Foreground(tui.ColorCyan).Bold(true).Render(h.Domain),
		label("port:") + port,
		label("source:") + source,
		label("first seen:") + field.Render(h.FirstSeen.Format(time.TimeOnly)) + "  " + action(h.FirstAction),
		label("last seen:") + field.Render(h.LastSeen.Format(time.TimeOnly)) + "  " + action(h.LastAction),
		label("count:") + field.Render(formatCount(h.Allowed+h.Blocked)+" total") + "  " +
			lipgloss.NewStyle().Foreground(tui.ColorCyan).Bold(true).Render(formatCount(h.Allowed)+" allowed") + "  " +
			lipgloss.NewStyle().Foreground(tui.ColorError).Bold(true).Render(formatCount(h.Blocked)+" blocked"),
	}
	if st == hostBlocked {
		lines = append(lines, label("reason:")+h.LastBlockReason)
	}
	return append(lines, "", sep)
}

// hostLabel omits the HTTPS default port to keep the common case uncluttered.
func hostLabel(k proxy.HostKey) string {
	if k.Port == "443" {
		return k.Domain
	}
	return allowValue(k)
}

func padRight(s string, n int) string {
	return s + strings.Repeat(" ", max(n-ansi.StringWidth(s), 0))
}

func padLeft(s string, n int) string {
	return strings.Repeat(" ", max(n-ansi.StringWidth(s), 0)) + s
}

// formatCount groups digits by thousands so large counts stay readable.
func formatCount(n int) string {
	return humanize.Comma(int64(n))
}
