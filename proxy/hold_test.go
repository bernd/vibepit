package proxy

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testHoldTimeout = 200 * time.Millisecond

type holdsFixture struct {
	holds     *Holds
	log       *LogBuffer
	hub       *EventHub
	denied    *DenySet
	allowlist *HTTPAllowlist
	// events and unsubscribe are nil when the fixture has no watcher.
	events      <-chan Event
	unsubscribe func()
}

func newHoldsFixture(t *testing.T, timeout time.Duration, watch bool) *holdsFixture {
	t.Helper()
	al, err := NewHTTPAllowlist(nil)
	require.NoError(t, err)
	f := &holdsFixture{
		log:       NewLogBuffer(100),
		denied:    &DenySet{},
		allowlist: al,
	}
	f.hub, f.holds = WireEvents(f.log, timeout, f.denied, func(t Target) bool {
		return al.Allows(t.Host, t.Port)
	})
	if watch {
		f.events, f.unsubscribe = f.hub.Subscribe()
		// Unwatching ends every open hold, so no Wait outlives the test.
		t.Cleanup(f.unsubscribe)
	}
	return f
}

func (f *holdsFixture) wait(ctx context.Context, k HostKey) <-chan HoldOutcome {
	ch := make(chan HoldOutcome, 1)
	go func() { ch <- f.holds.Wait(ctx, k) }()
	return ch
}

func (f *holdsFixture) holdEntries() []LogEntry {
	var holds []LogEntry
	for _, e := range f.log.Entries() {
		if e.Action == ActionHold {
			holds = append(holds, e)
		}
	}
	return holds
}

// waiters reports how many requests wait on the open hold for k.
func (f *holdsFixture) waiters(k HostKey) int {
	f.holds.mu.Lock()
	defer f.holds.mu.Unlock()
	if hd := f.holds.held[k.Target()]; hd != nil {
		return hd.waiters
	}
	return 0
}

func (f *holdsFixture) awaitWaiters(t *testing.T, k HostKey, n int) {
	t.Helper()
	require.Eventually(t, func() bool { return f.waiters(k) == n },
		time.Second, time.Millisecond, "waiting for %d waiters on %v", n, k)
}

func recvOutcome(t *testing.T, ch <-chan HoldOutcome, within time.Duration) HoldOutcome {
	t.Helper()
	select {
	case o := <-ch:
		return o
	case <-time.After(within):
		t.Fatal("timed out waiting for Wait to return")
		return HoldNone
	}
}

func assertStillWaiting(t *testing.T, ch <-chan HoldOutcome) {
	t.Helper()
	select {
	case o := <-ch:
		t.Fatalf("Wait returned %q, want it still waiting", o)
	case <-time.After(20 * time.Millisecond):
	}
}

func proxyKey(host string) HostKey {
	return HostKey{Source: SourceProxy, Domain: host, Port: "443"}
}

func TestHolds_NotHeld(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
		watch   bool
		deny    bool
		key     HostKey
	}{
		{name: "timeout zero", timeout: 0, watch: true, key: proxyKey("example.com")},
		{name: "no subscribers", timeout: testHoldTimeout, watch: false, key: proxyKey("example.com")},
		{name: "denied target", timeout: testHoldTimeout, watch: true, deny: true, key: proxyKey("example.com")},
		{name: "IPv6 literal", timeout: testHoldTimeout, watch: true, key: proxyKey("::1")},
		{name: "DNS source", timeout: testHoldTimeout, watch: true, key: HostKey{Source: SourceDNS, Domain: "example.com", Port: "443"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newHoldsFixture(t, tt.timeout, tt.watch)
			if tt.deny {
				f.denied.Add(tt.key.Target())
			}

			start := time.Now()
			assert.Equal(t, HoldNone, f.holds.Wait(context.Background(), tt.key))
			assert.Less(t, time.Since(start), 50*time.Millisecond)
			assert.Empty(t, f.log.Entries())
			assert.Empty(t, f.holds.Open())
		})
	}
}

func TestHolds_Timeout(t *testing.T) {
	f := newHoldsFixture(t, testHoldTimeout, true)
	k := proxyKey("example.com")

	start := time.Now()
	o := f.holds.Wait(context.Background(), k)
	assert.Equal(t, HoldTimeout, o)
	assert.GreaterOrEqual(t, time.Since(start), testHoldTimeout)

	holds := f.holdEntries()
	require.Len(t, holds, 1)
	h := holds[0]
	assert.Equal(t, SourceProxy, h.Source)
	assert.Equal(t, "example.com", h.Domain)
	assert.Equal(t, "443", h.Port)
	assert.Equal(t, "domain not in allowlist", h.Reason)
	assert.Equal(t, BlockNotAllowlisted, h.Code)
	assert.WithinDuration(t, start.Add(testHoldTimeout), h.Deadline, 50*time.Millisecond)
	assert.Equal(t, HoldTimeout, h.Outcome)

	logged := recvEvent(t, f.events)
	require.Equal(t, EventLog, logged.Type)
	assert.Equal(t, h.ID, logged.Log.ID)
	assert.Equal(t, ActionHold, logged.Log.Action)

	ended := recvEvent(t, f.events)
	require.Equal(t, EventHoldEnd, ended.Type)
	assert.Equal(t, HoldEnd{ID: h.ID, HostKey: k, Outcome: HoldTimeout}, ended.HoldEnd)
}

func TestHolds_ConcurrentRequestsShareOneHold(t *testing.T) {
	f := newHoldsFixture(t, testHoldTimeout*10, true)
	k := proxyKey("example.com")

	var results []<-chan HoldOutcome
	for range 3 {
		results = append(results, f.wait(context.Background(), k))
	}
	f.awaitWaiters(t, k, 3)
	require.Len(t, f.holdEntries(), 1)

	f.holds.End(k.Target(), HoldAllowed)
	for _, ch := range results {
		assert.Equal(t, HoldAllowed, recvOutcome(t, ch, time.Second))
	}
	assert.Len(t, f.holdEntries(), 1)
	assert.Empty(t, f.holds.Open())
}

func TestHolds_EndAllowedMatchesWildcard(t *testing.T) {
	f := newHoldsFixture(t, testHoldTimeout*10, true)
	a, b, other := proxyKey("a.example.com"), proxyKey("b.example.com"), proxyKey("other.org")

	aCh := f.wait(context.Background(), a)
	bCh := f.wait(context.Background(), b)
	otherCh := f.wait(context.Background(), other)
	f.awaitWaiters(t, a, 1)
	f.awaitWaiters(t, b, 1)
	f.awaitWaiters(t, other, 1)

	require.NoError(t, f.allowlist.Add([]string{"*.example.com:443"}))
	f.holds.EndAllowed()

	assert.Equal(t, HoldAllowed, recvOutcome(t, aCh, time.Second))
	assert.Equal(t, HoldAllowed, recvOutcome(t, bCh, time.Second))
	assertStillWaiting(t, otherCh)
	open := f.holds.Open()
	require.Len(t, open, 1)
	assert.Equal(t, "other.org", open[0].Domain)
}

func TestHolds_AllowedBeforeRegistration(t *testing.T) {
	f := newHoldsFixture(t, testHoldTimeout, true)
	k := proxyKey("example.com")

	require.NoError(t, f.allowlist.Add([]string{"example.com:443"}))
	f.holds.EndAllowed()

	start := time.Now()
	assert.Equal(t, HoldAllowed, f.holds.Wait(context.Background(), k))
	assert.Less(t, time.Since(start), 50*time.Millisecond)
	assert.Empty(t, f.holdEntries())
}

func TestHolds_AllowRaceNeverStrands(t *testing.T) {
	f := newHoldsFixture(t, 5*time.Second, true)
	// 200 holds publish more events than a subscriber buffers, and a dropped
	// subscriber would end the holds as unwatched.
	go func() {
		for range f.events {
		}
	}()

	for i := range 200 {
		k := proxyKey(fmt.Sprintf("host%d.example.com", i))
		result := f.wait(context.Background(), k)
		go func() {
			if err := f.allowlist.Add([]string{k.Target().String()}); err != nil {
				panic(err)
			}
			f.holds.EndAllowed()
		}()
		require.Equal(t, HoldAllowed, recvOutcome(t, result, time.Second), "iteration %d", i)
	}
	assert.Empty(t, f.holds.Open())
}

func TestHolds_DeniedBeforeRegistration(t *testing.T) {
	f := newHoldsFixture(t, testHoldTimeout, true)
	k := proxyKey("example.com")

	f.denied.Add(k.Target())
	f.holds.End(k.Target(), HoldDenied)

	start := time.Now()
	assert.Equal(t, HoldNone, f.holds.Wait(context.Background(), k))
	assert.Less(t, time.Since(start), 50*time.Millisecond)
	assert.Empty(t, f.holdEntries())
}

func TestHolds_Open(t *testing.T) {
	f := newHoldsFixture(t, testHoldTimeout*10, true)
	a, b := proxyKey("a.example.com"), proxyKey("b.example.com")

	aCh := f.wait(context.Background(), a)
	f.awaitWaiters(t, a, 1)
	f.wait(context.Background(), b)
	f.awaitWaiters(t, b, 1)

	open := f.holds.Open()
	assert.Equal(t, f.holdEntries(), open)
	require.Len(t, open, 2)
	for _, e := range open {
		assert.NotZero(t, e.ID)
		assert.False(t, e.Deadline.IsZero())
		assert.Equal(t, HoldNone, e.Outcome)
	}

	f.holds.End(a.Target(), HoldDenied)
	recvOutcome(t, aCh, time.Second)
	open = f.holds.Open()
	require.Len(t, open, 1)
	assert.Equal(t, "b.example.com", open[0].Domain)
}

func TestHolds_Denied(t *testing.T) {
	f := newHoldsFixture(t, testHoldTimeout*10, true)
	k := proxyKey("example.com")

	result := f.wait(context.Background(), k)
	f.awaitWaiters(t, k, 1)
	f.holds.End(k.Target(), HoldDenied)

	assert.Equal(t, HoldDenied, recvOutcome(t, result, time.Second))
	holds := f.holdEntries()
	require.Len(t, holds, 1)
	assert.Equal(t, HoldDenied, holds[0].Outcome)
}

func TestHolds_CancelledRequestLeavesOthers(t *testing.T) {
	f := newHoldsFixture(t, testHoldTimeout*10, true)
	k := proxyKey("example.com")

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	first := f.wait(ctx1, k)
	second := f.wait(ctx2, k)
	f.awaitWaiters(t, k, 2)
	logged := recvEvent(t, f.events)
	require.Equal(t, EventLog, logged.Type)

	cancel1()
	assert.Equal(t, HoldAbandoned, recvOutcome(t, first, time.Second))
	assertStillWaiting(t, second)
	assert.Equal(t, 1, f.waiters(k))
	require.Len(t, f.holds.Open(), 1)

	cancel2()
	assert.Equal(t, HoldAbandoned, recvOutcome(t, second, time.Second))
	ended := recvEvent(t, f.events)
	require.Equal(t, EventHoldEnd, ended.Type)
	assert.Equal(t, HoldEnd{ID: logged.Log.ID, HostKey: k, Outcome: HoldAbandoned}, ended.HoldEnd)

	assert.Len(t, f.log.Entries(), 1)
	assert.Equal(t, HoldAbandoned, f.holdEntries()[0].Outcome)
	assert.Empty(t, f.holds.Open())
}

func TestHolds_LastWatcherLeaving(t *testing.T) {
	f := newHoldsFixture(t, 10*time.Second, true)
	k := proxyKey("example.com")

	result := f.wait(context.Background(), k)
	f.awaitWaiters(t, k, 1)
	f.unsubscribe()

	assert.Equal(t, HoldUnwatched, recvOutcome(t, result, time.Second))
	assert.Equal(t, HoldUnwatched, f.holdEntries()[0].Outcome)
	assert.Empty(t, f.holds.Open())
}

func TestHolds_Limits(t *testing.T) {
	t.Run("held targets", func(t *testing.T) {
		f := newHoldsFixture(t, 10*time.Second, true)
		for i := range maxHeldTargets {
			k := proxyKey(fmt.Sprintf("host%d.example.com", i))
			f.wait(context.Background(), k)
			f.awaitWaiters(t, k, 1)
		}

		assert.Equal(t, HoldNone, f.holds.Wait(context.Background(), proxyKey("one-more.example.com")))
		assert.Len(t, f.holdEntries(), maxHeldTargets)
		assert.Len(t, f.holds.Open(), maxHeldTargets)
	})

	t.Run("waiting requests", func(t *testing.T) {
		f := newHoldsFixture(t, 10*time.Second, true)
		k := proxyKey("example.com")
		for range maxHeldRequests {
			f.wait(context.Background(), k)
		}
		f.awaitWaiters(t, k, maxHeldRequests)

		assert.Equal(t, HoldNone, f.holds.Wait(context.Background(), k))
		assert.Equal(t, HoldNone, f.holds.Wait(context.Background(), proxyKey("other.org")))
		assert.Equal(t, maxHeldRequests, f.waiters(k))
		assert.Len(t, f.holdEntries(), 1)
	})
}

func TestHolds_NewHoldAfterEnd(t *testing.T) {
	f := newHoldsFixture(t, testHoldTimeout, true)
	k := proxyKey("example.com")

	assert.Equal(t, HoldTimeout, f.holds.Wait(context.Background(), k))
	assert.Equal(t, HoldTimeout, f.holds.Wait(context.Background(), k))

	holds := f.holdEntries()
	require.Len(t, holds, 2)
	assert.NotEqual(t, holds[0].ID, holds[1].ID)
	assert.Equal(t, HoldTimeout, holds[0].Outcome)
	assert.Equal(t, HoldTimeout, holds[1].Outcome)
}

func TestHolds_UnwatchedHookRechecksWatchers(t *testing.T) {
	f := newHoldsFixture(t, 10*time.Second, true)
	k := proxyKey("example.com")

	result := f.wait(context.Background(), k)
	f.awaitWaiters(t, k, 1)
	// The hook runs after the hub's lock is released, so a new watcher can
	// arrive before it does. It must then leave the holds alone.
	f.holds.endUnwatched()

	assertStillWaiting(t, result)
	assert.Len(t, f.holds.Open(), 1)
}
