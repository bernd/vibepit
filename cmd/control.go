package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bernd/vibepit/config"
	"github.com/bernd/vibepit/proxy"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// ControlClient talks to a running proxy's embedded NATS control bus over mTLS.
type ControlClient struct {
	nc         *nats.Conn
	js         jetstream.JetStream
	connEvents chan bool
	closed     chan struct{}
	closeOnce  sync.Once

	// reconnects counts observed reconnections (bumped by signalConn on a
	// reconnect). WatchLogs uses it as the resubscribe generation so a retry and a
	// reconnect event firing together don't both rebuild the consumer.
	reconnects atomic.Uint64

	// requestTimeout bounds each request/reply call.
	requestTimeout time.Duration

	// lastAsyncErr records the most recent async protocol error (e.g. a NATS
	// permissions violation, which surfaces here rather than as a request error).
	// request() uses it to turn a bare timeout into a useful message when the
	// error arrived during the call.
	mu           sync.Mutex
	lastAsyncErr error
	lastAsyncAt  time.Time
}

func NewControlClient(session *SessionInfo) (*ControlClient, error) {
	if session.ControlPort == "" {
		return nil, fmt.Errorf("missing control API port for session %q", session.SessionID)
	}
	tlsCfg, err := LoadSessionTLSConfig(session.SessionID)
	if err != nil {
		return nil, fmt.Errorf("load TLS credentials: %w", err)
	}
	c := &ControlClient{
		connEvents:     make(chan bool, 8),
		closed:         make(chan struct{}),
		requestTimeout: 5 * time.Second,
	}
	nc, err := nats.Connect(
		fmt.Sprintf("tls://127.0.0.1:%s", session.ControlPort),
		nats.Secure(tlsCfg),
		nats.TLSHandshakeFirst(),
		nats.Timeout(5*time.Second),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(500*time.Millisecond),
		// Surface connection lifecycle so a long-lived consumer (the monitor)
		// notices a mid-session proxy death instead of waiting forever on a
		// silently-reconnecting connection.
		nats.DisconnectErrHandler(func(*nats.Conn, error) { c.signalConn(false) }),
		nats.ReconnectHandler(func(*nats.Conn) { c.signalConn(true) }),
		nats.ClosedHandler(func(*nats.Conn) { c.signalConn(false) }),
		// A NATS permissions violation is delivered as an async protocol error,
		// not as a request error — the request itself just times out. Record it so
		// request() can surface the real cause instead of a bare timeout.
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, e error) { c.recordAsyncErr(e) }),
	)
	if err != nil {
		return nil, fmt.Errorf("connect control bus: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream: %w", err)
	}
	c.nc = nc
	c.js = js
	return c, nil
}

// Close tears down the connection and unblocks any ConnEvents watcher. It is
// idempotent.
func (c *ControlClient) Close() {
	c.closeOnce.Do(func() { close(c.closed) })
	c.nc.Close()
}

// ConnEvents delivers connection-state changes: false on disconnect/close, true
// on reconnect. The channel is buffered and lossy so it never blocks a NATS
// callback goroutine, but a full buffer evicts the OLDEST event rather than
// dropping the newest (see signalConn) — the most recent transition is the one
// that matters, and losing it would leave a consumer acting on stale state.
func (c *ControlClient) ConnEvents() <-chan bool { return c.connEvents }

// Closed is closed when Close is called, so a watcher blocked on ConnEvents can
// select on it and exit at teardown rather than relying on a final lifecycle
// event (which the lossy connEvents buffer may drop).
func (c *ControlClient) Closed() <-chan struct{} { return c.closed }

func (c *ControlClient) signalConn(connected bool) {
	// A reconnect (connected=true is only signaled by the reconnect handler, never
	// the initial connect) advances the resubscribe generation WatchLogs keys off.
	if connected {
		c.reconnects.Add(1)
	}
	for {
		select {
		case c.connEvents <- connected:
			return
		default:
			// Buffer full: evict the oldest event to make room for this newer
			// state, so the latest transition is never the one that gets dropped.
			select {
			case <-c.connEvents:
			default:
			}
		}
	}
}

func (c *ControlClient) recordAsyncErr(e error) {
	if e == nil {
		return
	}
	c.mu.Lock()
	c.lastAsyncErr = e
	c.lastAsyncAt = time.Now()
	c.mu.Unlock()
}

// asyncErrSince returns the async error recorded after t, if any.
func (c *ControlClient) asyncErrSince(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastAsyncErr != nil && c.lastAsyncAt.After(t) {
		return c.lastAsyncErr
	}
	return nil
}

func decodeReply(msg *nats.Msg, into any) error {
	if code := msg.Header.Get("Nats-Service-Error-Code"); code != "" {
		return fmt.Errorf("%s: %s", code, msg.Header.Get("Nats-Service-Error"))
	}
	if into == nil {
		return nil
	}
	if err := json.Unmarshal(msg.Data, into); err != nil {
		return fmt.Errorf("decoding reply: %w", err)
	}
	return nil
}

func (c *ControlClient) request(subj string, body any, into any) error {
	data := []byte("{}")
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		data = b
	}
	start := time.Now()
	msg, err := c.nc.Request(subj, data, c.requestTimeout)
	if err != nil {
		// A permissions violation surfaces only via the async error handler and
		// makes the request time out with no reply. If one arrived during this
		// call, report it instead of the bare timeout.
		if ae := c.asyncErrSince(start); ae != nil {
			return fmt.Errorf("request %s: %w", subj, ae)
		}
		return fmt.Errorf("request %s: %w", subj, err)
	}
	return decodeReply(msg, into)
}

func (c *ControlClient) Stats() (proxy.StatsReply, error) {
	var stats proxy.StatsReply
	if err := c.request(proxy.SubjectStats, nil, &stats); err != nil {
		return proxy.StatsReply{}, err
	}
	return stats, nil
}

func (c *ControlClient) Config() (*config.MergedConfig, error) {
	var cfg config.MergedConfig
	return &cfg, c.request(proxy.SubjectConfig, nil, &cfg)
}

func (c *ControlClient) AllowHTTP(entries []string) ([]string, error) {
	return c.postAllow(proxy.SubjectAllowHTTP, entries)
}

func (c *ControlClient) AllowDNS(entries []string) ([]string, error) {
	return c.postAllow(proxy.SubjectAllowDNS, entries)
}

func (c *ControlClient) postAllow(subj string, entries []string) ([]string, error) {
	var result struct {
		Added []string `json:"added"`
	}
	if err := c.request(subj, map[string]any{"entries": entries}, &result); err != nil {
		return nil, err
	}
	return result.Added, nil
}

// initialLogHistory bounds how many retained entries the monitor replays on
// connect, matching the old control API's last-25 initial response instead of
// flooding the TUI with the full retained ring (up to LogBufferCapacity).
const initialLogHistory uint64 = 25

// SubscribeLogs delivers a bounded tail of retained history then live entries in
// stream order. It returns a stop function and a done channel that is closed
// when stop is called: callers blocked on ch should also select on done so they
// unblock at teardown (the channel is never closed, so it is always safe to
// send to / receive from). stop is idempotent.
//
// ctx bounds only the JetStream setup calls (stream lookup, info, consumer
// creation) so a caller canceling it can interrupt a stalled subscribe; the
// consumer itself runs until stop is called, independent of ctx.
func (c *ControlClient) SubscribeLogs(ctx context.Context, ch chan<- proxy.LogEntry) (func(), <-chan struct{}, error) {
	stream, err := c.js.Stream(ctx, proxy.StreamLogs)
	if err != nil {
		return nil, nil, fmt.Errorf("stream: %w", err)
	}
	// Start near the tail so a long-running session doesn't replay thousands of
	// historical entries on open. If the stream state can't be read, default to
	// live-only delivery rather than DeliverAll — replaying the full retained ring
	// (up to LogBufferCapacity) would flood the UI, so losing the small history
	// tail is the safer fallback on an already-degraded connection.
	cfg := jetstream.OrderedConsumerConfig{DeliverPolicy: jetstream.DeliverNewPolicy}
	if info, ierr := stream.Info(ctx); ierr == nil {
		if info.State.LastSeq > initialLogHistory {
			cfg.DeliverPolicy = jetstream.DeliverByStartSequencePolicy
			cfg.OptStartSeq = info.State.LastSeq - initialLogHistory + 1
		} else {
			// Short stream: replaying everything is bounded by definition.
			cfg.DeliverPolicy = jetstream.DeliverAllPolicy
		}
	}
	cons, err := stream.OrderedConsumer(ctx, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("ordered consumer: %w", err)
	}
	done := make(chan struct{})
	cc, err := cons.Consume(func(m jetstream.Msg) {
		var e proxy.LogEntry
		if json.Unmarshal(m.Data(), &e) != nil {
			return
		}
		// Never block the consumer callback past teardown: selecting on done
		// means a full buffer can't wedge the callback (and so cc.Stop() can't
		// hang waiting on it).
		select {
		case ch <- e:
		case <-done:
		}
	})
	if err != nil {
		return nil, nil, fmt.Errorf("consume: %w", err)
	}
	stop := sync.OnceFunc(func() {
		close(done) // unblock the callback and any waiter selecting on done
		cc.Stop()
	})
	return stop, done, nil
}

// logChannelBuffer is the buffer on the event channel WatchLogs returns and on
// the internal consumer-delivery channel that feeds it.
const logChannelBuffer = 256

// subscribeRetryDelay is how long WatchLogs waits before retrying a failed log
// subscription, so a transient JetStream API error recovers on its own instead
// of leaving the stream permanently silent.
const subscribeRetryDelay = time.Second

// LogEventKind distinguishes the two kinds of item on the WatchLogs stream.
type LogEventKind int

const (
	// LogEntryEvent carries a log line in Entry.
	LogEntryEvent LogEventKind = iota
	// LogConnEvent carries a connection-state change in Connected.
	LogConnEvent
)

// LogEvent is one item on the WatchLogs stream: either a delivered log entry or
// a connection-state transition.
type LogEvent struct {
	Kind      LogEventKind
	Entry     proxy.LogEntry
	Connected bool
}

// WatchLogs returns a single ordered stream of log entries and connection-state
// changes, and owns the whole subscription lifecycle behind it: it opens the
// ordered consumer, retries a failed subscription, and on reconnect tears the
// old consumer down and creates a fresh one. The last part matters because a
// restarted proxy comes up with a brand-new in-memory stream whose sequence
// numbers reset to 1, so the previous consumer's cursor would be stale and
// silently deliver nothing. Callers just render what arrives.
//
// The returned channel is closed when ctx is canceled or the client is closed;
// cancel ctx to stop watching.
func (c *ControlClient) WatchLogs(ctx context.Context) <-chan LogEvent {
	out := make(chan LogEvent, logChannelBuffer)
	go c.runWatch(ctx, out)
	return out
}

func (c *ControlClient) runWatch(ctx context.Context, out chan<- LogEvent) {
	defer close(out)

	// emit delivers an event without blocking past teardown: a caller that has
	// stopped reading (canceled ctx / closed the client) must not wedge this
	// goroutine. Returns false when the watcher should exit.
	emit := func(ev LogEvent) bool {
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		case <-c.Closed():
			return false
		}
	}

	// entriesCh receives from the currently active consumer. Each subscribe points
	// it at a fresh channel, and stopActive nils it the moment a consumer is torn
	// down, so entries the replaced (or a failed-resubscribe's stopped) consumer
	// had buffered can't leak into the new consumer's replay. A nil channel simply
	// never fires in the select.
	var entriesCh chan proxy.LogEntry
	var stop func()
	stopActive := func() {
		if stop != nil {
			stop()
			stop = nil
		}
		entriesCh = nil
	}
	defer stopActive()

	var retryC <-chan time.Time
	// subscribe opens a fresh consumer on a fresh channel, replacing any active
	// one. On failure it arms a retry timer instead of latching dead (and leaves
	// entriesCh nil, so nothing stale is read during the retry interval).
	subscribe := func() bool {
		stopActive()
		ch := make(chan proxy.LogEntry, logChannelBuffer)
		s, _, err := c.SubscribeLogs(ctx, ch)
		if err != nil {
			retryC = time.After(subscribeRetryDelay)
			return false
		}
		entriesCh = ch
		stop = s
		retryC = nil
		return true
	}

	// subEpoch records the reconnect generation our live consumer was opened for.
	// ensureSubscribed (re)subscribes only when there is no live consumer for the
	// current generation, so a retry and a reconnect event that fire together
	// don't each rebuild the consumer and replay history twice. A new in-memory
	// stream (sequence reset) only appears across a reconnect, so the reconnect
	// counter is the right generation key.
	var subEpoch uint64
	ensureSubscribed := func() bool {
		cur := c.reconnects.Load()
		if stop != nil && subEpoch == cur {
			return true
		}
		if !subscribe() {
			return false
		}
		subEpoch = cur
		return true
	}

	if !ensureSubscribed() {
		if !emit(LogEvent{Kind: LogConnEvent, Connected: false}) {
			return
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.Closed():
			return
		case e := <-entriesCh:
			if !emit(LogEvent{Kind: LogEntryEvent, Entry: e}) {
				return
			}
		case connected := <-c.ConnEvents():
			// On reconnect, rebuild the consumer before reporting recovery so it is
			// live by the time the caller clears its disconnect state; ensureSubscribed
			// coalesces with a coincident retry. A disconnect is reported as-is; the
			// dead consumer is replaced on the next reconnect.
			ev := LogEvent{Kind: LogConnEvent, Connected: connected}
			if connected && !ensureSubscribed() {
				ev.Connected = false // resubscribe failed; still down, retry armed
			}
			if !emit(ev) {
				return
			}
		case <-retryC:
			if ensureSubscribed() {
				if !emit(LogEvent{Kind: LogConnEvent, Connected: true}) {
					return
				}
			}
		}
	}
}
