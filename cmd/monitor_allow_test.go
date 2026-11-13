package cmd

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/bernd/vibepit/proxy"
	"github.com/stretchr/testify/assert"
)

func TestAllowStateHostStatus(t *testing.T) {
	key := proxy.HostKey{Source: proxy.SourceProxy, Domain: "a.com", Port: "443"}
	blocked := func(code proxy.BlockCode, allowlisted bool) proxy.HostStats {
		return proxy.HostStats{HostKey: key, LastAction: proxy.ActionBlock, LastBlockCode: code, Allowlisted: allowlisted}
	}

	tests := []struct {
		name   string
		allows allowState
		host   proxy.HostStats
		want   hostStatus
	}{
		{
			name: "last allow",
			host: proxy.HostStats{HostKey: key, LastAction: proxy.ActionAllow, Allowlisted: true},
			want: hostAllowed,
		},
		{
			name: "last allow outside the allowlist (host.vibepit ports)",
			host: proxy.HostStats{HostKey: key, LastAction: proxy.ActionAllow},
			want: hostAllowed,
		},
		{
			name: "last block",
			host: blocked(proxy.BlockNotAllowlisted, false),
			want: hostBlocked,
		},
		{
			name: "allowed elsewhere after the last block",
			host: blocked(proxy.BlockNotAllowlisted, true),
			want: hostAllowed,
		},
		{
			name:   "temp allow after the last block",
			allows: allowState{key: statusTemp},
			host:   blocked(proxy.BlockNotAllowlisted, true),
			want:   hostAllowedTemp,
		},
		{
			name:   "saved allow after the last block",
			allows: allowState{key: statusSaved},
			host:   blocked(proxy.BlockNotAllowlisted, true),
			want:   hostSaved,
		},
		{
			name:   "allow after a legacy block without code",
			allows: allowState{key: statusTemp},
			host:   blocked("", true),
			want:   hostAllowedTemp,
		},
		{
			name:   "allow the proxy does not have",
			allows: allowState{key: statusTemp},
			host:   blocked(proxy.BlockNotAllowlisted, false),
			want:   hostBlocked,
		},
		{
			name:   "unallowable block wins over the allow",
			allows: allowState{key: statusTemp},
			host:   blocked(proxy.BlockCIDR, true),
			want:   hostBlocked,
		},
		{
			name:   "unallowable block then allow",
			allows: allowState{key: statusTemp},
			host:   proxy.HostStats{HostKey: key, LastAction: proxy.ActionAllow, LastBlockCode: proxy.BlockResolutionFailed, Allowlisted: true},
			want:   hostAllowedTemp,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.allows.hostStatus(tt.host))
		})
	}
}

func TestHostStatusLabel(t *testing.T) {
	assert.Equal(t, "blocked", hostBlocked.label())
	assert.Equal(t, "allowed", hostAllowed.label())
	assert.Equal(t, "allowed*", hostAllowedTemp.label())
	assert.Equal(t, "saved", hostSaved.label())
}

func TestAllowStateEntryStatus(t *testing.T) {
	key := proxy.HostKey{Source: proxy.SourceProxy, Domain: "a.com", Port: "443"}
	allows := allowState{key: statusTemp}
	entry := func(action proxy.Action, code proxy.BlockCode, port string) proxy.LogEntry {
		return proxy.LogEntry{Domain: "a.com", Port: port, Source: proxy.SourceProxy, Action: action, Code: code}
	}

	tests := []struct {
		name   string
		allows allowState
		entry  proxy.LogEntry
		want   allowStatus
	}{
		{"no override", nil, entry(proxy.ActionBlock, proxy.BlockNotAllowlisted, "443"), statusNone},
		{"allow", allows, entry(proxy.ActionAllow, "", "443"), statusTemp},
		{"allowlist block", allows, entry(proxy.ActionBlock, proxy.BlockNotAllowlisted, "443"), statusTemp},
		{"unallowable block", allows, entry(proxy.ActionBlock, proxy.BlockCIDR, "443"), statusNone},
		{"override for other port", allows, entry(proxy.ActionBlock, proxy.BlockNotAllowlisted, "80"), statusNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.allows.entryStatus(tt.entry))
		})
	}
}

func TestAllowValue(t *testing.T) {
	assert.Equal(t, "a.com:443", allowValue(proxy.HostKey{Source: proxy.SourceProxy, Domain: "a.com", Port: "443"}))
	assert.Equal(t, "a.com", allowValue(proxy.HostKey{Source: proxy.SourceDNS, Domain: "a.com"}))
	assert.Equal(t, "[2001:db8::1]:443", allowValue(proxy.HostKey{Source: proxy.SourceProxy, Domain: "2001:db8::1", Port: "443"}))
}

// The host comes from the sandbox, and errors can quote it.
func TestMonitorFlashesSanitizeText(t *testing.T) {
	key := proxy.HostKey{Source: proxy.SourceProxy, Domain: "evil\x1b[2J.com", Port: "443"}
	failed := errors.New("bad\x1b]0;title\x07 thing")
	tests := []struct {
		name string
		msg  func(s *monitorScreen) tea.Msg
		want string
	}{
		{"allowed", func(s *monitorScreen) tea.Msg { return allowResultMsg{from: s, key: key, status: statusTemp} }, "allowed evil[2J.com:443"},
		{"allowed and saved", func(s *monitorScreen) tea.Msg { return allowResultMsg{from: s, key: key, status: statusSaved} }, "allowed and saved evil[2J.com:443"},
		{"allow failed", func(s *monitorScreen) tea.Msg { return allowResultMsg{from: s, key: key, err: failed} }, "allow evil[2J.com:443 failed: bad]0;title thing"},
		{"save failed", func(s *monitorScreen) tea.Msg {
			return allowResultMsg{from: s, key: key, status: statusTemp, err: failed}
		}, "allowed evil[2J.com:443, but saving failed: bad]0;title thing"},
		{"rejected", func(s *monitorScreen) tea.Msg { return rejectResultMsg{from: s, key: key} }, "rejected evil[2J.com:443"},
		{"reject failed", func(s *monitorScreen) tea.Msg { return rejectResultMsg{from: s, key: key, err: failed} }, "reject evil[2J.com:443 failed: bad]0;title thing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, w := newPollingTestScreen(t, nil, nil)
			s.Update(tt.msg(s), w)
			assert.Equal(t, tt.want, w.Flash())
		})
	}
}

func TestHostStatus_RejectionOnlyNamesAllowlistBlocks(t *testing.T) {
	k := proxy.HostKey{Source: proxy.SourceProxy, Domain: "example.com", Port: "443"}
	tests := []struct {
		name        string
		allowlisted bool
		code        proxy.BlockCode
		want        hostStatus
	}{
		{"not allowlisted", false, proxy.BlockNotAllowlisted, hostRejected},
		{"allowed, then blocked by CIDR", true, proxy.BlockCIDR, hostBlocked},
		{"allowed since the block", true, proxy.BlockNotAllowlisted, hostAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := proxy.HostStats{HostKey: k, Rejected: true, Allowlisted: tt.allowlisted, LastAction: proxy.ActionBlock, LastBlockCode: tt.code}
			assert.Equal(t, tt.want, allowState(nil).hostStatus(h))
		})
	}
}
