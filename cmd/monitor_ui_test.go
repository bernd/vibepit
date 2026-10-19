package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bernd/vibepit/config"
	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func makeTestSetup(n int) (*monitorScreen, *tui.Window) {
	s := newMonitorScreen(&SessionInfo{
		SessionID:  "test123456",
		ProjectDir: "/home/user/project",
	}, nil, nil)
	s.tab = tabLogs
	header := &tui.HeaderInfo{ProjectDir: "/home/user/project", SessionID: "test123456"}
	w := tui.NewWindow(header, s)
	w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

	for i := range n {
		s.logs.items = append(s.logs.items, proxy.LogEntry{
			ID:     uint64(i + 1),
			Domain: fmt.Sprintf("domain%d.com", i),
			Port:   "443",
			Action: proxy.ActionBlock,
			Source: proxy.SourceProxy,
			Code:   proxy.BlockNotAllowlisted,
		})
	}
	s.logs.cursor.ItemCount = len(s.logs.items)
	if len(s.logs.items) > 0 {
		s.logs.cursor.Pos = len(s.logs.items) - 1
		s.logs.selecting = true
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

func TestMonitorScreen_WindowSizeMsg(t *testing.T) {
	s, w := makeTestSetup(0)

	assert.Equal(t, 100, w.Width())
	assert.Equal(t, 40, w.Height())
	assert.Greater(t, w.VpHeight(), 0)
	assert.Equal(t, w.VpHeight()-2, s.logs.cursor.VpHeight)
}

func TestMonitorScreen_ViewContainsHeader(t *testing.T) {
	_, w := makeTestSetup(0)
	view := w.View().Content
	assert.Contains(t, view, "I PITY THE VIBES")
}

func TestRenderLogLine(t *testing.T) {
	entry := proxy.LogEntry{
		Domain: "example.com",
		Port:   "443",
		Action: proxy.ActionAllow,
		Source: proxy.SourceProxy,
	}
	line := renderLogLine(entry, statusNone, false)
	require.Contains(t, line, "example.com:443")
	require.Contains(t, line, "+")
}

func TestRenderLogLine_Block(t *testing.T) {
	entry := proxy.LogEntry{
		Domain: "evil.com",
		Action: proxy.ActionBlock,
		Source: proxy.SourceDNS,
		Reason: "not allowed",
	}
	line := renderLogLine(entry, statusNone, false)
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
			entry := proxy.LogEntry{
				Domain: "example.com",
				Port:   "443",
				Action: proxy.ActionBlock,
				Source: proxy.SourceProxy,
			}
			line := renderLogLine(entry, tt.status, false)
			require.Contains(t, line, tt.expectedSymbol)
			require.NotContains(t, line, "] x")
		})
	}
}

func TestRenderLogLine_Highlighted(t *testing.T) {
	entry := proxy.LogEntry{
		Domain: "example.com",
		Port:   "443",
		Action: proxy.ActionAllow,
		Source: proxy.SourceProxy,
	}
	normal := renderLogLine(entry, statusNone, false)
	highlighted := renderLogLine(entry, statusNone, true)
	require.NotEqual(t, normal, highlighted, "highlighted line should differ from normal")
}

func TestMonitorScreen_AllowAction(t *testing.T) {
	t.Run("allowResultMsg updates host status", func(t *testing.T) {
		s, w := makeTestSetup(5)
		key := s.logs.items[2].HostKey()

		s.Update(allowResultMsg{from: s, key: key, status: statusTemp}, w)
		assert.Equal(t, statusTemp, s.allows[key])
		assert.Equal(t, statusTemp, s.allows.entryStatus(s.logs.items[2]))
	})

	t.Run("allowResultMsg applies by key", func(t *testing.T) {
		s, w := makeTestSetup(5)
		key := s.logs.items[2].HostKey()
		slices.Reverse(s.logs.items)

		s.Update(allowResultMsg{from: s, key: key, status: statusTemp}, w)
		assert.Equal(t, statusTemp, s.allows.entryStatus(s.logs.items[2]))
		assert.Equal(t, statusNone, s.allows.entryStatus(s.logs.items[0]))
	})

	t.Run("allowResultMsg with error flashes it", func(t *testing.T) {
		s, w := makeTestSetup(5)

		s.Update(allowResultMsg{from: s, key: s.logs.items[0].HostKey(), err: fmt.Errorf("connection failed")}, w)
		assert.Contains(t, w.Flash(), "failed: connection failed")
		assert.Empty(t, s.allows)

		s.Update(logsPollResultMsg{from: s}, w)
		assert.Contains(t, w.Flash(), "connection failed", "a successful poll must not hide the allow error")
	})

	t.Run("failed save keeps the runtime allow", func(t *testing.T) {
		s, w := makeTestSetup(5)
		key := s.logs.items[0].HostKey()

		s.Update(allowResultMsg{from: s, key: key, status: statusTemp, err: fmt.Errorf("load project config: denied")}, w)
		assert.Equal(t, statusTemp, s.allows[key])
		assert.Contains(t, w.Flash(), "saving failed: load project config: denied")
		assert.NoError(t, w.Err())
	})

	t.Run("allowResultMsg saved status", func(t *testing.T) {
		s, w := makeTestSetup(5)
		key := s.logs.items[3].HostKey()

		s.Update(allowResultMsg{from: s, key: key, status: statusSaved}, w)
		assert.Equal(t, statusSaved, s.allows[key])
	})

	t.Run("marker follows host", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.logs.items[3] = s.logs.items[1]
		s.logs.items[3].ID = 4
		key := s.logs.items[1].HostKey()

		s.Update(allowResultMsg{from: s, key: key, status: statusTemp}, w)
		for _, i := range []int{1, 3} {
			line := renderLogLine(s.logs.items[i], s.allows.entryStatus(s.logs.items[i]), false)
			assert.Contains(t, line, "a")
			assert.NotContains(t, line, "] x")
		}
	})

	t.Run("A on temp-allowed saves", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.logs.cursor.Pos = 2
		s.allows = allowState{s.logs.items[2].HostKey(): statusTemp}

		_, cmd := s.Update(tea.KeyPressMsg{Code: 'A', Text: "A"}, w)
		assert.NotNil(t, cmd)
		assert.NotEqual(t, "already allowed", w.Flash())
	})

	t.Run("a on CIDR block flashes cannot allow", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.logs.cursor.Pos = 2
		s.logs.items[2].Code = proxy.BlockCIDR
		s.logs.items[2].Reason = "resolved IP 10.0.0.1 is in blocked CIDR range"

		_, cmd := s.Update(tea.KeyPressMsg{Code: 'a', Text: "a"}, w)
		assert.Nil(t, cmd)
		assert.Equal(t, "cannot allow: resolved IP 10.0.0.1 is in blocked CIDR range", w.Flash())
		assert.NotContains(t, footerKeyDescs(s.FooterKeys(w)), "allow")
	})

	t.Run("a on other line of allowed host flashes already allowed", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.logs.items[3] = s.logs.items[1]
		s.logs.items[3].ID = 4
		s.allows = allowState{s.logs.items[1].HostKey(): statusTemp}
		s.logs.cursor.Pos = 3

		_, cmd := s.Update(tea.KeyPressMsg{Code: 'a', Text: "a"}, w)
		assert.Nil(t, cmd)
		assert.Equal(t, "already allowed", w.Flash())
	})
}

func TestMonitorScreen_FlashOnAlreadyAllowed(t *testing.T) {
	s, w := makeTestSetup(5)
	s.logs.items[2].Action = proxy.ActionAllow // not blocked
	s.logs.cursor.Pos = 2
	s.Update(tea.KeyPressMsg{Code: 'a', Text: "a"}, w)
	assert.Equal(t, "already allowed", w.Flash())
}

func TestMonitorScreen_CursorNavigation(t *testing.T) {
	t.Run("j moves cursor down", func(t *testing.T) {
		s, w := makeTestSetup(20)
		s.logs.cursor.Pos = 5
		s.Update(tea.KeyPressMsg{Code: 'j', Text: "j"}, w)
		assert.Equal(t, 6, s.logs.cursor.Pos)
	})

	t.Run("k moves cursor up", func(t *testing.T) {
		s, w := makeTestSetup(20)
		s.logs.cursor.Pos = 5
		s.Update(tea.KeyPressMsg{Code: 'k', Text: "k"}, w)
		assert.Equal(t, 4, s.logs.cursor.Pos)
	})

	t.Run("j at end stays at end", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.logs.cursor.Pos = 4
		s.Update(tea.KeyPressMsg{Code: 'j', Text: "j"}, w)
		assert.Equal(t, 4, s.logs.cursor.Pos)
	})

	t.Run("k at start stays at start", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.logs.cursor.Pos = 0
		s.Update(tea.KeyPressMsg{Code: 'k', Text: "k"}, w)
		assert.Equal(t, 0, s.logs.cursor.Pos)
	})

	t.Run("G jumps to end", func(t *testing.T) {
		s, w := makeTestSetup(20)
		s.logs.cursor.Pos = 0
		s.Update(tea.KeyPressMsg{Code: 'G', Text: "G"}, w)
		assert.Equal(t, 19, s.logs.cursor.Pos)
	})

	t.Run("g jumps to start", func(t *testing.T) {
		s, w := makeTestSetup(20)
		s.logs.cursor.Pos = 15
		s.Update(tea.KeyPressMsg{Code: 'g', Text: "g"}, w)
		assert.Equal(t, 0, s.logs.cursor.Pos)
	})
}

func TestMonitorScreen_NotAllowable(t *testing.T) {
	tests := []struct {
		name  string
		edit  func(e *proxy.LogEntry)
		flash string
	}{
		{
			name: "resolution failed",
			edit: func(e *proxy.LogEntry) {
				e.Code = proxy.BlockResolutionFailed
				e.Reason = "DNS resolution failed during CIDR check"
			},
			flash: "cannot allow: DNS resolution failed during CIDR check",
		},
		{
			name:  "IPv6 literal",
			edit:  func(e *proxy.LogEntry) { e.Domain = "2001:db8::1" },
			flash: "cannot allow: the allowlist can't hold [2001:db8::1]:443",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, w := makeTestSetup(5)
			tt.edit(&s.logs.items[2])
			s.logs.cursor.Pos = 2

			descs := footerKeyDescs(s.FooterKeys(w))
			assert.NotContains(t, descs, "allow")
			assert.NotContains(t, descs, "allow+save")

			for _, key := range []string{"a", "A"} {
				_, cmd := s.Update(tea.KeyPressMsg{Code: rune(key[0]), Text: key}, w)
				assert.Nil(t, cmd, "key %s", key)
				assert.Equal(t, tt.flash, w.Flash())
			}
		})
	}
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
		s.logs.cursor.Pos = 2
		keys := s.FooterKeys(w)
		descs := footerKeyDescs(keys)
		assert.Contains(t, descs, "allow")
		assert.Contains(t, descs, "allow+save")
	})

	t.Run("hides allow keys on allowed entry", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.logs.items[2].Action = proxy.ActionAllow
		s.logs.cursor.Pos = 2
		keys := s.FooterKeys(w)
		descs := footerKeyDescs(keys)
		assert.NotContains(t, descs, "allow")
		assert.NotContains(t, descs, "allow+save")
	})

	t.Run("shows save key on temp-allowed entry", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.allows = allowState{s.logs.items[2].HostKey(): statusTemp}
		s.logs.cursor.Pos = 2
		keys := s.FooterKeys(w)
		descs := footerKeyDescs(keys)
		assert.Contains(t, descs, "save")
		assert.NotContains(t, descs, "allow+save")
	})

	t.Run("shows new message count", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.logs.newCount = 3
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
		s.logs.cursor.Pos = 2 // not at end (4)

		s.Update(logsPollResultMsg{
			from: s,
			entries: []proxy.LogEntry{
				{ID: 100, Domain: "new.com", Action: proxy.ActionAllow, Source: proxy.SourceProxy},
			},
		}, w)

		assert.Equal(t, 1, s.logs.newCount)
		assert.Equal(t, 2, s.logs.cursor.Pos)
	})

	t.Run("resets when cursor reaches end via G", func(t *testing.T) {
		s, w := makeTestSetup(5)
		s.logs.cursor.Pos = 2
		s.logs.newCount = 10
		s.Update(tea.KeyPressMsg{Code: 'G', Text: "G"}, w)
		assert.Equal(t, 0, s.logs.newCount)
		assert.Equal(t, 4, s.logs.cursor.Pos)
	})
}

// runCmd executes cmd and any batched commands, returning their messages.
func runCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var msgs []tea.Msg
		for _, c := range batch {
			msgs = append(msgs, runCmd(c)...)
		}
		return msgs
	}
	return []tea.Msg{msg}
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

// fakeProxyClient serves /logs and /hosts. hostsStatus 404 simulates a proxy
// that predates the hosts endpoint.
func fakeProxyClient(t *testing.T, requests *[]string, hostsStatus int) *ControlClient {
	t.Helper()
	return &ControlClient{
		http: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				*requests = append(*requests, req.URL.RequestURI())
				switch req.URL.Path {
				case "/logs":
					return jsonResponse(http.StatusOK, `[{"id":11,"domain":"api.openai.com","port":"443","action":"block","source":"proxy"}]`), nil
				case "/hosts":
					if hostsStatus != http.StatusOK {
						return jsonResponse(hostsStatus, "404 page not found\n"), nil
					}
					return jsonResponse(http.StatusOK, `[{"source":"proxy","domain":"api.openai.com","port":"443","blocked":1,"last_action":"block","last_seen":"2026-10-16T20:00:00Z","last_block_code":"not_allowlisted"}]`), nil
				}
				t.Fatalf("unexpected request %s", req.URL)
				return nil, nil
			}),
		},
		baseURL: "https://proxy.local",
	}
}

func newPollingTestScreen(t *testing.T, client *ControlClient, onBack func() tui.Screen) (*monitorScreen, *tui.Window) {
	t.Helper()
	s := newMonitorScreen(&SessionInfo{
		SessionID:  "test123456",
		ProjectDir: "/home/user/project",
	}, client, onBack)
	header := &tui.HeaderInfo{ProjectDir: "/home/user/project", SessionID: "test123456"}
	w := tui.NewWindow(header, s)
	w.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return s, w
}

func TestMonitorScreen_IgnoresResultsFromOtherScreens(t *testing.T) {
	old, _ := newPollingTestScreen(t, nil, nil)
	s, w := newPollingTestScreen(t, nil, nil)
	s.hostsPollInFlight, s.logsPollInFlight = true, true
	key := proxy.HostKey{Source: proxy.SourceProxy, Domain: "old.com", Port: "443"}

	s.Update(hostsPollResultMsg{from: old, hosts: []proxy.HostStats{
		mkHost("old.com", "443", proxy.SourceProxy, time.Now(), 0, 1, proxy.ActionBlock),
	}}, w)
	s.Update(hostsPollResultMsg{from: old, err: ErrUnsupportedEndpoint}, w)
	s.Update(logsPollResultMsg{from: old, entries: []proxy.LogEntry{{ID: 1, Domain: "old.com"}}}, w)
	s.Update(allowResultMsg{from: old, key: key, status: statusTemp}, w)

	assert.Empty(t, s.hosts.hosts)
	assert.False(t, s.hosts.unsupported)
	assert.Empty(t, s.logs.items)
	assert.Empty(t, s.allows)
	assert.True(t, s.hostsPollInFlight, "a stale result must not clear the in-flight flag")
	assert.True(t, s.logsPollInFlight)
}

func TestMonitorScreen_TickPollingIsAsync(t *testing.T) {
	var requests []string
	s, w := newPollingTestScreen(t, fakeProxyClient(t, &requests, http.StatusOK), nil)

	_, cmd := s.Update(tui.TickMsg{}, w)
	require.NotNil(t, cmd, "tick should return async poll commands")
	assert.Empty(t, requests, "Update should not fetch synchronously")
	assert.True(t, s.logsPollInFlight)
	assert.True(t, s.hostsPollInFlight)

	_, secondCmd := s.Update(tui.TickMsg{}, w)
	assert.Nil(t, secondCmd, "should not start polls while they are in flight")

	for _, msg := range runCmd(cmd) {
		_, followCmd := s.Update(msg, w)
		assert.Nil(t, followCmd)
	}
	assert.ElementsMatch(t, []string{"/logs", "/hosts"}, requests, "first poll asks for the recent tail")
	assert.False(t, s.logsPollInFlight)
	assert.False(t, s.hostsPollInFlight)
	require.Len(t, s.logs.items, 1)
	assert.Equal(t, uint64(11), s.logs.pollCursor)
	assert.True(t, s.logs.loaded)
	require.Len(t, s.hosts.hosts, 1)
	assert.Contains(t, w.View().Content, "HOSTS (1)")
	assert.Contains(t, w.View().Content, "LOGS (1)")
}

func TestMonitorScreen_PollUsesStrictCursorAfterFirstLoad(t *testing.T) {
	var queries []string
	client := &ControlClient{
		http: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				queries = append(queries, req.URL.RawQuery)
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Body:       io.NopCloser(strings.NewReader("[]")),
					Header:     make(http.Header),
				}, nil
			}),
		},
		baseURL: "https://proxy.local",
	}
	s := newMonitorScreen(&SessionInfo{SessionID: "test123456"}, client, nil)
	w := tui.NewWindow(&tui.HeaderInfo{SessionID: "test123456"}, s)
	w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

	// First load on an empty log, then a later poll: the cursor stays 0 but
	// the second request must be strict, not another tail.
	s.Update(s.pollLogsCmd()(), w)
	s.pollLogsCmd()()
	assert.Equal(t, []string{"", "after=0"}, queries)
}

func TestMonitorScreen_Tabs(t *testing.T) {
	t.Run("defaults to hosts tab", func(t *testing.T) {
		s, w := newPollingTestScreen(t, nil, nil)
		assert.Equal(t, tabHosts, s.tab)
		view := w.View().Content
		assert.Contains(t, view, "HOSTS (0)")
		assert.Contains(t, view, "LOGS (0)")
		assert.Contains(t, view, "0 hosts")
	})

	t.Run("tab switches tabs", func(t *testing.T) {
		s, w := newPollingTestScreen(t, nil, nil)
		s.Update(tea.KeyPressMsg{Code: tea.KeyTab}, w)
		assert.Equal(t, tabLogs, s.tab)
		s.Update(tea.KeyPressMsg{Code: tea.KeyTab}, w)
		assert.Equal(t, tabHosts, s.tab)
	})

	t.Run("footer per tab", func(t *testing.T) {
		s, w := newPollingTestScreen(t, nil, nil)
		descs := footerKeyDescs(s.FooterKeys(w))
		assert.Contains(t, descs, "logs")
		assert.Contains(t, descs, "sort: newest")

		s.Update(tea.KeyPressMsg{Code: tea.KeyTab}, w)
		descs = footerKeyDescs(s.FooterKeys(w))
		assert.Contains(t, descs, "hosts")
		assert.NotContains(t, descs, "sort: newest")
	})

	t.Run("underlines only the active tab", func(t *testing.T) {
		s, w := newPollingTestScreen(t, nil, nil)
		lines := strings.Split(ansi.Strip(s.renderTabs()), "\n")
		require.Len(t, lines, 2)
		assert.Equal(t, strings.Repeat("─", len("HOSTS (0)")), lines[1])

		s.Update(tea.KeyPressMsg{Code: tea.KeyTab}, w)
		lines = strings.Split(ansi.Strip(s.renderTabs()), "\n")
		require.Len(t, lines, 2)
		assert.Equal(t, strings.Repeat(" ", len("HOSTS (0)   "))+strings.Repeat("─", len("LOGS (0)")), lines[1])
	})

	t.Run("keys reach the hosts view", func(t *testing.T) {
		s, w := newPollingTestScreen(t, nil, nil)
		s.Update(tea.KeyPressMsg{Code: 's', Text: "s"}, w)
		assert.Equal(t, sortHost, s.hosts.sort)
	})
}

func TestMonitorScreen_HostsPolling(t *testing.T) {
	t.Run("hosts poll result populates view", func(t *testing.T) {
		s, w := newPollingTestScreen(t, nil, nil)
		s.Update(hostsPollResultMsg{from: s, hosts: []proxy.HostStats{
			mkHost("blocked.example.com", "443", proxy.SourceProxy, time.Now(), 0, 2, proxy.ActionBlock),
		}}, w)
		view := ansi.Strip(w.View().Content)
		assert.Contains(t, view, "HOSTS (1)")
		assert.Contains(t, view, "blocked.example.com")
	})

	t.Run("unsupported proxy falls back to logs", func(t *testing.T) {
		var requests []string
		s, w := newPollingTestScreen(t, fakeProxyClient(t, &requests, http.StatusNotFound), func() tui.Screen { return &testStubScreen{} })

		_, cmd := s.Update(tui.TickMsg{}, w)
		for _, msg := range runCmd(cmd) {
			s.Update(msg, w)
		}
		assert.Equal(t, tabLogs, s.tab)
		assert.Equal(t, -1, s.disconnectTick)
		assert.NoError(t, w.Err())

		s.Update(tea.KeyPressMsg{Code: tea.KeyTab}, w)
		assert.Contains(t, w.View().Content, "proxy does not support the hosts view, restart the session")

		requests = nil
		for range 8 {
			_, cmd := s.Update(tui.TickMsg{}, w)
			runCmd(cmd)
		}
		assert.NotContains(t, requests, "/hosts")
		assert.Contains(t, requests, "/logs?after=11")
	})

	t.Run("hosts poll error that is not a connection failure stays on hosts tab", func(t *testing.T) {
		s, w := newPollingTestScreen(t, nil, func() tui.Screen { return &testStubScreen{} })
		s.Update(hostsPollResultMsg{from: s, err: fmt.Errorf("GET /hosts: 500 Internal Server Error")}, w)
		assert.Equal(t, -1, s.disconnectTick)
		assert.Equal(t, tabHosts, s.tab)
		assert.Contains(t, ansi.Strip(w.View().Content), "cannot load hosts: GET /hosts: 500 Internal Server Error")

		s.Update(hostsPollResultMsg{from: s, hosts: []proxy.HostStats{}}, w)
		assert.NotContains(t, ansi.Strip(w.View().Content), "cannot load hosts")
	})

	t.Run("hosts poll connection failure stays on hosts tab", func(t *testing.T) {
		s, w := newPollingTestScreen(t, nil, func() tui.Screen { return &testStubScreen{} })
		timeout := &url.Error{Op: "Get", URL: "https://proxy.local/hosts", Err: context.DeadlineExceeded}
		s.Update(hostsPollResultMsg{from: s, err: fmt.Errorf("GET /hosts: %w", timeout)}, w)
		assert.Equal(t, -1, s.disconnectTick, "only the logs poll decides the session is gone")
		assert.NoError(t, w.Err())
		assert.Contains(t, ansi.Strip(w.View().Content), "cannot load hosts")
	})
}

func TestMonitorScreen_HostsAllow(t *testing.T) {
	makeScreen := func(t *testing.T, code proxy.BlockCode, reason string) (*monitorScreen, *tui.Window, *proxy.HTTPAllowlist) {
		t.Helper()
		httpAllowlist, err := proxy.NewHTTPAllowlist(nil)
		require.NoError(t, err)
		dnsAllowlist, err := proxy.NewDNSAllowlist(nil)
		require.NoError(t, err)
		client := testControlClient(t, proxy.NewControlAPI(proxy.NewLogBuffer(100), nil, httpAllowlist, dnsAllowlist))
		s, w := newPollingTestScreen(t, client, nil)

		h := mkHost("a.com", "443", proxy.SourceProxy, time.Now().Add(-time.Minute), 0, 1, proxy.ActionBlock)
		h.LastBlockCode = code
		h.LastBlockReason = reason
		s.Update(hostsPollResultMsg{from: s, hosts: []proxy.HostStats{h}}, w)
		s.Update(tea.KeyPressMsg{Code: 'j', Text: "j"}, w)
		return s, w, httpAllowlist
	}

	t.Run("a on hosts tab allows selected host", func(t *testing.T) {
		s, w, httpAllowlist := makeScreen(t, proxy.BlockNotAllowlisted, "domain not in allowlist")

		_, cmd := s.Update(tea.KeyPressMsg{Code: 'a', Text: "a"}, w)
		require.NotNil(t, cmd)
		for _, msg := range runCmd(cmd) {
			s.Update(msg, w)
		}

		key := proxy.HostKey{Source: proxy.SourceProxy, Domain: "a.com", Port: "443"}
		assert.Equal(t, statusTemp, s.allows[key])
		assert.True(t, httpAllowlist.Allows("a.com", "443"))
		assert.Equal(t, "allowed a.com:443", w.Flash())
		assert.Contains(t, ansi.Strip(w.View().Content), "allowed*")
	})

	t.Run("a on hosts tab with CIDR block", func(t *testing.T) {
		reason := "resolved IP 10.0.0.1 is in blocked CIDR range"
		s, w, _ := makeScreen(t, proxy.BlockCIDR, reason)

		_, cmd := s.Update(tea.KeyPressMsg{Code: 'a', Text: "a"}, w)
		assert.Nil(t, cmd)
		assert.Equal(t, "cannot allow: "+reason, w.Flash())
	})
}

func TestMonitorScreen_AllowCmd_SourceRouting(t *testing.T) {
	makeScreen := func(t *testing.T) (*monitorScreen, *proxy.HTTPAllowlist, *proxy.DNSAllowlist, string) {
		t.Helper()
		projectDir := t.TempDir()
		projectPath := config.DefaultProjectPath(projectDir)
		require.NoError(t, os.MkdirAll(filepath.Dir(projectPath), 0o755))
		require.NoError(t, os.WriteFile(projectPath, []byte("presets:\n  - default\n"), 0o644))

		httpAllowlist, err := proxy.NewHTTPAllowlist(nil)
		require.NoError(t, err)
		dnsAllowlist, err := proxy.NewDNSAllowlist(nil)
		require.NoError(t, err)
		api := proxy.NewControlAPI(proxy.NewLogBuffer(100), nil, httpAllowlist, dnsAllowlist)
		client := testControlClient(t, api)
		screen := newMonitorScreen(&SessionInfo{
			SessionID:  "test123456",
			ProjectDir: projectDir,
		}, client, nil)

		return screen, httpAllowlist, dnsAllowlist, projectPath
	}

	t.Run("proxy source uses HTTP allow and saves to allow-http", func(t *testing.T) {
		screen, httpAllowlist, dnsAllowlist, projectPath := makeScreen(t)

		key := proxy.HostKey{Source: proxy.SourceProxy, Domain: "api.openai.com", Port: "443"}
		msg := screen.allowCmd(key, true)()
		result, ok := msg.(allowResultMsg)
		require.True(t, ok)
		require.NoError(t, result.err)
		assert.Equal(t, key, result.key)
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

		key := proxy.HostKey{Source: proxy.SourceDNS, Domain: "internal.example.com"}
		msg := screen.allowCmd(key, true)()
		result, ok := msg.(allowResultMsg)
		require.True(t, ok)
		require.NoError(t, result.err)
		assert.Equal(t, key, result.key)
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
	assert.Contains(t, footerKeyDescs(s.FooterKeys(w)), "clear")

	screen, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape}, w)
	assert.Equal(t, s, screen, "first Esc clears the selection")
	_, ok := s.logs.selected()
	assert.False(t, ok)
	assert.Contains(t, footerKeyDescs(s.FooterKeys(w)), "sessions")

	screen, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEscape}, w)
	assert.Equal(t, stub, screen, "Esc without a selection returns the onBack screen")
}

func TestMonitorScreen_EscClearsHostsSelection(t *testing.T) {
	s, w := newPollingTestScreen(t, nil, func() tui.Screen { return &testStubScreen{} })
	s.Update(hostsPollResultMsg{from: s, hosts: []proxy.HostStats{
		mkHost("a.com", "443", proxy.SourceProxy, time.Now(), 0, 1, proxy.ActionBlock),
	}}, w)
	s.Update(tea.KeyPressMsg{Code: 'j', Text: "j"}, w)
	s.Update(tea.KeyPressMsg{Code: tea.KeyEnter}, w)

	screen, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape}, w)
	assert.Equal(t, s, screen)
	_, ok := s.hosts.selected()
	assert.False(t, ok)
	assert.Nil(t, s.hosts.expanded, "clearing also collapses")
}

func TestMonitorScreen_EscWithoutOnBack(t *testing.T) {
	s, w := makeTestSetup(5)
	// onBack is nil — Esc only clears the selection.
	screen, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape}, w)
	assert.Equal(t, s, screen)
	screen, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEscape}, w)
	assert.Equal(t, s, screen, "Esc without onBack should stay on monitor")
	assert.NotContains(t, footerKeyDescs(s.FooterKeys(w)), "clear")
}

func TestMonitorScreen_DisconnectTransition(t *testing.T) {
	t.Run("poll error starts disconnect timer", func(t *testing.T) {
		s, w := makeTestSetup(5)
		stub := &testStubScreen{}
		s.onBack = func() tui.Screen { return stub }

		s.Update(logsPollResultMsg{from: s, err: fmt.Errorf("connection refused")}, w)
		assert.Equal(t, 0, s.disconnectTick, "disconnectTick should be 0 on first error")
	})

	t.Run("transitions after 12 ticks (3s)", func(t *testing.T) {
		s, w := makeTestSetup(5)
		stub := &testStubScreen{}
		s.onBack = func() tui.Screen { return stub }

		s.Update(logsPollResultMsg{from: s, err: fmt.Errorf("connection refused")}, w)

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
		s.Update(logsPollResultMsg{from: s, err: fmt.Errorf("connection refused")}, w)

		for range 20 {
			screen, _ := s.Update(tui.TickMsg{}, w)
			assert.Equal(t, s, screen, "should not transition without onBack")
		}
	})
}

func TestMonitorScreen_EscResetsHeader(t *testing.T) {
	s, w := makeTestSetup(5)
	stub := &testStubScreen{}
	s.onBack = func() tui.Screen { return stub }

	// Header currently shows session info. The first Esc clears the selection.
	s.Update(tea.KeyPressMsg{Code: tea.KeyEscape}, w)
	s.Update(tea.KeyPressMsg{Code: tea.KeyEscape}, w)

	// After Esc, header should be reset to selector mode.
	view := w.View().Content
	assert.Contains(t, view, "session selector")
}

func TestMonitorScreen_DisconnectFooterMessage(t *testing.T) {
	s, w := makeTestSetup(5)
	s.onBack = func() tui.Screen { return &testStubScreen{} }

	s.Update(logsPollResultMsg{from: s, err: fmt.Errorf("connection refused")}, w)
	require.Error(t, w.Err())
	assert.Contains(t, w.Err().Error(), "test123456")
	assert.Contains(t, w.Err().Error(), "disconnected")
}

func TestMonitorScreen_FooterKeepsQuitAt100Columns(t *testing.T) {
	s, w := newPollingTestScreen(t, nil, nil)
	s.onBack = func() tui.Screen { return &testStubScreen{} }
	w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	s.Update(hostsPollResultMsg{from: s, hosts: []proxy.HostStats{
		mkHost("blocked.example.com", "443", proxy.SourceProxy, time.Now(), 0, 2, proxy.ActionBlock),
	}}, w)
	s.Update(tea.KeyPressMsg{Code: 'j', Text: "j"}, w)

	lines := strings.Split(ansi.Strip(w.View().Content), "\n")
	footer := lines[len(lines)-1]
	assert.LessOrEqual(t, ansi.StringWidth(footer), 100)
	assert.True(t, strings.HasSuffix(strings.TrimRight(footer, " "), "q quit"), "footer: %q", footer)
	assert.Contains(t, footer, "a allow")
}

func TestMonitorScreen_OnlyLogsPollTracksConnection(t *testing.T) {
	t.Run("hosts poll success does not hide a lost connection", func(t *testing.T) {
		stub := &testStubScreen{}
		s, w := newPollingTestScreen(t, nil, func() tui.Screen { return stub })

		s.Update(logsPollResultMsg{from: s, err: errors.New("GET /logs: 500")}, w)
		s.Update(hostsPollResultMsg{from: s, hosts: []proxy.HostStats{}}, w)
		assert.Equal(t, 0, s.disconnectTick)
		require.Error(t, w.Err())

		var screen tui.Screen
		for range disconnectGraceTicks {
			screen, _ = s.Update(tui.TickMsg{}, w)
		}
		assert.Equal(t, stub, screen)
	})

	t.Run("logs poll success reconnects", func(t *testing.T) {
		s, w := newPollingTestScreen(t, nil, func() tui.Screen { return &testStubScreen{} })
		s.Update(logsPollResultMsg{from: s, err: errors.New("GET /logs: 500")}, w)
		s.Update(logsPollResultMsg{from: s}, w)
		assert.Equal(t, -1, s.disconnectTick)
		assert.NoError(t, w.Err())
	})
}

func TestMonitorScreen_AllowResultOrder(t *testing.T) {
	s, w := makeTestSetup(5)
	key := s.logs.items[2].HostKey()

	s.Update(allowResultMsg{from: s, key: key, status: statusSaved}, w)
	s.Update(allowResultMsg{from: s, key: key, status: statusTemp}, w)
	assert.Equal(t, statusSaved, s.allows[key], "a late temp result must not undo the save")
}

func TestMonitorScreen_StaleHostsPollAfterAllow(t *testing.T) {
	s, w := newPollingTestScreen(t, fakeProxyClient(t, new([]string), http.StatusOK), nil)
	blocked := mkHost("a.com", "443", proxy.SourceProxy, time.Now(), 0, 1, proxy.ActionBlock)
	s.Update(hostsPollResultMsg{from: s, hosts: []proxy.HostStats{blocked}}, w)

	stale := s.pollHosts() // snapshot taken before the allow lands
	require.NotNil(t, stale)
	s.Update(allowResultMsg{from: s, key: blocked.HostKey, status: statusTemp}, w)
	require.Equal(t, hostAllowedTemp, s.allows.hostStatus(s.hosts.hosts[0]))

	s.Update(hostsPollResultMsg{from: s, hosts: []proxy.HostStats{blocked}, gen: 0}, w)
	assert.Equal(t, hostAllowedTemp, s.allows.hostStatus(s.hosts.hosts[0]), "stale snapshot is dropped")
	assert.False(t, s.hostsPollInFlight)

	s.Update(hostsPollResultMsg{from: s, hosts: []proxy.HostStats{blocked}, gen: s.allowGen}, w)
	assert.Equal(t, hostBlocked, s.allows.hostStatus(s.hosts.hosts[0]), "a current snapshot wins")
}

func TestMonitorScreen_HostsPollCadence(t *testing.T) {
	countPolls := func(s *monitorScreen, w *tui.Window, ticks int) (logs, hosts int) {
		for range ticks {
			w.Update(tui.TickMsg{})
			if s.logsPollInFlight {
				logs++
			}
			if s.hostsPollInFlight {
				hosts++
			}
			s.logsPollInFlight, s.hostsPollInFlight = false, false
		}
		return logs, hosts
	}
	ticksPerSecond := int(time.Second / tui.TickInterval)

	t.Run("every second while shown", func(t *testing.T) {
		s, w := newPollingTestScreen(t, fakeProxyClient(t, new([]string), http.StatusOK), nil)
		logs, hosts := countPolls(s, w, 10*ticksPerSecond)
		assert.Equal(t, logs, hosts)
	})

	t.Run("less often while hidden", func(t *testing.T) {
		s, w := newPollingTestScreen(t, fakeProxyClient(t, new([]string), http.StatusOK), nil)
		s.tab = tabLogs
		logs, hosts := countPolls(s, w, 10*ticksPerSecond)
		assert.Equal(t, 11, logs, "first tick plus once per second")
		assert.Equal(t, 2, hosts)
	})

	t.Run("switching to the tab polls at once", func(t *testing.T) {
		s, w := newPollingTestScreen(t, fakeProxyClient(t, new([]string), http.StatusOK), nil)
		s.tab = tabLogs
		_, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyTab}, w)
		require.NotNil(t, cmd)
		assert.True(t, s.hostsPollInFlight)
		msgs := runCmd(cmd)
		require.Len(t, msgs, 1)
		assert.IsType(t, hostsPollResultMsg{}, msgs[0])
	})
}

func TestMonitorScreen_LogAllowFollowsHostsTab(t *testing.T) {
	blockLine := proxy.LogEntry{ID: 1, Domain: "a.com", Port: "443", Action: proxy.ActionBlock,
		Source: proxy.SourceProxy, Reason: "domain not in allowlist", Code: proxy.BlockNotAllowlisted}
	tests := []struct {
		name      string
		hosts     []proxy.HostStats
		wantAllow bool
	}{
		{"host allowlisted outside the monitor", func() []proxy.HostStats {
			h := mkHost("a.com", "443", proxy.SourceProxy, time.Now(), 0, 1, proxy.ActionBlock)
			h.Allowlisted = true
			return []proxy.HostStats{h}
		}(), false},
		{"host still blocked", []proxy.HostStats{
			mkHost("a.com", "443", proxy.SourceProxy, time.Now(), 0, 1, proxy.ActionBlock),
		}, true},
		{"host not in the table", []proxy.HostStats{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, w := newPollingTestScreen(t, nil, nil)
			s.Update(hostsPollResultMsg{from: s, hosts: tt.hosts}, w)
			s.Update(logsPollResultMsg{from: s, entries: []proxy.LogEntry{blockLine}}, w)
			s.Update(tea.KeyPressMsg{Code: tea.KeyTab}, w)
			s.Update(tea.KeyPressMsg{Code: 'j', Text: "j"}, w)

			assert.Equal(t, tt.wantAllow, slices.Contains(s.FooterKeys(w), tui.FooterKey{Key: "a", Desc: "allow"}))
			_, cmd := s.Update(tea.KeyPressMsg{Code: 'a', Text: "a"}, w)
			if tt.wantAllow {
				assert.NotNil(t, cmd)
			} else {
				assert.Nil(t, cmd)
				assert.Equal(t, "already allowed", w.Flash())
			}
		})
	}
}

func TestMonitorScreen_SelectionTimeout(t *testing.T) {
	setup := func(t *testing.T) (*monitorScreen, *tui.Window) {
		t.Helper()
		s, w := newPollingTestScreen(t, nil, nil)
		s.Update(hostsPollResultMsg{from: s, hosts: []proxy.HostStats{
			mkHost("a.com", "443", proxy.SourceProxy, time.Now(), 0, 1, proxy.ActionBlock),
		}}, w)
		s.Update(logsPollResultMsg{from: s, entries: []proxy.LogEntry{
			{ID: 1, Domain: "a.com", Port: "443", Action: proxy.ActionBlock, Source: proxy.SourceProxy, Code: proxy.BlockNotAllowlisted},
		}}, w)
		return s, w
	}
	ticks := func(s *monitorScreen, w *tui.Window, n int) {
		for range n {
			s.Update(tui.TickMsg{}, w)
		}
	}
	j := tea.KeyPressMsg{Code: 'j', Text: "j"}

	t.Run("host selection ends after 6s without a key press", func(t *testing.T) {
		s, w := setup(t)
		s.Update(j, w)

		ticks(s, w, selectionTimeoutTicks-1)
		_, ok := s.hosts.selected()
		require.True(t, ok)

		ticks(s, w, 1)
		_, ok = s.hosts.selected()
		assert.False(t, ok)
	})

	t.Run("log selection and scroll position survive idle time", func(t *testing.T) {
		s, w := setup(t)
		mkEntries := func(from, n int) []proxy.LogEntry {
			var entries []proxy.LogEntry
			for i := range n {
				entries = append(entries, proxy.LogEntry{ID: uint64(from + i), Domain: "b.com", Port: "443",
					Action: proxy.ActionAllow, Source: proxy.SourceProxy})
			}
			return entries
		}
		s.Update(logsPollResultMsg{from: s, entries: mkEntries(2, 200)}, w)
		s.Update(tea.KeyPressMsg{Code: tea.KeyTab}, w)
		s.Update(tea.KeyPressMsg{Code: tea.KeyPgUp}, w)
		s.Update(tea.KeyPressMsg{Code: tea.KeyPgUp}, w)
		before, ok := s.logs.selected()
		require.True(t, ok)
		offset := s.logs.cursor.Offset
		require.Less(t, offset, len(s.logs.items)-s.logs.cursor.VpHeight, "must be scrolled away from the tail")

		ticks(s, w, 2*selectionTimeoutTicks)
		s.Update(logsPollResultMsg{from: s, entries: mkEntries(202, 5)}, w)

		after, ok := s.logs.selected()
		assert.True(t, ok)
		assert.Equal(t, before.ID, after.ID)
		assert.Equal(t, offset, s.logs.cursor.Offset)
		assert.Equal(t, 5, s.logs.newCount)
	})

	t.Run("any key press restarts the timeout", func(t *testing.T) {
		s, w := setup(t)
		s.Update(j, w)
		ticks(s, w, selectionTimeoutTicks-1)
		s.Update(tea.KeyPressMsg{Code: 's', Text: "s"}, w)
		ticks(s, w, selectionTimeoutTicks-1)
		_, ok := s.hosts.selected()
		assert.True(t, ok)
	})

	t.Run("expanded host keeps its selection", func(t *testing.T) {
		s, w := setup(t)
		s.Update(j, w)
		s.Update(tea.KeyPressMsg{Code: tea.KeyEnter}, w)
		ticks(s, w, 2*selectionTimeoutTicks)
		_, ok := s.hosts.selected()
		assert.True(t, ok)
	})

	t.Run("allow keys need a selection", func(t *testing.T) {
		s, w := setup(t)
		_, cmd := s.Update(tea.KeyPressMsg{Code: 'a', Text: "a"}, w)
		assert.Nil(t, cmd)
		assert.Equal(t, "select a host first", w.Flash())
		assert.NotContains(t, footerKeyDescs(s.FooterKeys(w)), "allow")

		s.Update(tea.KeyPressMsg{Code: tea.KeyTab}, w)
		_, cmd = s.Update(tea.KeyPressMsg{Code: 'A', Text: "A"}, w)
		assert.Nil(t, cmd)
		assert.Equal(t, "select an entry first", w.Flash())
	})
}

func TestLogViewSelection(t *testing.T) {
	entries := func(from, n int) []proxy.LogEntry {
		var out []proxy.LogEntry
		for i := range n {
			out = append(out, proxy.LogEntry{ID: uint64(from + i), Domain: fmt.Sprintf("d%d.com", from+i),
				Action: proxy.ActionBlock, Source: proxy.SourceProxy})
		}
		return out
	}

	t.Run("tails without a selection", func(t *testing.T) {
		v := &logView{}
		v.setHeight(5)
		v.appendEntries(entries(1, 10))
		v.appendEntries(entries(11, 3))
		_, ok := v.selected()
		assert.False(t, ok)
		assert.False(t, v.selecting)
		assert.Equal(t, 8, v.cursor.Offset)
		assert.Zero(t, v.newCount)
		assert.NotContains(t, ansi.Strip(v.render(allowState{})), "➔")
		assert.Contains(t, v.render(allowState{}), "d13.com")
	})

	t.Run("first key selects the newest entry and it stays put", func(t *testing.T) {
		v := &logView{}
		v.setHeight(5)
		v.appendEntries(entries(1, 10))
		v.handleKey(keyPress("k"))
		sel, ok := v.selected()
		require.True(t, ok)
		assert.Equal(t, "d10.com", sel.Domain)

		v.appendEntries(entries(11, 3))
		sel, _ = v.selected()
		assert.Equal(t, "d10.com", sel.Domain, "new entries must not move the selection")
		assert.Equal(t, 3, v.newCount)
	})

	t.Run("deselect resumes tailing", func(t *testing.T) {
		v := &logView{}
		v.setHeight(5)
		v.appendEntries(entries(1, 10))
		v.handleKey(keyPress("g"))
		v.appendEntries(entries(11, 3))

		v.deselect()
		assert.False(t, v.selecting)
		assert.Equal(t, 8, v.cursor.Offset)
		assert.Zero(t, v.newCount)
	})
}
