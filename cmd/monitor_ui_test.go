package cmd

import (
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

func TestWaitForLog_UnblocksOnDone(t *testing.T) {
	ch := make(chan proxy.LogEntry) // unbuffered, never written
	done := make(chan struct{})
	cmd := waitForLog(ch, done)

	got := make(chan tea.Msg, 1)
	go func() { got <- cmd() }()

	// No entry will ever arrive; closing done must release the goroutine.
	close(done)
	select {
	case m := <-got:
		assert.Nil(t, m, "waitForLog should yield nil and exit when done is closed")
	case <-time.After(time.Second):
		t.Fatal("waitForLog leaked: it did not unblock when done was closed")
	}
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

		s.Update(logEntryMsg(proxy.LogEntry{
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

func TestMonitorScreen_StartsSubscriptionOnFirstTick(t *testing.T) {
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

	// First tick should create the channel and start the subscription exactly
	// once. The channel is created on the Update goroutine, not in the cmd.
	_, cmd := s.Update(tui.TickMsg{}, w)
	require.NotNil(t, cmd, "first tick should start the log subscription")
	require.NotNil(t, s.logCh, "Update should create the log channel on the main goroutine")

	// Subsequent ticks must not start another subscription.
	_, secondCmd := s.Update(tui.TickMsg{}, w)
	assert.Nil(t, secondCmd, "should not re-subscribe after the first tick")

	// The first tick batches [startSubscription, watchConn]. Run the
	// subscription command (index 0): it establishes the consumer and stores the
	// stop func in s.stopFn under the mutex (so it survives an early teardown).
	// watchConn (index 1) blocks on the conn-event channel, so it is not run here.
	batch, ok := cmd().(tea.BatchMsg)
	require.True(t, ok, "first tick should batch the subscription and conn watch")
	require.Len(t, batch, 2)
	started, ok := batch[0]().(subscriptionStartedMsg)
	require.True(t, ok, "subscription command should yield a subscriptionStartedMsg")
	s.mu.Lock()
	require.NotNil(t, s.stopFn, "subscription goroutine should store the stop func under the lock")
	s.mu.Unlock()

	// subscriptionStartedMsg arms the first channel read.
	_, readCmd := s.Update(started, w)
	require.NotNil(t, readCmd, "subscriptionStartedMsg should arm the channel read")

	// A published entry flows through the channel and yields a logEntryMsg.
	bus.LogPublisher().PublishLog(proxy.LogEntry{Domain: "api.openai.com", Port: "443", Action: proxy.ActionBlock, Source: proxy.SourceProxy})
	require.NoError(t, bus.FlushPublishes(2*time.Second))

	msg := readCmd()
	entry, ok := msg.(logEntryMsg)
	require.True(t, ok, "channel read should yield a logEntryMsg")
	assert.Equal(t, "api.openai.com", proxy.LogEntry(entry).Domain)
}

// TestMonitorScreen_TeardownBeforeSubscriptionStarted covers the race where the
// user navigates back after the subscription is armed but before its goroutine
// reports in: the goroutine must stop the consumer itself (not leak it) and not
// store a stop func into the dead screen.
func TestMonitorScreen_TeardownBeforeSubscriptionStarted(t *testing.T) {
	bus, creds := newCmdTestBus(t)
	require.NoError(t, bus.RegisterHandlers())
	client := newCmdTestClient(t, bus, creds)

	s := newMonitorScreen(&SessionInfo{SessionID: "test123456"}, client,
		func() tui.Screen { return &testStubScreen{} })

	// Simulate transitionBack having already run (user left) before the
	// subscription goroutine completes.
	s.mu.Lock()
	s.torndown = true
	s.mu.Unlock()

	ch := make(chan proxy.LogEntry, logChannelBuffer)
	s.logCh = ch

	// Run the subscription goroutine body synchronously: SubscribeLogs succeeds
	// (client is live), then it must see torndown, stop the consumer, and yield
	// nil instead of a subscriptionStartedMsg.
	msg := s.startSubscription(ch)()
	assert.Nil(t, msg, "late subscription should self-clean and yield nil after teardown")

	s.mu.Lock()
	assert.Nil(t, s.stopFn, "stop func must not be stored on a torn-down screen")
	s.mu.Unlock()
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
	t.Run("subscribe error starts disconnect timer", func(t *testing.T) {
		s, w := makeTestSetup(5)
		stub := &testStubScreen{}
		s.onBack = func() tui.Screen { return stub }

		s.Update(subscribeErrMsg{err: fmt.Errorf("connection refused")}, w)
		assert.Equal(t, 0, s.disconnectTick, "disconnectTick should be 0 on first error")
	})

	t.Run("transitions after 12 ticks (3s)", func(t *testing.T) {
		s, w := makeTestSetup(5)
		stub := &testStubScreen{}
		s.onBack = func() tui.Screen { return stub }

		s.Update(subscribeErrMsg{err: fmt.Errorf("connection refused")}, w)

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
		s.Update(subscribeErrMsg{err: fmt.Errorf("connection refused")}, w)

		for range 20 {
			screen, _ := s.Update(tui.TickMsg{}, w)
			assert.Equal(t, s, screen, "should not transition without onBack")
		}
	})

	t.Run("mid-session disconnect starts the timer", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.onBack = func() tui.Screen { return &testStubScreen{} }

		// Simulate the connection being established and delivering a log...
		s.Update(logEntryMsg(proxy.LogEntry{Domain: "a.com"}), w)
		require.Equal(t, -1, s.disconnectTick, "connected state")

		// ...then the proxy dies mid-session.
		s.Update(connStatusMsg{connected: false}, w)
		assert.Equal(t, 0, s.disconnectTick, "disconnect should arm the timer")
	})

	t.Run("mid-session disconnect transitions after grace", func(t *testing.T) {
		s, w := makeTestSetup(5)
		stub := &testStubScreen{}
		s.onBack = func() tui.Screen { return stub }

		s.Update(connStatusMsg{connected: false}, w)
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

		s.Update(connStatusMsg{connected: false}, w)
		require.Equal(t, 0, s.disconnectTick, "armed on disconnect")

		s.Update(connStatusMsg{connected: true}, w)
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

		s.Update(connStatusMsg{connected: false}, w)
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

	s.Update(subscribeErrMsg{err: fmt.Errorf("connection refused")}, w)
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
	stop, _, err := client.SubscribeLogs(ch)
	require.NoError(t, err)
	defer stop()

	select {
	case e := <-ch:
		assert.Equal(t, "live.com", e.Domain)
	case <-time.After(3 * time.Second):
		t.Fatal("did not receive live log entry within 3s")
	}
}
