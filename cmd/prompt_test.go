package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	withCause := func(e proxy.LogEntry, c proxy.Cause) proxy.LogEntry {
		e.Cause = c
		return e
	}
	allow := func(id uint64, domain string) proxy.LogEntry {
		return proxy.LogEntry{ID: id, Source: proxy.SourceProxy, Domain: domain, Port: "443", Action: proxy.ActionAllow}
	}

	tests := []struct {
		name    string
		batches [][]proxy.LogEntry
		want    [][]string // allow values per batch
		cursor  uint64
	}{
		{
			name:    "ignores allowed entries",
			batches: [][]proxy.LogEntry{{allow(1, "a.com"), block(2, proxy.SourceProxy, "b.com", "443")}},
			want:    [][]string{{"b.com:443"}},
			cursor:  2,
		},
		{
			name: "dedupes same target across batches",
			batches: [][]proxy.LogEntry{
				{block(1, proxy.SourceProxy, "b.com", "443"), block(2, proxy.SourceProxy, "b.com", "443")},
				{block(3, proxy.SourceProxy, "b.com", "443"), block(4, proxy.SourceProxy, "b.com", "80")},
			},
			want:   [][]string{{"b.com:443"}, {"b.com:80"}},
			cursor: 4,
		},
		{
			name: "dns and proxy for same domain are distinct",
			batches: [][]proxy.LogEntry{
				{block(1, proxy.SourceDNS, "b.com", ""), block(2, proxy.SourceProxy, "b.com", "443")},
			},
			want:   [][]string{{"b.com", "b.com:443"}},
			cursor: 2,
		},
		{
			name: "skips blocks allowing can't undo",
			batches: [][]proxy.LogEntry{{
				withCause(block(1, proxy.SourceProxy, "a.com", "443"), proxy.CauseBlockedIP),
				withCause(block(2, proxy.SourceProxy, "b.com", "443"), proxy.CauseResolveFailed),
				withCause(block(3, proxy.SourceProxy, "c.com", "443"), proxy.CauseAllowlist),
				withCause(block(4, proxy.SourceDNS, "d.com", ""), proxy.CauseBlockedIP),
			}},
			want:   [][]string{{"c.com:443"}},
			cursor: 4,
		},
		{
			// A proxy from before causes were logged sends none.
			name:    "keeps blocks without a cause",
			batches: [][]proxy.LogEntry{{block(1, proxy.SourceProxy, "a.com", "443")}},
			want:    [][]string{{"a.com:443"}},
			cursor:  1,
		},
		{
			name:    "empty batch keeps cursor",
			batches: [][]proxy.LogEntry{{block(5, proxy.SourceProxy, "b.com", "443")}, {}},
			want:    [][]string{{"b.com:443"}, nil},
			cursor:  5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var bw blockWatcher
			for i, batch := range tt.batches {
				got := bw.Next(batch)
				var vals []string
				for _, e := range got {
					vals = append(vals, e.Target().String())
				}
				assert.Equal(t, tt.want[i], vals, "batch %d", i)
			}
			assert.Equal(t, tt.cursor, bw.cursor)
		})
	}
}

func TestBlockWatcher_Forget(t *testing.T) {
	var bw blockWatcher
	e := proxy.LogEntry{ID: 1, Source: proxy.SourceProxy, Domain: "b.com", Port: "443", Action: proxy.ActionBlock}
	require.Len(t, bw.Next([]proxy.LogEntry{e}), 1)
	e.ID = 2
	assert.Empty(t, bw.Next([]proxy.LogEntry{e}))
	bw.Forget(e.Target())
	e.ID = 3
	assert.Len(t, bw.Next([]proxy.LogEntry{e}), 1, "a forgotten target prompts again")
}

func TestRunBlockPrompter(t *testing.T) {
	tp := newTestProxy(t)
	log, client := tp.log, tp.client
	// Blocked before the poller starts: must not prompt.
	log.Add(proxy.LogEntry{Domain: "old.com", Port: "443", Action: proxy.ActionBlock, Source: proxy.SourceProxy})

	prompted := make(chan proxy.LogEntry, 10)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runBlockPrompter(ctx, client, 5*time.Millisecond, func(ctx context.Context, e proxy.LogEntry) error {
			prompted <- e
			return nil
		})
	}()

	// Give the poller a moment to prime its cursor past old.com.
	time.Sleep(20 * time.Millisecond)
	log.Add(proxy.LogEntry{Domain: "new.com", Port: "443", Action: proxy.ActionBlock, Source: proxy.SourceProxy})
	log.Add(proxy.LogEntry{Domain: "new.com", Port: "443", Action: proxy.ActionBlock, Source: proxy.SourceProxy})
	log.Add(proxy.LogEntry{Domain: "fine.com", Port: "443", Action: proxy.ActionAllow, Source: proxy.SourceProxy})

	select {
	case e := <-prompted:
		assert.Equal(t, "new.com", e.Domain)
	case <-time.After(time.Second):
		t.Fatal("no prompt for new.com")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("poller did not stop on cancel")
	}
	select {
	case e := <-prompted:
		t.Fatalf("unexpected extra prompt for %s", e.Domain)
	default:
	}
}

func TestRunBlockPrompter_RetriesPriming(t *testing.T) {
	tp := newTestProxy(t)
	log, api := tp.log, tp.api
	log.Add(proxy.LogEntry{Domain: "old.com", Port: "443", Action: proxy.ActionBlock, Source: proxy.SourceProxy})

	// Fail the first requests so the priming call does not succeed at once.
	var calls atomic.Int32
	client := testControlClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 3 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		api.ServeHTTP(w, r)
	}))

	prompted := make(chan proxy.LogEntry, 10)
	ctx := t.Context()
	go runBlockPrompter(ctx, client, 5*time.Millisecond, func(ctx context.Context, e proxy.LogEntry) error {
		prompted <- e
		return nil
	})

	require.Eventually(t, func() bool { return calls.Load() > 5 }, time.Second, 5*time.Millisecond)
	log.Add(proxy.LogEntry{Domain: "new.com", Port: "443", Action: proxy.ActionBlock, Source: proxy.SourceProxy})

	select {
	case e := <-prompted:
		assert.Equal(t, "new.com", e.Domain, "history from before the poller started must not be replayed")
	case <-time.After(time.Second):
		t.Fatal("no prompt for new.com")
	}
}

func TestRunBlockPrompter_ReasksAfterBarrierTimeout(t *testing.T) {
	tp := newTestProxy(t)
	prompted := make(chan proxy.LogEntry, 10)
	var calls atomic.Int32
	ctx := t.Context()
	go runBlockPrompter(ctx, tp.client, 5*time.Millisecond, func(ctx context.Context, e proxy.LogEntry) error {
		prompted <- e
		if calls.Add(1) == 1 {
			return fmt.Errorf("show: %w", overlay.ErrBarrierTimeout)
		}
		return nil
	})
	time.Sleep(20 * time.Millisecond)
	block := proxy.LogEntry{Domain: "new.com", Port: "443", Action: proxy.ActionBlock, Source: proxy.SourceProxy}
	for i := range 2 {
		tp.log.Add(block)
		select {
		case <-prompted:
		case <-time.After(time.Second):
			t.Fatalf("no prompt %d", i+1)
		}
	}
	tp.log.Add(block)
	select {
	case <-prompted:
		t.Fatal("a shown prompt is not repeated")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestRunBlockPrompter_BurstPastTail(t *testing.T) {
	tp := newTestProxy(t)
	prompted := make(chan proxy.LogEntry, 100)
	ctx := t.Context()
	go runBlockPrompter(ctx, tp.client, 20*time.Millisecond, func(ctx context.Context, e proxy.LogEntry) error {
		prompted <- e
		return nil
	})
	time.Sleep(50 * time.Millisecond) // primed on an empty log
	n := proxy.TailSize + 5
	for i := range n {
		tp.log.Add(proxy.LogEntry{Domain: fmt.Sprintf("d%d.com", i), Port: "443", Action: proxy.ActionBlock, Source: proxy.SourceProxy})
	}
	seen := map[string]bool{}
	for len(seen) < n {
		select {
		case e := <-prompted:
			seen[e.Domain] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d of %d targets prompted", len(seen), n)
		}
	}
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
		getSession  func() (*SessionInfo, error)
		wantErr     string
		wantLookups int32
	}{
		{name: "off by default", args: []string{"x"}, getSession: counting},
		{name: "explicitly off", args: []string{"x", "--prompt=false"}, getSession: counting},
		{name: "session lookup fails", args: []string{"x", "--prompt"}, getSession: counting, wantErr: "not published", wantLookups: 1},
		{name: "missing credentials", args: []string{"x", "--prompt"}, getSession: missingCreds, wantErr: "load TLS credentials"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookups.Store(0)
			var bp *blockPrompter
			var gotErr error
			cmd := &cli.Command{
				Name:  "x",
				Flags: []cli.Flag{promptCLIFlag},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					bp, gotErr = startBlockPrompter(ctx, cmd, tt.getSession)
					return nil
				},
			}
			require.NoError(t, cmd.Run(context.Background(), tt.args))
			require.NotNil(t, bp)
			assert.Empty(t, bp.attachOptions(), "no prompter, no attach options")
			assert.Nil(t, bp.onTerminal, "no prompter, no terminal hook")
			assert.Nil(t, bp.logf)
			assert.Empty(t, bp.notes.Lines(), "no prompter, nothing to report")
			bp.stop()
			assert.Equal(t, tt.wantLookups, lookups.Load())
			if tt.wantErr == "" {
				assert.NoError(t, gotErr)
			} else {
				assert.ErrorContains(t, gotErr, tt.wantErr)
			}
		})
	}
}

func TestLoggedPrompt(t *testing.T) {
	entry := proxy.LogEntry{Source: proxy.SourceProxy, Domain: "evil\x1b[2J.com", Port: "443", Action: proxy.ActionBlock}
	tests := []struct {
		name    string
		err     error
		wantLog string
	}{
		{name: "shown"},
		{name: "cancelled", err: context.Canceled},
		{name: "session ended", err: fmt.Errorf("show: %w", overlay.ErrClosed)},
		{name: "unavailable", err: fmt.Errorf("%w: trap", overlay.ErrUnavailable), wantLog: "prompt for evil[2J.com:443: overlay: unavailable: trap"},
		{name: "barrier timeout", err: overlay.ErrBarrierTimeout, wantLog: "prompt for evil[2J.com:443: overlay: terminal did not answer the barrier query"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var notes promptNotes
			prompt := loggedPrompt(notes.Printf, func(context.Context, proxy.LogEntry) error { return tt.err })
			assert.Equal(t, tt.err, prompt(context.Background(), entry))
			if tt.wantLog == "" {
				assert.Empty(t, notes.Lines())
			} else {
				assert.Equal(t, []string{tt.wantLog}, notes.Lines())
			}
		})
	}
}
