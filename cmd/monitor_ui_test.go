package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bernd/vibepit/config"
	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeTestSetup(n int) (*monitorScreen, *tui.Window) {
	s := newMonitorScreen(&SessionInfo{
		SessionID:  "test123456",
		ProjectDir: "/home/user/project",
	}, nil, nil)
	header := &tui.HeaderInfo{ProjectDir: "/home/user/project", SessionID: "test123456"}
	w := tui.NewWindow(header, s)
	w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

	for i := range n {
		s.items = append(s.items, logItem{
			entry: proxy.LogEntry{
				Domain: fmt.Sprintf("domain%d.com", i),
				Action: proxy.ActionBlock,
				Source: proxy.SourceProxy,
			},
		})
	}
	s.cursor.ItemCount = len(s.items)
	if len(s.items) > 0 {
		s.cursor.Pos = len(s.items) - 1
	}
	return s, w
}

func footerKeyDescs(keys []tui.FooterKey) []string {
	var descs []string
	for _, k := range keys {
		descs = append(descs, k.Desc)
	}
	return descs
}

// connEvent and entryEvent build the WatchLogs messages the monitor consumes, so
// tests can drive the connection-state and log-delivery paths through Update.
func connEvent(connected bool) logEventMsg {
	return logEventMsg{ev: LogEvent{Kind: LogConnEvent, Connected: connected}, ok: true}
}

func entryEvent(e proxy.LogEntry) logEventMsg {
	return logEventMsg{ev: LogEvent{Kind: LogEntryEvent, Entry: e}, ok: true}
}

func TestWaitForEvent_ClosedChannelYieldsNotOk(t *testing.T) {
	ch := make(chan LogEvent)
	close(ch)

	msg := waitForEvent(ch)()
	ev, ok := msg.(logEventMsg)
	require.True(t, ok, "waitForEvent should yield a logEventMsg")
	assert.False(t, ev.ok, "a closed stream must yield ok=false so the reader stops")
}

func TestMonitorScreen_WindowSizeMsg(t *testing.T) {
	s, w := makeTestSetup(0)

	assert.Equal(t, 100, w.Width())
	assert.Equal(t, 40, w.Height())
	assert.Greater(t, w.VpHeight(), 0)
	assert.Equal(t, w.VpHeight(), s.cursor.VpHeight)
}

func TestMonitorScreen_ViewContainsHeader(t *testing.T) {
	_, w := makeTestSetup(0)
	view := w.View().Content
	assert.Contains(t, view, "I PITY THE VIBES")
}

func TestRenderLogLine(t *testing.T) {
	item := logItem{
		entry: proxy.LogEntry{
			Domain: "example.com",
			Port:   "443",
			Action: proxy.ActionAllow,
			Source: proxy.SourceProxy,
		},
	}
	line := renderLogLine(item, false)
	require.Contains(t, line, "example.com:443")
	require.Contains(t, line, "+")
}

func TestRenderLogLine_Block(t *testing.T) {
	item := logItem{
		entry: proxy.LogEntry{
			Domain: "evil.com",
			Action: proxy.ActionBlock,
			Source: proxy.SourceDNS,
			Reason: "not allowed",
		},
	}
	line := renderLogLine(item, false)
	require.Contains(t, line, "evil.com")
	require.Contains(t, line, "x")
	require.Contains(t, line, "not allowed")
}

func TestRenderLogLine_AllowStatuses(t *testing.T) {
	tests := []struct {
		name           string
		status         allowStatus
		expectedSymbol string
	}{
		{
			name:           "temp shows lowercase a",
			status:         statusTemp,
			expectedSymbol: "a",
		},
		{
			name:           "saved shows uppercase A",
			status:         statusSaved,
			expectedSymbol: "A",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := logItem{
				entry: proxy.LogEntry{
					Domain: "example.com",
					Port:   "443",
					Action: proxy.ActionBlock,
					Source: proxy.SourceProxy,
				},
				status: tt.status,
			}
			line := renderLogLine(item, false)
			require.Contains(t, line, tt.expectedSymbol)
			require.NotContains(t, line, "] x")
		})
	}
}

func TestRenderLogLine_Highlighted(t *testing.T) {
	item := logItem{
		entry: proxy.LogEntry{
			Domain: "example.com",
			Port:   "443",
			Action: proxy.ActionAllow,
			Source: proxy.SourceProxy,
		},
	}
	normal := renderLogLine(item, false)
	highlighted := renderLogLine(item, true)
	require.NotEqual(t, normal, highlighted, "highlighted line should differ from normal")
}

func TestMonitorScreen_AllowAction(t *testing.T) {
	t.Run("allowResultMsg updates item status", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.cursor.Pos = 2

		s.Update(allowResultMsg{index: 2, status: statusTemp}, w)
		assert.Equal(t, statusTemp, s.items[2].status)
	})

	t.Run("allowResultMsg with error sets window error", func(t *testing.T) {
		s, w := makeTestSetup(5)

		s.Update(allowResultMsg{index: 0, err: fmt.Errorf("connection failed")}, w)
		assert.Error(t, w.Err())
		assert.Contains(t, w.Err().Error(), "connection failed")
	})

	t.Run("allowResultMsg saved status", func(t *testing.T) {
		s, w := makeTestSetup(5)

		s.Update(allowResultMsg{index: 3, status: statusSaved}, w)
		assert.Equal(t, statusSaved, s.items[3].status)
	})
}

func TestMonitorScreen_FlashOnAlreadyAllowed(t *testing.T) {
	s, w := makeTestSetup(5)
	s.items[2].entry.Action = proxy.ActionAllow // not blocked
	s.cursor.Pos = 2
	s.Update(tea.KeyPressMsg{Code: 'a', Text: "a"}, w)
	assert.Equal(t, "already allowed", w.Flash())
}

func TestMonitorScreen_CursorNavigation(t *testing.T) {
	t.Run("j moves cursor down", func(t *testing.T) {
		s, w := makeTestSetup(20)
		s.cursor.Pos = 5
		s.Update(tea.KeyPressMsg{Code: 'j', Text: "j"}, w)
		assert.Equal(t, 6, s.cursor.Pos)
	})

	t.Run("k moves cursor up", func(t *testing.T) {
		s, w := makeTestSetup(20)
		s.cursor.Pos = 5
		s.Update(tea.KeyPressMsg{Code: 'k', Text: "k"}, w)
		assert.Equal(t, 4, s.cursor.Pos)
	})

	t.Run("j at end stays at end", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.cursor.Pos = 4
		s.Update(tea.KeyPressMsg{Code: 'j', Text: "j"}, w)
		assert.Equal(t, 4, s.cursor.Pos)
	})

	t.Run("k at start stays at start", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.cursor.Pos = 0
		s.Update(tea.KeyPressMsg{Code: 'k', Text: "k"}, w)
		assert.Equal(t, 0, s.cursor.Pos)
	})

	t.Run("G jumps to end", func(t *testing.T) {
		s, w := makeTestSetup(20)
		s.cursor.Pos = 0
		s.Update(tea.KeyPressMsg{Code: 'G', Text: "G"}, w)
		assert.Equal(t, 19, s.cursor.Pos)
	})

	t.Run("g jumps to start", func(t *testing.T) {
		s, w := makeTestSetup(20)
		s.cursor.Pos = 15
		s.Update(tea.KeyPressMsg{Code: 'g', Text: "g"}, w)
		assert.Equal(t, 0, s.cursor.Pos)
	})
}

func TestMonitorScreen_Footer(t *testing.T) {
	t.Run("shows base keybindings", func(t *testing.T) {
		s, w := makeTestSetup(5)
		keys := s.FooterKeys(w)
		descs := footerKeyDescs(keys)
		assert.Contains(t, descs, "navigate")
		assert.Contains(t, descs, "jump")
		// "quit" is added by Window, verify via full view
		view := w.View().Content
		assert.Contains(t, view, "quit")
	})

	t.Run("shows allow keys on blocked entry", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.cursor.Pos = 2
		keys := s.FooterKeys(w)
		descs := footerKeyDescs(keys)
		assert.Contains(t, descs, "allow")
		assert.Contains(t, descs, "allow+save")
	})

	t.Run("hides allow keys on allowed entry", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.items[2].entry.Action = proxy.ActionAllow
		s.cursor.Pos = 2
		keys := s.FooterKeys(w)
		descs := footerKeyDescs(keys)
		assert.NotContains(t, descs, "allow")
		assert.NotContains(t, descs, "allow+save")
	})

	t.Run("shows save key on temp-allowed entry", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.items[2].status = statusTemp
		s.cursor.Pos = 2
		keys := s.FooterKeys(w)
		descs := footerKeyDescs(keys)
		assert.Contains(t, descs, "save")
		assert.NotContains(t, descs, "allow+save")
	})

	t.Run("shows new message count", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.newCount = 3
		status := s.FooterStatus(w)
		assert.Contains(t, status, "3 new")
	})

	t.Run("shows connection error", func(t *testing.T) {
		_, w := makeTestSetup(5)
		w.SetError(fmt.Errorf("connection refused"))
		view := w.View().Content
		assert.Contains(t, view, "connection refused")
	})

	t.Run("shows flash message", func(t *testing.T) {
		_, w := makeTestSetup(5)
		w.SetFlash("already allowed")
		view := w.View().Content
		assert.Contains(t, view, "already allowed")
	})

	t.Run("error takes priority over flash", func(t *testing.T) {
		_, w := makeTestSetup(5)
		w.SetError(fmt.Errorf("connection refused"))
		w.SetFlash("already allowed")
		view := w.View().Content
		assert.Contains(t, view, "connection refused")
		assert.NotContains(t, view, "already allowed")
	})
}

func TestMonitorScreen_NewCount(t *testing.T) {
	t.Run("increments when cursor not at end", func(t *testing.T) {
		s, w := makeTestSetup(5)
		// Shrink viewport so items exceed visible area.
		w.Update(tea.WindowSizeMsg{Width: 100, Height: 6})
		s.cursor.Pos = 2 // not at end (4)

		s.Update(entryEvent(proxy.LogEntry{
			Domain: "new.com", Action: proxy.ActionAllow, Source: proxy.SourceProxy,
		}), w)

		assert.Equal(t, 1, s.newCount)
		assert.Equal(t, 2, s.cursor.Pos)
	})

	t.Run("resets when cursor reaches end via G", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.cursor.Pos = 2
		s.newCount = 10
		s.Update(tea.KeyPressMsg{Code: 'G', Text: "G"}, w)
		assert.Equal(t, 0, s.newCount)
		assert.Equal(t, 4, s.cursor.Pos)
	})
}

func TestMonitorScreen_StartsWatchOnFirstTick(t *testing.T) {
	bus, creds := newCmdTestBus(t)
	require.NoError(t, bus.RegisterHandlers())
	client := newCmdTestClient(t, bus, creds)

	s := newMonitorScreen(&SessionInfo{
		SessionID:  "test123456",
		ProjectDir: "/home/user/project",
	}, client, nil)
	header := &tui.HeaderInfo{ProjectDir: "/home/user/project", SessionID: "test123456"}
	w := tui.NewWindow(header, s)
	w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

	// First tick opens the WatchLogs stream exactly once and arms a reader.
	_, cmd := s.Update(tui.TickMsg{}, w)
	require.NotNil(t, cmd, "first tick should open the log watch")
	require.NotNil(t, s.events, "first tick should store the event stream")

	// Subsequent ticks must not open another watch.
	prev := s.events
	_, secondCmd := s.Update(tui.TickMsg{}, w)
	assert.Nil(t, secondCmd, "should not re-open the watch after the first tick")
	assert.Equal(t, prev, s.events, "the event stream must not be replaced on later ticks")

	// A published entry flows through the stream and yields a log-entry event.
	bus.LogPublisher().PublishLog(proxy.LogEntry{Domain: "api.openai.com", Port: "443", Action: proxy.ActionBlock, Source: proxy.SourceProxy})
	require.NoError(t, bus.FlushPublishes(2*time.Second))

	msg := cmd()
	ev, ok := msg.(logEventMsg)
	require.True(t, ok, "watch read should yield a logEventMsg")
	require.True(t, ev.ok)
	require.Equal(t, LogEntryEvent, ev.ev.Kind)
	assert.Equal(t, "api.openai.com", ev.ev.Entry.Domain)
}

// TestMonitorScreen_TransitionBackCancelsWatch verifies leaving the screen
// cancels the watch context, which closes the event stream so the reader exits
// rather than leaking.
func TestMonitorScreen_TransitionBackCancelsWatch(t *testing.T) {
	bus, creds := newCmdTestBus(t)
	require.NoError(t, bus.RegisterHandlers())
	client := newCmdTestClient(t, bus, creds)

	s := newMonitorScreen(&SessionInfo{SessionID: "test123456"}, client,
		func() tui.Screen { return &testStubScreen{} })
	header := &tui.HeaderInfo{SessionID: "test123456"}
	w := tui.NewWindow(header, s)
	w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

	_, cmd := s.Update(tui.TickMsg{}, w)
	require.NotNil(t, cmd, "first tick opens the watch")
	events := s.events
	require.NotNil(t, events)

	screen := s.transitionBack(w)
	_, isStub := screen.(*testStubScreen)
	assert.True(t, isStub, "transitionBack should return the onBack screen")

	// Canceling the watch closes the stream; a pending read yields ok=false.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("watch channel did not close after transitionBack canceled the context")
		}
	}
}

func TestMonitorScreen_AllowCmd_SourceRouting(t *testing.T) {
	makeScreen := func(t *testing.T) (*monitorScreen, *proxy.HTTPAllowlist, *proxy.DNSAllowlist, string) {
		t.Helper()
		projectDir := t.TempDir()
		projectPath := config.DefaultProjectPath(projectDir)
		require.NoError(t, os.MkdirAll(filepath.Dir(projectPath), 0o755))
		require.NoError(t, os.WriteFile(projectPath, []byte("presets:\n  - default\n"), 0o644))

		httpAllowlist, dnsAllowlist, client := newCmdTestClientWithAllowlists(t)
		screen := newMonitorScreen(&SessionInfo{
			SessionID:  "test123456",
			ProjectDir: projectDir,
		}, client, nil)

		return screen, httpAllowlist, dnsAllowlist, projectPath
	}

	t.Run("proxy source uses HTTP allow and saves to allow-http", func(t *testing.T) {
		screen, httpAllowlist, dnsAllowlist, projectPath := makeScreen(t)

		msg := screen.allowCmd(0, proxy.LogEntry{
			Domain: "api.openai.com",
			Port:   "443",
			Source: proxy.SourceProxy,
		}, true)()
		result, ok := msg.(allowResultMsg)
		require.True(t, ok)
		require.NoError(t, result.err)
		assert.Equal(t, statusSaved, result.status)
		assert.True(t, httpAllowlist.Allows("api.openai.com", "443"))
		assert.False(t, dnsAllowlist.Allows("api.openai.com"))

		cfg, err := config.Load(filepath.Join(t.TempDir(), "missing.yaml"), projectPath)
		require.NoError(t, err)
		assert.Contains(t, cfg.Project.AllowHTTP, "api.openai.com:443")
		assert.NotContains(t, cfg.Project.AllowDNS, "api.openai.com")
	})

	t.Run("dns source uses DNS allow and saves to allow-dns", func(t *testing.T) {
		screen, httpAllowlist, dnsAllowlist, projectPath := makeScreen(t)

		msg := screen.allowCmd(0, proxy.LogEntry{
			Domain: "internal.example.com",
			Source: proxy.SourceDNS,
		}, true)()
		result, ok := msg.(allowResultMsg)
		require.True(t, ok)
		require.NoError(t, result.err)
		assert.Equal(t, statusSaved, result.status)
		assert.True(t, dnsAllowlist.Allows("internal.example.com"))
		assert.False(t, httpAllowlist.Allows("internal.example.com", "443"))

		cfg, err := config.Load(filepath.Join(t.TempDir(), "missing.yaml"), projectPath)
		require.NoError(t, err)
		assert.Contains(t, cfg.Project.AllowDNS, "internal.example.com")
		assert.NotContains(t, cfg.Project.AllowHTTP, "internal.example.com")
	})
}

func TestMonitorScreen_EscReturnsSessionScreen(t *testing.T) {
	s, w := makeTestSetup(5)
	stub := &testStubScreen{}
	s.onBack = func() tui.Screen { return stub }

	screen, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape}, w)
	assert.Equal(t, stub, screen, "Esc should return the onBack screen")
}

func TestMonitorScreen_EscWithoutOnBack(t *testing.T) {
	s, w := makeTestSetup(5)
	// onBack is nil — Esc should be ignored.
	screen, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape}, w)
	assert.Equal(t, s, screen, "Esc without onBack should stay on monitor")
}

func TestMonitorScreen_DisconnectTransition(t *testing.T) {
	t.Run("disconnect event starts disconnect timer", func(t *testing.T) {
		s, w := makeTestSetup(5)
		stub := &testStubScreen{}
		s.onBack = func() tui.Screen { return stub }

		s.Update(connEvent(false), w)
		assert.Equal(t, 0, s.disconnectTick, "disconnectTick should be 0 on first disconnect")
	})

	t.Run("transitions after 12 ticks (3s)", func(t *testing.T) {
		s, w := makeTestSetup(5)
		stub := &testStubScreen{}
		s.onBack = func() tui.Screen { return stub }

		s.Update(connEvent(false), w)

		// Simulate 11 ticks — should stay on monitor.
		for range 11 {
			screen, _ := s.Update(tui.TickMsg{}, w)
			assert.Equal(t, s, screen, "should not transition before 3s")
		}

		// 12th tick — should transition.
		screen, _ := s.Update(tui.TickMsg{}, w)
		assert.Equal(t, stub, screen, "should transition to session selector after 3s")
	})

	t.Run("no transition without onBack", func(t *testing.T) {
		s, w := makeTestSetup(5)
		// onBack is nil.
		s.Update(connEvent(false), w)

		for range 20 {
			screen, _ := s.Update(tui.TickMsg{}, w)
			assert.Equal(t, s, screen, "should not transition without onBack")
		}
	})

	t.Run("mid-session disconnect starts the timer", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.onBack = func() tui.Screen { return &testStubScreen{} }

		// A delivered log entry must not by itself drive connection state.
		s.Update(entryEvent(proxy.LogEntry{Domain: "a.com"}), w)
		require.Equal(t, -1, s.disconnectTick, "log delivery does not change connection state")

		// ...then the proxy dies mid-session.
		s.Update(connEvent(false), w)
		assert.Equal(t, 0, s.disconnectTick, "disconnect should arm the timer")
	})

	t.Run("mid-session disconnect transitions after grace", func(t *testing.T) {
		s, w := makeTestSetup(5)
		stub := &testStubScreen{}
		s.onBack = func() tui.Screen { return stub }

		s.Update(connEvent(false), w)
		for range disconnectGraceTicks - 1 {
			screen, _ := s.Update(tui.TickMsg{}, w)
			assert.Equal(t, s, screen, "should not transition before grace elapses")
		}
		screen, _ := s.Update(tui.TickMsg{}, w)
		assert.Equal(t, stub, screen, "should transition after grace")
	})

	t.Run("reconnect cancels the timer", func(t *testing.T) {
		s, w := makeTestSetup(5)
		stub := &testStubScreen{}
		s.onBack = func() tui.Screen { return stub }

		s.Update(connEvent(false), w)
		require.Equal(t, 0, s.disconnectTick, "armed on disconnect")

		s.Update(connEvent(true), w)
		assert.Equal(t, -1, s.disconnectTick, "reconnect should disarm the timer")

		// Subsequent ticks must not transition.
		for range disconnectGraceTicks + 2 {
			screen, _ := s.Update(tui.TickMsg{}, w)
			assert.Equal(t, s, screen, "should stay on monitor after reconnect")
		}
	})

	t.Run("standalone disconnect surfaces error without arming timer", func(t *testing.T) {
		s, w := makeTestSetup(5)
		// onBack is nil — standalone monitor with nowhere to return to.

		s.Update(connEvent(false), w)
		require.Error(t, w.Err(), "disconnect should surface an error in standalone mode")
		assert.Contains(t, w.Err().Error(), "disconnected")
		assert.Equal(t, -1, s.disconnectTick, "timer must not arm without onBack")

		// Ticks must not transition (there is no screen to go back to).
		for range disconnectGraceTicks + 2 {
			screen, _ := s.Update(tui.TickMsg{}, w)
			assert.Equal(t, s, screen, "standalone monitor should stay put")
		}
	})
}

func TestMonitorScreen_EscResetsHeader(t *testing.T) {
	s, w := makeTestSetup(5)
	stub := &testStubScreen{}
	s.onBack = func() tui.Screen { return stub }

	// Header currently shows session info.
	s.Update(tea.KeyPressMsg{Code: tea.KeyEscape}, w)

	// After Esc, header should be reset to selector mode.
	view := w.View().Content
	assert.Contains(t, view, "session selector")
}

func TestMonitorScreen_DisconnectFooterMessage(t *testing.T) {
	s, w := makeTestSetup(5)
	s.onBack = func() tui.Screen { return &testStubScreen{} }

	s.Update(connEvent(false), w)
	require.Error(t, w.Err())
	assert.Contains(t, w.Err().Error(), "test123456")
	assert.Contains(t, w.Err().Error(), "disconnected")
}

func TestMonitor_LiveDelivery(t *testing.T) {
	bus, creds := newCmdTestBus(t)
	require.NoError(t, bus.RegisterHandlers())

	bus.LogPublisher().PublishLog(proxy.LogEntry{Domain: "live.com", Action: proxy.ActionBlock, Source: proxy.SourceProxy})
	require.NoError(t, bus.FlushPublishes(2*time.Second))

	client := newCmdTestClient(t, bus, creds)

	ch := make(chan proxy.LogEntry, 8)
	stop, _, err := client.SubscribeLogs(context.Background(), ch)
	require.NoError(t, err)
	defer stop()

	select {
	case e := <-ch:
		assert.Equal(t, "live.com", e.Domain)
	case <-time.After(3 * time.Second):
		t.Fatal("did not receive live log entry within 3s")
	}
}
