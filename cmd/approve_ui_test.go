package cmd

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
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

var approveEpoch = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

// stepClock is a window clock the tests move by hand.
type stepClock struct{ now time.Time }

func (c *stepClock) Now() time.Time { return c.now }
func (c *stepClock) Tick() tea.Cmd  { return nil }

type approveFixture struct {
	s           *approveScreen
	w           *tui.Window
	q           *promptQueue
	clock       *stepClock
	proxy       *testProxy
	projectPath string
}

func makeApproveSetup(t *testing.T, entries ...proxy.LogEntry) *approveFixture {
	t.Helper()
	q := newPromptQueue()
	for _, e := range entries {
		q.Add(e)
	}
	projectDir := t.TempDir()
	projectPath := config.DefaultProjectPath(projectDir)
	require.NoError(t, os.MkdirAll(filepath.Dir(projectPath), 0o755))
	require.NoError(t, os.WriteFile(projectPath, []byte("presets:\n  - default\n"), 0o644))

	tp := newTestProxy(t)
	session := &SessionInfo{SessionID: "test123456", ProjectDir: projectDir}
	s := newApproveScreen(session, tp.client, q)
	w := tui.NewWindow(&tui.HeaderInfo{ProjectDir: projectDir, SessionID: session.SessionID}, s)
	clock := &stepClock{now: approveEpoch}
	w.SetClock(clock)
	w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	// Bubble Tea draws before it delivers the first key, and the keys act
	// on what was drawn.
	w.View()
	return &approveFixture{s: s, w: w, q: q, clock: clock, proxy: tp, projectPath: projectPath}
}

var blockedEntry = proxy.LogEntry{
	Domain: "api.example.com",
	Port:   "443",
	Action: proxy.ActionBlock,
	Source: proxy.SourceProxy,
	Reason: "domain not in allowlist",
}

func singleQueue(e proxy.LogEntry) *promptQueue {
	q := newPromptQueue()
	q.Add(e)
	return q
}

func heldEntry(host string, id uint64, deadline time.Time) proxy.LogEntry {
	return proxy.LogEntry{
		ID: id, Domain: host, Port: "443", Action: proxy.ActionHold,
		Source: proxy.SourceProxy, Reason: "domain not in allowlist", Deadline: deadline,
	}
}

func TestApproveScreen_HeldCountdown(t *testing.T) {
	f := makeApproveSetup(t, heldEntry("docs.example.com", 1, approveEpoch.Add(6200*time.Millisecond)))
	view := f.s.View(f.w)
	assert.Contains(t, view, "held")
	assert.Contains(t, view, "docs.example.com:443")
	assert.Contains(t, view, "the agent is waiting · 7s left")
}

func TestApproveScreen_HoldEndsAtDeadline(t *testing.T) {
	f := makeApproveSetup(t, heldEntry("docs.example.com", 1, approveEpoch.Add(5*time.Second)))
	f.clock.now = approveEpoch.Add(5 * time.Second)
	view := f.s.View(f.w)
	assert.Contains(t, view, "hold ended · timed out")
	assert.NotContains(t, view, "waiting")
	assert.NotContains(t, view, "blocked")
}

// titleLine returns the view's line naming the first target.
func titleLine(t *testing.T, view, target string) string {
	t.Helper()
	for line := range strings.SplitSeq(view, "\n") {
		if strings.Contains(line, target) {
			return line
		}
	}
	require.FailNow(t, "no line names the target", "%s in %q", target, view)
	return ""
}

// A hold that ends without a decision is still the same prompt: the title
// keeps saying held, only dimmed, and the line under it says why it ended.
// The proxy refused nothing when the agent's request went away, so the
// screen must not claim a block.
func TestApproveScreen_SaysWhyHoldEnded(t *testing.T) {
	tests := []struct {
		name    string
		outcome proxy.HoldOutcome // empty: the deadline passed before the event
		want    string
	}{
		{"request gone", proxy.HoldAbandoned, "hold ended · request gone"},
		{"timeout", proxy.HoldTimeout, "hold ended · timed out"},
		{"nobody watching", proxy.HoldUnwatched, "hold ended · nobody watching"},
		{"deadline passed", "", "hold ended · timed out"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := heldEntry("docs.example.com", 1, approveEpoch.Add(5*time.Second))
			f := makeApproveSetup(t, e)
			live := titleLine(t, f.s.View(f.w), "docs.example.com:443")
			if tt.outcome != "" {
				f.q.EndHold(proxy.HoldEnd{ID: 1, HostKey: e.HostKey(), Outcome: tt.outcome})
			} else {
				f.clock.now = approveEpoch.Add(5 * time.Second)
			}
			view := f.s.View(f.w)
			ended := titleLine(t, view, "docs.example.com:443")
			assert.Contains(t, ended, "held")
			assert.NotEqual(t, live, ended, "the title is dimmed once nothing waits")
			assert.Contains(t, view, tt.want)
			assert.NotContains(t, view, "blocked")
			assert.NotContains(t, view, "waiting")
		})
	}
}

func TestApproveScreen_ListsEndedHoldsAsHeld(t *testing.T) {
	gone := heldEntry("two.example.com", 2, approveEpoch.Add(5*time.Second))
	f := makeApproveSetup(t,
		blockedEntry,
		gone,
		proxy.LogEntry{Domain: "three.example.com", Port: "443", Action: proxy.ActionBlock, Source: proxy.SourceProxy},
	)
	f.q.EndHold(proxy.HoldEnd{ID: 2, HostKey: gone.HostKey(), Outcome: proxy.HoldAbandoned})
	view := f.s.View(f.w)
	assert.Contains(t, view, "? two.example.com:443  hold ended · request gone")
	assert.Contains(t, view, "x three.example.com:443  blocked")
}

func TestApproveScreen_PlainBlockSaysNothingAboutHolds(t *testing.T) {
	f := makeApproveSetup(t, blockedEntry)
	assert.NotContains(t, f.s.View(f.w), "no longer held")
}

// The proxy runs in UTC; the user reads the time on their own clock.
func TestApproveScreen_ShowsLocalTime(t *testing.T) {
	orig := time.Local
	time.Local = time.FixedZone("UTC+1", 3600)
	t.Cleanup(func() { time.Local = orig })
	e := blockedEntry
	e.Time = time.Date(2026, 11, 2, 7, 26, 3, 0, time.UTC)
	f := makeApproveSetup(t, e)
	assert.Contains(t, f.s.View(f.w), "08:26:03")
}

func TestApproveScreen_ListsOtherTargets(t *testing.T) {
	f := makeApproveSetup(t,
		heldEntry("one.example.com", 1, approveEpoch.Add(5*time.Second)),
		heldEntry("two.example.com", 2, approveEpoch.Add(3*time.Second)),
		proxy.LogEntry{Domain: "three.example.com", Port: "443", Action: proxy.ActionBlock, Source: proxy.SourceProxy},
	)
	view := f.s.View(f.w)
	assert.Contains(t, view, "also waiting (2)")
	assert.Contains(t, view, "two.example.com:443")
	assert.Contains(t, view, "three.example.com:443")
}

func TestApproveScreen_DecidesHeadThenNext(t *testing.T) {
	f := makeApproveSetup(t,
		heldEntry("one.example.com", 1, approveEpoch.Add(5*time.Second)),
		heldEntry("two.example.com", 2, approveEpoch.Add(5*time.Second)),
	)
	s, w := f.s, f.w

	_, cmd := s.Update(tea.KeyPressMsg{Code: 'a', Text: "a"}, w)
	require.NotNil(t, cmd)
	msg := cmd()
	require.NoError(t, msg.(decisionResultMsg).err)
	assert.True(t, f.proxy.http.Allows("one.example.com", "443"))
	_, cmd = s.Update(msg, w)
	assert.Nil(t, cmd, "the screen stays open on the second target")
	assert.Equal(t, 1, f.q.Len())
	assert.Contains(t, s.View(w), "two.example.com:443")

	_, cmd = s.Update(tea.KeyPressMsg{Code: 'n', Text: "n"}, w)
	require.NotNil(t, cmd)
	msg = cmd()
	require.NoError(t, msg.(decisionResultMsg).err)
	res, err := s.client.Check(heldEntry("two.example.com", 2, time.Time{}))
	require.NoError(t, err)
	assert.True(t, res.Denied)
	_, cmd = s.Update(msg, w)
	require.NotNil(t, cmd)
	assert.IsType(t, tea.QuitMsg{}, cmd())
}

func TestApproveScreen_KeysActOnlyOnTheTargetShown(t *testing.T) {
	for _, key := range []string{"a", "A", "n"} {
		t.Run(key, func(t *testing.T) {
			f := makeApproveSetup(t,
				heldEntry("one.example.com", 1, approveEpoch.Add(5*time.Second)),
				heldEntry("two.example.com", 2, approveEpoch.Add(5*time.Second)),
			)
			s, w := f.s, f.w
			require.Contains(t, s.View(w), "one.example.com:443")
			// The prompter settles the first target before the screen
			// draws again.
			f.q.EndHold(proxy.HoldEnd{ID: 1, HostKey: heldEntry("one.example.com", 1, time.Time{}).HostKey(), Outcome: proxy.HoldAllowed})

			press := tea.KeyPressMsg{Code: rune(key[0]), Text: key}
			_, cmd := s.Update(press, w)
			assert.Nil(t, cmd, "two.example.com was never on screen")
			assert.False(t, s.busy)
			assert.Equal(t, []string{"two.example.com:443"}, queued(f.q))

			require.Contains(t, s.View(w), "two.example.com:443")
			_, cmd = s.Update(press, w)
			require.NotNil(t, cmd)
			require.NoError(t, cmd().(decisionResultMsg).err)
			res, err := s.client.Check(heldEntry("two.example.com", 2, time.Time{}))
			require.NoError(t, err)
			assert.True(t, res.Decided(), "the target on screen is decided")
		})
	}
}

func TestApproveScreen_QuitsWhenQueueEmptiedElsewhere(t *testing.T) {
	f := makeApproveSetup(t, blockedEntry)
	f.q.Remove(blockedEntry.Target())
	_, cmd := f.s.Update(tui.TickMsg{}, f.w)
	require.NotNil(t, cmd)
	assert.IsType(t, tea.QuitMsg{}, cmd())
}

func TestApproveScreen_TickQuitWaitsForBusyAndError(t *testing.T) {
	f := makeApproveSetup(t, blockedEntry)
	f.q.Remove(blockedEntry.Target())
	f.s.busy = true
	_, cmd := f.s.Update(tui.TickMsg{}, f.w)
	assert.Nil(t, cmd)
	f.s.busy = false
	f.w.SetError(assert.AnError)
	_, cmd = f.s.Update(tui.TickMsg{}, f.w)
	assert.Nil(t, cmd)
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
	assert.Equal(t, []string{"allow", "allow+save", "reject", "deny+save", "dismiss"}, footerKeyDescs(s.FooterKeys(w)))
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

func TestApproveScreen_SaveErrorSaysTheAllowWorked(t *testing.T) {
	f := makeApproveSetup(t, blockedEntry)
	s, w := f.s, f.w
	// A directory in place of the config file makes the save fail.
	require.NoError(t, os.Remove(f.projectPath))
	require.NoError(t, os.Mkdir(f.projectPath, 0o755))

	_, cmd := s.Update(tea.KeyPressMsg{Code: 'A', Text: "A"}, w)
	require.NotNil(t, cmd)
	_, cmd = s.Update(cmd(), w)
	assert.Nil(t, cmd, "the error stays on screen")
	assert.True(t, f.proxy.http.Allows("api.example.com", "443"))
	view := w.View().Content
	assert.NotContains(t, view, "connection error")
	assert.Contains(t, view, "allowed for this session")
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

func TestApproveScreen_DenyOnOlderProxy(t *testing.T) {
	// A proxy from before /deny answers 404. Nothing else can record the
	// decision, so the prompt closes; this client won't ask again.
	f := makeApproveSetup(t, blockedEntry)
	s, w := f.s, f.w
	s.client = testControlClient(t, http.NotFoundHandler())
	_, cmd := s.Update(tea.KeyPressMsg{Code: 'n', Text: "n"}, w)
	require.NotNil(t, cmd)
	msg := cmd()
	require.IsType(t, decisionResultMsg{}, msg)
	require.NoError(t, msg.(decisionResultMsg).err)

	_, cmd = s.Update(msg, w)
	require.NotNil(t, cmd)
	assert.IsType(t, tea.QuitMsg{}, cmd())
}

func TestApproveScreen_DenyErrorStaysOpen(t *testing.T) {
	f := makeApproveSetup(t, blockedEntry)
	s, w := f.s, f.w
	_, cmd := s.Update(decisionResultMsg{from: s, target: blockedEntry.Target(), err: assert.AnError}, w)
	assert.Nil(t, cmd)
	assert.Error(t, w.Err())
	_, cmd = s.Update(checkResultMsg{decided: []proxy.Target{blockedEntry.Target()}}, w)
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

func TestApproveScreen_ReportsHowItEnded(t *testing.T) {
	one := heldEntry("one.example.com", 1, approveEpoch.Add(5*time.Second))
	two := heldEntry("two.example.com", 2, approveEpoch.Add(5*time.Second))
	quit := func(t *testing.T, cmd tea.Cmd) {
		t.Helper()
		require.NotNil(t, cmd)
		assert.IsType(t, tea.QuitMsg{}, cmd())
	}

	for _, key := range []tea.KeyPressMsg{{Code: tea.KeyEscape}, {Code: 'q', Text: "q"}, {Code: 'c', Mod: tea.ModCtrl}} {
		t.Run("dismissed with "+key.String(), func(t *testing.T) {
			f := makeApproveSetup(t, one, two)
			_, cmd := f.s.Update(key, f.w)
			quit(t, cmd)
			assert.Equal(t, promptResult{Dismissed: []proxy.Target{one.Target(), two.Target()}}, f.s.result())
		})
	}

	t.Run("dismissed after the queue emptied elsewhere", func(t *testing.T) {
		f := makeApproveSetup(t, one)
		f.q.Remove(one.Target())
		f.q.Add(two)
		f.s.View(f.w)
		_, cmd := f.s.Update(tea.KeyPressMsg{Code: tea.KeyEscape}, f.w)
		quit(t, cmd)
		assert.Equal(t, promptResult{Dismissed: []proxy.Target{two.Target()}}, f.s.result())
	})

	t.Run("closed on an empty queue", func(t *testing.T) {
		f := makeApproveSetup(t, one)
		_, cmd := f.s.Update(tea.KeyPressMsg{Code: 'a', Text: "a"}, f.w)
		require.NotNil(t, cmd)
		_, cmd = f.s.Update(cmd(), f.w)
		quit(t, cmd)
		// Queued as the screen closed: not dismissed, the next prompt shows it.
		f.q.Add(two)
		assert.Equal(t, promptResult{}, f.s.result())
	})
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

// A failed save still allowed the target for the session; the error says
// so first, since the footer may cut the rest off.
func TestApproveScreen_FailedSaveSaysAllowed(t *testing.T) {
	f := makeApproveSetup(t, blockedEntry)
	require.NoError(t, os.Remove(f.projectPath))
	require.NoError(t, os.Mkdir(f.projectPath, 0o755))

	_, cmd := f.s.Update(tea.KeyPressMsg{Code: 'A', Text: "A"}, f.w)
	res := cmd().(decisionResultMsg)
	require.Error(t, res.err)
	assert.True(t, strings.HasPrefix(res.err.Error(), "allowed for this session, not saved: "), res.err.Error())
	assert.True(t, f.proxy.http.Allows("api.example.com", "443"))
}

func TestApproveScreen_ErrorStaysOpen(t *testing.T) {
	f := makeApproveSetup(t, blockedEntry)
	s, w := f.s, f.w
	_, cmd := s.Update(decisionResultMsg{from: s, target: blockedEntry.Target(), err: assert.AnError}, w)
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
	_, cmd := s.Update(decisionResultMsg{from: s, target: blockedEntry.Target(), err: assert.AnError}, w)
	assert.Nil(t, cmd)

	// The live allowlist may already contain the target (save failed after
	// the runtime allow). An allowed check must not hide the error.
	_, cmd = s.Update(checkResultMsg{decided: []proxy.Target{blockedEntry.Target()}}, w)
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
	s.Update(decisionResultMsg{from: s, target: blockedEntry.Target(), err: assert.AnError}, w)

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
				p := tea.NewProgram(tui.NewWindow(header, newApproveScreen(session, tp.client, singleQueue(blockedEntry))),
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
				require.FailNow(t, "the prompt didn't take the key")
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

func TestApproveScreen_OtherTargetsFitTheViewport(t *testing.T) {
	f := makeApproveSetup(t)
	for i := range 60 {
		f.q.Add(proxy.LogEntry{Domain: fmt.Sprintf("h%d.example.com", i), Port: "443", Action: proxy.ActionBlock, Source: proxy.SourceProxy})
	}
	view := f.s.View(f.w)
	assert.LessOrEqual(t, len(strings.Split(view, "\n")), f.w.VpHeight())
	assert.Contains(t, view, "also waiting (59)")
	assert.Contains(t, view, " more")
}

func TestApproveScreen_DenySaveFailureBlocksAllowAndCanRetry(t *testing.T) {
	for _, entry := range []proxy.LogEntry{blockedEntry, {Action: proxy.ActionBlock, Source: proxy.SourceDNS, Domain: "api.example.com", Reason: "domain not in allowlist"}} {
		t.Run(string(entry.Source), func(t *testing.T) {
			f := makeApproveSetup(t, entry)
			require.NoError(t, os.Remove(f.projectPath))
			require.NoError(t, os.Mkdir(f.projectPath, 0755))
			_, command := f.s.Update(tea.KeyPressMsg{Code: 'N', Text: "N"}, f.w)
			require.NotNil(t, command)
			_, command = f.s.Update(command(), f.w)
			require.Nil(t, command)
			require.Error(t, f.w.Err())
			saveErr := f.w.Err()
			for _, key := range []string{"a", "A"} {
				_, command = f.s.Update(tea.KeyPressMsg{Code: rune(key[0]), Text: key}, f.w)
				assert.Nil(t, command)
				assert.Equal(t, saveErr, f.w.Err())
			}
			assert.NotContains(t, footerKeyDescs(f.s.FooterKeys(f.w)), "allow")
			assert.NotContains(t, footerKeyDescs(f.s.FooterKeys(f.w)), "allow+save")
			assert.Contains(t, f.s.View(f.w), "denied by rule")
			assert.Contains(t, f.s.View(f.w), "blocked")
			assert.NotContains(t, f.s.View(f.w), "hold ended")
			require.NoError(t, os.Remove(f.projectPath))
			_, command = f.s.Update(tea.KeyPressMsg{Code: 'N', Text: "N"}, f.w)
			require.NotNil(t, command)
			_, command = f.s.Update(command(), f.w)
			require.NoError(t, f.w.Err())
			assert.Equal(t, 0, f.q.Len())
			data, err := os.ReadFile(f.projectPath)
			require.NoError(t, err)
			assert.Contains(t, string(data), entry.Target().String())
		})
	}
}

func TestApproveScreen_HeldDenySaveFailureSurvivesSettlement(t *testing.T) {
	for _, before := range []bool{true, false} {
		for _, another := range []bool{false, true} {
			t.Run(fmt.Sprintf("eventBeforeResult=%v/another=%v", before, another), func(t *testing.T) {
				entry := heldEntry("retry.example.com", 1, approveEpoch.Add(time.Minute))
				f := makeApproveSetup(t, entry)
				if another {
					f.q.Add(heldEntry("next.example.com", 2, approveEpoch.Add(time.Minute)))
				}
				require.NoError(t, os.Remove(f.projectPath))
				require.NoError(t, os.Mkdir(f.projectPath, 0755))
				_, command := f.s.Update(tea.KeyPressMsg{Code: 'N', Text: "N"}, f.w)
				require.NotNil(t, command)
				_, duplicate := f.s.Update(tea.KeyPressMsg{Code: 'N', Text: "N"}, f.w)
				require.Nil(t, duplicate)
				result := command()
				end := func() { f.q.EndHold(proxy.HoldEnd{ID: 1, HostKey: entry.HostKey(), Outcome: proxy.HoldDenied}) }
				if before {
					end()
				}
				_, command = f.s.Update(result, f.w)
				require.Nil(t, command)
				if !before {
					end()
				}
				require.Error(t, f.w.Err())
				require.Contains(t, f.s.View(f.w), "retry.example.com:443")
				require.Contains(t, f.s.View(f.w), "Retry saving")
				require.Contains(t, f.s.View(f.w), "hold ended · denied")
				require.NotContains(t, f.s.View(f.w), "the agent is waiting")
				f.clock.now = approveEpoch.Add(2 * time.Minute)
				require.Contains(t, f.s.View(f.w), "hold ended · denied")
				require.NotContains(t, strings.Split(f.s.View(f.w), "also waiting")[0], "timed out")
				_, command = f.s.Update(tui.TickMsg{}, f.w)
				require.Nil(t, command)
				for _, key := range []string{"a", "A", "n"} {
					_, command = f.s.Update(tea.KeyPressMsg{Code: rune(key[0]), Text: key}, f.w)
					require.Nil(t, command)
				}
				require.NoError(t, os.Remove(f.projectPath))
				_, command = f.s.Update(tea.KeyPressMsg{Code: 'N', Text: "N"}, f.w)
				require.NotNil(t, command)
				f.s.Update(command(), f.w)
				require.NoError(t, f.w.Err())
				data, err := os.ReadFile(f.projectPath)
				require.NoError(t, err)
				require.Contains(t, string(data), "retry.example.com:443")
				require.NotContains(t, string(data), "next.example.com")
				if another {
					require.Contains(t, f.s.View(f.w), "next.example.com:443")
				} else {
					require.Empty(t, f.s.View(f.w))
				}
			})
		}
	}
}

func TestApproveScreen_DismissFailedHeldDenySave(t *testing.T) {
	entry := heldEntry("retry.example.com", 1, approveEpoch.Add(time.Minute))
	next := heldEntry("next.example.com", 2, approveEpoch.Add(time.Minute))
	f := makeApproveSetup(t, entry, next)
	require.NoError(t, os.Remove(f.projectPath))
	require.NoError(t, os.Mkdir(f.projectPath, 0755))
	_, command := f.s.Update(tea.KeyPressMsg{Code: 'N', Text: "N"}, f.w)
	require.NotNil(t, command)
	f.q.EndHold(proxy.HoldEnd{ID: 1, HostKey: entry.HostKey(), Outcome: proxy.HoldDenied})
	f.s.Update(command(), f.w)
	require.Error(t, f.w.Err())
	f.s.View(f.w)
	// esc skips only the pinned target; the next one can still be decided.
	_, command = f.s.Update(tea.KeyPressMsg{Code: tea.KeyEscape}, f.w)
	require.Nil(t, command)
	require.NoError(t, f.w.Err())
	require.Contains(t, f.s.View(f.w), "next.example.com:443")
	require.NotContains(t, f.s.View(f.w), "retry.example.com")
	require.True(t, footerHas(f.s.FooterKeys(f.w), "a", "allow"))
	_, command = f.s.Update(tea.KeyPressMsg{Code: 'a', Text: "a"}, f.w)
	require.NotNil(t, command, "the next target can be allowed")

	// q still dismisses everything at once.
	f = makeApproveSetup(t, entry, next)
	require.NoError(t, os.Remove(f.projectPath))
	require.NoError(t, os.Mkdir(f.projectPath, 0755))
	_, command = f.s.Update(tea.KeyPressMsg{Code: 'N', Text: "N"}, f.w)
	f.s.Update(command(), f.w)
	f.s.View(f.w)
	_, command = f.s.Update(tea.KeyPressMsg{Code: 'q', Text: "q"}, f.w)
	require.NotNil(t, command)
	require.IsType(t, tea.QuitMsg{}, command())
	require.ElementsMatch(t, []proxy.Target{entry.Target(), next.Target()}, f.s.result().Dismissed)
}
