package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bernd/vibepit/config"
	"github.com/bernd/vibepit/overlay"
	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	"github.com/bernd/vibepit/vt"
	"github.com/charmbracelet/colorprofile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type approveFixture struct {
	s           *approveScreen
	w           *tui.Window
	proxy       *testProxy
	projectPath string
}

func makeApproveSetup(t *testing.T, entry proxy.LogEntry) *approveFixture {
	t.Helper()
	projectDir := t.TempDir()
	projectPath := config.DefaultProjectPath(projectDir)
	require.NoError(t, os.MkdirAll(filepath.Dir(projectPath), 0o755))
	require.NoError(t, os.WriteFile(projectPath, []byte("presets:\n  - default\n"), 0o644))

	tp := newTestProxy(t)
	session := &SessionInfo{SessionID: "test123456", ProjectDir: projectDir}
	s := newApproveScreen(session, tp.client, entry)
	w := tui.NewWindow(&tui.HeaderInfo{ProjectDir: projectDir, SessionID: session.SessionID}, s)
	w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return &approveFixture{s: s, w: w, proxy: tp, projectPath: projectPath}
}

var blockedEntry = proxy.LogEntry{
	Domain: "api.example.com",
	Port:   "443",
	Action: proxy.ActionBlock,
	Source: proxy.SourceProxy,
	Reason: "domain not in allowlist",
}

func TestApproveScreen_View(t *testing.T) {
	f := makeApproveSetup(t, blockedEntry)
	s, w := f.s, f.w
	view := s.View(w)
	assert.Contains(t, view, "api.example.com:443")
	assert.Contains(t, view, "domain not in allowlist")
}

func TestApproveScreen_ViewSanitizesUntrustedText(t *testing.T) {
	entry := blockedEntry
	entry.Domain = "evil\x1b[2J.example.com"
	entry.Reason = "bad\x9bthing\x7f"
	f := makeApproveSetup(t, entry)
	s, w := f.s, f.w
	view := s.View(w)
	assert.NotContains(t, view, "\x1b[2J")
	assert.NotContains(t, view, "\x9b")
	assert.NotContains(t, view, "\x7f")
	assert.NotContains(t, view, "\ufffd")
	assert.Contains(t, view, "evil[2J.example.com:443") // escape byte gone, text stays
	assert.Contains(t, view, "badthing")
}

func TestApproveScreen_FooterKeys(t *testing.T) {
	f := makeApproveSetup(t, blockedEntry)
	s, w := f.s, f.w
	assert.Equal(t, []string{"allow", "allow+save", "deny", "dismiss"}, footerKeyDescs(s.FooterKeys(w)))
}

func TestApproveScreen_Allow(t *testing.T) {
	tests := []struct {
		name      string
		key       string
		wantSaved bool
	}{
		{name: "a allows for session", key: "a"},
		{name: "A allows and saves", key: "A", wantSaved: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := makeApproveSetup(t, blockedEntry)
			s, w := f.s, f.w
			httpAL := f.proxy.http
			projectPath := f.projectPath

			_, cmd := s.Update(tea.KeyPressMsg{Code: rune(tt.key[0]), Text: tt.key}, w)
			require.NotNil(t, cmd)
			msg := cmd()
			res, ok := msg.(decisionResultMsg)
			require.True(t, ok)
			require.NoError(t, res.err)
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
	f := makeApproveSetup(t, blockedEntry)
	s, w := f.s, f.w
	httpAL := f.proxy.http
	_, cmd := s.Update(tea.KeyPressMsg{Code: 'n', Text: "n"}, w)
	require.NotNil(t, cmd)
	msg := cmd()
	require.IsType(t, decisionResultMsg{}, msg)
	require.NoError(t, msg.(decisionResultMsg).err)

	res, err := s.client.Check(blockedEntry)
	require.NoError(t, err)
	assert.True(t, res.Denied, "deny is recorded in the proxy")
	assert.False(t, httpAL.Allows("api.example.com", "443"))

	_, cmd = s.Update(msg, w)
	require.NotNil(t, cmd)
	assert.IsType(t, tea.QuitMsg{}, cmd())
}

func TestApproveScreen_DenyErrorStaysOpen(t *testing.T) {
	f := makeApproveSetup(t, blockedEntry)
	s, w := f.s, f.w
	_, cmd := s.Update(decisionResultMsg{err: assert.AnError}, w)
	assert.Nil(t, cmd)
	assert.Error(t, w.Err())
	_, cmd = s.Update(checkResultMsg{res: CheckResult{Denied: true}}, w)
	assert.Nil(t, cmd, "pending error blocks auto-close")
}

func TestApproveScreen_DismissRecordsNothing(t *testing.T) {
	for _, key := range []string{"esc", "q"} {
		t.Run(key, func(t *testing.T) {
			f := makeApproveSetup(t, blockedEntry)
			s, w := f.s, f.w
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
	f := makeApproveSetup(t, blockedEntry)
	s, w := f.s, f.w
	require.NoError(t, s.client.Deny(blockedEntry))
	msg := s.checkCmd()()
	_, cmd := s.Update(msg, w)
	require.NotNil(t, cmd)
	assert.IsType(t, tea.QuitMsg{}, cmd())
}

func TestApproveScreen_DNSSource(t *testing.T) {
	entry := proxy.LogEntry{Domain: "dns.example.com", Action: proxy.ActionBlock, Source: proxy.SourceDNS}
	f := makeApproveSetup(t, entry)
	s, w := f.s, f.w
	dnsAL := f.proxy.dns
	_, cmd := s.Update(tea.KeyPressMsg{Code: 'a', Text: "a"}, w)
	res := cmd().(decisionResultMsg)
	require.NoError(t, res.err)
	assert.True(t, dnsAL.Allows("dns.example.com"))
}

func TestApproveScreen_ErrorStaysOpen(t *testing.T) {
	f := makeApproveSetup(t, blockedEntry)
	s, w := f.s, f.w
	_, cmd := s.Update(decisionResultMsg{err: assert.AnError}, w)
	assert.Nil(t, cmd)
	assert.Error(t, w.Err())
}

func TestApproveScreen_ClosesWhenAllowedElsewhere(t *testing.T) {
	f := makeApproveSetup(t, blockedEntry)
	s, w := f.s, f.w
	httpAL := f.proxy.http

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
	f := makeApproveSetup(t, blockedEntry)
	s, w := f.s, f.w
	_, cmd := s.Update(checkResultMsg{err: assert.AnError}, w)
	assert.Nil(t, cmd)
	assert.NoError(t, w.Err())
}

func TestApproveScreen_NoCheckWhileBusy(t *testing.T) {
	f := makeApproveSetup(t, blockedEntry)
	s, w := f.s, f.w
	s.busy = true
	_, cmd := s.Update(tui.TickMsg{}, w)
	assert.Nil(t, cmd)
}

func TestApproveScreen_FailedAllowStaysOpenUntilAcknowledged(t *testing.T) {
	f := makeApproveSetup(t, blockedEntry)
	s, w := f.s, f.w
	s.busy = true
	_, cmd := s.Update(decisionResultMsg{err: assert.AnError}, w)
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
	f := makeApproveSetup(t, blockedEntry)
	s, w := f.s, f.w
	httpAL := f.proxy.http
	s.Update(decisionResultMsg{err: assert.AnError}, w)

	_, cmd := s.Update(tea.KeyPressMsg{Code: 'a', Text: "a"}, w)
	require.NotNil(t, cmd)
	assert.NoError(t, w.Err())
	res := cmd().(decisionResultMsg)
	require.NoError(t, res.err)
	assert.True(t, httpAL.Allows("api.example.com", "443"))
}

// The prompt's output passes overlay.NewFilter, which drops every query.
// The approve screen must look the same through it as on a cooked TTY,
// whatever TERM makes Bubble Tea's renderer use, and still take keys.
func TestApproveScreen_RendersThroughPromptFilter(t *testing.T) {
	for _, term := range []string{"xterm-256color", "xterm-ghostty", "screen", "linux"} {
		t.Run(term, func(t *testing.T) {
			tp := newTestProxy(t)
			session := &SessionInfo{SessionID: "test123456", ProjectDir: t.TempDir()}
			header := &tui.HeaderInfo{ProjectDir: session.ProjectDir, SessionID: session.SessionID}
			start := func(out io.Writer, in io.Reader) (*tea.Program, <-chan error) {
				p := tea.NewProgram(tui.NewWindow(header, newApproveScreen(session, tp.client, blockedEntry)),
					tea.WithInput(in),
					tea.WithOutput(out),
					tea.WithWindowSize(100, 30),
					tea.WithColorProfile(colorprofile.TrueColor),
					tea.WithEnvironment([]string{"TERM=" + term}),
					tea.WithoutSignalHandler(),
				)
				errc := make(chan error, 1)
				go func() {
					_, err := p.Run()
					errc <- err
				}()
				return p, errc
			}
			filtered, direct := newScreen(t, 100, 30), newScreen(t, 100, 30)
			keysR, keysW := io.Pipe()
			idleR, idleW := io.Pipe()
			t.Cleanup(func() {
				_ = keysW.Close()
				_ = idleW.Close()
			})
			_, filteredDone := start(overlay.NewFilter(filtered), keysR)
			directProg, directDone := start(onlcr{direct}, idleR)

			require.Eventually(t, func() bool {
				got := screenText(filtered)
				return strings.Contains(got, "Allow this connection?") && got == screenText(direct)
			}, 10*time.Second, 20*time.Millisecond)

			_, err := keysW.Write([]byte("a"))
			require.NoError(t, err)
			select {
			case err := <-filteredDone:
				require.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("the prompt didn't take the key")
			}
			assert.True(t, tp.http.Allows("api.example.com", "443"))
			directProg.Kill()
			<-directDone
		})
	}
}

// onlcr maps LF to CR LF, as a TTY with ONLCR does. Bubble Tea's renderer
// counts on it when its input isn't a TTY.
type onlcr struct{ w io.Writer }

func (o onlcr) Write(p []byte) (int, error) {
	if _, err := o.w.Write(bytes.ReplaceAll(p, []byte("\n"), []byte("\r\n"))); err != nil {
		return 0, err
	}
	return len(p), nil
}

func newScreen(t *testing.T, cols, rows int) *vt.Terminal {
	t.Helper()
	term, err := vt.NewTerminal(uint16(cols), uint16(rows))
	require.NoError(t, err)
	t.Cleanup(func() { _ = term.Close() })
	return term
}

// screenText is the visible screen as plain text, or "" on error, so it is
// safe inside require.Eventually.
func screenText(term *vt.Terminal) string {
	b, err := term.Format(vt.FormatOptions{Output: vt.OutputPlain, Trim: true, Region: vt.RegionScreen})
	if err != nil {
		return ""
	}
	return string(b)
}
