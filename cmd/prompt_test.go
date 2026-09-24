package cmd

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bernd/vibepit/overlay"
	"github.com/bernd/vibepit/proxy"
	"github.com/charmbracelet/colorprofile"
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
	// Valid credentials: the control client is set up, nothing dials yet.
	creds, err := proxy.GenerateMTLSCredentials(time.Hour)
	require.NoError(t, err)
	credDir := t.TempDir()
	for name, data := range map[string][]byte{"ca.pem": creds.CACertPEM(), "client-cert.pem": creds.ClientCertPEM(), "client-key.pem": creds.ClientKeyPEM()} {
		require.NoError(t, os.WriteFile(filepath.Join(credDir, name), data, 0o600))
	}
	goodSession := func() (*SessionInfo, error) {
		return &SessionInfo{ControlPort: "1", SessionID: "s", CredDir: credDir}, nil
	}
	origLogDir := promptLogDir
	t.Cleanup(func() { promptLogDir = origLogDir })
	logDir := t.TempDir()
	promptLogDir = func() string { return logDir }

	tests := []struct {
		name       string
		args       []string
		notKitty   bool
		noInline   bool
		getSession func() (*SessionInfo, error)
		wantErr    string
		wantOpts   bool
	}{
		{name: "explicit off never sets up", args: []string{"x", "--prompt=off"}, getSession: failingSession},
		{name: "false is off", args: []string{"x", "--prompt=false"}, getSession: failingSession},
		{name: "empty is off", args: []string{"x", "--prompt="}, getSession: failingSession},
		{name: "unknown mode", args: []string{"x", "-P", "popup"}, getSession: failingSession, wantErr: "unknown mode"},
		{name: "auto, not kitty, stays quiet", args: []string{"x"}, notKitty: true, getSession: failingSession},
		{name: "explicit auto, not kitty, stays quiet", args: []string{"x", "--prompt=auto"}, notKitty: true, getSession: failingSession},
		{name: "kitty, not kitty, no inline, fails loudly", args: []string{"x", "--prompt=kitty"}, notKitty: true, noInline: true, getSession: failingSession, wantErr: "remote control"},
		{name: "kitty, not kitty, falls back to inline", args: []string{"x", "--prompt=kitty"}, notKitty: true, getSession: failingSession, wantErr: "not published"},
		{name: "auto, session lookup fails, skips silently", args: []string{"x"}, getSession: failingSession},
		{name: "kitty, session lookup fails loudly", args: []string{"x", "--prompt=kitty"}, getSession: failingSession, wantErr: "not published"},
		{name: "auto, missing credentials, skips silently", args: []string{"x"}, getSession: missingCreds},
		{name: "kitty, missing credentials fails loudly", args: []string{"x", "--prompt=kitty"}, getSession: missingCreds, wantErr: "load TLS credentials"},
		{name: "inline, not supported by caller", args: []string{"x", "-P", "inline"}, noInline: true, getSession: failingSession, wantErr: "not supported"},
		{name: "inline, session lookup fails loudly", args: []string{"x", "-P", "inline"}, getSession: failingSession, wantErr: "not published"},
		{name: "inline, missing credentials fails loudly", args: []string{"x", "--prompt", "inline"}, getSession: missingCreds, wantErr: "load TLS credentials"},
		{name: "inline, set up, takes the session terminal", args: []string{"x", "-P", "inline"}, getSession: goodSession, wantOpts: true},
		{name: "kitty, not kitty, falls back to a working inline prompt", args: []string{"x", "--prompt=kitty"}, notKitty: true, getSession: goodSession, wantOpts: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.notKitty {
				t.Setenv("KITTY_LISTEN_ON", "")
			}
			var gotErr error
			var bp *blockPrompter
			cmd := &cli.Command{
				Name:  "x",
				Flags: []cli.Flag{promptCLIFlag},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					bp, gotErr = startBlockPrompter(ctx, cmd, tt.getSession, !tt.noInline)
					return nil
				},
			}
			require.NoError(t, cmd.Run(context.Background(), tt.args))
			require.NotNil(t, bp)
			if tt.wantOpts {
				assert.Len(t, bp.AttachOptions(), 1, "the inline prompt hooks into the attach")
			} else {
				assert.Empty(t, bp.AttachOptions())
			}
			bp.Stop()
			if tt.wantErr != "" {
				assert.ErrorContains(t, gotErr, tt.wantErr)
			} else {
				assert.NoError(t, gotErr)
			}
		})
	}
}

// TestInlinePrompter drives the approve screen through an overlay terminal
// over a fake session: the prompt takes over the screen and the key, the
// allow reaches the control API, and the session gets its terminal back.
func TestInlinePrompter(t *testing.T) {
	tp := newTestProxy(t)
	stdin, stdinW := io.Pipe()
	out, outW := io.Pipe()
	defer stdinW.Close()
	defer outW.Close()
	var screen, session syncWriter
	term := overlay.New(overlay.Config{
		Stdin:        stdin,
		Stdout:       &screen,
		ContainerIn:  &session,
		ContainerOut: out,
		Size:         func() (int, int, error) { return 30, 100, nil },
		ProgramOptions: []tea.ProgramOption{
			tea.WithEnvironment([]string{"TERM=xterm-256color"}),
			tea.WithColorProfile(colorprofile.NoTTY),
		},
	})
	go func() { _ = term.Run(context.Background()) }()

	p := &inlinePrompter{
		term:    term,
		session: &SessionInfo{SessionID: "sess-1", ProjectDir: "/src/project"},
		client:  tp.client,
	}
	entry := proxy.LogEntry{Action: proxy.ActionBlock, Source: proxy.SourceProxy, Domain: "evil.example", Port: "443", Reason: "domain not in allowlist", Time: time.Now()}
	done := make(chan error, 1)
	go func() { done <- p.Show(context.Background(), entry) }()

	require.Eventually(t, func() bool { return strings.Contains(screen.String(), "evil.example:443") }, 3*time.Second, 5*time.Millisecond, "prompt shows the target")
	assert.Contains(t, screen.String(), "domain not in allowlist")
	assert.Contains(t, screen.String(), "sess-1", "prompt uses the vibepit header")

	// A key typed as the prompt appears is taken as typing meant for the
	// agent, not as an answer, and dropped.
	_, _ = stdinW.Write([]byte("a"))
	time.Sleep(approveQuiet + 100*time.Millisecond)
	select {
	case <-done:
		t.Fatal("a key typed right away must not answer the prompt")
	default:
	}
	assert.False(t, tp.http.Allows("evil.example", "443"))

	_, _ = stdinW.Write([]byte("a"))
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("prompt did not close after allow")
	}
	assert.True(t, tp.http.Allows("evil.example", "443"), "allow reached the control API")
	assert.Empty(t, session.String(), "no key reaches the session while the prompt shows")

	_, _ = stdinW.Write([]byte("ls\r"))
	require.Eventually(t, func() bool { return session.String() == "ls\r" }, time.Second, 5*time.Millisecond)
}

type syncWriter struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestLoggedPrompt(t *testing.T) {
	var buf strings.Builder
	logger := log.New(&buf, "", 0)
	entry := proxy.LogEntry{Source: proxy.SourceProxy, Domain: "evil\x1b[2J.example", Port: "443"}

	fail := errors.New("output already paused")
	err := loggedPrompt(logger, func(context.Context, proxy.LogEntry) error { return fail })(context.Background(), entry)
	assert.ErrorIs(t, err, fail, "errors are passed on")
	assert.Contains(t, buf.String(), "output already paused")
	assert.NotContains(t, buf.String(), "\x1b", "target is sanitized")

	buf.Reset()
	for _, quiet := range []error{nil, context.Canceled, overlay.ErrClosed} {
		_ = loggedPrompt(logger, func(context.Context, proxy.LogEntry) error { return quiet })(context.Background(), entry)
	}
	assert.Empty(t, buf.String(), "success and shutdown are not failures")
}

func TestOpenPromptLog(t *testing.T) {
	dir := t.TempDir()
	orig := promptLogDir
	t.Cleanup(func() { promptLogDir = orig })
	promptLogDir = func() string { return filepath.Join(dir, "state", promptLogDirName) }
	logDir := promptLogDir()
	sess1 := filepath.Join(logDir, "sess-1.log")

	// A log not written to for long is removed, a recent one kept.
	require.NoError(t, os.MkdirAll(logDir, 0o700))
	oldLog := filepath.Join(logDir, "old.log")
	recentLog := filepath.Join(logDir, "recent.log")
	require.NoError(t, os.WriteFile(oldLog, []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(recentLog, []byte("x"), 0o600))
	past := time.Now().Add(-promptLogMaxAge - time.Hour)
	require.NoError(t, os.Chtimes(oldLog, past, past))

	logger := openPromptLog(&SessionInfo{SessionID: "sess-1"})
	logger.Printf("hello")
	data, err := os.ReadFile(sess1)
	require.NoError(t, err)
	assert.Contains(t, string(data), "hello")
	assert.NoFileExists(t, oldLog)
	assert.FileExists(t, recentLog)

	// A log removed as old while its session was quiet comes back.
	require.NoError(t, os.Remove(sess1))
	logger.Printf("still here")
	data, err = os.ReadFile(sess1)
	require.NoError(t, err)
	assert.Contains(t, string(data), "still here")

	// The cap holds for the file, across clients of one session.
	full := filepath.Join(logDir, "sess-2.log")
	require.NoError(t, os.WriteFile(full, make([]byte, maxPromptLog-40), 0o600))
	first := openPromptLog(&SessionInfo{SessionID: "sess-2"})
	second := openPromptLog(&SessionInfo{SessionID: "sess-2"})
	first.Printf("fits")
	second.Printf("this line does not fit")
	fi, err := os.Stat(full)
	require.NoError(t, err)
	assert.LessOrEqual(t, fi.Size(), int64(maxPromptLog))
	data, err = os.ReadFile(full)
	require.NoError(t, err)
	assert.Contains(t, string(data), "fits")
	assert.NotContains(t, string(data), "does not fit")

	promptLogDir = func() string { return filepath.Join(dir, "state", promptLogDirName, "sess-1.log", "x") }
	openPromptLog(&SessionInfo{SessionID: "sess-1"}).Printf("dropped") // must not panic
}
