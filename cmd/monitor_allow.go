package cmd

import (
	"fmt"
	"image/color"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bernd/vibepit/config"
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
)

func (st hostStatus) label() string {
	switch st {
	case hostAllowed:
		return "allowed"
	case hostAllowedTemp:
		return "allowed*"
	case hostSaved:
		return "saved"
	default:
		return "blocked"
	}
}

func (st hostStatus) color() color.Color {
	switch st {
	case hostAllowed:
		return tui.ColorCyan
	case hostAllowedTemp, hostSaved:
		return tui.ColorOrange
	default:
		return tui.ColorError
	}
}

// hostStatus derives a host's status. The proxy reports whether its
// allowlist admits the host, so allows made elsewhere count and an allow the
// proxy lost shows as blocked. CIDR and resolution checks run only after the
// allowlist passed, so those blocks win over any allow. allowState only labels
// the allows made from this monitor.
func (s allowState) hostStatus(h proxy.HostStats) hostStatus {
	if isBlocked(h) {
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
	return h.LastAction == proxy.ActionBlock && (!h.Allowlisted || !proxy.AllowFixes(h.HostKey, h.LastBlockCode))
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
	if !t.blocked {
		t.current = s[h.HostKey]
	}
	return t
}

func (s allowState) forEntry(e proxy.LogEntry) allowTarget {
	return allowTarget{key: e.HostKey(), current: s.entryStatus(e), blocked: e.Action == proxy.ActionBlock,
		code: e.Code, reason: e.Reason}
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
		value := allowValue(key)

		var err error
		switch key.Source {
		case proxy.SourceDNS:
			_, err = s.client.AllowDNS([]string{value})
		default:
			_, err = s.client.AllowHTTP([]string{value})
		}
		if err != nil {
			return allowResultMsg{from: s, key: key, err: err}
		}

		status := statusTemp
		if save {
			status = statusSaved
			projectPath := config.DefaultProjectPath(s.session.ProjectDir)
			switch key.Source {
			case proxy.SourceDNS:
				err = config.AppendAllowDNS(projectPath, []string{value})
			default:
				err = config.AppendAllowHTTP(projectPath, []string{value})
			}
			if err != nil {
				// The proxy already allows the host for this session.
				return allowResultMsg{from: s, key: key, status: statusTemp, err: err}
			}
		}
		return allowResultMsg{from: s, key: key, status: status}
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
		w.SetFlash("cannot allow: " + reason)
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
		w.SetFlashFor(fmt.Sprintf("allow %s failed: %v", allowValue(msg.key), msg.err), allowErrorFlash)
		return
	}
	if msg.status == statusTemp && s.allows[msg.key] == statusSaved {
		return // a slower temp allow must not undo a save that finished first
	}
	s.allows[msg.key] = msg.status
	s.hosts.markAllowlisted(msg.key)
	s.allowGen++
	switch {
	case msg.err != nil:
		w.SetFlashFor(fmt.Sprintf("allowed %s, but saving failed: %v", allowValue(msg.key), msg.err), allowErrorFlash)
	case msg.status == statusTemp:
		w.SetFlash(fmt.Sprintf("allowed %s", allowValue(msg.key)))
	case msg.status == statusSaved:
		w.SetFlash(fmt.Sprintf("allowed and saved %s", allowValue(msg.key)))
	}
}
