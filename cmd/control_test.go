package cmd

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/adrg/xdg"
	"github.com/bernd/vibepit/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newCmdTestBus starts an embedded bus over mTLS and registers its handlers.
func newCmdTestBus(t *testing.T) (*proxy.Bus, *proxy.MTLSCredentials) {
	return newCmdTestBusWithConfig(t, proxy.ProxyConfig{})
}

// newCmdTestBusWithConfig is like newCmdTestBus but serves the given config on
// the config subject so tests can assert it round-trips to the client.
func newCmdTestBusWithConfig(t *testing.T, cfg proxy.ProxyConfig) (*proxy.Bus, *proxy.MTLSCredentials) {
	t.Helper()
	creds, err := proxy.GenerateMTLSCredentials(time.Hour)
	require.NoError(t, err)
	serverTLS, err := creds.ServerTLSConfig()
	require.NoError(t, err)
	t.Setenv(proxy.EnvProxyInternalCert, string(creds.InternalClientCertPEM()))
	t.Setenv(proxy.EnvProxyInternalKey, string(creds.InternalClientKeyPEM()))
	t.Setenv(proxy.EnvProxyCACert, string(creds.CACertPEM()))
	internalTLS, err := proxy.LoadInternalClientTLSConfigFromEnv()
	require.NoError(t, err)
	al, err := proxy.NewHTTPAllowlist(nil)
	require.NoError(t, err)
	dal, err := proxy.NewDNSAllowlist(nil)
	require.NoError(t, err)
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
	creds, err := proxy.GenerateMTLSCredentials(time.Hour)
	require.NoError(t, err)
	serverTLS, err := creds.ServerTLSConfig()
	require.NoError(t, err)
	t.Setenv(proxy.EnvProxyInternalCert, string(creds.InternalClientCertPEM()))
	t.Setenv(proxy.EnvProxyInternalKey, string(creds.InternalClientKeyPEM()))
	t.Setenv(proxy.EnvProxyCACert, string(creds.CACertPEM()))
	internalTLS, err := proxy.LoadInternalClientTLSConfigFromEnv()
	require.NoError(t, err)
	al, err := proxy.NewHTTPAllowlist(nil)
	require.NoError(t, err)
	dal, err := proxy.NewDNSAllowlist(nil)
	require.NoError(t, err)
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
		s := stats["a.com"]
		return s.Allowed == 1 && s.Blocked == 1
	}, 3*time.Second, 50*time.Millisecond)
}

func TestControlClient_Config(t *testing.T) {
	bus, creds := newCmdTestBusWithConfig(t, proxy.ProxyConfig{
		AllowHTTP: []string{"a.com:443", "b.com:443"},
		AllowDNS:  []string{"c.com"},
		BlockCIDR: []string{"10.0.0.0/8"},
		AllowCIDR: []string{"100.64.0.0/10"},
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
}

func TestControlClient_SubscribeLogs(t *testing.T) {
	bus, creds := newCmdTestBus(t)
	require.NoError(t, bus.RegisterHandlers())

	bus.LogPublisher().PublishLog(proxy.LogEntry{Domain: "sub.com", Action: proxy.ActionAllow})
	require.NoError(t, bus.FlushPublishes(2*time.Second))

	client := newCmdTestClient(t, bus, creds)

	ch := make(chan proxy.LogEntry, 8)
	stop, _, err := client.SubscribeLogs(ch)
	require.NoError(t, err)
	defer stop()

	select {
	case e := <-ch:
		assert.Equal(t, "sub.com", e.Domain)
	case <-time.After(3 * time.Second):
		t.Fatal("did not receive log entry within 3s")
	}
}

// TestControlClient_SubscribeLogs_StopClosesDone verifies stop closes the done
// channel (so waiters unblock at teardown) and is idempotent (no double-close
// panic).
func TestControlClient_SubscribeLogs_StopClosesDone(t *testing.T) {
	bus, creds := newCmdTestBus(t)
	require.NoError(t, bus.RegisterHandlers())
	client := newCmdTestClient(t, bus, creds)

	ch := make(chan proxy.LogEntry, 4)
	stop, done, err := client.SubscribeLogs(ch)
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
	stop, _, err := client.SubscribeLogs(ch)
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
