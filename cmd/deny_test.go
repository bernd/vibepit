package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bernd/vibepit/config"
	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

func TestDenyEntries(t *testing.T) {
	for _, source := range []proxy.Source{proxy.SourceProxy, proxy.SourceDNS} {
		for _, scenario := range []string{"success", "unsupported", "save failure", "no save"} {
			t.Run(string(source)+"/"+scenario, func(t *testing.T) {
				project := t.TempDir()
				applied := false
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					require.Equal(t, map[proxy.Source]string{proxy.SourceProxy: "/deny-http", proxy.SourceDNS: "/deny-dns"}[source], r.URL.Path)
					if scenario == "unsupported" {
						http.NotFound(w, r)
						return
					}
					applied = true
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"added":["example.com"]}`))
				}))
				defer srv.Close()
				client := &ControlClient{http: srv.Client(), baseURL: srv.URL}
				path := config.DefaultProjectPath(project)
				if scenario == "save failure" {
					require.NoError(t, os.MkdirAll(path, 0700))
				}
				entries := []string{"example.com"}
				if source == proxy.SourceProxy {
					entries = []string{"example.com:443"}
				}
				got, err := denyEntries(client, &SessionInfo{ProjectDir: project}, source, entries, scenario != "no save")
				require.Equal(t, applied, got)
				switch scenario {
				case "unsupported":
					require.Error(t, err)
					require.NoFileExists(t, path)
				case "save failure":
					require.ErrorContains(t, err, "applied")
					require.True(t, applied)
				case "no save":
					require.NoError(t, err)
					require.NoFileExists(t, path)
				default:
					require.NoError(t, err)
					require.FileExists(t, path)
				}
			})
		}
	}
}

func TestDeniedHostStatusAndAllowTarget(t *testing.T) {
	h := proxy.HostStats{HostKey: proxy.HostKey{Source: proxy.SourceProxy, Domain: "example.com", Port: "443"}, Denied: true, Allowlisted: false, LastAction: proxy.ActionAllow}
	s := allowState{h.HostKey: statusSaved}
	require.Equal(t, hostDenied, s.hostStatus(h))
	require.False(t, s.forHost(h).canAllow())
	require.False(t, s.forHost(h).canSave())
}

func TestApproveDenySaved(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(fmt.Sprint(broken), func(t *testing.T) {
			f := makeApproveSetup(t, blockedEntry)
			if broken {
				require.NoError(t, os.WriteFile(f.projectPath, []byte("[bad yaml"), 0600))
			}
			_, cmd := f.s.Update(tea.KeyPressMsg{Code: 'N'}, f.w)
			require.NotNil(t, cmd)
			msg := cmd()
			f.s.Update(msg, f.w)
			check, err := f.proxy.client.Check(blockedEntry)
			require.NoError(t, err)
			require.True(t, check.Denied)
			if broken {
				require.ErrorContains(t, f.w.Err(), "applied")
				f.s.Update(checkResultMsg{decided: []proxy.Target{blockedEntry.Target()}}, f.w)
				f.s.Update(tui.TickMsg{}, f.w)
				require.ErrorContains(t, f.w.Err(), "applied")
			} else {
				require.Zero(t, f.q.Len())
			}
		})
	}
}

func TestMonitorDenySaved(t *testing.T) {
	for _, tab := range []monitorTab{tabHosts, tabLogs} {
		t.Run(fmt.Sprint(tab), func(t *testing.T) {
			s, w := makeTestSetup(1)
			s.tab = tab
			tp := newTestProxy(t)
			s.client = tp.client
			s.session.ProjectDir = t.TempDir()
			key := s.logs.items[0].HostKey()
			h := proxy.HostStats{HostKey: key, LastAction: proxy.ActionAllow, Allowlisted: true, Allowed: 4}
			s.hosts.setHosts([]proxy.HostStats{h}, &s.held, w.Now())
			s.hosts.selecting = true
			require.Contains(t, s.FooterKeys(w), tui.FooterKey{Key: "N", Desc: "deny+save"})
			_, cmd := s.Update(tea.KeyPressMsg{Code: 'N'}, w)
			require.NotNil(t, cmd)
			msg := cmd()
			other, _ := makeTestSetup(0)
			other.Update(msg, w)
			require.Zero(t, other.allowGen)
			s.Update(msg, w)
			require.True(t, s.hosts.hosts[0].Denied)
			require.False(t, s.hosts.hosts[0].Allowlisted)
			require.Equal(t, 4, s.hosts.hosts[0].Allowed)
			require.False(t, s.entryTarget(s.logs.items[0], w.Now()).canAllow())
			require.Equal(t, proxy.ActionBlock, s.logs.items[0].Action)
			require.Equal(t, uint64(1), s.allowGen)
		})
	}
}

func TestRuleCommands(t *testing.T) {
	for _, action := range []ruleAction{ruleAllow, ruleDeny} {
		for _, source := range []proxy.Source{proxy.SourceProxy, proxy.SourceDNS} {
			for _, scenario := range []string{"help", "empty", "invalid", "multiple", "no save", "runtime error", "save error", "discovery error"} {
				t.Run(action.String()+"/"+string(source)+"/"+scenario, func(t *testing.T) {
					project := t.TempDir()
					session := &SessionInfo{ProjectDir: project}
					path := config.DefaultProjectPath(project)
					switch {
					case scenario == "save error":
						require.NoError(t, os.MkdirAll(path, 0700))
					case action == ruleAllow:
						// Allow saves need an existing config; deny saves create one.
						require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
						require.NoError(t, os.WriteFile(path, []byte("presets: [default]\n"), 0600))
					}
					command := ruleCommand(action, source, nil, nil)
					requested := false
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requested = true
						require.Equal(t, "/"+command.Name, r.URL.Path)
						var req struct {
							Entries []string `json:"entries"`
						}
						require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
						require.Len(t, req.Entries, 2)
						if scenario == "runtime error" {
							http.Error(w, "failed", 500)
							return
						}
						_, _ = w.Write([]byte(`{"added":[]}`))
					}))
					defer srv.Close()
					discovered := false
					command = ruleCommand(action, source, func(_ context.Context, filter string) (*SessionInfo, error) {
						discovered = true
						require.Equal(t, "chosen", filter)
						if scenario == "discovery error" {
							return nil, errors.New("no session")
						}
						return session, nil
					}, func(info *SessionInfo) (*ControlClient, error) {
						require.Same(t, session, info)
						return &ControlClient{http: srv.Client(), baseURL: srv.URL}, nil
					})
					entries := []string{"one.example:443", "two.example:80"}
					if source == proxy.SourceDNS {
						entries = []string{"one.example", "two.example"}
					}
					args := []string{"vibepit", command.Name, "--session", "chosen"}
					switch scenario {
					case "help":
						args = append(args, "--help")
					case "empty":
					case "invalid":
						args = append(args, "bad value")
					default:
						if scenario == "no save" {
							args = append(args, "--no-save")
						}
						args = append(args, entries...)
					}
					var output bytes.Buffer
					root := &cli.Command{Name: "vibepit", Writer: &output, ErrWriter: &output, Commands: []*cli.Command{command}}
					err := root.Run(context.Background(), args)
					saved := func() bool {
						data, readErr := os.ReadFile(path)
						return readErr == nil && strings.Contains(string(data), entries[0])
					}
					switch scenario {
					case "help", "empty":
						require.NoError(t, err)
						require.False(t, discovered)
						require.Contains(t, output.String(), command.Name)
					case "invalid":
						require.ErrorContains(t, err, command.Name)
						require.False(t, discovered)
					case "discovery error":
						require.ErrorContains(t, err, "no session")
						require.False(t, requested)
					case "runtime error":
						require.Error(t, err)
						require.False(t, saved())
					case "save error":
						require.ErrorContains(t, err, action.String()+" applied and remains active")
						require.True(t, requested)
					case "no save":
						require.NoError(t, err)
						require.True(t, requested)
						require.False(t, saved())
					default:
						require.NoError(t, err)
						require.True(t, requested)
						require.True(t, saved())
					}
				})
			}
		}
	}
	root := RootCommand()
	names := map[string]bool{}
	for _, c := range root.Commands {
		names[c.Name] = true
	}
	for _, name := range []string{"allow-http", "allow-dns", "deny-http", "deny-dns"} {
		require.True(t, names[name], name)
	}
}

func TestMonitorDeniedBlocksLateAllowAndHeldTarget(t *testing.T) {
	s, w := makeTestSetup(1)
	e := s.logs.items[0]
	key := e.HostKey()
	h := proxy.HostStats{HostKey: key, Denied: true, LastAction: proxy.ActionAllow}
	s.hosts.setHosts([]proxy.HostStats{h}, &s.held, w.Now())
	s.Update(allowResultMsg{from: s, key: key, status: statusSaved}, w)
	require.False(t, s.hosts.hosts[0].Allowlisted)
	require.NotContains(t, w.Flash(), "allowed and saved")
	require.False(t, s.entryTarget(e, w.Now()).canAllow())
}

func TestApproveDenySavedGuards(t *testing.T) {
	for _, guard := range []string{"busy", "unseen"} {
		t.Run(guard, func(t *testing.T) {
			f := makeApproveSetup(t, blockedEntry)
			if guard == "busy" {
				f.s.busy = true
			} else {
				f.s.shown = proxy.Target{}
			}
			_, cmd := f.s.Update(tea.KeyPressMsg{Code: 'N'}, f.w)
			require.Nil(t, cmd)
		})
	}
}

func TestMonitorDenySavedSelectionGuards(t *testing.T) {
	for _, tab := range []monitorTab{tabHosts, tabLogs} {
		for _, invalid := range []bool{false, true} {
			t.Run(fmt.Sprint(tab, invalid), func(t *testing.T) {
				s, w := makeTestSetup(0)
				s.tab = tab
				s.client = newTestProxy(t).client
				if invalid {
					h := proxy.HostStats{HostKey: proxy.HostKey{Source: proxy.SourceProxy, Domain: "bad value", Port: "443"}}
					s.hosts.setHosts([]proxy.HostStats{h}, &s.held, w.Now())
					s.hosts.selecting = true
					s.logs.items = []proxy.LogEntry{{Source: proxy.SourceProxy, Domain: "bad value", Port: "443"}}
					s.logs.selecting = true
				}
				_, cmd := s.Update(tea.KeyPressMsg{Code: 'N'}, w)
				require.Nil(t, cmd)
				require.NotEmpty(t, w.Flash())
			})
		}
	}
}

func TestMonitorDenySavedPartialFailureAndStalePoll(t *testing.T) {
	s, w := makeTestSetup(1)
	s.client = newTestProxy(t).client
	s.session.ProjectDir = t.TempDir()
	require.NoError(t, os.MkdirAll(config.DefaultProjectPath(s.session.ProjectDir), 0700))
	e := s.logs.items[0]
	h := proxy.HostStats{HostKey: e.HostKey(), LastAction: proxy.ActionAllow, Allowlisted: true}
	s.hosts.setHosts([]proxy.HostStats{h}, &s.held, w.Now())
	_, cmd := s.Update(tea.KeyPressMsg{Code: 'N'}, w)
	require.NotNil(t, cmd)
	s.Update(cmd(), w)
	require.True(t, s.hosts.hosts[0].Denied)
	require.Contains(t, w.Flash(), "saving failed")
	s.Update(hostsPollResultMsg{from: s, gen: 0, hosts: []proxy.HostStats{h}}, w)
	require.True(t, s.hosts.hosts[0].Denied)
	require.Equal(t, proxy.ActionBlock, s.logs.items[0].Action)
}

func TestMonitorDenySavedEvictedLogHost(t *testing.T) {
	s, w := makeTestSetup(1)
	s.client = newTestProxy(t).client
	s.session.ProjectDir = t.TempDir()
	_, cmd := s.Update(tea.KeyPressMsg{Code: 'N'}, w)
	require.NotNil(t, cmd)
	s.Update(cmd(), w)
	require.False(t, s.entryTarget(s.logs.items[0], w.Now()).canAllow())
	_, cmd = s.Update(tea.KeyPressMsg{Code: 'a'}, w)
	require.Nil(t, cmd)
}

func TestMonitorDenySavedHistoricalAllowedAndExpiredHold(t *testing.T) {
	for _, action := range []proxy.Action{proxy.ActionAllow, proxy.ActionHold} {
		for _, source := range []proxy.Source{proxy.SourceProxy, proxy.SourceDNS} {
			t.Run(string(action)+"/"+string(source), func(t *testing.T) {
				s, w := makeTestSetup(1)
				s.client = newTestProxy(t).client
				s.session.ProjectDir = t.TempDir()
				e := &s.logs.items[0]
				e.Action = action
				e.Source = source
				if source == proxy.SourceDNS {
					e.Port = ""
				}
				if action == proxy.ActionHold {
					e.Deadline = w.Now().Add(-time.Second)
				}
				before := *e
				_, cmd := s.Update(tea.KeyPressMsg{Code: 'N'}, w)
				require.NotNil(t, cmd)
				s.Update(cmd(), w)
				require.Equal(t, before, s.logs.items[0], "historical log entry is unchanged")
				require.False(t, s.entryTarget(before, w.Now()).canAllow())
				check, err := s.client.Check(before)
				require.NoError(t, err)
				require.True(t, check.Denied)
			})
		}
	}
}

func TestApproveDenySavedUnsupported(t *testing.T) {
	f := makeApproveSetup(t, blockedEntry)
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	f.s.client = &ControlClient{http: srv.Client(), baseURL: srv.URL}
	_, cmd := f.s.Update(tea.KeyPressMsg{Code: 'N'}, f.w)
	require.NotNil(t, cmd)
	f.s.Update(cmd(), f.w)
	require.Error(t, f.w.Err())
	require.Equal(t, 1, f.q.Len())
	contents, err := os.ReadFile(f.projectPath)
	require.NoError(t, err)
	require.NotContains(t, string(contents), "deny")
}

// A live deny that fails changed nothing, so the item must not stay pinned
// once its hold ends elsewhere.
func TestApproveFailedLiveDenyDoesNotPinItem(t *testing.T) {
	entry := heldEntry("stuck.example.com", 1, approveEpoch.Add(time.Minute))
	next := heldEntry("next.example.com", 2, approveEpoch.Add(time.Minute))
	f := makeApproveSetup(t, entry, next)
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	f.s.client = &ControlClient{http: srv.Client(), baseURL: srv.URL}
	_, cmd := f.s.Update(tea.KeyPressMsg{Code: 'N', Text: "N"}, f.w)
	require.NotNil(t, cmd)
	f.s.Update(cmd(), f.w)
	require.Error(t, f.w.Err())
	require.Nil(t, f.s.saving)
	f.q.EndHold(proxy.HoldEnd{ID: 1, HostKey: entry.HostKey(), Outcome: proxy.HoldTimeout})
	f.q.Remove(entry.Target())
	view := f.s.View(f.w)
	require.NotContains(t, view, "stuck.example.com")
	require.Contains(t, view, "next.example.com:443")
}

// A retry of a failed save that fails at the proxy keeps the denied target,
// even after its hold settled: the deny is active and the save still owed.
func TestApproveFailedRetryKeepsDeniedTarget(t *testing.T) {
	entry := heldEntry("retry.example.com", 1, approveEpoch.Add(time.Minute))
	f := makeApproveSetup(t, entry)
	require.NoError(t, os.Remove(f.projectPath))
	require.NoError(t, os.Mkdir(f.projectPath, 0755))
	_, cmd := f.s.Update(tea.KeyPressMsg{Code: 'N', Text: "N"}, f.w)
	require.NotNil(t, cmd)
	f.s.Update(cmd(), f.w)
	require.Error(t, f.w.Err())
	f.q.EndHold(proxy.HoldEnd{ID: 1, HostKey: entry.HostKey(), Outcome: proxy.HoldDenied})
	f.q.Remove(entry.Target())

	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	f.s.client = &ControlClient{http: srv.Client(), baseURL: srv.URL}
	f.s.View(f.w)
	_, cmd = f.s.Update(tea.KeyPressMsg{Code: 'N', Text: "N"}, f.w)
	require.NotNil(t, cmd)
	f.s.Update(cmd(), f.w)
	require.Error(t, f.w.Err())
	require.NotNil(t, f.s.saving)
	require.Contains(t, f.s.View(f.w), "Retry saving")
	require.Contains(t, f.s.View(f.w), "retry.example.com:443")
}

func footerHas(keys []tui.FooterKey, key, desc string) bool {
	for _, k := range keys {
		if k.Key == key && k.Desc == desc {
			return true
		}
	}
	return false
}

// N is offered until a deny is in force, then only to retry a failed save,
// and a press while one is in flight sends nothing.
func TestMonitorDenySavedLifecycle(t *testing.T) {
	s, w := makeTestSetup(1)
	s.client = newTestProxy(t).client
	s.session.ProjectDir = t.TempDir()
	projectPath := config.DefaultProjectPath(s.session.ProjectDir)
	require.NoError(t, os.MkdirAll(projectPath, 0700)) // a directory: saving fails
	e := s.logs.items[0]
	s.hosts.setHosts([]proxy.HostStats{{HostKey: e.HostKey(), LastAction: proxy.ActionBlock}}, &s.held, w.Now())
	s.hosts.selecting = true
	require.True(t, footerHas(s.FooterKeys(w), "N", "deny+save"))

	_, cmd := s.Update(tea.KeyPressMsg{Code: 'N'}, w)
	require.NotNil(t, cmd)
	_, again := s.Update(tea.KeyPressMsg{Code: 'N'}, w)
	require.Nil(t, again, "in flight")
	s.Update(cmd(), w)
	require.Contains(t, w.Flash(), "saving failed")
	require.True(t, footerHas(s.FooterKeys(w), "N", "retry save"))

	require.NoError(t, os.Remove(projectPath))
	_, cmd = s.Update(tea.KeyPressMsg{Code: 'N'}, w)
	require.NotNil(t, cmd)
	s.Update(cmd(), w)
	require.Contains(t, w.Flash(), "denied and saved")
	for _, k := range s.FooterKeys(w) {
		require.NotEqual(t, "N", k.Key)
	}
	_, cmd = s.Update(tea.KeyPressMsg{Code: 'N'}, w)
	require.Nil(t, cmd)
	require.Contains(t, w.Flash(), "already denied")
}

// An allow that lands before a deny is overridden, but what it left in the
// config, or failed to, is still reported.
func TestMonitorOverriddenAllowReportsSave(t *testing.T) {
	for _, tc := range []struct {
		status allowStatus
		err    error
		want   string
	}{
		{statusSaved, nil, "saved allow entry has no effect"},
		{statusTemp, errors.New("disk full"), "saving the allow failed"},
		{statusTemp, nil, "overridden by deny"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			s, w := makeTestSetup(1)
			key := s.logs.items[0].HostKey()
			s.hosts.setHosts([]proxy.HostStats{{HostKey: key, Denied: true}}, &s.held, w.Now())
			s.Update(allowResultMsg{from: s, key: key, status: tc.status, err: tc.err}, w)
			require.Contains(t, w.Flash(), tc.want)
			require.False(t, s.hosts.hosts[0].Allowlisted)
		})
	}
}

// A held host the poll doesn't list yet exists only as a synthesized row.
// After a deny it must stay denied while the hold's end is still in transit.
func TestMonitorDeniedSynthesizedHeldRowStaysDenied(t *testing.T) {
	s, w := makeTestSetup(0)
	s.client = newTestProxy(t).client
	s.session.ProjectDir = t.TempDir()
	key := proxy.HostKey{Source: proxy.SourceProxy, Domain: "held.example.com", Port: "443"}
	now := w.Now()
	s.held.Apply(proxy.Event{Type: proxy.EventHoldOpen, Log: proxy.LogEntry{
		ID: 7, Time: now, Domain: key.Domain, Port: key.Port, Source: key.Source,
		Action: proxy.ActionHold, Code: proxy.BlockNotAllowlisted, Deadline: now.Add(time.Minute),
	}})
	s.hosts.setHosts(nil, &s.held, now)
	require.Len(t, s.hosts.hosts, 1)
	s.tab = tabHosts
	s.hosts.selecting = true

	_, cmd := s.Update(tea.KeyPressMsg{Code: 'N'}, w)
	require.NotNil(t, cmd)
	s.Update(cmd(), w)
	s.hosts.refresh(&s.held, now) // the hold is still open: no HoldEnd yet
	s.hosts.setHosts(nil, &s.held, now)

	for _, h := range s.hosts.hosts {
		require.NotEqual(t, key, h.HostKey, "a denied host is no longer shown as held")
	}
	require.False(t, s.entryTarget(proxy.LogEntry{Domain: key.Domain, Port: key.Port, Source: key.Source, Action: proxy.ActionHold}, now).canAllow())
	_, cmd = s.Update(tea.KeyPressMsg{Code: 'a'}, w)
	require.Nil(t, cmd)
}

// A restarted proxy enforces only the denies it loaded, so a stream reset
// drops what the monitor knew; the next poll reports the new state.
func TestMonitorFeedResetForgetsDenies(t *testing.T) {
	s, w := makeTestSetup(1)
	key := s.logs.items[0].HostKey()
	s.hosts.setHosts([]proxy.HostStats{{HostKey: key, Denied: true}}, &s.held, w.Now())
	s.hosts.setUnsavedDeny(key, true)
	require.True(t, s.isDenied(key))
	gen := s.allowGen

	s.Update(feedMsg{from: s, batch: feedBatch{Reset: true}}, w)
	require.False(t, s.isDenied(key))
	require.False(t, s.hosts.unsavedDenies[key])
	// A poll that started against the old proxy is discarded.
	s.Update(hostsPollResultMsg{from: s, gen: gen, hosts: []proxy.HostStats{{HostKey: key, Denied: true}}}, w)
	require.False(t, s.isDenied(key))
	s.Update(hostsPollResultMsg{from: s, gen: s.allowGen, hosts: []proxy.HostStats{{HostKey: key, LastAction: proxy.ActionAllow, Allowlisted: true}}}, w)
	require.False(t, s.hosts.hosts[0].Denied)
	s.Update(allowResultMsg{from: s, key: key, status: statusTemp}, w)
	require.NotContains(t, w.Flash(), "overridden")
}

// Once a held target is denied, n is neither offered nor sent on either tab,
// even before the hold's end event arrives.
func TestMonitorNoSessionDenyForDeniedHeldTarget(t *testing.T) {
	for _, tab := range []monitorTab{tabHosts, tabLogs} {
		t.Run(fmt.Sprint(tab), func(t *testing.T) {
			s, w := makeTestSetup(0)
			s.client = newTestProxy(t).client
			now := w.Now()
			e := proxy.LogEntry{ID: 7, Time: now, Domain: "held.example.com", Port: "443", Source: proxy.SourceProxy,
				Action: proxy.ActionHold, Code: proxy.BlockNotAllowlisted, Deadline: now.Add(time.Minute)}
			s.held.Apply(proxy.Event{Type: proxy.EventHoldOpen, Log: e})
			s.logs.items = []proxy.LogEntry{e}
			s.logs.selecting = true
			s.hosts.setHosts([]proxy.HostStats{{HostKey: e.HostKey(), LastAction: proxy.ActionHold}}, &s.held, now)
			s.hosts.selecting = true
			s.tab = tab
			require.True(t, footerHas(s.FooterKeys(w), "n", "reject"))

			s.hosts.markDenied(e.HostKey(), &s.held, now)
			require.False(t, footerHas(s.FooterKeys(w), "n", "reject"))
			_, cmd := s.Update(tea.KeyPressMsg{Code: 'n', Text: "n"}, w)
			require.Nil(t, cmd)
		})
	}
}
