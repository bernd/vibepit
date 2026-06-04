package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
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
}

func NewControlClient(session *SessionInfo) (*ControlClient, error) {
	if session.ControlPort == "" {
		return nil, fmt.Errorf("missing control API port for session %q", session.SessionID)
	}
	tlsCfg, err := LoadSessionTLSConfig(session.SessionID)
	if err != nil {
		return nil, fmt.Errorf("load TLS credentials: %w", err)
	}
	c := &ControlClient{connEvents: make(chan bool, 8), closed: make(chan struct{})}
	nc, err := nats.Connect(
		fmt.Sprintf("tls://127.0.0.1:%s", session.ControlPort),
		nats.Secure(tlsCfg),
		nats.Timeout(5*time.Second),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(500*time.Millisecond),
		// Surface connection lifecycle so a long-lived consumer (the monitor)
		// notices a mid-session proxy death instead of waiting forever on a
		// silently-reconnecting connection.
		nats.DisconnectErrHandler(func(*nats.Conn, error) { c.signalConn(false) }),
		nats.ReconnectHandler(func(*nats.Conn) { c.signalConn(true) }),
		nats.ClosedHandler(func(*nats.Conn) { c.signalConn(false) }),
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
// on reconnect. The channel is buffered and lossy (a full buffer drops events)
// so it never blocks a NATS callback goroutine; consumers only act on the latest
// state transition.
func (c *ControlClient) ConnEvents() <-chan bool { return c.connEvents }

// Closed is closed when Close is called, so a watcher blocked on ConnEvents can
// select on it and exit at teardown rather than relying on a final lifecycle
// event (which the lossy connEvents buffer may drop).
func (c *ControlClient) Closed() <-chan struct{} { return c.closed }

func (c *ControlClient) signalConn(connected bool) {
	select {
	case c.connEvents <- connected:
	default:
	}
}

func decodeReply(msg *nats.Msg, into any) error {
	if code := msg.Header.Get("Nats-Service-Error-Code"); code != "" {
		return fmt.Errorf("%s: %s", code, msg.Header.Get("Nats-Service-Error"))
	}
	if into == nil {
		return nil
	}
	return json.Unmarshal(msg.Data, into)
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
	msg, err := c.nc.Request(subj, data, 5*time.Second)
	if err != nil {
		return fmt.Errorf("request %s: %w", subj, err)
	}
	return decodeReply(msg, into)
}

func (c *ControlClient) Stats() (map[string]proxy.DomainStats, error) {
	var stats map[string]proxy.DomainStats
	return stats, c.request(proxy.SubjectStats, nil, &stats)
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
func (c *ControlClient) SubscribeLogs(ch chan<- proxy.LogEntry) (func(), <-chan struct{}, error) {
	ctx := context.Background()
	stream, err := c.js.Stream(ctx, proxy.StreamLogs)
	if err != nil {
		return nil, nil, fmt.Errorf("stream: %w", err)
	}
	// Start near the tail so a long-running session doesn't replay thousands of
	// historical entries on open. Fall back to all if the stream is short or its
	// state can't be read.
	cfg := jetstream.OrderedConsumerConfig{DeliverPolicy: jetstream.DeliverAllPolicy}
	if info, ierr := stream.Info(ctx); ierr == nil && info.State.LastSeq > initialLogHistory {
		cfg.DeliverPolicy = jetstream.DeliverByStartSequencePolicy
		cfg.OptStartSeq = info.State.LastSeq - initialLogHistory + 1
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
