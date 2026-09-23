package cmd

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bernd/vibepit/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

func TestBlockWatcher_Next(t *testing.T) {
	block := func(id uint64, src proxy.Source, domain, port string) proxy.LogEntry {
		return proxy.LogEntry{ID: id, Source: src, Domain: domain, Port: port, Action: proxy.ActionBlock}
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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

func TestStartBlockPrompter_Policy(t *testing.T) {
	// Make kitty detection succeed: env vars plus a fake kitten in PATH.
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "kitten"), []byte("#!/bin/sh\n"), 0o755))
	t.Setenv("PATH", bin)
	t.Setenv("KITTY_LISTEN_ON", "unix:@test")
	t.Setenv("KITTY_WINDOW_ID", "1")

	failingSession := func() (*SessionInfo, error) { return nil, errors.New("find control port: not published") }
	// Credential directory exists but holds no TLS material, so
	// NewControlClient fails after the session lookup succeeded.
	missingCreds := func() (*SessionInfo, error) {
		return &SessionInfo{ControlPort: "1", SessionID: "s", CredDir: t.TempDir()}, nil
	}

	tests := []struct {
		name       string
		args       []string
		notKitty   bool
		getSession func() (*SessionInfo, error)
		wantErr    string
	}{
		{name: "explicit off never sets up", args: []string{"x", "--prompt=false"}, getSession: failingSession},
		{name: "auto, not kitty, stays quiet", args: []string{"x"}, notKitty: true, getSession: failingSession},
		{name: "explicit, not kitty, fails loudly", args: []string{"x", "--prompt"}, notKitty: true, getSession: failingSession, wantErr: "remote control"},
		{name: "auto, session lookup fails, skips silently", args: []string{"x"}, getSession: failingSession},
		{name: "explicit, session lookup fails loudly", args: []string{"x", "--prompt"}, getSession: failingSession, wantErr: "not published"},
		{name: "auto, missing credentials, skips silently", args: []string{"x"}, getSession: missingCreds},
		{name: "explicit, missing credentials fails loudly", args: []string{"x", "--prompt"}, getSession: missingCreds, wantErr: "load TLS credentials"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.notKitty {
				t.Setenv("KITTY_LISTEN_ON", "")
			}
			var gotErr error
			var stop func()
			cmd := &cli.Command{
				Name:  "x",
				Flags: []cli.Flag{&cli.BoolFlag{Name: promptFlag}},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					stop, gotErr = startBlockPrompter(ctx, cmd, tt.getSession)
					return nil
				},
			}
			require.NoError(t, cmd.Run(context.Background(), tt.args))
			require.NotNil(t, stop)
			stop()
			if tt.wantErr != "" {
				assert.ErrorContains(t, gotErr, tt.wantErr)
			} else {
				assert.NoError(t, gotErr)
			}
		})
	}
}
