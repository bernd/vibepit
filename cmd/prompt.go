package cmd

import (
	"context"
	"errors"
	"fmt"
	"slices"
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

// promptFunc asks the user about the queued targets. The prompt posts the
// decisions to the control API itself and removes decided targets from the
// queue; returning with targets left means they were dismissed.
type promptFunc func(ctx context.Context, q *promptQueue) error

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

// runBlockPrompter follows the proxy's event stream and queues every unseen
// target that allowing would unblock. When the queue has targets and no
// prompt shows, it calls prompt with the queue, which keeps filling while
// the prompt shows, until ctx is done. Feed errors are retried by the feed;
// prompt failures are for the prompt function to report, see loggedPrompt.
// It returns early, with the reason, once no more prompts can be shown:
// they are unavailable, or too many targets were blocked.
func runBlockPrompter(ctx context.Context, client *ControlClient, interval time.Duration, prompt promptFunc) error {
	// An open stream makes the proxy hold requests, so it must not outlive
	// the prompts.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	p := &promptLoop{client: client, q: newPromptQueue()}
	// Tail 0 skips whatever was blocked before we started watching.
	feed := startFeed(ctx, client, 0, interval)
	var (
		shown    chan error // the open prompt's result; nil while none shows
		limitErr error
	)
	for {
		if shown == nil {
			if p.q.Len() > 0 && ctx.Err() == nil {
				shown = make(chan error, 1)
				p.showing = true
				go func(done chan<- error) { done <- prompt(ctx, p.q) }(shown)
			} else if limitErr != nil {
				return limitErr
			}
		}
		select {
		case b, ok := <-feed:
			if !ok {
				// ctx is done. Let an open prompt restore the screen first.
				if shown != nil {
					<-shown
				}
				return nil
			}
			if err := p.queue(b.Events); err != nil {
				limitErr = err
			}
		case err := <-shown:
			shown = nil
			if err := p.settle(err); err != nil {
				return err
			}
		}
	}
}

// promptLoop is the state runBlockPrompter keeps between prompts. Only its
// goroutine adds to q; the open prompt removes what it decides.
type promptLoop struct {
	bw     blockWatcher
	client *ControlClient
	q      *promptQueue
	// showing is set while a prompt is open. late holds the targets queued
	// since its queue ran empty: the prompt quits on an empty queue, so it
	// may never show them, and they weren't its to dismiss.
	showing bool
	late    []proxy.Target
}

// queue adds the targets in events that should be asked about to q. It
// fails with errTooManyTargets once a new target is past maxPromptTargets.
func (p *promptLoop) queue(events []proxy.Event) error {
	var limitErr error
	for _, ev := range events {
		switch ev.Type {
		case proxy.EventHoldEnd:
			p.q.EndHold(ev.HoldEnd)
			continue
		case proxy.EventLog, proxy.EventHoldOpen:
		default:
			continue
		}
		e := ev.Log
		if !e.Allowable() {
			continue
		}
		// A queued target is being asked about already: a new hold only
		// restarts its countdown. A block changes nothing, and re-adding it
		// could bring back a target the prompt just decided.
		if p.q.Has(e.Target()) {
			if e.Action == proxy.ActionHold {
				p.add(e)
			}
			continue
		}
		fresh, err := p.bw.Next([]proxy.LogEntry{e})
		if err != nil {
			limitErr = err
		}
		for _, e := range fresh {
			// Another client may have decided already. A failed check, e.g. an
			// older proxy without /check, falls back to prompting.
			if res, err := p.client.Check(e); err == nil && res.Decided() {
				continue
			}
			p.add(e)
		}
	}
	return limitErr
}

func (p *promptLoop) add(e proxy.LogEntry) {
	// Checking after the add can't miss an emptied queue: the prompt only
	// removes targets, so if it ran empty before the add, e is alone now.
	// A target the prompt goes on to show may be marked too; dismissing it
	// then just asks again.
	if p.q.Add(e) && p.showing && (len(p.late) > 0 || p.q.Len() == 1) {
		p.late = append(p.late, e.Target())
	}
}

// settle updates bw for the targets the prompt left in q after it returned
// err and takes them out of q. A dismissed or failed prompt decided nothing,
// so its targets may be asked about again on their next block. Late targets
// stay queued for the next prompt. Without a shadow no prompt can be shown,
// now or later: that error is returned.
func (p *promptLoop) settle(err error) error {
	if errors.Is(err, overlay.ErrUnavailable) {
		return err
	}
	for _, it := range p.q.Items() {
		t := it.Entry.Target()
		if slices.Contains(p.late, t) {
			continue
		}
		p.q.Remove(t)
		switch {
		case err == nil, errors.Is(err, overlay.ErrBarrierTimeout), errors.Is(err, overlay.ErrSessionBusy):
			// A dismissal is not a decision. Barrier timeouts and a busy
			// session showed nothing, and aren't the prompt's fault: after
			// two timeouts in a row the terminal takes input after a pause
			// in typing, which can't time out, and a busy session reads its
			// input again.
			p.bw.Forget(t)
		default:
			p.bw.Retry(t)
		}
	}
	p.showing = false
	p.late = nil
	return nil
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
	stop  func()            // ends prompting and closes an open prompt; always safe to call
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
// terminal, so a failure prints normally and stops the command. Prompting
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
		show := func(ctx context.Context, q *promptQueue) error {
			header := &tui.HeaderInfo{ProjectDir: session.ProjectDir, SessionID: session.SessionID}
			return t.Show(ctx, tui.NewWindow(header, newApproveScreen(session, cc, q)))
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
	return func(ctx context.Context, q *promptQueue) error {
		err := prompt(ctx, q)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, overlay.ErrClosed) {
			if items := q.Items(); len(items) > 0 {
				logf("prompt for %s: %v", items[0].Entry.Target(), err)
			} else {
				logf("prompt: %v", err)
			}
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
