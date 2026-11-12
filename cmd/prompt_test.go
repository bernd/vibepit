package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bernd/vibepit/overlay"
	"github.com/bernd/vibepit/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

func TestBlockWatcher_Next(t *testing.T) {
	block := func(id uint64, src proxy.Source, domain, port string) proxy.LogEntry {
		return proxy.LogEntry{ID: id, Source: src, Domain: domain, Port: port, Action: proxy.ActionBlock}
	}
	withCode := func(e proxy.LogEntry, c proxy.BlockCode) proxy.LogEntry {
		e.Code = c
		return e
	}
	allow := func(id uint64, domain string) proxy.LogEntry {
		return proxy.LogEntry{ID: id, Source: proxy.SourceProxy, Domain: domain, Port: "443", Action: proxy.ActionAllow}
	}

	tests := []struct {
		name    string
		batches [][]proxy.LogEntry
		want    [][]string // allow values per batch
	}{
		{
			name:    "ignores allowed entries",
			batches: [][]proxy.LogEntry{{allow(1, "a.com"), block(2, proxy.SourceProxy, "b.com", "443")}},
			want:    [][]string{{"b.com:443"}},
		},
		{
			name: "dedupes same target across batches",
			batches: [][]proxy.LogEntry{
				{block(1, proxy.SourceProxy, "b.com", "443"), block(2, proxy.SourceProxy, "b.com", "443")},
				{block(3, proxy.SourceProxy, "b.com", "443"), block(4, proxy.SourceProxy, "b.com", "80")},
			},
			want: [][]string{{"b.com:443"}, {"b.com:80"}},
		},
		{
			name: "dns and proxy for same domain are distinct",
			batches: [][]proxy.LogEntry{
				{block(1, proxy.SourceDNS, "b.com", ""), block(2, proxy.SourceProxy, "b.com", "443")},
			},
			want: [][]string{{"b.com", "b.com:443"}},
		},
		{
			name: "skips blocks allowing can't undo",
			batches: [][]proxy.LogEntry{{
				withCode(block(1, proxy.SourceProxy, "a.com", "443"), proxy.BlockCIDR),
				withCode(block(2, proxy.SourceProxy, "b.com", "443"), proxy.BlockResolutionFailed),
				withCode(block(3, proxy.SourceProxy, "c.com", "443"), proxy.BlockNotAllowlisted),
				withCode(block(4, proxy.SourceDNS, "d.com", ""), proxy.BlockCIDR),
			}},
			want: [][]string{{"c.com:443"}},
		},
		{
			// A proxy from before causes were logged sends none.
			name:    "keeps blocks without a cause",
			batches: [][]proxy.LogEntry{{block(1, proxy.SourceProxy, "a.com", "443")}},
			want:    [][]string{{"a.com:443"}},
		},
		{
			name:    "empty batch",
			batches: [][]proxy.LogEntry{{block(5, proxy.SourceProxy, "b.com", "443")}, {}},
			want:    [][]string{{"b.com:443"}, nil},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var bw blockWatcher
			for i, batch := range tt.batches {
				got, err := bw.Next(batch)
				require.NoError(t, err)
				var vals []string
				for _, e := range got {
					vals = append(vals, e.Target().String())
				}
				assert.Equal(t, tt.want[i], vals, "batch %d", i)
			}
		})
	}
}

// next is Next for a batch that must not reach the target limit.
func next(t *testing.T, bw *blockWatcher, batch ...proxy.LogEntry) []proxy.LogEntry {
	t.Helper()
	fresh, err := bw.Next(batch)
	require.NoError(t, err)
	return fresh
}

func TestBlockWatcher_Forget(t *testing.T) {
	var bw blockWatcher
	e := proxy.LogEntry{ID: 1, Source: proxy.SourceProxy, Domain: "b.com", Port: "443", Action: proxy.ActionBlock}
	require.Len(t, next(t, &bw, e), 1)
	e.ID++
	assert.Empty(t, next(t, &bw, e), "asked already")
	bw.Forget(e.Target())
	e.ID++
	assert.Len(t, next(t, &bw, e), 1, "a forgotten target prompts again")

	bw.Forget(proxy.Target{Source: proxy.SourceProxy, Host: "never.com", Port: "443"})
	assert.Len(t, bw.asked, 1, "forgetting an unseen target doesn't track it")
}

func TestBlockWatcher_LimitsTargets(t *testing.T) {
	var bw blockWatcher
	block := func(i int) proxy.LogEntry {
		return proxy.LogEntry{ID: uint64(i + 1), Source: proxy.SourceProxy, Domain: fmt.Sprintf("d%d.com", i), Port: "443", Action: proxy.ActionBlock}
	}
	var batch []proxy.LogEntry
	for i := range maxPromptTargets {
		batch = append(batch, block(i))
	}
	require.Len(t, next(t, &bw, batch...), maxPromptTargets)
	bw.Forget(batch[0].Target())
	assert.Len(t, next(t, &bw, batch[0]), 1, "a known target is asked about again")

	fresh, err := bw.Next([]proxy.LogEntry{block(maxPromptTargets)})
	assert.ErrorIs(t, err, errTooManyTargets, "a forgotten target still counts")
	assert.Empty(t, fresh)
}

// blocked is a blocked request for domain:443 that allowing would unblock.
func blocked(domain string) proxy.LogEntry {
	return proxy.LogEntry{Domain: domain, Port: "443", Action: proxy.ActionBlock, Source: proxy.SourceProxy}
}

// startPrompter runs runBlockPrompter until the test ends and returns its
// result.
func startPrompter(t *testing.T, client *ControlClient, interval time.Duration, prompt promptFunc) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- runBlockPrompter(t.Context(), client, interval, prompt) }()
	return done
}

// prompterClient returns a client for api and a function that waits until
// the stream it opens has sent its cursor: what is logged from then on
// reaches the prompter, what was logged before is skipped.
func prompterClient(t *testing.T, api http.Handler) (*ControlClient, func()) {
	t.Helper()
	connected := make(chan struct{}, 1)
	h := &cutStream{api: api, drop: func(_ int, e proxy.Event) bool {
		if e.Type == proxy.EventCursor {
			select {
			case connected <- struct{}{}:
			default:
			}
		}
		return false
	}}
	return testControlClient(t, h), func() {
		t.Helper()
		receive(t, connected, "event stream")
	}
}

// holdRequest holds a request for domain:443 until the test ends or the
// hold ends.
func (ep *eventsProxy) holdRequest(t *testing.T, domain string) {
	go ep.holds.Wait(t.Context(), proxy.HostKey{Source: proxy.SourceProxy, Domain: domain, Port: "443"})
}

// queued returns the targets in q, first target first.
func queued(q *promptQueue) []string {
	var targets []string
	for _, it := range q.Items() {
		targets = append(targets, it.Entry.Target().String())
	}
	return targets
}

// decideAll is a prompt that, like the approve screen, decides the targets
// it shows until the queue is empty.
func decideAll(prompted chan<- []string) promptFunc {
	return func(ctx context.Context, q *promptQueue) (promptResult, error) {
		for q.Len() > 0 {
			targets := queued(q)
			prompted <- targets
			for _, it := range q.Items()[:len(targets)] {
				q.Remove(it.Entry.Target())
			}
		}
		return promptResult{}, nil
	}
}

// dismiss is what the approve screen reports when the user dismisses it
// with q as it is.
func dismiss(q *promptQueue) promptResult {
	var res promptResult
	for _, it := range q.Items() {
		res.Dismissed = append(res.Dismissed, it.Entry.Target())
	}
	return res
}

func receive[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(time.Second):
		require.FailNow(t, "no "+what)
		panic("unreachable")
	}
}

func assertQuiet[T any](t *testing.T, ch <-chan T, what string) {
	t.Helper()
	select {
	case v := <-ch:
		assert.Fail(t, fmt.Sprintf("unexpected %s: %v", what, v))
	case <-time.After(50 * time.Millisecond):
	}
}

func TestRunBlockPrompter(t *testing.T) {
	ep := newEventsProxy(t)
	client, connected := prompterClient(t, ep.api)
	// Blocked before the prompter starts: must not prompt.
	ep.log.Add(blocked("old.com"))

	prompted := make(chan []string, 10)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runBlockPrompter(ctx, client, 5*time.Millisecond, decideAll(prompted))
	}()
	connected()
	ep.log.Add(blocked("new.com"))
	ep.log.Add(blocked("new.com"))
	ep.log.Add(proxy.LogEntry{Domain: "fine.com", Port: "443", Action: proxy.ActionAllow, Source: proxy.SourceProxy})

	assert.Equal(t, []string{"new.com:443"}, receive(t, prompted, "prompt for new.com"))

	cancel()
	receive(t, done, "stop on cancel")
	assertQuiet(t, prompted, "prompt")
}

func TestRunBlockPrompter_ConnectsLate(t *testing.T) {
	ep := newEventsProxy(t)
	ep.log.Add(blocked("old.com"))

	// Fail the first requests so the feed doesn't connect at once.
	var calls atomic.Int32
	client, connected := prompterClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 3 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		ep.api.ServeHTTP(w, r)
	}))

	prompted := make(chan []string, 10)
	startPrompter(t, client, 5*time.Millisecond, decideAll(prompted))
	connected()
	ep.log.Add(blocked("new.com"))

	assert.Equal(t, []string{"new.com:443"}, receive(t, prompted, "prompt for new.com"),
		"history from before the prompter started must not be replayed")
	assertQuiet(t, prompted, "prompt")
}

func TestRunBlockPrompter_ReasksAfterAFailedPrompt(t *testing.T) {
	program := errors.New("program failed")
	tests := []struct {
		name    string
		fail    func(call int) error // the error of prompt call (from 1), nil: decides
		prompts int                  // prompts shown in all
		wantErr error                // what the prompter stops with; nil: it keeps going
	}{
		{"barrier timeouts don't count as failures", func(call int) error {
			if call <= maxPromptFailures+2 {
				return fmt.Errorf("show: %w", overlay.ErrBarrierTimeout)
			}
			return nil
		}, maxPromptFailures + 3, nil},
		{"a busy session doesn't count as a failure", func(call int) error {
			if call <= maxPromptFailures+2 {
				return fmt.Errorf("show: %w", overlay.ErrSessionBusy)
			}
			return nil
		}, maxPromptFailures + 3, nil},
		{"program errors in a row stop prompting", func(int) error { return program }, maxPromptFailures, errPromptsFailing},
		{"a shown prompt resets the count", func(call int) error {
			if call%2 == 1 {
				return program
			}
			return nil
		}, 2 * maxPromptFailures, nil},
		{"program error once", func(call int) error {
			if call == 1 {
				return program
			}
			return nil
		}, 2, nil},
		{"prompts unavailable", func(int) error { return fmt.Errorf("show: %w", overlay.ErrUnavailable) }, 1, overlay.ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ep := newEventsProxy(t)
			client, connected := prompterClient(t, ep.api)
			prompted := make(chan []string, 20)
			var calls atomic.Int32
			done := startPrompter(t, client, 5*time.Millisecond, func(ctx context.Context, q *promptQueue) (promptResult, error) {
				prompted <- queued(q)
				if err := tt.fail(int(calls.Add(1))); err != nil {
					return promptResult{}, err
				}
				q.Clear()
				return promptResult{}, nil
			})
			connected()
			// A decided target isn't asked about again, so every prompt
			// gets a new one.
			var host string
			for i := range tt.prompts {
				host = fmt.Sprintf("h%d.com", i)
				ep.log.Add(blocked(host))
				receive(t, prompted, fmt.Sprintf("prompt %d", i+1))
			}
			if tt.wantErr != nil {
				assert.ErrorIs(t, receive(t, done, "return"), tt.wantErr)
			}
			// The last target was decided, or prompting stopped.
			ep.log.Add(blocked(host))
			assertQuiet(t, prompted, "prompt")
		})
	}
}

func TestRunBlockPrompter_StopsWhenPromptsAreUnavailable(t *testing.T) {
	ep := newEventsProxy(t)
	client, connected := prompterClient(t, ep.api)
	prompted := make(chan []string, 10)
	done := startPrompter(t, client, 5*time.Millisecond, func(ctx context.Context, q *promptQueue) (promptResult, error) {
		prompted <- queued(q)
		return promptResult{}, fmt.Errorf("show: %w", overlay.ErrUnavailable)
	})
	connected()
	ep.log.Add(blocked("a.com"))
	assert.ErrorIs(t, receive(t, done, "return"), overlay.ErrUnavailable)
	assert.Len(t, prompted, 1)
}

func TestRunBlockPrompter_BurstPastTail(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) (*proxy.LogBuffer, *ControlClient, func())
	}{
		{"stream", func(t *testing.T) (*proxy.LogBuffer, *ControlClient, func()) {
			ep := newEventsProxy(t)
			client, connected := prompterClient(t, ep.api)
			return ep.log, client, connected
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, client, ready := tt.setup(t)
			prompted := make(chan []string, 200)
			startPrompter(t, client, 20*time.Millisecond, decideAll(prompted))
			ready()
			n := proxy.TailSize + 5
			for i := range n {
				log.Add(blocked(fmt.Sprintf("d%d.com", i)))
			}
			seen := map[string]bool{}
			for len(seen) < n {
				for _, target := range receive(t, prompted, fmt.Sprintf("prompt after %d of %d targets", len(seen), n)) {
					seen[target] = true
				}
			}
		})
	}
}

func TestRunBlockPrompter_QueuesTargetsWhileShowing(t *testing.T) {
	ep := newEventsProxy(t)
	client, connected := prompterClient(t, ep.api)
	shown := make(chan *promptQueue, 10)
	release := make(chan struct{})
	startPrompter(t, client, 5*time.Millisecond, func(ctx context.Context, q *promptQueue) (promptResult, error) {
		shown <- q
		<-release
		q.Clear()
		return promptResult{}, nil
	})
	connected()

	ep.holdRequest(t, "a.com")
	q := receive(t, shown, "prompt")
	for i, domain := range []string{"b.com", "c.com"} {
		ep.holdRequest(t, domain)
		require.Eventually(t, func() bool { return q.Len() == i+2 }, time.Second, time.Millisecond, "%s queued", domain)
	}
	assert.Equal(t, []string{"a.com:443", "b.com:443", "c.com:443"}, queued(q))
	for _, it := range q.Items() {
		assert.False(t, it.Deadline.IsZero(), "%s is held", it.Entry.Target())
	}
	close(release)
	assertQuiet(t, shown, "second prompt")
}

func TestRunBlockPrompter_DismissPromptsAgain(t *testing.T) {
	ep := newEventsProxy(t)
	client, connected := prompterClient(t, ep.api)
	prompted := make(chan []string, 10)
	startPrompter(t, client, 5*time.Millisecond, func(ctx context.Context, q *promptQueue) (promptResult, error) {
		prompted <- queued(q)
		return dismiss(q), nil
	})
	connected()

	ep.log.Add(blocked("a.com"))
	assert.Equal(t, []string{"a.com:443"}, receive(t, prompted, "prompt"))
	assertQuiet(t, prompted, "prompt before the next block")
	ep.log.Add(blocked("a.com"))
	assert.Equal(t, []string{"a.com:443"}, receive(t, prompted, "prompt after the dismissal"))
}

// The proxy answers a held request with a block once the hold ends. That
// block is the dismissed hold's own, not a new request: the target stays
// dismissed until one comes.
func TestRunBlockPrompter_DismissedHoldStaysDismissed(t *testing.T) {
	ep := newEventsProxy(t)
	client, connected := prompterClient(t, ep.api)
	prompted := make(chan []string, 10)
	startPrompter(t, client, 5*time.Millisecond, func(ctx context.Context, q *promptQueue) (promptResult, error) {
		prompted <- queued(q)
		return dismiss(q), nil
	})
	connected()

	ep.holdRequest(t, "a.com")
	assert.Equal(t, []string{"a.com:443"}, receive(t, prompted, "prompt"))
	open := ep.holds.Open()
	require.Len(t, open, 1)
	ep.holds.End(open[0].Target(), proxy.HoldTimeout)
	end := blocked("a.com")
	end.HoldID = open[0].ID
	ep.log.Add(end)
	assertQuiet(t, prompted, "prompt for the block that ended the dismissed hold")

	ep.log.Add(blocked("a.com"))
	assert.Equal(t, []string{"a.com:443"}, receive(t, prompted, "prompt for a new request"))
}

// The proxy opens a hold only for an undecided target, so an open hold
// reaches the prompt without a /check round trip.
func TestRunBlockPrompter_OpenHoldsSkipTheCheck(t *testing.T) {
	ep := newEventsProxy(t)
	var checks atomic.Int32
	client, connected := prompterClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/check" {
			checks.Add(1)
		}
		ep.api.ServeHTTP(w, r)
	}))
	prompted := make(chan []string, 10)
	startPrompter(t, client, 5*time.Millisecond, decideAll(prompted))
	connected()

	ep.holdRequest(t, "held.com")
	assert.Equal(t, []string{"held.com:443"}, receive(t, prompted, "prompt for the hold"))
	assert.Zero(t, checks.Load(), "checks for an open hold")

	ep.log.Add(blocked("blocked.com"))
	assert.Equal(t, []string{"blocked.com:443"}, receive(t, prompted, "prompt for the block"))
	assert.Equal(t, int32(1), checks.Load(), "a block is still checked")
}

func TestRunBlockPrompter_DismissAfterTheQueueEmptied(t *testing.T) {
	ep := newEventsProxy(t)
	client, connected := prompterClient(t, ep.api)
	prompted := make(chan []string, 10)
	emptied := make(chan struct{})
	var calls atomic.Int32
	startPrompter(t, client, 5*time.Millisecond, func(ctx context.Context, q *promptQueue) (promptResult, error) {
		prompted <- queued(q)
		if calls.Add(1) == 1 {
			// Decided elsewhere while the screen stays open; it shows the
			// next target and the user dismisses that one.
			q.Remove(q.Items()[0].Entry.Target())
			close(emptied)
			for q.Len() == 0 {
				time.Sleep(time.Millisecond)
			}
		}
		return dismiss(q), nil
	})
	connected()

	ep.log.Add(blocked("a.com"))
	receive(t, emptied, "emptied queue")
	ep.log.Add(blocked("b.com"))
	assert.Equal(t, []string{"a.com:443"}, receive(t, prompted, "prompt"))
	assertQuiet(t, prompted, "prompt for the dismissed b.com")
	ep.log.Add(blocked("b.com"))
	assert.Equal(t, []string{"b.com:443"}, receive(t, prompted, "prompt after the next block"))
}

func TestRunBlockPrompter_TargetsQueuedAfterTheDismissalPrompt(t *testing.T) {
	ep := newEventsProxy(t)
	client, connected := prompterClient(t, ep.api)
	prompted := make(chan []string, 10)
	dismissed := make(chan struct{})
	var calls atomic.Int32
	startPrompter(t, client, 5*time.Millisecond, func(ctx context.Context, q *promptQueue) (promptResult, error) {
		prompted <- queued(q)
		res := dismiss(q)
		if calls.Add(1) == 1 {
			// esc is pressed; a target comes in before the screen is gone.
			close(dismissed)
			for q.Len() < 2 {
				time.Sleep(time.Millisecond)
			}
		}
		return res, nil
	})
	connected()

	ep.log.Add(blocked("a.com"))
	receive(t, dismissed, "dismissal")
	ep.log.Add(blocked("b.com"))
	assert.Equal(t, []string{"a.com:443"}, receive(t, prompted, "prompt"))
	assert.Equal(t, []string{"b.com:443"}, receive(t, prompted, "prompt for the target queued after the dismissal"))
	assertQuiet(t, prompted, "prompt for the dismissed a.com")
}

func TestRunBlockPrompter_DecidedTargetsDontPromptAgain(t *testing.T) {
	ep := newEventsProxy(t)
	client, connected := prompterClient(t, ep.api)
	prompted := make(chan []string, 10)
	startPrompter(t, client, 5*time.Millisecond, func(ctx context.Context, q *promptQueue) (promptResult, error) {
		prompted <- queued(q)
		q.Remove(q.Items()[0].Entry.Target()) // allowed
		return promptResult{}, nil
	})
	connected()

	ep.log.Add(blocked("a.com"))
	assert.Equal(t, []string{"a.com:443"}, receive(t, prompted, "prompt"))
	ep.log.Add(blocked("a.com"))
	assertQuiet(t, prompted, "prompt for a decided target")
}

func TestRunBlockPrompter_TargetsAfterTheQueueEmptiedPrompt(t *testing.T) {
	ep := newEventsProxy(t)
	client, connected := prompterClient(t, ep.api)
	prompted := make(chan []string, 10)
	drained := make(chan struct{})
	var calls atomic.Int32
	startPrompter(t, client, 5*time.Millisecond, func(ctx context.Context, q *promptQueue) (promptResult, error) {
		prompted <- queued(q)
		if calls.Add(1) > 1 {
			q.Clear()
			return promptResult{}, nil
		}
		// Decide the only target, then return as a target comes in, as the
		// screen does when it quits on the empty queue.
		q.Remove(q.Items()[0].Entry.Target())
		close(drained)
		for q.Len() == 0 {
			time.Sleep(time.Millisecond)
		}
		return promptResult{}, nil
	})
	connected()

	ep.log.Add(blocked("a.com"))
	receive(t, drained, "drained queue")
	ep.log.Add(blocked("b.com"))
	assert.Equal(t, []string{"a.com:443"}, receive(t, prompted, "prompt"))
	assert.Equal(t, []string{"b.com:443"}, receive(t, prompted, "prompt for the target that came in as the prompt closed"))
}

func TestRunBlockPrompter_HoldEndUpdatesQueue(t *testing.T) {
	ep := newEventsProxy(t)
	client, connected := prompterClient(t, ep.api)
	shown := make(chan *promptQueue, 10)
	startPrompter(t, client, 5*time.Millisecond, func(ctx context.Context, q *promptQueue) (promptResult, error) {
		shown <- q
		<-ctx.Done()
		return promptResult{}, ctx.Err()
	})
	connected()

	ep.holdRequest(t, "a.com")
	q := receive(t, shown, "prompt")
	require.Len(t, q.Items(), 1)
	require.False(t, q.Items()[0].Deadline.IsZero(), "held")

	ep.holds.End(blocked("a.com").Target(), proxy.HoldTimeout)
	require.Eventually(t, func() bool {
		items := q.Items()
		return len(items) == 1 && items[0].Deadline.IsZero()
	}, time.Second, time.Millisecond, "a timed-out hold stays queued as a block")
}

func TestRunBlockPrompter_ProxyRestart(t *testing.T) {
	old := newEventsProxy(t)
	old.addEntries(5)
	rp := &restartProxy{api: old.api}
	client, connected := prompterClient(t, rp)
	shown := make(chan *promptQueue, 10)
	startPrompter(t, client, 5*time.Millisecond, func(ctx context.Context, q *promptQueue) (promptResult, error) {
		shown <- q
		<-ctx.Done()
		return promptResult{}, ctx.Err()
	})
	connected()

	old.holdRequest(t, "a.com")
	q := receive(t, shown, "prompt")
	require.Len(t, q.Items(), 1)
	require.False(t, q.Items()[0].Deadline.IsZero(), "held")

	fresh := newEventsProxy(t)
	rp.restart(fresh.api)
	connected()
	require.Eventually(t, func() bool {
		items := q.Items()
		return len(items) == 1 && items[0].Deadline.IsZero() && items[0].Entry.ID == 0
	}, time.Second, time.Millisecond, "the old proxy's hold is gone: a.com stays queued as a block")

	fresh.holdRequest(t, "b.com")
	require.Eventually(t, func() bool { return q.Len() == 2 }, time.Second, time.Millisecond, "a hold on the new proxy is queued")
	assert.Equal(t, []string{"a.com:443", "b.com:443"}, queued(q))
	assert.False(t, q.Items()[1].Deadline.IsZero(), "b.com is held")
}

func TestRunBlockPrompter_StopsForProxyWithoutStream(t *testing.T) {
	tp := newTestProxy(t)
	err := runBlockPrompter(t.Context(), tp.client, 5*time.Millisecond, func(context.Context, *promptQueue) (promptResult, error) {
		t.Error("no prompt without the event stream")
		return promptResult{}, nil
	})
	assert.ErrorIs(t, err, errNoEventStream)
}

func TestStartBlockPrompter(t *testing.T) {
	var lookups atomic.Int32
	counting := func() (*SessionInfo, error) {
		lookups.Add(1)
		return nil, errors.New("find control port: not published")
	}
	missingCreds := func() (*SessionInfo, error) {
		return &SessionInfo{ControlPort: "1", SessionID: "no-such-session-for-prompt-test"}, nil
	}
	tests := []struct {
		name        string
		args        []string
		global      string // global config content; empty for no file
		getSession  func() (*SessionInfo, error)
		wantErr     string
		wantWarn    string
		wantLookups int32
	}{
		{name: "off by default", args: []string{"x"}, getSession: counting},
		{name: "flag off", args: []string{"x", "--prompt=false"}, getSession: counting},
		{name: "flag on, session lookup fails", args: []string{"x", "--prompt"}, getSession: counting, wantErr: "--prompt: find control port: not published", wantLookups: 1},
		{name: "flag on, missing credentials", args: []string{"x", "--prompt"}, getSession: missingCreds, wantErr: "load TLS credentials"},
		{name: "config on, session lookup fails", args: []string{"x"}, global: "prompt: true\n", getSession: counting, wantWarn: "not published", wantLookups: 1},
		{name: "flag off overrides config", args: []string{"x", "--prompt=false"}, global: "prompt: true\n", getSession: counting},
		{name: "flag on still fails with config on", args: []string{"x", "--prompt"}, global: "prompt: true\n", getSession: counting, wantErr: "not published", wantLookups: 1},
		{name: "invalid config", args: []string{"x"}, global: "prompt: maybe\n", getSession: counting, wantWarn: "cannot read its setting"},
		{name: "flag off skips invalid config", args: []string{"x", "--prompt=false"}, global: "prompt: maybe\n", getSession: counting},
		{name: "flag on skips invalid config", args: []string{"x", "--prompt"}, global: "prompt: maybe\n", getSession: counting, wantErr: "not published", wantLookups: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookups.Store(0)

			globalPath := filepath.Join(t.TempDir(), "config.yaml")
			if tt.global != "" {
				require.NoError(t, os.WriteFile(globalPath, []byte(tt.global), 0o644))
			}
			var warnings []string
			warnf := func(format string, args ...any) {
				warnings = append(warnings, fmt.Sprintf(format, args...))
			}

			var bp *blockPrompter
			var gotErr error
			cmd := &cli.Command{
				Name:  "x",
				Flags: []cli.Flag{promptCLIFlag()},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					bp, gotErr = startBlockPrompterFrom(ctx, cmd, globalPath, warnf, tt.getSession)
					return nil
				},
			}
			require.NoError(t, cmd.Run(context.Background(), tt.args))
			require.NotNil(t, bp)
			assert.Zero(t, bp.hooks, "no prompter, no terminal hooks")
			assert.Empty(t, bp.notes.Lines(), "no prompter, nothing to report")
			bp.stop()
			assert.Equal(t, tt.wantLookups, lookups.Load())
			if tt.wantErr == "" {
				assert.NoError(t, gotErr)
			} else {
				assert.ErrorContains(t, gotErr, tt.wantErr)
			}
			if tt.wantWarn == "" {
				assert.Empty(t, warnings)
			} else if assert.Len(t, warnings, 1) {
				assert.Contains(t, warnings[0], tt.wantWarn)
				assert.Contains(t, warnings[0], globalPath, "the warning names the config file")
			}
		})
	}
}

func TestLoggedPrompt(t *testing.T) {
	evil := proxy.LogEntry{Source: proxy.SourceProxy, Domain: "evil\x1b[2J.com", Port: "443", Action: proxy.ActionBlock}
	queue := []proxy.LogEntry{evil, blocked("b.com")}
	tests := []struct {
		name    string
		queue   []proxy.LogEntry
		err     error
		wantLog string
	}{
		{name: "shown", queue: queue},
		{name: "cancelled", queue: queue, err: context.Canceled},
		{name: "session ended", queue: queue, err: fmt.Errorf("show: %w", overlay.ErrClosed)},
		{name: "unavailable", queue: queue, err: fmt.Errorf("%w: trap", overlay.ErrUnavailable), wantLog: "prompt for evil[2J.com:443: overlay: unavailable: trap"},
		{name: "barrier timeout", queue: queue, err: overlay.ErrBarrierTimeout, wantLog: "prompt for evil[2J.com:443: overlay: terminal did not answer the barrier query"},
		{name: "empty queue", err: errors.New("program failed"), wantLog: "prompt: program failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var notes promptNotes
			q := newPromptQueue()
			for _, e := range tt.queue {
				q.Add(e)
			}
			want := promptResult{Dismissed: []proxy.Target{evil.Target()}}
			prompt := loggedPrompt(notes.Printf, func(context.Context, *promptQueue) (promptResult, error) { return want, tt.err })
			res, err := prompt(context.Background(), q)
			assert.Equal(t, tt.err, err)
			assert.Equal(t, want, res)
			if tt.wantLog == "" {
				assert.Empty(t, notes.Lines())
			} else {
				assert.Equal(t, []string{tt.wantLog}, notes.Lines())
			}
		})
	}
}
