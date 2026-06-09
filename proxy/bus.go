package proxy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime/debug"
	"sync"
	"sync/atomic"
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

	// NATSSandboxInboxPrefix scopes the sandbox role's reply inbox so it cannot
	// subscribe to other clients' inboxes (request/reply payloads, ordered-consumer
	// log deliveries). A future sandbox-side client must dial with
	// nats.CustomInboxPrefix(NATSSandboxInboxPrefix) for request/reply to work.
	NATSSandboxInboxPrefix = "_INBOX.sandbox"

	natsReadyTimeout    = 5 * time.Second
	publishAsyncPending = 256
	// logPublishBuffer is the hand-off queue between the request path and the
	// background publisher goroutine. The request path drops entries when it is
	// full rather than blocking on the publish; sized to absorb bursts.
	logPublishBuffer = 1024
	// shutdownFlushTimeout bounds how long Shutdown waits for outstanding async
	// publish acks before tearing the bus down.
	shutdownFlushTimeout = 2 * time.Second
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
	ns        *natsserver.Server
	nc        *nats.Conn
	js        jetstream.JetStream
	storeDir  string
	clientURL string
	opts      BusOptions

	// fatal receives an error if the embedded server stops unexpectedly, so
	// Server.Run can exit and let the container restart policy rebuild the control
	// plane instead of running on with a dead control bus. shutdownInitiated is
	// closed by Shutdown to mark a deliberate teardown so the watcher does not
	// report normal shutdown as a fault.
	fatal             chan error
	shutdownInitiated chan struct{}
	shutdownOnce      sync.Once

	// logCh hands log entries from the request path to the background publisher
	// goroutine. Marshaling and PublishAsync happen only on that goroutine, so the
	// request path never blocks and the pending-window check is race-free.
	// pubDone signals the goroutine to drain and exit; pubStopped is closed when
	// it has.
	logCh      chan logMsg
	pubDone    chan struct{}
	pubStopped chan struct{}

	// droppedLogs counts log entries dropped before they reached the stream —
	// when the request-path hand-off queue is full, when the pending-ack window is
	// full, or on a marshal error. Surfaced in the stats reply so an undercount
	// (best-effort observability under overload) is visible rather than silent.
	droppedLogs atomic.Uint64
}

// logMsg is the hand-off unit on logCh. A flush barrier (flush != nil) lets
// FlushPublishes wait until the publisher has drained every entry queued before
// it; otherwise it carries a log entry to publish.
type logMsg struct {
	entry LogEntry
	flush chan struct{}
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
			Publish: &natsserver.SubjectPermission{Allow: []string{"obs.>"}},
			// Scoped reply inbox only: the sandbox role must not see other clients'
			// inboxes (host request replies, the monitor's ordered-consumer log
			// deliveries). A future sandbox client dials with
			// nats.CustomInboxPrefix(NATSSandboxInboxPrefix).
			Subscribe: &natsserver.SubjectPermission{Allow: []string{NATSSandboxInboxPrefix + ".>"}},
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
		Host:              "0.0.0.0",
		Port:              portOrEphemeral(opts.Port),
		JetStream:         true,
		StoreDir:          storeDir,
		NoSigs:            true,
		NoLog:             true,
		TLSConfig:         opts.ServerTLS,
		TLSMap:            true,
		TLSHandshakeFirst: true,
		AllowNonTLS:       false,
		Users:             natsUsers(),
	})
	if err != nil {
		os.RemoveAll(storeDir) //nolint:errcheck
		return nil, fmt.Errorf("nats server: %w", err)
	}

	// Tear down partially-constructed state unless ownership is handed to the
	// returned Bus (ok=true on success).
	var nc *nats.Conn
	ok := false
	defer func() {
		if ok {
			return
		}
		if nc != nil {
			nc.Close()
		}
		ns.Shutdown()
		os.RemoveAll(storeDir) //nolint:errcheck
	}()

	go ns.Start()
	if !ns.ReadyForConnections(natsReadyTimeout) {
		return nil, fmt.Errorf("nats server not ready")
	}

	// Dial our own server over loopback (the server cert's SAN is 127.0.0.1),
	// using the bound port even though the listener is on 0.0.0.0.
	clientURL := fmt.Sprintf("tls://127.0.0.1:%d", ns.Addr().(*net.TCPAddr).Port)
	nc, err = nats.Connect(clientURL,
		nats.Secure(opts.InternalTLS), nats.TLSHandshakeFirst(), nats.Timeout(natsReadyTimeout),
		// Never give up reconnecting to our own in-process server. The default
		// (60 attempts) would permanently close the loopback connection after a
		// transient stall, silently killing logging, stats, and the reply handlers
		// while the proxy keeps filtering traffic.
		nats.MaxReconnects(-1), nats.ReconnectWait(500*time.Millisecond))
	if err != nil {
		return nil, fmt.Errorf("internal client connect: %w", err)
	}
	js, err := jetstream.New(nc, jetstream.WithPublishAsyncMaxPending(publishAsyncPending))
	if err != nil {
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
		return nil, fmt.Errorf("create log stream: %w", err)
	}

	ok = true
	b := &Bus{
		ns: ns, nc: nc, js: js, storeDir: storeDir, clientURL: clientURL, opts: opts,
		fatal:             make(chan error, 1),
		shutdownInitiated: make(chan struct{}),
		logCh:             make(chan logMsg, logPublishBuffer),
		pubDone:           make(chan struct{}),
		pubStopped:        make(chan struct{}),
	}
	go b.watchServer()
	go b.runPublisher()
	return b, nil
}

// Fatal delivers an error when the embedded server stops unexpectedly. Server.Run
// selects on it so a dead control bus terminates the proxy and the container
// restart policy can rebuild it.
func (b *Bus) Fatal() <-chan error { return b.fatal }

// watchServer reports unexpected embedded-server death to Fatal. A deliberate
// Shutdown closes shutdownInitiated first, so normal teardown is not reported as
// a fault.
func (b *Bus) watchServer() {
	b.ns.WaitForShutdown()
	select {
	case <-b.shutdownInitiated:
		// Intentional teardown; nothing to report.
	default:
		select {
		case b.fatal <- fmt.Errorf("control bus server stopped unexpectedly"):
		default:
		}
	}
}

// ClientURL returns the loopback TLS URL clients dial (the server cert's SAN is
// 127.0.0.1; the listener binds 0.0.0.0 for container DNAT reachability).
func (b *Bus) ClientURL() string { return b.clientURL }

// Addr returns the listener host:port (used by tests / port discovery).
func (b *Bus) Addr() string { return b.ns.Addr().String() }

// Shutdown stops the publisher, waits for tail publishes to be acked, drains the
// internal client, stops the embedded server, and removes the store directory.
func (b *Bus) Shutdown() {
	// Mark this teardown intentional before stopping the server so watchServer
	// does not report it as an unexpected death, and signal the publisher to drain
	// its queue and exit.
	b.shutdownOnce.Do(func() {
		if b.shutdownInitiated != nil {
			close(b.shutdownInitiated)
		}
		if b.pubDone != nil {
			close(b.pubDone)
		}
	})
	// Wait for the publisher to drain buffered entries (issuing their PublishAsync)
	// and exit, bounded so a broken server can't wedge shutdown.
	if b.pubStopped != nil {
		select {
		case <-b.pubStopped:
		case <-time.After(shutdownFlushTimeout):
		}
	}
	if b.js != nil {
		// Best-effort: wait for outstanding async publish acks so tail log entries
		// land in the stream before teardown. nc.Drain() flushes protocol bytes but
		// does not wait for JetStream acks, and the memory stream is destroyed on
		// ns.Shutdown().
		_ = b.waitAcks(shutdownFlushTimeout)
	}
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
	PublishLog(entry LogEntry)
}

type busPublisher struct {
	ch      chan<- logMsg
	dropped *atomic.Uint64
}

func (p busPublisher) PublishLog(e LogEntry) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	// Runs synchronously on the proxy/DNS request path, so it must never block or
	// allocate beyond this hand-off: marshaling and the JetStream publish (which
	// can stall up to ~200ms when the pending-ack window fills) happen on the
	// background goroutine instead. Drop when the queue is full — logs/stats are
	// best-effort observability, not control, so a dropped entry under sustained
	// overload is preferable to slowing the filter decision. The drop is counted
	// so the undercount surfaces in stats.
	select {
	case p.ch <- logMsg{entry: e}:
	default:
		p.dropped.Add(1)
	}
}

// LogPublisher returns the producer-facing publisher.
func (b *Bus) LogPublisher() LogPublisher { return busPublisher{ch: b.logCh, dropped: &b.droppedLogs} }

// runPublisher owns all marshaling and PublishAsync calls. Because it is the
// sole publisher, the pending-window check and PublishAsync are serial (no
// TOCTOU race), and any publish stall stays off the request path. On pubDone it
// drains the queue so tail entries land before teardown, then exits.
func (b *Bus) runPublisher() {
	defer close(b.pubStopped)
	for {
		select {
		case m := <-b.logCh:
			b.handlePublish(m)
		case <-b.pubDone:
			for {
				select {
				case m := <-b.logCh:
					b.handlePublish(m)
				default:
					return
				}
			}
		}
	}
}

func (b *Bus) handlePublish(m logMsg) {
	if m.flush != nil {
		close(m.flush) // barrier reached: every entry queued before it is published
		return
	}
	if b.js.PublishAsyncPending() >= publishAsyncPending {
		b.droppedLogs.Add(1)
		return
	}
	data, err := json.Marshal(m.entry)
	if err != nil {
		b.droppedLogs.Add(1)
		return
	}
	_, _ = b.js.PublishAsync(SubjectLogs, data)
}

// FlushPublishes waits until every entry queued before this call has been
// published and acked. The barrier ensures the background publisher has drained
// the queue up to this point before we wait on PublishAsyncComplete.
func (b *Bus) FlushPublishes(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	barrier := make(chan struct{})
	select {
	case b.logCh <- logMsg{flush: barrier}:
	case <-time.After(time.Until(deadline)):
		return fmt.Errorf("publish flush timeout")
	}
	select {
	case <-barrier:
	case <-time.After(time.Until(deadline)):
		return fmt.Errorf("publish flush timeout")
	}
	return b.waitAcks(time.Until(deadline))
}

// waitAcks waits for outstanding async publishes to be acked.
func (b *Bus) waitAcks(timeout time.Duration) error {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-b.js.PublishAsyncComplete():
		return nil
	case <-t.C:
		return fmt.Errorf("publish flush timeout")
	}
}

// StatsReply is the response to the stats subject: per-domain counters plus the
// number of log entries dropped before reaching the stream. Logs/stats are
// best-effort under overload, so Dropped makes a potential undercount visible
// instead of silent.
type StatsReply struct {
	Domains map[string]DomainStats `json:"domains"`
	Dropped uint64                 `json:"dropped"`
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
		// A panic here would otherwise kill the nats dispatch goroutine (and the
		// whole proxy). Drop the bad entry and keep consuming.
		defer func() {
			if p := recover(); p != nil {
				fmt.Fprintf(os.Stderr, "proxy: log stats consumer panicked: %v\n%s\n", p, debug.Stack())
			}
		}()
		var e LogEntry
		if json.Unmarshal(m.Data(), &e) == nil {
			agg.fold(e)
		}
	}); err != nil {
		return fmt.Errorf("stats consume: %w", err)
	}

	if err := b.replyHandler(SubjectStats, func([]byte) (any, error) {
		return StatsReply{Domains: agg.snapshot(), Dropped: b.droppedLogs.Load()}, nil
	}); err != nil {
		return err
	}
	if err := b.replyHandler(SubjectConfig, func([]byte) (any, error) { return b.opts.Config, nil }); err != nil {
		return err
	}
	// Allowlist handlers are only registered when configured; a caller may
	// construct a bus without them (production always wires them via server.go).
	if b.opts.HTTPAllowlist != nil {
		if err := b.replyHandler(SubjectAllowHTTP, b.handleAllow(b.opts.HTTPAllowlist.Add)); err != nil {
			return err
		}
	}
	if b.opts.DNSAllowlist != nil {
		if err := b.replyHandler(SubjectAllowDNS, b.handleAllow(b.opts.DNSAllowlist.Add)); err != nil {
			return err
		}
	}
	return b.nc.Flush()
}

// handlerError wraps an error a handler wants reported as a server-side (500)
// failure. Any other non-nil error from a handler is treated as bad client input
// (400). Use internalErr to construct one.
type handlerError struct{ err error }

func (e handlerError) Error() string { return e.err.Error() }
func (e handlerError) Unwrap() error { return e.err }

// internalErr marks a handler error as a server-internal (500) failure.
func internalErr(err error) error { return handlerError{err: err} }

// replyHandler registers a panic-safe request/reply handler on the internal
// connection. A handler panic, an internal handler error, or a marshaling
// failure is logged and answered with a 500 service error; a plain handler error
// is answered with 400 (bad client input). This keeps a fault from propagating
// out of the nats dispatch goroutine and terminating the proxy — the same guard
// the old HTTP control API had in ServeHTTP.
func (b *Bus) replyHandler(subj string, fn func([]byte) (any, error)) error {
	_, err := b.nc.Subscribe(subj, func(msg *nats.Msg) {
		defer func() {
			if p := recover(); p != nil {
				fmt.Fprintf(os.Stderr, "proxy: control bus handler %q panicked: %v\n%s\n", subj, p, debug.Stack())
				replyServiceError(msg, "500", "internal handler error")
			}
		}()
		out, herr := fn(msg.Data)
		if herr != nil {
			var ie handlerError
			if errors.As(herr, &ie) {
				fmt.Fprintf(os.Stderr, "proxy: control bus handler %q internal error: %v\n", subj, ie.err)
				replyServiceError(msg, "500", "internal handler error")
			} else {
				replyServiceError(msg, "400", herr.Error())
			}
			return
		}
		data, err := json.Marshal(out)
		if err != nil {
			fmt.Fprintf(os.Stderr, "proxy: control bus handler %q reply marshal failed: %v\n", subj, err)
			replyServiceError(msg, "500", "internal handler error")
			return
		}
		_ = msg.Respond(data)
	})
	return err
}

// replyServiceError answers a request with a NATS service-error header and no body.
func replyServiceError(msg *nats.Msg, code, text string) {
	resp := nats.NewMsg(msg.Reply)
	resp.Header.Set("Nats-Service-Error-Code", code)
	resp.Header.Set("Nats-Service-Error", text)
	_ = msg.RespondMsg(resp)
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
		return allowResult{Added: req.Entries}, nil
	}
}

// allowResult is the reply body for the allow.http / allow.dns subjects.
type allowResult struct {
	Added []string `json:"added"`
}
