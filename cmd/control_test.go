package cmd

import (
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
	bus, creds := newCmdTestBus(t)
	require.NoError(t, bus.RegisterHandlers())
	client := newCmdTestClient(t, bus, creds)

	cfg, err := client.Config()
	require.NoError(t, err)
	require.NotNil(t, cfg)
}

func TestControlClient_SubscribeLogs(t *testing.T) {
	bus, creds := newCmdTestBus(t)
	require.NoError(t, bus.RegisterHandlers())

	bus.LogPublisher().PublishLog(proxy.LogEntry{Domain: "sub.com", Action: proxy.ActionAllow})
	require.NoError(t, bus.FlushPublishes(2*time.Second))

	client := newCmdTestClient(t, bus, creds)

	ch := make(chan proxy.LogEntry, 8)
	stop, err := client.SubscribeLogs(ch)
	require.NoError(t, err)
	defer stop()

	select {
	case e := <-ch:
		assert.Equal(t, "sub.com", e.Domain)
	case <-time.After(3 * time.Second):
		t.Fatal("did not receive log entry within 3s")
	}
}
