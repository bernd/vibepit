package proxy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
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
		// Bind all interfaces: in production the proxy runs in a container and
		// the host reaches the control port via Docker's published-port DNAT to
		// the bridge IP, not loopback. The auth boundary (TLSMap +
		// RequireAndVerifyClientCert) gates every connection regardless of bind,
		// and the host port is published only to 127.0.0.1 on the host side.
		Host:      "0.0.0.0",
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

	// Dial our own server over loopback (the server cert's SAN is 127.0.0.1),
	// using the bound port even though the listener is on 0.0.0.0.
	loopbackURL := fmt.Sprintf("tls://127.0.0.1:%d", ns.Addr().(*net.TCPAddr).Port)
	nc, err := nats.Connect(loopbackURL,
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

// ClientURL returns the loopback TLS URL clients dial (the server cert's SAN is
// 127.0.0.1; the listener binds 0.0.0.0 for container DNAT reachability).
func (b *Bus) ClientURL() string {
	return fmt.Sprintf("tls://127.0.0.1:%d", b.ns.Addr().(*net.TCPAddr).Port)
}

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

// LogPublisher is the dependency producers use to emit log entries onto the bus.
type LogPublisher interface {
	PublishLog(LogEntry)
}

type busPublisher struct{ js jetstream.JetStream }

func (p busPublisher) PublishLog(e LogEntry) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	// Async with a bounded pending window: back-pressures by blocking, never
	// drops. A connection-fatal error surfaces via the connection error handler.
	_, _ = p.js.PublishAsync(SubjectLogs, data)
}

// LogPublisher returns the producer-facing publisher.
func (b *Bus) LogPublisher() LogPublisher { return busPublisher{js: b.js} }

// FlushPublishes waits for outstanding async publishes to be acked.
func (b *Bus) FlushPublishes(timeout time.Duration) error {
	select {
	case <-b.js.PublishAsyncComplete():
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("publish flush timeout")
	}
}

// StatsAggregator consumes the log stream and folds entries into DomainStats.
type StatsAggregator struct {
	mu    sync.Mutex
	stats map[string]*DomainStats
}

func newStatsAggregator() *StatsAggregator {
	return &StatsAggregator{stats: map[string]*DomainStats{}}
}

func (a *StatsAggregator) fold(e LogEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.stats[e.Domain]
	if !ok {
		s = &DomainStats{}
		a.stats[e.Domain] = s
	}
	switch e.Action {
	case ActionAllow:
		s.Allowed++
	case ActionBlock:
		s.Blocked++
	}
}

func (a *StatsAggregator) snapshot() map[string]DomainStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]DomainStats, len(a.stats))
	for k, v := range a.stats {
		out[k] = *v
	}
	return out
}

// RegisterHandlers starts the stats consumer and the request/reply handlers on
// the internal connection. Call once, before producers start publishing.
func (b *Bus) RegisterHandlers() error {
	ctx := context.Background()
	stream, err := b.js.Stream(ctx, StreamLogs)
	if err != nil {
		return fmt.Errorf("stream handle: %w", err)
	}
	agg := newStatsAggregator()
	cons, err := stream.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return fmt.Errorf("stats consumer: %w", err)
	}
	if _, err := cons.Consume(func(m jetstream.Msg) {
		var e LogEntry
		if json.Unmarshal(m.Data(), &e) == nil {
			agg.fold(e)
		}
	}); err != nil {
		return fmt.Errorf("stats consume: %w", err)
	}

	reply := func(subj string, fn func([]byte) (any, error)) error {
		_, err := b.nc.Subscribe(subj, func(msg *nats.Msg) {
			out, herr := fn(msg.Data)
			if herr != nil {
				resp := nats.NewMsg(msg.Reply)
				resp.Header.Set("Nats-Service-Error-Code", "400")
				resp.Header.Set("Nats-Service-Error", herr.Error())
				_ = msg.RespondMsg(resp)
				return
			}
			data, _ := json.Marshal(out)
			_ = msg.Respond(data)
		})
		return err
	}

	if err := reply(SubjectStats, func([]byte) (any, error) { return agg.snapshot(), nil }); err != nil {
		return err
	}
	if err := reply(SubjectConfig, func([]byte) (any, error) { return b.opts.Config, nil }); err != nil {
		return err
	}
	if err := reply(SubjectAllowHTTP, b.handleAllow(b.opts.HTTPAllowlist.Add)); err != nil {
		return err
	}
	if err := reply(SubjectAllowDNS, b.handleAllow(b.opts.DNSAllowlist.Add)); err != nil {
		return err
	}
	return b.nc.Flush()
}

func (b *Bus) handleAllow(add func([]string) error) func([]byte) (any, error) {
	return func(data []byte) (any, error) {
		var req struct {
			Entries []string `json:"entries"`
		}
		if err := json.Unmarshal(data, &req); err != nil {
			return nil, fmt.Errorf("invalid JSON")
		}
		if len(req.Entries) == 0 {
			return nil, fmt.Errorf("entries required")
		}
		if err := add(req.Entries); err != nil {
			return nil, err
		}
		return map[string]any{"added": req.Entries}, nil
	}
}
