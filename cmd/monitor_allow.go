package cmd

import (
	"fmt"
	"image/color"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
)

// allowStatus tracks whether a host has been temporarily or permanently
// added to the allow list from this monitor.
type allowStatus int

const (
	statusNone  allowStatus = iota
	statusTemp              // temporarily allowed this session
	statusSaved             // saved to persistent allow list
)

// allowState holds allows made from this monitor, keyed by host so every tab
// and every log line of a host share them. It labels allows; whether a host
// is allowed comes from the proxy.
type allowState map[proxy.HostKey]allowStatus

type hostStatus int

const (
	hostBlocked hostStatus = iota
	hostAllowed
	hostAllowedTemp
	hostSaved
	hostDenied
	hostRejected
	// hostHoldEnded is a host whose last request was held and left before
	// a decision was logged; nothing went through, but nothing was blocked.
	hostHoldEnded
)

func (st hostStatus) label() string {
	switch st {
	case hostAllowed:
		return "allowed"
	case hostAllowedTemp:
		return "allowed*"
	case hostSaved:
		return "saved"
	case hostDenied:
		return "denied"
	case hostRejected:
		return "rejected"
	case hostHoldEnded:
		return "held"
	default:
		return "blocked"
	}
}

// faded reports whether the status is drawn dim: it tells of a request that
// was never decided.
func (st hostStatus) faded() bool {
	return st == hostHoldEnded
}

// style is base in the status's color, dim if faded.
func (st hostStatus) style(base lipgloss.Style) lipgloss.Style {
	return base.Foreground(st.color()).Faint(st.faded())
}

// labelStyle is style for the status label, which stands out unless faded.
func (st hostStatus) labelStyle(base lipgloss.Style) lipgloss.Style {
	return st.style(base).Bold(!st.faded())
}

// blocks reports whether hosts with this status get nothing through.
func (st hostStatus) blocks() bool {
	return st == hostBlocked || st == hostDenied || st == hostRejected || st == hostHoldEnded
}

func (st hostStatus) color() color.Color {
	switch st {
	case hostAllowed:
		return tui.ColorCyan
	case hostAllowedTemp, hostSaved:
		return tui.ColorOrange
	case hostRejected:
		return tui.ColorRejected
	case hostDenied:
		return tui.ColorDenied
	case hostHoldEnded:
		return tui.ColorYellow
	default:
		return tui.ColorError
	}
}

// hostStatus derives a host's status. The proxy reports whether its
// allowlist admits the host, so allows made elsewhere count and an allow the
// proxy lost shows as blocked. CIDR and resolution checks run only after the
// allowlist passed, so those blocks win over any allow. allowState only labels
// the allows made from this monitor. A deny rule wins over everything,
// including one that blocked the host as an alias of a denied name; a
// rejection only names a block, since an allow overrides it.
func (s allowState) hostStatus(h proxy.HostStats) hostStatus {
	if isBlocked(h) {
		switch {
		case h.Denied || h.LastBlockCode == proxy.BlockDenied:
			return hostDenied
		// Once allowlisted, a block comes from a later check, such as the
		// CIDR one, which the rejection doesn't explain.
		case h.Rejected && !h.Allowlisted:
			return hostRejected
		case h.LastAction == proxy.ActionHold:
			return hostHoldEnded
		}
		return hostBlocked
	}
	switch s[h.HostKey] {
	case statusSaved:
		return hostSaved
	case statusTemp:
		return hostAllowedTemp
	}
	return hostAllowed
}

func isBlocked(h proxy.HostStats) bool {
	// A hold that ended without a logged decision, because its request
	// left, didn't let anything through either, and an allow still fixes it.
	decided := h.LastAction == proxy.ActionBlock || h.LastAction == proxy.ActionHold
	return h.Denied || decided && (!h.Allowlisted || !proxy.AllowFixes(h.HostKey, h.LastBlockCode))
}

// entryStatus returns the allow marker for a log line.
func (s allowState) entryStatus(e proxy.LogEntry) allowStatus {
	if e.Action == proxy.ActionBlock && !proxy.AllowFixes(e.HostKey(), e.Code) {
		return statusNone
	}
	return s[e.HostKey()]
}

// allowTarget is what the allow keys act on: a host row or a log line.
type allowTarget struct {
	key     proxy.HostKey
	current allowStatus
	blocked bool
	code    proxy.BlockCode
	reason  string
}

func (s allowState) forHost(h proxy.HostStats) allowTarget {
	t := allowTarget{key: h.HostKey, blocked: isBlocked(h), code: h.LastBlockCode, reason: h.LastBlockReason}
	if h.Denied {
		t.code, t.reason = proxy.BlockDenied, "target is denied"
	}
	if !t.blocked {
		t.current = s[h.HostKey]
	}
	return t
}

// forEntry treats a hold line as blocked: a hold that ended without an
// allow ended in a block.
func (s allowState) forEntry(e proxy.LogEntry) allowTarget {
	blocked := e.Action == proxy.ActionBlock || e.Action == proxy.ActionHold
	return allowTarget{key: e.HostKey(), current: s.entryStatus(e), blocked: blocked, code: e.Code, reason: e.Reason}
}

// forHeld is the target of a host the proxy is holding requests for. Only
// allowlist blocks are held, so an allow always releases them.
func (s allowState) forHeld(k proxy.HostKey) allowTarget {
	return allowTarget{key: k, blocked: true, code: proxy.BlockNotAllowlisted}
}

// canAllow reports whether the a and A keys add an allowlist entry.
func (t allowTarget) canAllow() bool {
	return t.current == statusNone && t.blocked && proxy.AllowFixes(t.key, t.code)
}

// canSave reports whether A saves a temporary allow.
func (t allowTarget) canSave() bool {
	return t.current == statusTemp
}

func allowValue(k proxy.HostKey) string {
	return k.Target().String()
}

// allowResultMsg is returned by the async allow command.
type allowResultMsg struct {
	from   *monitorScreen
	key    proxy.HostKey
	status allowStatus
	err    error
}

func (s *monitorScreen) allowCmd(key proxy.HostKey, save bool) tea.Cmd {
	return func() tea.Msg {
		status, err := allowEntry(s.client, s.session, key, save)
		return allowResultMsg{from: s, key: key, status: status, err: err}
	}
}

// allowFooterKeys returns the allow key hints for an entry or host.
func allowFooterKeys(t allowTarget) []tui.FooterKey {
	switch {
	case t.canSave():
		return []tui.FooterKey{{Key: "A", Desc: "save"}}
	case t.canAllow():
		return []tui.FooterKey{{Key: "a", Desc: "allow"}, {Key: "A", Desc: "allow+save"}}
	}
	return nil
}

// requestAllow applies the allow rules shared by both tabs. It returns the
// command to run, or nil after flashing why nothing happens.
func (s *monitorScreen) requestAllow(w *tui.Window, t allowTarget, save bool) tea.Cmd {
	switch {
	case t.canAllow() || save && t.canSave():
		return s.allowCmd(t.key, save)
	case t.current == statusNone && t.blocked:
		reason := t.reason
		if t.code == proxy.BlockNotAllowlisted || t.code == "" {
			// An allowlist block the allowlist can't fix, e.g. an IPv6 literal.
			reason = "the allowlist can't hold " + allowValue(t.key)
		}
		w.SetFlash(flashText("cannot allow: %s", reason))
	default:
		w.SetFlash("already allowed")
	}
	return nil
}

// allowErrorFlash is longer than a normal flash because allow errors carry
// detail such as a config file path. They are flashes, not window errors,
// because the window error is the connection state and polls clear it.
const allowErrorFlash = 6 * time.Second

func (s *monitorScreen) handleAllowResult(msg allowResultMsg, w *tui.Window) {
	if msg.err != nil && msg.status == statusNone {
		w.SetFlashFor(flashText("allow %s failed: %v", allowValue(msg.key), msg.err), allowErrorFlash)
		return
	}
	if msg.status == statusTemp && s.allows[msg.key] == statusSaved {
		return // a slower temp allow must not undo a save that finished first
	}
	if s.isDenied(msg.key) {
		// The proxy refuses allows of denied targets, so this allow landed
		// before a deny whose result came back first. The deny wins, but
		// what the allow left behind is still reported.
		v := allowValue(msg.key)
		switch {
		case msg.err != nil:
			w.SetFlashFor(flashText("allow %s overridden by deny; saving the allow failed: %v", v, msg.err), allowErrorFlash)
		case msg.status == statusSaved:
			w.SetFlashFor(flashText("allow %s overridden by deny; its saved allow entry has no effect", v), allowErrorFlash)
		default:
			w.SetFlashFor(flashText("allow %s overridden by deny", v), allowErrorFlash)
		}
		return
	}
	s.allows[msg.key] = msg.status
	s.hosts.markAllowlisted(msg.key)
	s.allowGen++
	switch {
	case msg.err != nil:
		w.SetFlashFor(flashText("allowed %s, but saving failed: %v", allowValue(msg.key), msg.err), allowErrorFlash)
	case msg.status == statusTemp:
		w.SetFlash(flashText("allowed %s", allowValue(msg.key)))
	case msg.status == statusSaved:
		w.SetFlash(flashText("allowed and saved %s", allowValue(msg.key)))
	}
}

// flashText formats a flash. The hosts come from the sandbox, and errors
// and reasons can quote them, so control characters are dropped.
func flashText(format string, args ...any) string {
	return tui.SanitizeText(fmt.Sprintf(format, args...))
}

// rejectResultMsg is returned by the async reject command (n).
type rejectResultMsg struct {
	from *monitorScreen
	key  proxy.HostKey
	err  error
}

// rejectSelected rejects the selected host for the session while the proxy
// holds requests for it. Anywhere else n does nothing: a rejection only
// ends holds and stops new ones.
func (s *monitorScreen) rejectSelected(w *tui.Window) tea.Cmd {
	k, held := s.heldSelection(w.Now())
	if !held || s.client == nil {
		return nil
	}
	return func() tea.Msg {
		return rejectResultMsg{from: s, key: k, err: s.client.DenyTarget(k.Target())}
	}
}

// handleRejectResult marks the host; the hold's end arrives on the feed.
func (s *monitorScreen) handleRejectResult(msg rejectResultMsg, w *tui.Window) {
	if msg.err != nil {
		w.SetFlashFor(flashText("reject %s failed: %v", allowValue(msg.key), msg.err), allowErrorFlash)
		return
	}
	s.endHold(msg.key, proxy.HoldRejected)
	s.hosts.markRejected(msg.key, &s.held, w.Now())
	w.SetFlash(flashText("rejected %s", allowValue(msg.key)))
}
