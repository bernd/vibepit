package cmd

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/adrg/xdg"
	"github.com/bernd/vibepit/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestMTLS generates session mTLS credentials, builds the server TLS config,
// and builds the internal client TLS config the proxy uses to dial its own bus.
func newTestMTLS(t *testing.T) (creds *proxy.MTLSCredentials, serverTLS, internalTLS *tls.Config) {
	t.Helper()
	creds, err := proxy.GenerateMTLSCredentials(time.Hour)
	require.NoError(t, err)
	serverTLS, err = creds.ServerTLSConfig()
	require.NoError(t, err)
	internalTLS, err = creds.InternalClientTLSConfig()
	require.NoError(t, err)
	return creds, serverTLS, internalTLS
}

// newTestAllowlists builds empty HTTP and DNS allowlists for a test bus.
func newTestAllowlists(t *testing.T) (*proxy.HTTPAllowlist, *proxy.DNSAllowlist) {
	t.Helper()
	al, err := proxy.NewHTTPAllowlist(nil)
	require.NoError(t, err)
	dal, err := proxy.NewDNSAllowlist(nil)
	require.NoError(t, err)
	return al, dal
}

// newCmdTestBus starts an embedded bus over mTLS and registers its handlers.
func newCmdTestBus(t *testing.T) (*proxy.Bus, *proxy.MTLSCredentials) {
	return newCmdTestBusWithConfig(t, proxy.ProxyConfig{})
}

// newCmdTestBusWithConfig is like newCmdTestBus but serves the given config on
// the config subject so tests can assert it round-trips to the client.
func newCmdTestBusWithConfig(t *testing.T, cfg proxy.ProxyConfig) (*proxy.Bus, *proxy.MTLSCredentials) {
	t.Helper()
	creds, serverTLS, internalTLS := newTestMTLS(t)
	al, dal := newTestAllowlists(t)
	bus, err := proxy.NewBus(proxy.BusOptions{
		ServerTLS: serverTLS, InternalTLS: internalTLS,
		HTTPAllowlist: al, DNSAllowlist: dal, Config: cfg,
	})
	require.NoError(t, err)
	t.Cleanup(bus.Shutdown)
	return bus, creds
}

// newCmdTestClient writes user creds to an isolated session dir and dials the bus.
func newCmdTestClient(t *testing.T, bus *proxy.Bus, creds *proxy.MTLSCredentials) *ControlClient {
	t.Helper()
	origStateHome := xdg.StateHome
	xdg.StateHome = t.TempDir()
	t.Cleanup(func() { xdg.StateHome = origStateHome })
	sessionID := "test-session"
	_, err := WriteSessionCredentials(sessionID, creds)
	require.NoError(t, err)
	_, port, err := net.SplitHostPort(bus.Addr())
	require.NoError(t, err)
	client, err := NewControlClient(&SessionInfo{SessionID: sessionID, ControlPort: port})
	require.NoError(t, err)
	t.Cleanup(client.Close)
	return client
}

// newCmdTestClientWithAllowlists starts a bus whose allowlists are returned to
// the caller so tests can assert server-side mutations, registers its handlers,
// and returns a connected client.
func newCmdTestClientWithAllowlists(t *testing.T) (*proxy.HTTPAllowlist, *proxy.DNSAllowlist, *ControlClient) {
	t.Helper()
	creds, serverTLS, internalTLS := newTestMTLS(t)
	al, dal := newTestAllowlists(t)
	bus, err := proxy.NewBus(proxy.BusOptions{
		ServerTLS: serverTLS, InternalTLS: internalTLS,
		HTTPAllowlist: al, DNSAllowlist: dal, Config: proxy.ProxyConfig{},
	})
	require.NoError(t, err)
	t.Cleanup(bus.Shutdown)
	require.NoError(t, bus.RegisterHandlers())
	client := newCmdTestClient(t, bus, creds)
	return al, dal, client
}

// TestControlClient_SurfacesPermissionViolation covers finding 8: a request the
// user role isn't allowed to publish must report the NATS permissions violation
// (delivered async) instead of degrading to a bare "nats: timeout".
func TestControlClient_SurfacesPermissionViolation(t *testing.T) {
	bus, creds := newCmdTestBus(t)
	require.NoError(t, bus.RegisterHandlers())
	client := newCmdTestClient(t, bus, creds)
	client.requestTimeout = 2 * time.Second

	// "forbidden" is not in the user role's publish allowlist.
	err := client.request("forbidden", nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Permissions Violation")
}

// TestControlClient_SignalConnKeepsLatest verifies the conn-event buffer evicts
// the oldest event when full, so the most recent connection state is never the
// one dropped (a dropped final transition would wedge the monitor on stale state).
func TestControlClient_SignalConnKeepsLatest(t *testing.T) {
	c := &ControlClient{connEvents: make(chan bool, 2), closed: make(chan struct{})}

	// Overfill a cap-2 buffer; the final signaled state is true.
	c.signalConn(true)
	c.signalConn(false)
	c.signalConn(true)
	c.signalConn(false)
	c.signalConn(true)

	var last bool
	got := false
	for {
		select {
		case last = <-c.connEvents:
			got = true
			continue
		default:
		}
		break
	}
	require.True(t, got, "buffer should retain events")
	assert.True(t, last, "the latest connection state must be retained, not evicted")
}

func TestControlClient_AllowHTTP(t *testing.T) {
	bus, creds := newCmdTestBus(t)
	require.NoError(t, bus.RegisterHandlers())
	client := newCmdTestClient(t, bus, creds)

	added, err := client.AllowHTTP([]string{"example.com:443"})
	require.NoError(t, err)
	assert.Equal(t, []string{"example.com:443"}, added)
}

func TestControlClient_AllowDNS(t *testing.T) {
	bus, creds := newCmdTestBus(t)
	require.NoError(t, bus.RegisterHandlers())
	client := newCmdTestClient(t, bus, creds)

	added, err := client.AllowDNS([]string{"internal.example.com"})
	require.NoError(t, err)
	assert.Equal(t, []string{"internal.example.com"}, added)
}

func TestControlClient_AllowHTTP_Empty(t *testing.T) {
	bus, creds := newCmdTestBus(t)
	require.NoError(t, bus.RegisterHandlers())
	client := newCmdTestClient(t, bus, creds)

	_, err := client.AllowHTTP([]string{})
	require.Error(t, err)
	assert.ErrorContains(t, err, "400")
}

func TestControlClient_Stats(t *testing.T) {
	bus, creds := newCmdTestBus(t)
	require.NoError(t, bus.RegisterHandlers())

	pub := bus.LogPublisher()
	pub.PublishLog(proxy.LogEntry{Domain: "a.com", Action: proxy.ActionAllow})
	pub.PublishLog(proxy.LogEntry{Domain: "a.com", Action: proxy.ActionBlock})
	require.NoError(t, bus.FlushPublishes(2*time.Second))

	client := newCmdTestClient(t, bus, creds)

	require.Eventually(t, func() bool {
		stats, err := client.Stats()
		if err != nil {
			return false
		}
		s := stats.Domains["a.com"]
		return s.Allowed == 1 && s.Blocked == 1
	}, 3*time.Second, 50*time.Millisecond)
}

func TestControlClient_Config(t *testing.T) {
	bus, creds := newCmdTestBusWithConfig(t, proxy.ProxyConfig{
		AllowHTTP:  []string{"a.com:443", "b.com:443"},
		AllowDNS:   []string{"c.com"},
		BlockCIDR:  []string{"10.0.0.0/8"},
		AllowCIDR:  []string{"100.64.0.0/10"},
		ExtraHosts: []string{"myhost.local:192.168.1.100"},
	})
	require.NoError(t, bus.RegisterHandlers())
	client := newCmdTestClient(t, bus, creds)

	cfg, err := client.Config()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, []string{"a.com:443", "b.com:443"}, cfg.AllowHTTP)
	assert.Equal(t, []string{"c.com"}, cfg.AllowDNS)
	assert.Equal(t, []string{"10.0.0.0/8"}, cfg.BlockCIDR)
	assert.Equal(t, []string{"100.64.0.0/10"}, cfg.AllowCIDR)
	assert.Equal(t, []string{"myhost.local:192.168.1.100"}, cfg.ExtraHosts)
}

func TestControlClient_SubscribeLogs(t *testing.T) {
	bus, creds := newCmdTestBus(t)
	require.NoError(t, bus.RegisterHandlers())

	bus.LogPublisher().PublishLog(proxy.LogEntry{Domain: "sub.com", Action: proxy.ActionAllow})
	require.NoError(t, bus.FlushPublishes(2*time.Second))

	client := newCmdTestClient(t, bus, creds)

	ch := make(chan proxy.LogEntry, 8)
	stop, _, err := client.SubscribeLogs(context.Background(), ch)
	require.NoError(t, err)
	defer stop()

	select {
	case e := <-ch:
		assert.Equal(t, "sub.com", e.Domain)
	case <-time.After(3 * time.Second):
		t.Fatal("did not receive log entry within 3s")
	}
}

// TestControlClient_WatchLogs_DeliversAndCancels verifies WatchLogs delivers
// retained entries as log-entry events and closes its stream when the context is
// canceled (so the owning UI's reader exits cleanly at teardown).
func TestControlClient_WatchLogs_DeliversAndCancels(t *testing.T) {
	bus, creds := newCmdTestBus(t)
	require.NoError(t, bus.RegisterHandlers())

	bus.LogPublisher().PublishLog(proxy.LogEntry{Domain: "watch.com", Action: proxy.ActionBlock})
	require.NoError(t, bus.FlushPublishes(2*time.Second))

	client := newCmdTestClient(t, bus, creds)

	ctx, cancel := context.WithCancel(context.Background())
	events := client.WatchLogs(ctx)

	// The retained entry is delivered as a log-entry event.
	select {
	case ev := <-events:
		require.Equal(t, LogEntryEvent, ev.Kind)
		assert.Equal(t, "watch.com", ev.Entry.Domain)
	case <-time.After(3 * time.Second):
		t.Fatal("WatchLogs did not deliver the retained entry")
	}

	// Canceling the context closes the stream once any buffered events drain.
	cancel()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("WatchLogs channel did not close after cancel")
		}
	}
}

// requireWatchEntry reads the WatchLogs stream until a log entry for domain
// arrives, ignoring connection events and other entries.
func requireWatchEntry(t *testing.T, events <-chan LogEvent, domain string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("watch stream closed before entry %q", domain)
			}
			if ev.Kind == LogEntryEvent && ev.Entry.Domain == domain {
				return
			}
		case <-deadline:
			t.Fatalf("did not receive entry %q within 3s", domain)
		}
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}

// TestControlClient_WatchLogs_ResubscribesOnReconnect covers the reconnect path:
// a reconnect must rebuild the consumer (the stream may have reset) and report
// recovery. Driven by signalConn so the resubscribe is exercised deterministically
// without a real network reconnect — a fresh consumer replays retained history.
func TestControlClient_WatchLogs_ResubscribesOnReconnect(t *testing.T) {
	bus, creds := newCmdTestBus(t)
	require.NoError(t, bus.RegisterHandlers())

	bus.LogPublisher().PublishLog(proxy.LogEntry{Domain: "before.com", Action: proxy.ActionAllow})
	require.NoError(t, bus.FlushPublishes(2*time.Second))

	client := newCmdTestClient(t, bus, creds)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := client.WatchLogs(ctx)

	// Initial subscription replays the retained entry.
	requireWatchEntry(t, events, "before.com")

	// A reconnect advances the generation, so the watcher opens a fresh consumer
	// which replays the retained history again.
	client.signalConn(true)
	requireWatchEntry(t, events, "before.com")
}

// TestControlClient_WatchLogs_RecoversAcrossProxyRestart covers a real reconnect
// to a restarted proxy whose stream was recreated (sequence numbers reset). The
// watcher must resubscribe and deliver the new stream's entries, and must not
// replay entries from the old stream (stale-channel/cursor isolation).
func TestControlClient_WatchLogs_RecoversAcrossProxyRestart(t *testing.T) {
	creds, serverTLS, internalTLS := newTestMTLS(t)

	port := freePort(t)
	startBus := func() *proxy.Bus {
		var bus *proxy.Bus
		// Retry the bind: the prior server may not have fully released the port yet.
		require.Eventually(t, func() bool {
			al, e := proxy.NewHTTPAllowlist(nil)
			require.NoError(t, e)
			dal, e := proxy.NewDNSAllowlist(nil)
			require.NoError(t, e)
			b, e := proxy.NewBus(proxy.BusOptions{
				Port: port, ServerTLS: serverTLS, InternalTLS: internalTLS,
				HTTPAllowlist: al, DNSAllowlist: dal,
			})
			if e != nil {
				return false
			}
			bus = b
			return true
		}, 3*time.Second, 50*time.Millisecond)
		require.NoError(t, bus.RegisterHandlers())
		return bus
	}

	bus1 := startBus()
	bus1.LogPublisher().PublishLog(proxy.LogEntry{Domain: "old.com", Action: proxy.ActionAllow})
	require.NoError(t, bus1.FlushPublishes(2*time.Second))

	client := newCmdTestClient(t, bus1, creds)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := client.WatchLogs(ctx)
	requireWatchEntry(t, events, "old.com")

	// Restart the proxy on the same port with a brand-new, empty stream.
	bus1.Shutdown()
	bus2 := startBus()
	t.Cleanup(bus2.Shutdown)
	bus2.LogPublisher().PublishLog(proxy.LogEntry{Domain: "new.com", Action: proxy.ActionAllow})
	require.NoError(t, bus2.FlushPublishes(2*time.Second))

	// The client auto-reconnects; the watcher resubscribes to the new stream and
	// delivers new.com. old.com belongs to the destroyed stream and must not
	// reappear from a stale cursor or a leftover channel.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("watch stream closed before delivering new.com")
			}
			if ev.Kind != LogEntryEvent {
				continue
			}
			require.NotEqual(t, "old.com", ev.Entry.Domain, "stale entry replayed after restart")
			if ev.Entry.Domain == "new.com" {
				return
			}
		case <-deadline:
			t.Fatal("did not receive new.com from the restarted proxy within 5s")
		}
	}
}

// requireNoWatchEntry asserts no log entry for domain arrives within the window
// (connection events are ignored). A replay would arrive within sub-millisecond
// on an in-process bus, so the window only needs to be comfortably above that.
func requireNoWatchEntry(t *testing.T, events <-chan LogEvent, domain string, within time.Duration) {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return // stream closed; no entry
			}
			if ev.Kind == LogEntryEvent && ev.Entry.Domain == domain {
				t.Fatalf("unexpected replay of %q: a same-generation resubscribe was not coalesced", domain)
			}
		case <-deadline:
			return
		}
	}
}

// TestControlClient_WatchLogs_CoalescesSameGenerationResubscribe covers the
// retry/reconnect race: when a reconnect event arrives for a generation that a
// coincident retry already resubscribed, the watcher must not open a second
// consumer and replay history again. It is driven deterministically by injecting
// a reconnect event onto the conn-event channel WITHOUT advancing the reconnect
// counter — the exact state a buffered reconnect leaves after a retry already
// rebuilt the consumer for that generation.
func TestControlClient_WatchLogs_CoalescesSameGenerationResubscribe(t *testing.T) {
	bus, creds := newCmdTestBus(t)
	require.NoError(t, bus.RegisterHandlers())

	bus.LogPublisher().PublishLog(proxy.LogEntry{Domain: "x.com", Action: proxy.ActionAllow})
	require.NoError(t, bus.FlushPublishes(2*time.Second))

	client := newCmdTestClient(t, bus, creds)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := client.WatchLogs(ctx)

	// Initial subscription (generation 0) replays the retained entry.
	requireWatchEntry(t, events, "x.com")

	// A real reconnect advances the generation and rebuilds the consumer, replaying
	// history once.
	client.signalConn(true)
	requireWatchEntry(t, events, "x.com")

	// A second reconnect EVENT at the same generation (reconnects counter not
	// advanced) must be coalesced: the live consumer already covers this
	// generation, so no rebuild and no second replay.
	client.connEvents <- true
	requireNoWatchEntry(t, events, "x.com", time.Second)
}

// TestControlClient_SubscribeLogs_StopClosesDone verifies stop closes the done
// channel (so waiters unblock at teardown) and is idempotent (no double-close
// panic).
func TestControlClient_SubscribeLogs_StopClosesDone(t *testing.T) {
	bus, creds := newCmdTestBus(t)
	require.NoError(t, bus.RegisterHandlers())
	client := newCmdTestClient(t, bus, creds)

	ch := make(chan proxy.LogEntry, 4)
	stop, done, err := client.SubscribeLogs(context.Background(), ch)
	require.NoError(t, err)

	stop()
	stop() // idempotent — must not panic

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stop should close the done channel")
	}
}

// TestControlClient_CloseUnblocksConnWatcher verifies Close closes the Closed
// channel — the backstop so a ConnEvents watcher unblocks at teardown even when
// the lossy buffer dropped the final lifecycle event — and is idempotent.
func TestControlClient_CloseUnblocksConnWatcher(t *testing.T) {
	bus, creds := newCmdTestBus(t)
	require.NoError(t, bus.RegisterHandlers())
	client := newCmdTestClient(t, bus, creds)

	// Saturate the lossy buffer so the close lifecycle event is dropped — exactly
	// the edge the Closed backstop exists for.
	for range cap(client.connEvents) {
		client.signalConn(false)
	}

	client.Close()
	client.Close() // idempotent — must not panic on double close

	// A watcher that drains buffered flap events but also selects on Closed,
	// mirroring monitorScreen.watchConn. Once the buffer empties, only Closed is
	// ready, so it must exit via Closed rather than blocking forever.
	exited := make(chan struct{})
	go func() {
		for {
			select {
			case <-client.ConnEvents():
			case <-client.Closed():
				close(exited)
				return
			}
		}
	}()

	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not unblock the conn watcher via Closed")
	}
}

// TestControlClient_SubscribeLogs_BoundsInitialHistory verifies the initial
// replay is capped at the last initialLogHistory entries (not the whole ring).
func TestControlClient_SubscribeLogs_BoundsInitialHistory(t *testing.T) {
	bus, creds := newCmdTestBus(t)
	require.NoError(t, bus.RegisterHandlers())

	const total = 40
	pub := bus.LogPublisher()
	for i := range total {
		pub.PublishLog(proxy.LogEntry{Domain: fmt.Sprintf("d%02d.com", i), Action: proxy.ActionAllow})
	}
	require.NoError(t, bus.FlushPublishes(2*time.Second))

	client := newCmdTestClient(t, bus, creds)
	ch := make(chan proxy.LogEntry, 256)
	stop, _, err := client.SubscribeLogs(context.Background(), ch)
	require.NoError(t, err)
	defer stop()

	// Drain the initial replay until it goes quiet.
	var got []proxy.LogEntry
	deadline := time.After(3 * time.Second)
loop:
	for {
		select {
		case e := <-ch:
			got = append(got, e)
		case <-time.After(300 * time.Millisecond):
			break loop
		case <-deadline:
			break loop
		}
	}

	require.Len(t, got, int(initialLogHistory), "initial replay should be bounded")
	assert.Equal(t, "d15.com", got[0].Domain, "oldest delivered is total-25")
	assert.Equal(t, "d39.com", got[len(got)-1].Domain, "newest delivered is the last entry")
}
