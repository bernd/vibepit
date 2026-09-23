package cmd

import (
	"context"
	"testing"

	"github.com/bernd/vibepit/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

func TestParseApproveTarget(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		target  string
		want    proxy.LogEntry
		wantErr bool
	}{
		{
			name:   "proxy host and port",
			source: "proxy",
			target: "api.example.com:443",
			want:   proxy.LogEntry{Source: proxy.SourceProxy, Domain: "api.example.com", Port: "443", Action: proxy.ActionBlock},
		},
		{
			name:   "dns bare domain",
			source: "dns",
			target: "example.com",
			want:   proxy.LogEntry{Source: proxy.SourceDNS, Domain: "example.com", Action: proxy.ActionBlock},
		},
		{name: "proxy without port", source: "proxy", target: "example.com", wantErr: true},
		{name: "unknown source", source: "smtp", target: "example.com:25", wantErr: true},
		{name: "empty target", source: "proxy", target: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseApproveTarget(tt.source, tt.target)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestApproveCmdline_RoundTrip(t *testing.T) {
	entries := []proxy.LogEntry{
		{Source: proxy.SourceProxy, Domain: "api.example.com", Port: "443", Action: proxy.ActionBlock, Reason: "domain not in allowlist"},
		{Source: proxy.SourceDNS, Domain: "example.com", Action: proxy.ActionBlock, Reason: "not allowed"},
	}
	for _, e := range entries {
		t.Run(string(e.Source), func(t *testing.T) {
			session := &SessionInfo{ControlPort: "4711", SessionID: "sess1", ProjectDir: "/src/proj", CredDir: "/state/sess1"}
			args := approveCmdline("/usr/bin/vibepit", session, e)
			require.Equal(t, "/usr/bin/vibepit", args[0])
			require.Equal(t, "approve", args[1])

			cmd := ApproveCommand()
			var got proxy.LogEntry
			var gotSession *SessionInfo
			cmd.Action = func(ctx context.Context, c *cli.Command) error {
				var err error
				gotSession, err = approveSession(ctx, c)
				require.NoError(t, err)
				got, err = parseApproveTarget(c.String("source"), c.Args().First())
				got.Reason = c.String("reason")
				return err
			}
			require.NoError(t, cmd.Run(context.Background(), args[1:]))
			assert.Equal(t, session, gotSession)
			assert.Equal(t, e, got)
		})
	}
}

func TestAllowValueForEntry(t *testing.T) {
	tests := []struct {
		name  string
		entry proxy.LogEntry
		want  string
	}{
		{name: "proxy domain", entry: proxy.LogEntry{Source: proxy.SourceProxy, Domain: "a.com", Port: "443"}, want: "a.com:443"},
		{name: "proxy ipv4", entry: proxy.LogEntry{Source: proxy.SourceProxy, Domain: "192.0.2.1", Port: "80"}, want: "192.0.2.1:80"},
		{name: "proxy ipv6 gets brackets", entry: proxy.LogEntry{Source: proxy.SourceProxy, Domain: "2001:db8::1", Port: "443"}, want: "[2001:db8::1]:443"},
		{name: "proxy without port", entry: proxy.LogEntry{Source: proxy.SourceProxy, Domain: "a.com"}, want: "a.com"},
		{name: "dns", entry: proxy.LogEntry{Source: proxy.SourceDNS, Domain: "a.com"}, want: "a.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, allowValueForEntry(tt.entry))
		})
	}
}

func TestApproveCmdline_RoundTripIPv6(t *testing.T) {
	e := proxy.LogEntry{Source: proxy.SourceProxy, Domain: "2001:db8::1", Port: "443", Action: proxy.ActionBlock}
	args := approveCmdline("/usr/bin/vibepit", &SessionInfo{SessionID: "s"}, e)
	got, err := parseApproveTarget("proxy", args[len(args)-1])
	require.NoError(t, err)
	assert.Equal(t, "2001:db8::1", got.Domain)
	assert.Equal(t, "443", got.Port)
}
