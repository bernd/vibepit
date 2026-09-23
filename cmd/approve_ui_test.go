package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/bernd/vibepit/config"
	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeApproveSetup(t *testing.T, entry proxy.LogEntry) (*approveScreen, *tui.Window, *proxy.HTTPAllowlist, *proxy.DNSAllowlist, string) {
	t.Helper()
	projectDir := t.TempDir()
	projectPath := config.DefaultProjectPath(projectDir)
	require.NoError(t, os.MkdirAll(filepath.Dir(projectPath), 0o755))
	require.NoError(t, os.WriteFile(projectPath, []byte("presets:\n  - default\n"), 0o644))

	httpAL, err := proxy.NewHTTPAllowlist(nil)
	require.NoError(t, err)
	dnsAL, err := proxy.NewDNSAllowlist(nil)
	require.NoError(t, err)
	api := proxy.NewControlAPI(proxy.NewLogBuffer(100), nil, httpAL, dnsAL)
	client := testControlClient(t, api)

	session := &SessionInfo{SessionID: "test123456", ProjectDir: projectDir}
	s := newApproveScreen(session, client, entry)
	w := tui.NewWindow(&tui.HeaderInfo{ProjectDir: projectDir, SessionID: session.SessionID}, s)
	w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return s, w, httpAL, dnsAL, projectPath
}

var blockedEntry = proxy.LogEntry{
	Domain: "api.example.com",
	Port:   "443",
	Action: proxy.ActionBlock,
	Source: proxy.SourceProxy,
	Reason: "domain not in allowlist",
}

func TestApproveScreen_View(t *testing.T) {
	s, w, _, _, _ := makeApproveSetup(t, blockedEntry)
	view := s.View(w)
	assert.Contains(t, view, "api.example.com:443")
	assert.Contains(t, view, "domain not in allowlist")
}

func TestApproveScreen_ViewSanitizesUntrustedText(t *testing.T) {
	entry := blockedEntry
	entry.Domain = "evil\x1b[2J.example.com"
	entry.Reason = "bad\x9bthing\x7f"
	s, w, _, _, _ := makeApproveSetup(t, entry)
	view := s.View(w)
	assert.NotContains(t, view, "\x1b[2J")
	assert.NotContains(t, view, "\x9b")
	assert.NotContains(t, view, "\x7f")
	assert.NotContains(t, view, "\ufffd")
	assert.Contains(t, view, "evil[2J.example.com:443") // escape byte gone, text stays
	assert.Contains(t, view, "badthing")
}

func TestApproveScreen_FooterKeys(t *testing.T) {
	s, w, _, _, _ := makeApproveSetup(t, blockedEntry)
	assert.Equal(t, []string{"allow", "allow+save", "deny", "dismiss"}, footerKeyDescs(s.FooterKeys(w)))
}

func TestApproveScreen_Allow(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		wantStatus allowStatus
		wantSaved  bool
	}{
		{name: "a allows for session", key: "a", wantStatus: statusTemp},
		{name: "A allows and saves", key: "A", wantStatus: statusSaved, wantSaved: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, w, httpAL, _, projectPath := makeApproveSetup(t, blockedEntry)

			_, cmd := s.Update(tea.KeyPressMsg{Code: rune(tt.key[0]), Text: tt.key}, w)
			require.NotNil(t, cmd)
			msg := cmd()
			res, ok := msg.(allowResultMsg)
			require.True(t, ok)
			require.NoError(t, res.err)
			assert.Equal(t, tt.wantStatus, res.status)
			assert.True(t, httpAL.Allows("api.example.com", "443"))

			data, err := os.ReadFile(projectPath)
			require.NoError(t, err)
			assert.Equal(t, tt.wantSaved, strings.Contains(string(data), "api.example.com:443"))

			// Feeding the result back quits the overlay.
			_, cmd = s.Update(res, w)
			require.NotNil(t, cmd)
			assert.IsType(t, tea.QuitMsg{}, cmd())
		})
	}
}

func TestApproveScreen_Deny(t *testing.T) {
	s, w, httpAL, _, _ := makeApproveSetup(t, blockedEntry)
	_, cmd := s.Update(tea.KeyPressMsg{Code: 'n', Text: "n"}, w)
	require.NotNil(t, cmd)
	msg := cmd()
	require.IsType(t, denyResultMsg{}, msg)
	require.NoError(t, msg.(denyResultMsg).err)

	res, err := s.client.Check(blockedEntry)
	require.NoError(t, err)
	assert.True(t, res.Denied, "deny is recorded in the proxy")
	assert.False(t, httpAL.Allows("api.example.com", "443"))

	_, cmd = s.Update(msg, w)
	require.NotNil(t, cmd)
	assert.IsType(t, tea.QuitMsg{}, cmd())
}

func TestApproveScreen_DenyErrorStaysOpen(t *testing.T) {
	s, w, _, _, _ := makeApproveSetup(t, blockedEntry)
	_, cmd := s.Update(denyResultMsg{err: assert.AnError}, w)
	assert.Nil(t, cmd)
	assert.Error(t, w.Err())
	_, cmd = s.Update(checkResultMsg{res: CheckResult{Denied: true}}, w)
	assert.Nil(t, cmd, "pending error blocks auto-close")
}

func TestApproveScreen_DismissRecordsNothing(t *testing.T) {
	for _, key := range []string{"esc", "q"} {
		t.Run(key, func(t *testing.T) {
			s, w, _, _, _ := makeApproveSetup(t, blockedEntry)
			var cmd tea.Cmd
			if key == "esc" {
				_, cmd = s.Update(tea.KeyPressMsg{Code: tea.KeyEscape}, w)
			} else {
				_, cmd = s.Update(tea.KeyPressMsg{Code: 'q', Text: "q"}, w)
			}
			require.NotNil(t, cmd)
			assert.IsType(t, tea.QuitMsg{}, cmd())

			res, err := s.client.Check(blockedEntry)
			require.NoError(t, err)
			assert.Equal(t, CheckResult{}, res)
		})
	}
}

func TestApproveScreen_ClosesWhenDeniedElsewhere(t *testing.T) {
	s, w, _, _, _ := makeApproveSetup(t, blockedEntry)
	require.NoError(t, s.client.Deny(blockedEntry))
	msg := s.checkCmd()()
	_, cmd := s.Update(msg, w)
	require.NotNil(t, cmd)
	assert.IsType(t, tea.QuitMsg{}, cmd())
}

func TestApproveScreen_DNSSource(t *testing.T) {
	entry := proxy.LogEntry{Domain: "dns.example.com", Action: proxy.ActionBlock, Source: proxy.SourceDNS}
	s, w, _, dnsAL, _ := makeApproveSetup(t, entry)
	_, cmd := s.Update(tea.KeyPressMsg{Code: 'a', Text: "a"}, w)
	res := cmd().(allowResultMsg)
	require.NoError(t, res.err)
	assert.True(t, dnsAL.Allows("dns.example.com"))
}

func TestApproveScreen_ErrorStaysOpen(t *testing.T) {
	s, w, _, _, _ := makeApproveSetup(t, blockedEntry)
	_, cmd := s.Update(allowResultMsg{err: assert.AnError}, w)
	assert.Nil(t, cmd)
	assert.Error(t, w.Err())
}

func TestApproveScreen_ClosesWhenAllowedElsewhere(t *testing.T) {
	s, w, httpAL, _, _ := makeApproveSetup(t, blockedEntry)

	// First tick triggers a check; target not yet allowed, prompt stays.
	_, cmd := s.Update(tui.TickMsg{}, w)
	require.NotNil(t, cmd)
	msg := cmd()
	require.IsType(t, checkResultMsg{}, msg)
	_, cmd = s.Update(msg, w)
	assert.Nil(t, cmd)

	// Another client allows it; the next check closes the prompt.
	require.NoError(t, httpAL.Add([]string{"api.example.com:443"}))
	s.checkInFlight = false
	msg = s.checkCmd()()
	_, cmd = s.Update(msg, w)
	require.NotNil(t, cmd)
	assert.IsType(t, tea.QuitMsg{}, cmd())
}

func TestApproveScreen_CheckErrorKeepsPromptOpen(t *testing.T) {
	s, w, _, _, _ := makeApproveSetup(t, blockedEntry)
	_, cmd := s.Update(checkResultMsg{err: assert.AnError}, w)
	assert.Nil(t, cmd)
	assert.NoError(t, w.Err())
}

func TestApproveScreen_NoCheckWhileBusy(t *testing.T) {
	s, w, _, _, _ := makeApproveSetup(t, blockedEntry)
	s.busy = true
	_, cmd := s.Update(tui.TickMsg{}, w)
	assert.Nil(t, cmd)
}

func TestApproveScreen_FailedAllowStaysOpenUntilAcknowledged(t *testing.T) {
	s, w, _, _, _ := makeApproveSetup(t, blockedEntry)
	s.busy = true
	_, cmd := s.Update(allowResultMsg{err: assert.AnError}, w)
	assert.Nil(t, cmd)

	// The live allowlist may already contain the target (save failed after
	// the runtime allow). An allowed check must not hide the error.
	_, cmd = s.Update(checkResultMsg{res: CheckResult{Allowed: true}}, w)
	assert.Nil(t, cmd)
	assert.Error(t, w.Err())

	// Ticks stop polling once an error is pending.
	_, cmd = s.Update(tui.TickMsg{}, w)
	assert.Nil(t, cmd)

	// The user can still dismiss explicitly.
	_, cmd = s.Update(tea.KeyPressMsg{Code: tea.KeyEscape}, w)
	require.NotNil(t, cmd)
	assert.IsType(t, tea.QuitMsg{}, cmd())
}

func TestApproveScreen_RetryAfterFailedAllowClearsError(t *testing.T) {
	s, w, httpAL, _, _ := makeApproveSetup(t, blockedEntry)
	s.Update(allowResultMsg{err: assert.AnError}, w)

	_, cmd := s.Update(tea.KeyPressMsg{Code: 'a', Text: "a"}, w)
	require.NotNil(t, cmd)
	assert.NoError(t, w.Err())
	res := cmd().(allowResultMsg)
	require.NoError(t, res.err)
	assert.True(t, httpAL.Allows("api.example.com", "443"))
}
