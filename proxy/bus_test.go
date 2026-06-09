package proxy

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
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

// dialAs connects to the bus as the given client cert.
func dialAs(t *testing.T, bus *Bus, certPEM, keyPEM, caPEM []byte) *nats.Conn {
	t.Helper()
	tlsCfg, err := clientTLSFromPEM(certPEM, keyPEM, caPEM)
	require.NoError(t, err)
	nc, err := nats.Connect(bus.ClientURL(), nats.Secure(tlsCfg), nats.TLSHandshakeFirst(), nats.Timeout(5*time.Second))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

// TestBus_FatalOnUnexpectedServerDeath covers finding 4: if the embedded server
// stops without an intentional Shutdown, Fatal must report it so Server.Run can
// exit and the container restart policy rebuilds the control plane.
func TestBus_FatalOnUnexpectedServerDeath(t *testing.T) {
	bus, _ := newTestBus(t)

	// Stop the embedded server directly, bypassing Bus.Shutdown.
	bus.ns.Shutdown()

	select {
	case err := <-bus.Fatal():
		require.Error(t, err)
		assert.Contains(t, err.Error(), "stopped unexpectedly")
	case <-time.After(5 * time.Second):
		t.Fatal("Fatal() did not report unexpected server death")
	}
}

// TestBus_NoFatalOnIntentionalShutdown ensures normal teardown is not reported as
// a fault (which would otherwise crash-loop the proxy on every clean shutdown).
func TestBus_NoFatalOnIntentionalShutdown(t *testing.T) {
	bus, _ := newTestBus(t)

	bus.Shutdown()

	select {
	case err := <-bus.Fatal():
		t.Fatalf("intentional shutdown must not signal Fatal, got: %v", err)
	case <-time.After(500 * time.Millisecond):
		// Expected: no fatal report.
	}
}

func TestBus_UserMappingAndPermissions(t *testing.T) {
	bus, creds := newTestBus(t)

	permCh := make(chan struct{}, 1)
	tlsCfg, err := clientTLSFromPEM(creds.SandboxClientCertPEM(), creds.SandboxClientKeyPEM(), creds.CACertPEM())
	require.NoError(t, err)
	nc, err := nats.Connect(bus.ClientURL(), nats.Secure(tlsCfg), nats.TLSHandshakeFirst(),
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

// TestBus_SandboxInboxScoped covers finding 7: the sandbox role may subscribe
// only to its own scoped inbox prefix, not the shared _INBOX.> namespace, so it
// cannot eavesdrop on other clients' request replies or log deliveries.
func TestBus_SandboxInboxScoped(t *testing.T) {
	bus, creds := newTestBus(t)

	permCh := make(chan struct{}, 1)
	tlsCfg, err := clientTLSFromPEM(creds.SandboxClientCertPEM(), creds.SandboxClientKeyPEM(), creds.CACertPEM())
	require.NoError(t, err)
	nc, err := nats.Connect(bus.ClientURL(), nats.Secure(tlsCfg), nats.TLSHandshakeFirst(),
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

	// Subscribing to the shared inbox namespace must be denied.
	_, err = nc.Subscribe("_INBOX.>", func(*nats.Msg) {})
	require.NoError(t, err) // permission errors arrive asynchronously
	require.NoError(t, nc.Flush())
	select {
	case <-permCh:
	case <-time.After(time.Second):
		t.Fatal("expected permission violation subscribing to _INBOX.> as sandbox")
	}

	// Subscribing to its own scoped prefix is allowed (no violation).
	_, err = nc.Subscribe(NATSSandboxInboxPrefix+".reply", func(*nats.Msg) {})
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
	select {
	case <-permCh:
		t.Fatal("scoped sandbox inbox subscription must not be denied")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestBusPublisher_DropAccounting covers finding 10: entries dropped on the
// request path (queue full) are counted, so the undercount is not silent.
func TestBusPublisher_DropAccounting(t *testing.T) {
	var dropped atomic.Uint64
	ch := make(chan logMsg, 1) // no draining goroutine; capacity 1
	pub := busPublisher{ch: ch, dropped: &dropped}

	pub.PublishLog(LogEntry{Domain: "a"}) // fills the single buffer slot
	pub.PublishLog(LogEntry{Domain: "b"}) // dropped
	pub.PublishLog(LogEntry{Domain: "c"}) // dropped

	assert.Equal(t, uint64(2), dropped.Load())
}

// TestBus_AsyncPublishErrorCounted covers the follow-up to finding 10: a publish
// that fails asynchronously (here, to a subject no stream is bound to, so there
// is no responder) is counted via WithPublishAsyncErrHandler, not silently lost.
func TestBus_AsyncPublishErrorCounted(t *testing.T) {
	bus, _ := newTestBus(t)

	// Synchronous send succeeds; the failure is delivered to the async handler.
	_, err := bus.js.PublishAsync("no.such.stream.subject", []byte("{}"))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return bus.droppedLogs.Load() >= 1
	}, 5*time.Second, 20*time.Millisecond, "async publish failure should be counted")
}

// TestBus_StatsExposesDropped covers finding 10: the stats reply carries the
// drop counter so a consumer can detect undercount.
func TestBus_StatsExposesDropped(t *testing.T) {
	bus, creds := newTestBus(t)
	require.NoError(t, bus.RegisterHandlers())
	bus.droppedLogs.Store(7) // simulate prior drops

	user := dialAs(t, bus, creds.ClientCertPEM(), creds.ClientKeyPEM(), creds.CACertPEM())
	msg, err := user.Request(SubjectStats, []byte("{}"), 2*time.Second)
	require.NoError(t, err)
	var stats StatsReply
	require.NoError(t, json.Unmarshal(msg.Data, &stats))
	assert.Equal(t, uint64(7), stats.Dropped)
}

// TestBus_PublishLogNonBlocking covers finding 5: PublishLog runs on the request
// hot path and must never block, even when entries arrive far faster than they
// can be published. A large burst must return promptly (the queue drops on
// overflow), and FlushPublishes must still drain and ack what was queued.
func TestBus_PublishLogNonBlocking(t *testing.T) {
	bus, _ := newTestBus(t)
	require.NoError(t, bus.RegisterHandlers())
	pub := bus.LogPublisher()

	const burst = logPublishBuffer * 4
	done := make(chan struct{})
	go func() {
		for i := range burst {
			pub.PublishLog(LogEntry{Domain: "burst.com", Action: ActionBlock, Source: SourceProxy})
			_ = i
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("PublishLog blocked on the request path under burst")
	}

	// What made it through the queue is published and acked.
	require.NoError(t, bus.FlushPublishes(2*time.Second))
}

func TestBus_StatsAndHandlers(t *testing.T) {
	bus, creds := newTestBus(t)
	require.NoError(t, bus.RegisterHandlers())

	// Publish two log entries via the LogPublisher.
	pub := bus.LogPublisher()
	pub.PublishLog(LogEntry{Domain: "a.com", Action: ActionAllow, Source: SourceProxy})
	pub.PublishLog(LogEntry{Domain: "a.com", Action: ActionBlock, Source: SourceProxy})
	require.NoError(t, bus.FlushPublishes(2*time.Second))

	user := dialAs(t, bus, creds.ClientCertPEM(), creds.ClientKeyPEM(), creds.CACertPEM())

	// stats request/reply reflects the published entries.
	require.Eventually(t, func() bool {
		msg, err := user.Request(SubjectStats, []byte("{}"), time.Second)
		if err != nil {
			return false
		}
		var stats StatsReply
		if err := json.Unmarshal(msg.Data, &stats); err != nil {
			return false
		}
		return stats.Domains["a.com"].Allowed == 1 && stats.Domains["a.com"].Blocked == 1
	}, 3*time.Second, 50*time.Millisecond)

	// allow.http adds an entry and returns it.
	body, _ := json.Marshal(map[string]any{"entries": []string{"example.com:443"}})
	msg, err := user.Request(SubjectAllowHTTP, body, time.Second)
	require.NoError(t, err)
	require.Empty(t, msg.Header.Get("Nats-Service-Error-Code"))
	var added struct {
		Added []string `json:"added"`
	}
	require.NoError(t, json.Unmarshal(msg.Data, &added))
	require.Equal(t, []string{"example.com:443"}, added.Added)
}

// TestBus_HandlerPanicRecovered proves a panicking handler does not crash the
// proxy: the caller gets a 500 service error and the bus keeps serving.
func TestBus_HandlerPanicRecovered(t *testing.T) {
	bus, creds := newTestBus(t)
	require.NoError(t, bus.replyHandler("test.panic", func([]byte) (any, error) {
		panic("boom")
	}))
	require.NoError(t, bus.RegisterHandlers())

	// Publish to the panicking subject as the broad internal role.
	internal := dialAs(t, bus, creds.InternalClientCertPEM(), creds.InternalClientKeyPEM(), creds.CACertPEM())
	msg, err := internal.Request("test.panic", []byte("{}"), 2*time.Second)
	require.NoError(t, err, "panic must be recovered and a reply still sent (no crash)")
	assert.Equal(t, "500", msg.Header.Get("Nats-Service-Error-Code"))

	// The bus is still alive: a normal request from the user role still works.
	user := dialAs(t, bus, creds.ClientCertPEM(), creds.ClientKeyPEM(), creds.CACertPEM())
	stats, err := user.Request(SubjectStats, []byte("{}"), 2*time.Second)
	require.NoError(t, err)
	assert.Empty(t, stats.Header.Get("Nats-Service-Error-Code"))
}

// TestBus_ReplyHandlerErrorCodes covers finding 9: a plain handler error maps to
// 400, an internalErr maps to 500, and a reply that fails to marshal is answered
// with a 500 service error (not a silent empty body).
func TestBus_ReplyHandlerErrorCodes(t *testing.T) {
	bus, creds := newTestBus(t)
	require.NoError(t, bus.replyHandler("test.client", func([]byte) (any, error) {
		return nil, fmt.Errorf("bad input")
	}))
	require.NoError(t, bus.replyHandler("test.internal", func([]byte) (any, error) {
		return nil, internalErr(fmt.Errorf("backend down"))
	}))
	require.NoError(t, bus.replyHandler("test.badmarshal", func([]byte) (any, error) {
		return make(chan int), nil // channels are not JSON-marshalable
	}))
	require.NoError(t, bus.RegisterHandlers())

	internal := dialAs(t, bus, creds.InternalClientCertPEM(), creds.InternalClientKeyPEM(), creds.CACertPEM())
	cases := []struct{ subj, code string }{
		{"test.client", "400"},
		{"test.internal", "500"},
		{"test.badmarshal", "500"},
	}
	for _, tc := range cases {
		t.Run(tc.subj, func(t *testing.T) {
			msg, err := internal.Request(tc.subj, []byte("{}"), 2*time.Second)
			require.NoError(t, err)
			assert.Equal(t, tc.code, msg.Header.Get("Nats-Service-Error-Code"))
		})
	}
}

// TestBus_RegisterHandlers_NilAllowlists verifies a bus built without allowlists
// (a misconfigured/partial caller) registers handlers without panicking and the
// other subjects still work; the allow.* subjects are simply not served.
func TestBus_RegisterHandlers_NilAllowlists(t *testing.T) {
	creds, err := GenerateMTLSCredentials(time.Hour)
	require.NoError(t, err)
	serverTLS, err := creds.ServerTLSConfig()
	require.NoError(t, err)
	internalTLS, err := clientTLSFromPEM(creds.InternalClientCertPEM(), creds.InternalClientKeyPEM(), creds.CACertPEM())
	require.NoError(t, err)

	bus, err := NewBus(BusOptions{ServerTLS: serverTLS, InternalTLS: internalTLS})
	require.NoError(t, err)
	t.Cleanup(bus.Shutdown)

	require.NotPanics(t, func() {
		require.NoError(t, bus.RegisterHandlers())
	})

	// stats still works without allowlists configured.
	user := dialAs(t, bus, creds.ClientCertPEM(), creds.ClientKeyPEM(), creds.CACertPEM())
	msg, err := user.Request(SubjectStats, []byte("{}"), 2*time.Second)
	require.NoError(t, err)
	require.Empty(t, msg.Header.Get("Nats-Service-Error-Code"))
}

// TestNatsUsers_Permissions pins the exact per-role permission sets so an
// accidental change to natsUsers() (a widened scope, a typo'd subject, a dropped
// restriction) fails loudly and forces a deliberate, reviewed test update.
//
// The expected role names and subjects are written as LITERAL strings on
// purpose — NOT via the NATSUserCN/StreamLogs/Subject* constants the production
// code uses. Referencing the same constants would let an accidental constant
// change move both the code and this test together and slip through unnoticed;
// hardcoding makes the test an independent source of truth for the wire format.
func TestNatsUsers_Permissions(t *testing.T) {
	users := natsUsers()
	require.Len(t, users, 3, "expected exactly internal/user/sandbox roles")

	get := func(name string) *natsserver.User {
		t.Helper()
		for _, u := range users {
			if u.Username == name {
				return u
			}
		}
		t.Fatalf("role %q not found in natsUsers()", name)
		return nil
	}

	// internal: broad — only reachable over the loopback TLS listener with a
	// cert that never leaves the proxy process.
	internal := get("CN=vibepit-internal")
	assert.Equal(t, []string{">"}, internal.Permissions.Publish.Allow, "internal publish")
	assert.Equal(t, []string{">"}, internal.Permissions.Subscribe.Allow, "internal subscribe")

	// user: the four control subjects + strictly stream-scoped JetStream API;
	// subscribe is limited to reply inboxes (no logs.events, no control subjects).
	user := get("CN=vibepit-user")
	assert.Equal(t, []string{
		"stats", "config", "allow.http", "allow.dns",
		"$JS.API.STREAM.INFO.VIBEPIT_LOGS",
		"$JS.API.CONSUMER.CREATE.VIBEPIT_LOGS.>",
		"$JS.API.CONSUMER.INFO.VIBEPIT_LOGS.>",
		"$JS.API.CONSUMER.DELETE.VIBEPIT_LOGS.>",
		"$JS.API.CONSUMER.MSG.NEXT.VIBEPIT_LOGS.>",
	}, user.Permissions.Publish.Allow, "user publish")
	assert.Equal(t, []string{"_INBOX.>"}, user.Permissions.Subscribe.Allow, "user subscribe")

	// sandbox: future obs publisher only.
	sandbox := get("CN=vibepit-sandbox")
	assert.Equal(t, []string{"obs.>"}, sandbox.Permissions.Publish.Allow, "sandbox publish")
	assert.Equal(t, []string{NATSSandboxInboxPrefix + ".>"}, sandbox.Permissions.Subscribe.Allow, "sandbox subscribe")

	// Roles are allow-list only — an accidental Deny entry would also be a change.
	for _, u := range users {
		assert.Nil(t, u.Permissions.Publish.Deny, "%s publish deny", u.Username)
		assert.Nil(t, u.Permissions.Subscribe.Deny, "%s subscribe deny", u.Username)
	}
}

// TestBus_UserRoleDeniedForeignSubject confirms the user-role allow-list is
// actually enforced on the wire: a control subject is permitted, but a foreign
// subject (the sandbox's obs.>) triggers a permission violation.
func TestBus_UserRoleDeniedForeignSubject(t *testing.T) {
	bus, creds := newTestBus(t)

	permCh := make(chan struct{}, 1)
	tlsCfg, err := clientTLSFromPEM(creds.ClientCertPEM(), creds.ClientKeyPEM(), creds.CACertPEM())
	require.NoError(t, err)
	nc, err := nats.Connect(bus.ClientURL(), nats.Secure(tlsCfg), nats.TLSHandshakeFirst(),
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

	// Allowed: a control subject the user role may publish.
	require.NoError(t, nc.Publish(SubjectStats, []byte("{}")))
	require.NoError(t, nc.Flush())

	// Denied: a subject only the sandbox role may publish.
	require.NoError(t, nc.Publish("obs.test", []byte("{}")))
	require.NoError(t, nc.Flush())
	select {
	case <-permCh:
	case <-time.After(time.Second):
		t.Fatal("expected permission violation publishing obs.test as user")
	}
}
