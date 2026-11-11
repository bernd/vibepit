package cmd

import (
	"context"
	"errors"
	"fmt"
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
	cursor  uint64
	targets map[proxy.Target]targetState
}

type targetState struct {
	asked bool // prompted already; not asked about again
	tries int  // failed prompts
}

const (
	// maxPromptTries bounds how often a target is asked about when its
	// prompt fails, so a prompt that always fails doesn't come back at
	// every block. Barrier timeouts don't count, see runBlockPrompter.
	maxPromptTries = 5
	// maxPromptTargets bounds the targets a session tracks. The sandbox
	// picks their names, so past it prompting stops rather than holding
	// more: the targets stay blocked, as without --prompt.
	maxPromptTargets = 1000
)

var errTooManyTargets = fmt.Errorf("more than %d blocked targets, no more prompts in this session", maxPromptTargets)

// Next returns the entries in batch that allowing would unblock and that
// have not been seen before, and advances the cursor past the batch. It
// fails with errTooManyTargets at a target past maxPromptTargets.
func (bw *blockWatcher) Next(batch []proxy.LogEntry) ([]proxy.LogEntry, error) {
	if bw.targets == nil {
		bw.targets = make(map[proxy.Target]targetState)
	}
	var fresh []proxy.LogEntry
	for _, e := range batch {
		bw.cursor = e.ID
		if !e.Allowable() {
			continue
		}
		key := e.Target()
		st, known := bw.targets[key]
		if !known && len(bw.targets) >= maxPromptTargets {
			return fresh, errTooManyTargets
		}
		if st.asked {
			continue
		}
		st.asked = true
		bw.targets[key] = st
		fresh = append(fresh, e)
	}
	return fresh, nil
}

// Forget lets target prompt again on its next block. It still counts
// toward maxPromptTargets.
func (bw *blockWatcher) Forget(target proxy.Target) {
	if st, ok := bw.targets[target]; ok {
		st.asked = false
		bw.targets[target] = st
	}
}

// Retry lets target prompt again on its next block after a failed prompt,
// until it failed maxPromptTries times.
func (bw *blockWatcher) Retry(target proxy.Target) {
	st, ok := bw.targets[target]
	if !ok {
		return
	}
	st.tries++
	st.asked = st.tries >= maxPromptTries
	bw.targets[target] = st
}

// runBlockPrompter polls the control API for new blocked requests and calls
// prompt for each unseen target, one at a time, until ctx is done. Poll
// errors are retried on the next tick; prompt failures are for the prompt
// function to report, see loggedPrompt. It returns early, with the reason,
// once no more prompts can be shown: they are unavailable, or too many
// targets were blocked.
func runBlockPrompter(ctx context.Context, client *ControlClient, interval time.Duration, prompt promptFunc) error {
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
			return nil
		case <-ticker.C:
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		entries, err := client.LogsAfter(bw.cursor)
		if err != nil {
			continue
		}
		fresh, limitErr := bw.Next(entries)
		for _, e := range fresh {
			// Another client may have decided already. A failed check, e.g. an
			// older proxy without /check, falls back to prompting.
			if res, err := client.Check(e); err == nil && res.Decided() {
				continue
			}
			// A failed prompt may not have been seen: ask again on the next
			// block. Without a shadow no prompt can be shown, now or later.
			switch err := prompt(ctx, e); {
			case err == nil:
			case errors.Is(err, overlay.ErrUnavailable):
				return err
			case errors.Is(err, overlay.ErrBarrierTimeout), errors.Is(err, overlay.ErrSessionBusy):
				// Nothing was shown, and it isn't the prompt's fault: after two
				// timeouts in a row the terminal takes input after a pause in
				// typing, which can't time out, and a busy session reads its
				// input again.
				bw.Forget(e.Target())
			default:
				bw.Retry(e.Target())
			}
		}
		if limitErr != nil {
			return limitErr
		}
	}
}

// prompterStopGrace bounds how long shutdown waits for an open prompt to
// restore the screen.
const prompterStopGrace = 3 * time.Second

// startPrompterLoop runs runBlockPrompter in the background and notes why
// it stopped early to logf. The returned stop function cancels it and waits,
// up to prompterStopGrace, for it to return, so an open prompt is closed
// before the CLI exits.
func startPrompterLoop(ctx context.Context, cc *ControlClient, prompt promptFunc, logf func(format string, args ...any)) func() {
	pctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		// loggedPrompt noted the prompt that found prompts unavailable.
		if err := runBlockPrompter(pctx, cc, pollInterval, prompt); err != nil && !errors.Is(err, overlay.ErrUnavailable) {
			logf("prompts stopped: %v", err)
		}
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(prompterStopGrace):
		}
	}
}

// blockPrompter is the prompting set up for one attach: the hooks that hand
// it the session's terminal, and a stop function.
type blockPrompter struct {
	hooks ctr.TerminalHooks // start prompting on the session's terminal; zero without --prompt
	stop  func()            // ends polling and closes an open prompt; always safe to call
	notes *promptNotes      // what prompting couldn't show; nil without --prompt
}

// report prints the notes collected during the session. Call it after stop,
// once the session gave the terminal back.
func (bp *blockPrompter) report() {
	for _, line := range bp.notes.Lines() {
		tui.Warn("%s", line)
	}
}

var noBlockPrompter = &blockPrompter{stop: func() {}}

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
	notes := &promptNotes{}

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
		// show puts the approve screen over the session.
		show := func(ctx context.Context, entry proxy.LogEntry) error {
			header := &tui.HeaderInfo{ProjectDir: session.ProjectDir, SessionID: session.SessionID}
			return t.Show(ctx, tui.NewWindow(header, newApproveScreen(session, cc, entry)))
		}
		stopLoop = startPrompterLoop(ctx, cc, loggedPrompt(notes.Printf, show), notes.Printf)
	}
	return &blockPrompter{
		hooks: ctr.TerminalHooks{OnTerminal: onTerminal, Logf: notes.Printf},
		notes: notes,
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

// loggedPrompt notes why a prompt failed or wasn't shown: the session owns
// the terminal, so it's reported after the session. A prompt ended by the
// session ending isn't a failure.
func loggedPrompt(logf func(format string, args ...any), prompt promptFunc) promptFunc {
	return func(ctx context.Context, e proxy.LogEntry) error {
		err := prompt(ctx, e)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, overlay.ErrClosed) {
			logf("prompt for %s: %v", e.Target(), err)
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
