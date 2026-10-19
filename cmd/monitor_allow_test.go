package cmd

import (
	"testing"

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
