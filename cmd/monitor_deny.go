package cmd

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
)

type denySavedResultMsg struct {
	from    *monitorScreen
	key     proxy.HostKey
	applied bool
	err     error
}

// validDenyTarget reports whether k names one concrete target a deny rule
// can hold: the same targets an allow entry can.
func validDenyTarget(k proxy.HostKey) bool {
	return proxy.AllowFixes(k, "")
}

// isDenied reports whether an enforced rule denies k, as far as this monitor
// knows: from its own denies or a host poll, including hosts since evicted.
func (s *monitorScreen) isDenied(k proxy.HostKey) bool {
	return s.hosts.denied[k]
}

// denyFooterKeys offers N for a target not denied yet, and for a deny this
// monitor applied but couldn't save. A deny already in force has nothing
// left to do.
func denyFooterKeys(k proxy.HostKey, denied, unsaved bool) []tui.FooterKey {
	switch {
	case !validDenyTarget(k):
		return nil
	case unsaved:
		return []tui.FooterKey{{Key: "N", Desc: "retry save"}}
	case denied:
		return nil
	}
	return []tui.FooterKey{{Key: "N", Desc: "deny+save"}}
}

func (s *monitorScreen) denySavedSelected(w *tui.Window) tea.Cmd {
	var k proxy.HostKey
	if s.tab == tabLogs {
		e, ok := s.logs.selected()
		if !ok {
			w.SetFlash("select an entry first")
			return nil
		}
		k = e.HostKey()
	} else {
		h, ok := s.hosts.selected()
		if !ok {
			w.SetFlash("select a host first")
			return nil
		}
		k = h.HostKey
	}
	switch {
	case !validDenyTarget(k):
		w.SetFlash("cannot deny: invalid target")
		return nil
	case s.denying[k]:
		return nil // the first press is still being applied
	case s.isDenied(k) && !s.hosts.unsavedDenies[k]:
		w.SetFlash(flashText("%s is already denied", allowValue(k)))
		return nil
	case s.client == nil:
		return nil
	}
	if s.denying == nil {
		s.denying = map[proxy.HostKey]bool{}
	}
	s.denying[k] = true
	return func() tea.Msg {
		applied, err := denyEntries(s.client, s.session, k.Source, []string{allowValue(k)}, true)
		return denySavedResultMsg{from: s, key: k, applied: applied, err: err}
	}
}

func (s *monitorScreen) handleDenySavedResult(msg denySavedResultMsg, w *tui.Window) {
	delete(s.denying, msg.key)
	if msg.applied {
		s.hosts.setUnsavedDeny(msg.key, msg.err != nil)
		s.endHold(msg.key, proxy.HoldDenied)
		s.hosts.markDenied(msg.key, &s.held, w.Now())
		delete(s.allows, msg.key)
		s.allowGen++
	}
	switch {
	case msg.err != nil && msg.applied:
		w.SetFlashFor(flashText("denied %s, but saving failed: %v", allowValue(msg.key), msg.err), allowErrorFlash)
	case msg.err != nil:
		w.SetFlashFor(flashText("deny %s failed: %v", allowValue(msg.key), msg.err), allowErrorFlash)
	default:
		w.SetFlash(flashText("denied and saved %s", allowValue(msg.key)))
	}
}

func (v *hostsView) setUnsavedDeny(k proxy.HostKey, unsaved bool) {
	if !unsaved {
		delete(v.unsavedDenies, k)
		return
	}
	addKey(&v.unsavedDenies, k)
}

// markDenied applies a successful deny until the next poll confirms it. Only
// the merged rows: merge derives them from v.denied, so the polled rows stay
// what the proxy reported, and a feed reset that clears v.denied drops this
// monitor's denies with the proxy that held them. merge also stops showing
// the host as held, before the hold's end arrives.
func (v *hostsView) markDenied(k proxy.HostKey, held *heldTargets, now time.Time) {
	addKey(&v.denied, k)
	v.merge(held, now)
}
