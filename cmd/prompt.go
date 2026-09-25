package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/overlay"
	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	"github.com/urfave/cli/v3"
)

const promptFlag = "prompt"

var promptCLIFlag = &cli.BoolFlag{
	Name:  promptFlag,
	Usage: "Show an allow/deny prompt over the session when the proxy blocks a connection",
}

// promptFunc asks the user about a blocked entry. The prompt posts the
// decision to the control API itself.
type promptFunc func(ctx context.Context, entry proxy.LogEntry) error

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

// Forget lets target prompt again on its next block.
func (bw *blockWatcher) Forget(target proxy.Target) {
	delete(bw.seen, target)
}

// runBlockPrompter polls the control API for new blocked requests and calls
// prompt for each unseen target, one at a time, until ctx is done. Poll
// errors are retried on the next tick; prompt failures are for the prompt
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
			if err := prompt(ctx, e); errors.Is(err, overlay.ErrBarrierTimeout) {
				// Nothing was shown: ask again on the next block.
				bw.Forget(e.Target())
			}
		}
	}
}

// prompterStopGrace bounds how long shutdown waits for an open prompt to
// restore the screen.
const prompterStopGrace = 3 * time.Second

// startPrompterLoop runs runBlockPrompter in the background. The returned
// stop function cancels it and waits, up to grace, for it to return, so an
// open prompt is closed before the CLI exits.
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

// prompter shows the approve screen over the session.
type prompter struct {
	term    *overlay.Terminal
	session *SessionInfo
	client  *ControlClient
}

func (p *prompter) Show(ctx context.Context, entry proxy.LogEntry) error {
	header := &tui.HeaderInfo{ProjectDir: p.session.ProjectDir, SessionID: p.session.SessionID}
	return p.term.Show(ctx, tui.NewWindow(header, newApproveScreen(p.session, p.client, entry)))
}

// blockPrompter is the prompting set up for one attach: the attach options
// that hand it the session's terminal, and a stop function.
type blockPrompter struct {
	opts []ctr.AttachOption
	stop func()
}

var noBlockPrompter = &blockPrompter{stop: func() {}}

// AttachOptions returns the options to pass to the session attach.
func (bp *blockPrompter) AttachOptions() []ctr.AttachOption { return bp.opts }

// Stop ends polling and closes an open prompt. Always safe to call.
func (bp *blockPrompter) Stop() { bp.stop() }

// startBlockPrompter sets up prompting when --prompt is set. The session
// and the control client are resolved before the attach takes over the
// terminal, so a failure prints normally and stops the command. Polling
// starts once the attach hands over the session's terminal.
func startBlockPrompter(ctx context.Context, cmd *cli.Command, getSession func() (*SessionInfo, error)) (*blockPrompter, error) {
	if !cmd.Bool(promptFlag) {
		return noBlockPrompter, nil
	}
	session, err := getSession()
	if err != nil {
		return noBlockPrompter, fmt.Errorf("--prompt: %w", err)
	}
	cc, err := NewControlClient(session)
	if err != nil {
		return noBlockPrompter, fmt.Errorf("--prompt: %w", err)
	}
	logger, logPath := openPromptLog(session.SessionID)

	var (
		mu       sync.Mutex
		stopped  bool
		stopLoop func()
	)
	onTerminal := func(t *overlay.Terminal) {
		mu.Lock()
		defer mu.Unlock()
		if stopped || stopLoop != nil {
			return
		}
		p := &prompter{term: t, session: session, client: cc}
		stopLoop = startPrompterLoop(ctx, cc, pollInterval, prompterStopGrace, loggedPrompt(logger, p.Show))
	}
	tui.Status("Prompting", "for blocked connections (log: %s)", logPath)
	return &blockPrompter{
		opts: []ctr.AttachOption{ctr.WithTerminal(onTerminal), ctr.WithLogf(logger.Printf)},
		stop: func() {
			mu.Lock()
			stopped = true
			s := stopLoop
			mu.Unlock()
			if s != nil {
				s()
			}
			cc.Close()
		},
	}, nil
}

// loggedPrompt logs why a prompt failed or wasn't shown: the session owns
// the terminal, so the prompt log is the only place for it. A prompt ended
// by the session ending isn't a failure.
func loggedPrompt(logger *log.Logger, prompt promptFunc) promptFunc {
	return func(ctx context.Context, e proxy.LogEntry) error {
		err := prompt(ctx, e)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, overlay.ErrClosed) {
			logger.Printf("prompt for %s: %v", tui.SanitizeText(e.Target().String()), err)
		}
		return err
	}
}

// sessionInfoForRunning builds the SessionInfo for an already running
// session from the proxy container's published control port.
func sessionInfoForRunning(ctx context.Context, client *ctr.Client, sessionID, projectDir string) (*SessionInfo, error) {
	proxyID, err := client.FindProxyContainerID(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("find proxy container: %w", err)
	}
	port, err := client.FindControlPort(ctx, proxyID)
	if err != nil {
		return nil, fmt.Errorf("find control port: %w", err)
	}
	return &SessionInfo{ControlPort: strconv.Itoa(port), SessionID: sessionID, ProjectDir: projectDir}, nil
}
