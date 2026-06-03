package proxy

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// newTestBus starts a Bus on an ephemeral port using freshly generated creds.
func newTestBus(t *testing.T) (*Bus, *MTLSCredentials) {
	t.Helper()
	creds, err := GenerateMTLSCredentials(time.Hour)
	require.NoError(t, err)
	serverTLS, err := creds.ServerTLSConfig()
	require.NoError(t, err)
	internalTLS, err := clientTLSFromPEM(creds.InternalClientCertPEM(), creds.InternalClientKeyPEM(), creds.CACertPEM())
	require.NoError(t, err)

	al, err := NewHTTPAllowlist(nil)
	require.NoError(t, err)
	dal, err := NewDNSAllowlist(nil)
	require.NoError(t, err)

	bus, err := NewBus(BusOptions{
		Port:          0, // ephemeral
		ServerTLS:     serverTLS,
		InternalTLS:   internalTLS,
		HTTPAllowlist: al,
		DNSAllowlist:  dal,
		Config:        ProxyConfig{},
	})
	require.NoError(t, err)
	t.Cleanup(bus.Shutdown)
	return bus, creds
}

// dialAs connects to the bus as the given client cert. Reserved for Task 4's
// multi-user handler tests.
func dialAs(t *testing.T, bus *Bus, certPEM, keyPEM, caPEM []byte) *nats.Conn {
	t.Helper()
	tlsCfg, err := clientTLSFromPEM(certPEM, keyPEM, caPEM)
	require.NoError(t, err)
	nc, err := nats.Connect(bus.ClientURL(), nats.Secure(tlsCfg), nats.Timeout(5*time.Second))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

func TestBus_UserMappingAndPermissions(t *testing.T) {
	bus, creds := newTestBus(t)

	permCh := make(chan struct{}, 1)
	tlsCfg, err := clientTLSFromPEM(creds.SandboxClientCertPEM(), creds.SandboxClientKeyPEM(), creds.CACertPEM())
	require.NoError(t, err)
	nc, err := nats.Connect(bus.ClientURL(), nats.Secure(tlsCfg),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, e error) {
			if e != nil {
				select {
				case permCh <- struct{}{}:
				default:
				}
			}
		}))
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	require.NoError(t, nc.Publish("obs.test", []byte("{}")))
	require.NoError(t, nc.Flush())

	require.NoError(t, nc.Publish("allow.http", []byte("{}")))
	require.NoError(t, nc.Flush())
	select {
	case <-permCh:
	case <-time.After(time.Second):
		t.Fatal("expected permission violation publishing allow.http as sandbox")
	}
}
