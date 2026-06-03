package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Subjects and stream identity for the control-plane bus.
const (
	StreamLogs       = "VIBEPIT_LOGS"
	SubjectLogs      = "logs.events"
	SubjectStats     = "stats"
	SubjectConfig    = "config"
	SubjectAllowHTTP = "allow.http"
	SubjectAllowDNS  = "allow.dns"

	natsReadyTimeout    = 5 * time.Second
	publishAsyncPending = 256
)

// BusOptions configures an embedded control-plane bus.
type BusOptions struct {
	Port          int         // 0 = ephemeral (tests); production uses ControlAPIPort
	ServerTLS     *tls.Config // server cert + ClientAuth + CA
	InternalTLS   *tls.Config // vibepit-internal client cert for the loopback dial
	HTTPAllowlist *HTTPAllowlist
	DNSAllowlist  *DNSAllowlist
	Config        ProxyConfig
}

// Bus is the embedded NATS server plus the proxy's own internal client.
type Bus struct {
	ns       *natsserver.Server
	nc       *nats.Conn
	js       jetstream.JetStream
	storeDir string
	opts     BusOptions
}

func natsUsers() []*natsserver.User {
	all := func() *natsserver.Permissions {
		return &natsserver.Permissions{
			Publish:   &natsserver.SubjectPermission{Allow: []string{">"}},
			Subscribe: &natsserver.SubjectPermission{Allow: []string{">"}},
		}
	}
	return []*natsserver.User{
		{Username: NATSInternalCN, Permissions: all()},
		{Username: NATSUserCN, Permissions: &natsserver.Permissions{
			Publish: &natsserver.SubjectPermission{Allow: []string{
				SubjectStats, SubjectConfig, SubjectAllowHTTP, SubjectAllowDNS,
				"$JS.API.STREAM.INFO." + StreamLogs,
				"$JS.API.CONSUMER.CREATE." + StreamLogs + ".>",
				"$JS.API.CONSUMER.INFO." + StreamLogs + ".>",
				"$JS.API.CONSUMER.DELETE." + StreamLogs + ".>",
				"$JS.API.CONSUMER.MSG.NEXT." + StreamLogs + ".>",
			}},
			Subscribe: &natsserver.SubjectPermission{Allow: []string{"_INBOX.>"}},
		}},
		{Username: NATSSandboxCN, Permissions: &natsserver.Permissions{
			Publish:   &natsserver.SubjectPermission{Allow: []string{"obs.>"}},
			Subscribe: &natsserver.SubjectPermission{Allow: []string{"_INBOX.>"}},
		}},
	}
}

// NewBus starts the embedded server, creates the log stream, and connects the
// proxy's internal client. Producers and handlers are registered by the caller
// via RegisterHandlers / LogPublisher (added in a later task).
func NewBus(opts BusOptions) (*Bus, error) {
	storeDir, err := mkStoreDir()
	if err != nil {
		return nil, err
	}
	ns, err := natsserver.NewServer(&natsserver.Options{
		Host:      "127.0.0.1",
		Port:      portOrEphemeral(opts.Port),
		JetStream: true,
		StoreDir:  storeDir,
		NoSigs:    true,
		NoLog:     true,
		TLSConfig: opts.ServerTLS,
		TLSMap:    true,
		Users:     natsUsers(),
	})
	if err != nil {
		os.RemoveAll(storeDir) //nolint:errcheck
		return nil, fmt.Errorf("nats server: %w", err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(natsReadyTimeout) {
		ns.Shutdown()
		os.RemoveAll(storeDir) //nolint:errcheck
		return nil, fmt.Errorf("nats server not ready")
	}

	nc, err := nats.Connect(ns.ClientURL(),
		nats.Secure(opts.InternalTLS), nats.Timeout(natsReadyTimeout))
	if err != nil {
		ns.Shutdown()
		os.RemoveAll(storeDir) //nolint:errcheck
		return nil, fmt.Errorf("internal client connect: %w", err)
	}
	js, err := jetstream.New(nc, jetstream.WithPublishAsyncMaxPending(publishAsyncPending))
	if err != nil {
		nc.Close()
		ns.Shutdown()
		os.RemoveAll(storeDir) //nolint:errcheck
		return nil, fmt.Errorf("jetstream: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), natsReadyTimeout)
	defer cancel()
	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name:     StreamLogs,
		Subjects: []string{SubjectLogs},
		Storage:  jetstream.MemoryStorage,
		MaxMsgs:  LogBufferCapacity,
		Discard:  jetstream.DiscardOld,
	}); err != nil {
		nc.Close()
		ns.Shutdown()
		os.RemoveAll(storeDir) //nolint:errcheck
		return nil, fmt.Errorf("create log stream: %w", err)
	}

	return &Bus{ns: ns, nc: nc, js: js, storeDir: storeDir, opts: opts}, nil
}

// ClientURL returns the TLS URL external clients dial.
func (b *Bus) ClientURL() string { return b.ns.ClientURL() }

// Addr returns the listener host:port (used by tests / port discovery).
func (b *Bus) Addr() string { return b.ns.Addr().String() }

// Shutdown drains the internal client, stops the embedded server, and removes
// the JetStream store directory.
func (b *Bus) Shutdown() {
	if b.nc != nil {
		_ = b.nc.Drain()
	}
	if b.ns != nil {
		b.ns.Shutdown()
	}
	if b.storeDir != "" {
		os.RemoveAll(b.storeDir) //nolint:errcheck
	}
}

func mkStoreDir() (string, error) {
	dir, err := os.MkdirTemp("", "vibepit-js")
	if err != nil {
		return "", fmt.Errorf("jetstream store dir: %w", err)
	}
	return dir, nil
}

func portOrEphemeral(p int) int {
	if p <= 0 {
		return -1 // nats-server: choose a random free port
	}
	return p
}
