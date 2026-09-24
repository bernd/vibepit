package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/kitty"
	"github.com/bernd/vibepit/overlay"
	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	"github.com/urfave/cli/v3"
)

const promptFlag = "prompt"

var promptCLIFlag = &cli.StringFlag{
	Name:    promptFlag,
	Aliases: []string{"P"},
	Usage:   "Prompt when a connection is blocked: inline, kitty, or off (default: kitty overlay when available)",
}

type promptMode string

const (
	promptAuto   promptMode = "auto"
	promptOff    promptMode = "off"
	promptKitty  promptMode = "kitty"
	promptInline promptMode = "inline"
)

// parsePromptMode reads the --prompt flag. Unset means auto: the kitty
// overlay when kitty remote control is available, nothing otherwise.
func parsePromptMode(cmd *cli.Command) (promptMode, error) {
	if !cmd.IsSet(promptFlag) {
		return promptAuto, nil
	}
	switch v := cmd.String(promptFlag); v {
	case "auto":
		return promptAuto, nil
	case "", "off", "false", "none":
		return promptOff, nil
	case "kitty":
		return promptKitty, nil
	case "inline":
		return promptInline, nil
	default:
		return "", fmt.Errorf("--prompt: unknown mode %q (want inline, kitty, or off)", v)
	}
}

// promptFunc asks the user whether to allow a blocked entry. The decision
// is posted to the control API by the prompt itself.
type promptFunc func(ctx context.Context, entry proxy.LogEntry) error

// kittyPrompter runs `vibepit approve` in a kitty overlay window.
type kittyPrompter struct {
	env     kitty.Env
	exe     string
	session *SessionInfo
}

func (p *kittyPrompter) Show(ctx context.Context, entry proxy.LogEntry) error {
	return p.env.LaunchOverlay(ctx, approveCmdline(p.exe, p.session, entry))
}

// inlinePrompter draws the approve screen over the session in the terminal
// that runs it.
type inlinePrompter struct {
	term    *overlay.Terminal
	session *SessionInfo
	client  *ControlClient
}

func (p *inlinePrompter) Show(ctx context.Context, entry proxy.LogEntry) error {
	return p.term.Show(ctx, newApproveModel(p.session, p.client, entry))
}

// blockPrompter is a configured prompter. The inline variant only starts
// polling once the session terminal exists, so it hands out attach options.
type blockPrompter struct {
	opts []ctr.AttachOption
	stop func()
}

var noBlockPrompter = &blockPrompter{stop: func() {}}

// AttachOptions returns the options to pass to the session attach.
func (bp *blockPrompter) AttachOptions() []ctr.AttachOption { return bp.opts }

// Stop ends polling and closes an open prompt. Always safe to call.
func (bp *blockPrompter) Stop() { bp.stop() }

// blockWatcher tracks the log cursor and which blocked targets have already
// been surfaced, so each domain:port triggers at most one prompt per session
// even though the agent typically retries a blocked request many times.
type blockWatcher struct {
	cursor uint64
	seen   map[proxy.Target]bool
}

// Next returns the blocked entries in batch that have not been seen before
// and advances the cursor past the batch.
func (bw *blockWatcher) Next(batch []proxy.LogEntry) []proxy.LogEntry {
	if bw.seen == nil {
		bw.seen = make(map[proxy.Target]bool)
	}
	var fresh []proxy.LogEntry
	for _, e := range batch {
		bw.cursor = e.ID
		if e.Action != proxy.ActionBlock {
			continue
		}
		key := e.Target()
		if bw.seen[key] {
			continue
		}
		bw.seen[key] = true
		fresh = append(fresh, e)
	}
	return fresh
}

// runBlockPrompter polls the control API for new blocked requests and calls
// prompt for each unseen target, one at a time, until ctx is done. Poll
// errors are retried on the next tick; prompt errors are for the prompt
// function to report, see loggedPrompt.
func runBlockPrompter(ctx context.Context, client *ControlClient, interval time.Duration, prompt promptFunc) {
	var bw blockWatcher
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Skip whatever was blocked before we started watching: start the
	// cursor at the newest entry. Retry until that succeeds, otherwise the
	// first poll would replay old blocks.
	for {
		entries, err := client.Logs()
		if err == nil {
			if len(entries) > 0 {
				bw.cursor = entries[len(entries)-1].ID
			}
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		entries, err := client.LogsAfter(bw.cursor)
		if err != nil {
			continue
		}
		for _, e := range bw.Next(entries) {
			// Another client may have decided already. A failed check, e.g. an
			// older proxy without /check, falls back to prompting.
			if res, err := client.Check(e); err == nil && res.Decided() {
				continue
			}
			_ = prompt(ctx, e)
		}
	}
}

// startBlockPrompter sets up the prompter selected by --prompt for the
// session returned by getSession. The session is resolved lazily so that a
// failing lookup cannot break sessions that do not prompt. inlineOK tells
// whether the caller can host an inline prompt.
//
// An explicit mode makes every setup failure fatal, except that kitty falls
// back to inline when kitty remote control is unavailable and inlineOK is
// set. In auto mode prompting is a convenience that must never stop run or
// connect: outside kitty it stays quiet, and a failure inside kitty only
// prints a warning.
func startBlockPrompter(ctx context.Context, cmd *cli.Command, getSession func() (*SessionInfo, error), inlineOK bool) (*blockPrompter, error) {
	mode, err := parsePromptMode(cmd)
	if err != nil {
		return noBlockPrompter, err
	}
	switch mode {
	case promptOff:
		return noBlockPrompter, nil
	case promptInline:
		if !inlineOK {
			return noBlockPrompter, errors.New("--prompt inline: not supported by this command yet")
		}
		return startInline(ctx, getSession)
	}

	env, err := kitty.Detect(os.Getenv, exec.LookPath)
	if err != nil {
		switch {
		case mode == promptKitty && inlineOK:
			tui.Status("Falling back", "to inline prompt: %v", err)
			return startInline(ctx, getSession)
		case mode == promptKitty:
			return noBlockPrompter, fmt.Errorf("--prompt: %w", err)
		default:
			return noBlockPrompter, nil
		}
	}
	bp, err := setupKittyPrompter(ctx, env, getSession)
	switch {
	case err == nil:
		tui.Status("Watching", "blocked connections via kitty overlay")
		return bp, nil
	case mode == promptKitty:
		return noBlockPrompter, fmt.Errorf("--prompt: %w", err)
	default:
		tui.Status("Skipping", "kitty prompt for blocked connections: %v", err)
		return noBlockPrompter, nil
	}
}

func startInline(ctx context.Context, getSession func() (*SessionInfo, error)) (*blockPrompter, error) {
	bp, err := setupInlinePrompter(ctx, getSession)
	if err != nil {
		return noBlockPrompter, fmt.Errorf("--prompt: %w", err)
	}
	tui.Status("Watching", "blocked connections via inline prompt")
	return bp, nil
}

// promptDeps is what both prompters need: the session, its control client,
// and the prompt log.
type promptDeps struct {
	session *SessionInfo
	client  *ControlClient
	logger  *log.Logger
}

func openPromptDeps(getSession func() (*SessionInfo, error)) (*promptDeps, error) {
	session, err := getSession()
	if err != nil {
		return nil, err
	}
	cc, err := NewControlClient(session)
	if err != nil {
		return nil, err
	}
	return &promptDeps{session: session, client: cc, logger: openPromptLog(session)}, nil
}

// startLoop polls for blocked targets and runs prompt for each, logging
// failures. The returned function stops it.
func (d *promptDeps) startLoop(ctx context.Context, prompt promptFunc) func() {
	return startPrompterLoop(ctx, d.client, pollInterval, prompterStopGrace, loggedPrompt(d.logger, prompt))
}

func (d *promptDeps) close() {
	d.client.Close()
}

func setupKittyPrompter(ctx context.Context, env kitty.Env, getSession func() (*SessionInfo, error)) (*blockPrompter, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	d, err := openPromptDeps(getSession)
	if err != nil {
		return nil, err
	}
	stopLoop := d.startLoop(ctx, (&kittyPrompter{env: env, exe: exe, session: d.session}).Show)
	return &blockPrompter{stop: func() {
		stopLoop()
		d.close()
	}}, nil
}

// setupInlinePrompter resolves the session and control client up front, so
// failures surface before the terminal is taken over, and starts polling
// once attach provides the session terminal.
func setupInlinePrompter(ctx context.Context, getSession func() (*SessionInfo, error)) (*blockPrompter, error) {
	d, err := openPromptDeps(getSession)
	if err != nil {
		return nil, err
	}

	var (
		mu       sync.Mutex
		stopped  bool
		stopLoop func()
	)
	onTerminal := func(t *overlay.Terminal) {
		t.SetLogf(d.logger.Printf)
		mu.Lock()
		defer mu.Unlock()
		if stopped || stopLoop != nil {
			return
		}
		stopLoop = d.startLoop(ctx, (&inlinePrompter{term: t, session: d.session, client: d.client}).Show)
	}
	return &blockPrompter{
		opts: []ctr.AttachOption{ctr.WithTerminal(onTerminal)},
		stop: func() {
			mu.Lock()
			stopped = true
			s := stopLoop
			mu.Unlock()
			if s != nil {
				s()
			}
			d.close()
		},
	}, nil
}

// Prompt logs hold the prompt's diagnostics, one file per session. The
// session owns the terminal, so there is nowhere else to report a prompt
// that failed. They live next to the session directories rather than in
// one, because those are removed when the session stops, which is when a
// log is read. Clients attached to the same session share its file.
const (
	promptLogDirName = "prompt-logs"
	// maxPromptLog caps one session's log. Past it, lines are dropped.
	maxPromptLog = 1 << 20
	// promptLogMaxAge is how long logs are kept after their last line.
	promptLogMaxAge = 7 * 24 * time.Hour
)

// promptLogDir is where prompt logs go; tests point it elsewhere.
var promptLogDir = func() string {
	return filepath.Join(filepath.Dir(sessionBaseDir()), promptLogDirName)
}

// openPromptLog returns a logger appending to the session's prompt log,
// after removing logs not written to for promptLogMaxAge. Logging is best
// effort: without the file, diagnostics are dropped.
func openPromptLog(session *SessionInfo) *log.Logger {
	dir := promptLogDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return log.New(io.Discard, "", 0)
	}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > promptLogMaxAge {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	return log.New(&promptLogWriter{path: filepath.Join(dir, session.SessionID+".log")}, "", log.LstdFlags)
}

// promptLogWriter opens the log for every line. Lines are rare, and so the
// size cap holds across clients sharing the file, and a log removed as old
// while its session was quiet is created again.
type promptLogWriter struct {
	mu   sync.Mutex
	path string
}

func (w *promptLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return len(p), nil
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || fi.Size()+int64(len(p)) > maxPromptLog {
		return len(p), nil
	}
	_, _ = f.Write(p)
	return len(p), nil
}

// loggedPrompt logs prompt failures. Cancellation is how prompts end when
// the session does, so it is not a failure.
func loggedPrompt(logger *log.Logger, prompt promptFunc) promptFunc {
	return func(ctx context.Context, e proxy.LogEntry) error {
		err := prompt(ctx, e)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, overlay.ErrClosed) {
			logger.Printf("prompt for %s: %v", tui.SanitizeText(e.Target().String()), err)
		}
		return err
	}
}

// prompterStopGrace bounds how long shutdown waits for an open overlay to be
// closed. It covers kitty's close-window round trip, which LaunchOverlay
// itself caps at two seconds.
const prompterStopGrace = 3 * time.Second

// startPrompterLoop runs runBlockPrompter in the background. The returned
// stop function cancels it and waits, up to grace, for it to return, so an
// open overlay is closed before the CLI exits.
func startPrompterLoop(ctx context.Context, cc *ControlClient, interval, grace time.Duration, prompt promptFunc) func() {
	pctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runBlockPrompter(pctx, cc, interval, prompt)
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(grace):
		}
	}
}

// sessionInfoForRunning builds the SessionInfo for an already running
// session by looking up the proxy container's published control port.
func sessionInfoForRunning(ctx context.Context, client *ctr.Client, sessionID, projectDir string) (*SessionInfo, error) {
	proxyID, err := client.FindProxyContainerID(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("find proxy container: %w", err)
	}
	port, err := client.FindControlPort(ctx, proxyID)
	if err != nil {
		return nil, fmt.Errorf("find control port: %w", err)
	}
	return newSessionInfo(strconv.Itoa(port), sessionID, projectDir), nil
}
